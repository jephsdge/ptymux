package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const defaultClientOperationTimeout = 30 * time.Second

type ClientConfig struct {
	BaseURL          string
	Token            string
	Name             string
	Password         string
	OperationTimeout time.Duration
	HTTPClient       *http.Client
	WebSocketDialer  *websocket.Dialer
}

type Client struct {
	baseURL          *url.URL
	wsURL            string
	token            string
	name             string
	password         string
	http             *http.Client
	dialer           *websocket.Dialer
	operationTimeout time.Duration
	nextID           uint64
}

func NewClient(config ClientConfig) (*Client, error) {
	baseURL, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("remote: parse base URL: %w", err)
	}
	if baseURL.Scheme != "http" || baseURL.Host == "" || (baseURL.Path != "" && baseURL.Path != "/") || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("remote: base URL must be an http://host URL")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	wsURL := *baseURL
	wsURL.Scheme = "ws"
	wsURL.Path = baseURL.Path + "/v1/ws"

	httpClient := config.HTTPClient
	if httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		httpClient = &http.Client{Transport: transport}
	}
	operationTimeout := config.OperationTimeout
	if operationTimeout < 0 {
		return nil, errors.New("remote: operation timeout must be positive")
	}
	if operationTimeout == 0 {
		operationTimeout = defaultClientOperationTimeout
	}
	dialer := config.WebSocketDialer
	if dialer == nil {
		copyOfDefault := *websocket.DefaultDialer
		copyOfDefault.Proxy = nil
		dialer = &copyOfDefault
	}
	return &Client{
		baseURL:          baseURL,
		wsURL:            wsURL.String(),
		token:            config.Token,
		name:             config.Name,
		password:         config.Password,
		http:             httpClient,
		dialer:           dialer,
		operationTimeout: operationTimeout,
	}, nil
}

func (c *Client) Register(ctx context.Context) (Registration, error) {
	var response Registration
	err := c.managementJSON(ctx, http.MethodPost, "/v1/clients/register", RegisterRequest{Name: c.name}, false, &response)
	return response, err
}

func (c *Client) Rotate(ctx context.Context) (Rotation, error) {
	var response Rotation
	err := c.managementJSON(ctx, http.MethodPost, "/v1/clients/rotate", nil, true, &response)
	return response, err
}

func (c *Client) Revoke(ctx context.Context) error {
	var response ManagementResponse
	return c.managementJSON(ctx, http.MethodPost, "/v1/clients/revoke", nil, true, &response)
}

func (c *Client) Unary(ctx context.Context, request Request) (Response, error) {
	ctx, cancel := c.operationContext(ctx)
	defer cancel()
	if request.Operation == OperationFollow {
		return Response{}, errors.New("remote: follow requires Follow")
	}
	if request.Version == 0 {
		request.Version = ProtocolVersion
	}
	if request.ID == "" {
		request.ID = c.requestID()
	}
	if protocolErr := validateRequest(request); protocolErr != nil {
		return Response{}, protocolErr
	}
	conn, _, err := c.dial(ctx)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	if err := conn.WriteJSON(request); err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("remote: write WebSocket request: %w", ctx.Err())
		}
		return Response{}, fmt.Errorf("remote: write WebSocket request: %w", err)
	}
	messageType, data, err := conn.ReadMessage()
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("remote: read WebSocket response: %w", ctx.Err())
		}
		return Response{}, fmt.Errorf("remote: read WebSocket response: %w", err)
	}
	if messageType != websocket.TextMessage {
		return Response{}, errors.New("remote: expected JSON WebSocket response")
	}
	response, err := decodeProtocolResponse(data)
	if err != nil {
		return Response{}, err
	}
	if response.ID != request.ID || response.Type != ResponseTypeResponse {
		return Response{}, errors.New("remote: mismatched response")
	}
	if response.Error != nil {
		return response, response.Error
	}
	return response, nil
}

func (c *Client) Follow(ctx context.Context, request Request, output io.Writer) error {
	if output == nil {
		return errors.New("remote: nil follow writer")
	}
	request.Operation = OperationFollow
	if request.Version == 0 {
		request.Version = ProtocolVersion
	}
	if request.ID == "" {
		request.ID = c.requestID()
	}
	if protocolErr := validateRequest(request); protocolErr != nil {
		return protocolErr
	}
	conn, _, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()

	if err := conn.WriteJSON(request); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("remote: write follow request: %w", err)
	}

	started := false
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("remote: read follow stream: %w", err)
		}
		switch messageType {
		case websocket.BinaryMessage:
			if !started {
				return errors.New("remote: follow data arrived before acknowledgement")
			}
			if _, err := output.Write(data); err != nil {
				return err
			}
		case websocket.TextMessage:
			response, err := decodeProtocolResponse(data)
			if err != nil {
				return err
			}
			if response.ID != request.ID {
				return errors.New("remote: mismatched follow response ID")
			}
			if response.Error != nil && response.Type == ResponseTypeResponse {
				return response.Error
			}
			switch response.Type {
			case ResponseTypeFollowStarted:
				if started || response.Error != nil {
					return errors.New("remote: invalid follow acknowledgement")
				}
				started = true
			case ResponseTypeFollowEnded:
				if !started {
					return errors.New("remote: follow ended before acknowledgement")
				}
				if response.Error != nil {
					return response.Error
				}
				return nil
			default:
				return errors.New("remote: unexpected follow response type")
			}
		}
	}
}

func (c *Client) managementJSON(ctx context.Context, method, path string, body interface{}, credentials bool, output interface{}) error {
	ctx, cancel := c.operationContext(ctx)
	defer cancel()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	endpoint := *c.baseURL
	endpoint.Path = c.baseURL.Path + path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if credentials {
		request.Header.Set("X-Ptymux-Client-Name", c.name)
		request.Header.Set("X-Ptymux-Client-Password", c.password)
	}
	httpClient := *c.http
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("remote: management request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPBodyBytes))
	if err != nil {
		return fmt.Errorf("remote: read management response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope ManagementResponse
		if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error != nil {
			return envelope.Error
		}
		return fmt.Errorf("remote: management HTTP status %d", response.StatusCode)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("remote: decode management response: %w", err)
	}
	return nil
}

func (c *Client) dial(ctx context.Context) (*websocket.Conn, *http.Response, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.token)
	header.Set("X-Ptymux-Client-Name", c.name)
	header.Set("X-Ptymux-Client-Password", c.password)
	conn, response, err := c.dialer.DialContext(ctx, c.wsURL, header)
	if err != nil {
		if response != nil {
			defer response.Body.Close()
			var envelope ManagementResponse
			if decodeErr := json.NewDecoder(io.LimitReader(response.Body, maxHTTPBodyBytes)).Decode(&envelope); decodeErr == nil && envelope.Error != nil {
				return nil, response, envelope.Error
			}
		}
		return nil, response, fmt.Errorf("remote: WebSocket dial: %w", err)
	}
	conn.EnableWriteCompression(false)
	conn.SetReadLimit(maxWebSocketMessageBytes)
	return conn, response, nil
}

func (c *Client) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.operationTimeout)
}

func (c *Client) requestID() string {
	return fmt.Sprintf("req-%d", atomic.AddUint64(&c.nextID, 1))
}

func decodeProtocolResponse(data []byte) (Response, error) {
	var response Response
	if err := decodeStrictJSON(bytes.NewReader(data), &response); err != nil {
		return Response{}, fmt.Errorf("remote: decode WebSocket response: %w", err)
	}
	if response.Version != ProtocolVersion {
		return Response{}, errors.New("remote: unsupported response version")
	}
	return response, nil
}
