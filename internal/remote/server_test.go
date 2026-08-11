package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"ptymux/internal/server"
)

const testGlobalToken = "test-global-token"

func TestNewClientRequiresHTTPAndDerivesWebSocketURL(t *testing.T) {
	client, err := NewClient(ClientConfig{BaseURL: "http://relay.example:8443/"})
	if err != nil {
		t.Fatal(err)
	}
	if client.baseURL.String() != "http://relay.example:8443" {
		t.Fatalf("base URL = %q", client.baseURL.String())
	}
	if client.wsURL != "ws://relay.example:8443/v1/ws" {
		t.Fatalf("WebSocket URL = %q", client.wsURL)
	}
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default HTTP transport type = %T", client.http.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default HTTP proxy is enabled")
	}
	if client.dialer.Proxy != nil {
		t.Fatal("default WebSocket proxy is enabled")
	}

	for _, baseURL := range []string{
		"https://relay.example:8443",
		"ws://relay.example:8443",
		"http:///missing-host",
		"http://relay.example/path",
		"http://relay.example?query=value",
		"http://relay.example#fragment",
	} {
		if _, err := NewClient(ClientConfig{BaseURL: baseURL}); err == nil {
			t.Fatalf("NewClient(%q) unexpectedly succeeded", baseURL)
		}
	}
}

func TestClientManagementDoesNotFollowRedirects(t *testing.T) {
	redirected := make(chan struct{}, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected <- struct{}{}
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:    source.URL,
		Token:      testGlobalToken,
		Name:       "alice",
		HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Register(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP status 302") {
		t.Fatalf("Register error = %v, want redirect status", err)
	}
	select {
	case <-redirected:
		t.Fatal("management request followed redirect")
	default:
	}
}

func TestClientManagementOperationTimeout(t *testing.T) {
	release := make(chan struct{})
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer testServer.Close()
	defer close(release)

	client, err := NewClient(ClientConfig{
		BaseURL:          testServer.URL,
		Token:            testGlobalToken,
		Name:             "alice",
		OperationTimeout: 20 * time.Millisecond,
		HTTPClient:       testServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Register(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Register error = %v, want deadline exceeded", err)
	}
}

func TestClientUnaryOperationTimeout(t *testing.T) {
	release := make(chan struct{})
	upgrader := websocket.Upgrader{}
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		<-release
	}))
	defer testServer.Close()
	defer close(release)

	client, err := NewClient(ClientConfig{
		BaseURL:          testServer.URL,
		Token:            testGlobalToken,
		Name:             "alice",
		Password:         "password",
		OperationTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Unary(context.Background(), Request{Operation: OperationList})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Unary error = %v, want deadline exceeded", err)
	}
}

func TestWebSocketAuthenticationAndOriginBeforeUpgrade(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	registration := env.register(t, "alice")

	tests := []struct {
		name       string
		token      string
		clientName string
		password   string
		origin     string
		status     int
		code       string
	}{
		{name: "missing token", status: http.StatusUnauthorized, code: ErrorAuthenticationFailed},
		{name: "missing client credentials", token: testGlobalToken, status: http.StatusUnauthorized, code: ErrorAuthenticationFailed},
		{name: "bad client wins over origin", token: testGlobalToken, clientName: "alice", password: "wrong", origin: "https://example.test", status: http.StatusUnauthorized, code: ErrorAuthenticationFailed},
		{name: "origin rejected after authentication", token: testGlobalToken, clientName: "alice", password: registration.Password, origin: "https://example.test", status: http.StatusForbidden, code: ErrorOriginNotAllowed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{}
			if test.token != "" {
				header.Set("Authorization", "Bearer "+test.token)
			}
			if test.clientName != "" {
				header.Set("X-Ptymux-Client-Name", test.clientName)
				header.Set("X-Ptymux-Client-Password", test.password)
			}
			if test.origin != "" {
				header.Set("Origin", test.origin)
			}
			_, response, err := env.dialer.Dial("ws"+env.http.URL[len("http"):]+"/v1/ws", header)
			if err == nil {
				t.Fatal("WebSocket upgrade unexpectedly succeeded")
			}
			if response == nil {
				t.Fatalf("upgrade returned no HTTP response: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			var envelope ManagementResponse
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error == nil || envelope.Error.Code != test.code {
				t.Fatalf("error = %+v, want code %q", envelope.Error, test.code)
			}
		})
	}

	client := env.client(t, "alice", registration.Password)
	connection, response, err := client.dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if extension := response.Header.Get("Sec-WebSocket-Extensions"); extension != "" {
		t.Fatalf("WebSocket compression was negotiated: %q", extension)
	}
}

func TestHealthEndpointsRequireOnlyGlobalToken(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	for _, path := range []string{"/healthz", "/readyz"} {
		request, err := http.NewRequest(http.MethodGet, env.http.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+testGlobalToken)
		response, err := env.http.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, response.StatusCode)
		}
	}
}

func TestPendingWebSocketRevalidatesAfterCredentialChange(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Registry, Registration) error
	}{
		{
			name: "rotate",
			mutate: func(registry *Registry, registration Registration) error {
				_, _, err := registry.Rotate(registration.Client.Name, registration.Password)
				return err
			},
		},
		{
			name: "revoke",
			mutate: func(registry *Registry, registration Registration) error {
				return registry.Revoke(registration.Client.Name, registration.Password)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
			registration := env.register(t, test.name)
			client := env.client(t, test.name, registration.Password)

			env.server.lifecycleMu.Lock()
			locked := true
			defer func() {
				if locked {
					env.server.lifecycleMu.Unlock()
				}
			}()
			connection, _, err := client.dial(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := test.mutate(env.registry, registration); err != nil {
				t.Fatal(err)
			}
			env.server.lifecycleMu.Unlock()
			locked = false

			_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := connection.ReadMessage(); err == nil {
				t.Fatal("connection authenticated with stale credentials remained open")
			} else {
				var closeErr *websocket.CloseError
				if !errors.As(err, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation || closeErr.Text != ErrorAuthenticationFailed {
					t.Fatalf("close error = %v, want authentication failure", err)
				}
			}
			if count := env.server.connectionCount(); count != 0 {
				t.Fatalf("active connections = %d, want 0", count)
			}
		})
	}
}

func TestRegistrationRateLimit(t *testing.T) {
	env := newRemoteTestEnvWithConfig(t, ServerConfig{AuthRate: 0.001, AuthBurst: 1})
	env.register(t, "first-rate")
	_, err := env.client(t, "second-rate", "").Register(context.Background())
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != ErrorRateLimited {
		t.Fatalf("second registration error = %v, want rate_limited", err)
	}
}

func TestAuthenticationFailuresExhaustRateLimit(t *testing.T) {
	env := newRemoteTestEnvWithConfig(t, ServerConfig{AuthRate: 0.001, AuthBurst: 1})
	registration := env.register(t, "auth-rate")
	badClient := env.client(t, "auth-rate", "wrong")
	if _, err := badClient.Unary(context.Background(), Request{Operation: OperationList}); err == nil {
		t.Fatal("bad password unexpectedly authenticated")
	}
	goodClient := env.client(t, "auth-rate", registration.Password)
	_, err := goodClient.Unary(context.Background(), Request{Operation: OperationList})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != ErrorAuthenticationFailed {
		t.Fatalf("rate-limited authentication error = %v, want authentication_failed", err)
	}
}

func TestConnectionLimitRejectsUpgrade(t *testing.T) {
	env := newRemoteTestEnvWithConfig(t, ServerConfig{MaxConnections: 2, MaxConnectionsPerClient: 1})
	registration := env.register(t, "connection-limit")
	client := env.client(t, "connection-limit", registration.Password)
	first, _, err := client.dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	request, err := http.NewRequest(http.MethodGet, env.http.URL+"/v1/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testGlobalToken)
	request.Header.Set("X-Ptymux-Client-Name", "connection-limit")
	request.Header.Set("X-Ptymux-Client-Password", registration.Password)
	response, err := env.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusTooManyRequests)
	}
	var envelope ManagementResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != ErrorConnectionLimit {
		t.Fatalf("error = %+v, want connection_limit", envelope.Error)
	}
}

func TestTargetLimitAllowsIdempotentCreate(t *testing.T) {
	env := newRemoteTestEnvWithConfig(t, ServerConfig{MaxTargetsPerClient: 1})
	registration := env.register(t, "target-limit")
	client := env.client(t, "target-limit", registration.Password)
	first := testTarget()
	if _, err := client.Unary(context.Background(), Request{Operation: OperationCreate, Target: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Unary(context.Background(), Request{Operation: OperationCreate, Target: first}); err != nil {
		t.Fatalf("idempotent create failed: %v", err)
	}
	second := Target{Session: "other", Pane: "default", Tab: "default"}
	_, err := client.Unary(context.Background(), Request{Operation: OperationCreate, Target: second})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != ErrorTargetLimit {
		t.Fatalf("second target error = %v, want target_limit", err)
	}
}

func TestRegisterReturnsCredentialsAfterCommittedDirectorySyncError(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	env.registry.syncDirectory = func(string) error { return errors.New("directory sync failed") }
	registration := env.register(t, "registered")
	if registration.Version != ProtocolVersion || registration.Client.Name != "registered" || registration.Password == "" {
		t.Fatalf("unexpected registration: %+v", registration)
	}
	client := env.client(t, "registered", registration.Password)
	response, err := client.Unary(context.Background(), Request{
		Operation: "create",
		Target:    testTarget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("create error: %+v", response.Error)
	}

	badClient := env.client(t, "registered", "wrong")
	_, err = badClient.Unary(context.Background(), Request{Operation: "list"})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != ErrorAuthenticationFailed {
		t.Fatalf("bad credential error = %v, want authentication_failed", err)
	}
}

func TestOwnersWithSameTargetAreIsolatedAndCloseMissing(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	firstRegistration := env.register(t, "first")
	secondRegistration := env.register(t, "second")
	first := env.client(t, "first", firstRegistration.Password)
	second := env.client(t, "second", secondRegistration.Password)
	target := testTarget()

	for _, client := range []*Client{first, second} {
		if _, err := client.Unary(context.Background(), Request{Operation: "create", Target: target}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.Unary(context.Background(), Request{Operation: "close", Target: target}); err != nil {
		t.Fatal(err)
	}
	firstList, err := first.Unary(context.Background(), Request{Operation: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstList.Snapshot.Sessions) != 0 {
		t.Fatalf("first owner still sees targets: %+v", firstList.Snapshot)
	}
	secondList, err := second.Unary(context.Background(), Request{Operation: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasTarget(secondList.Snapshot, target) {
		t.Fatalf("second owner target was affected: %+v", secondList.Snapshot)
	}
	_, err = first.Unary(context.Background(), Request{Operation: "close", Target: target})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != ErrorTargetNotFound {
		t.Fatalf("close missing error = %v, want target_not_found", err)
	}
}

func TestRotateClosesOldGenerationAfterCommittedDirectorySyncError(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	registration := env.register(t, "rotate")
	oldClient := env.client(t, "rotate", registration.Password)
	target := testTarget()
	if _, err := oldClient.Unary(context.Background(), Request{Operation: "create", Target: target}); err != nil {
		t.Fatal(err)
	}
	oldConnection, _, err := oldClient.dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer oldConnection.Close()

	env.registry.syncDirectory = func(string) error { return errors.New("directory sync failed") }
	rotation, err := oldClient.Rotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rotation.Password == registration.Password || rotation.Principal.OwnerID != registration.Client.OwnerID || rotation.Principal.CredentialGeneration != 2 {
		t.Fatalf("unexpected rotation: %+v", rotation)
	}
	_ = oldConnection.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := oldConnection.ReadMessage(); err == nil {
		t.Fatal("old-generation connection remained open")
	}
	if _, err := oldClient.Unary(context.Background(), Request{Operation: "list"}); err == nil {
		t.Fatal("old password still authenticated")
	}
	newClient := env.client(t, "rotate", rotation.Password)
	list, err := newClient.Unary(context.Background(), Request{Operation: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasTarget(list.Snapshot, target) {
		t.Fatalf("target was lost during rotation: %+v", list.Snapshot)
	}
}

func TestRevokeClosesOwnerAfterCommittedDirectorySyncError(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	firstRegistration := env.register(t, "revoked")
	secondRegistration := env.register(t, "survivor")
	first := env.client(t, "revoked", firstRegistration.Password)
	second := env.client(t, "survivor", secondRegistration.Password)
	target := testTarget()
	if _, err := first.Unary(context.Background(), Request{Operation: "create", Target: target}); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Unary(context.Background(), Request{Operation: "create", Target: target}); err != nil {
		t.Fatal(err)
	}
	firstPrincipal, err := env.registry.Authenticate("revoked", firstRegistration.Password)
	if err != nil {
		t.Fatal(err)
	}
	secondPrincipal, err := env.registry.Authenticate("survivor", secondRegistration.Password)
	if err != nil {
		t.Fatal(err)
	}
	firstService := env.server.serviceFor(firstPrincipal, false)
	secondService := env.server.serviceFor(secondPrincipal, false)

	env.registry.syncDirectory = func(string) error { return errors.New("directory sync failed") }
	if err := first.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !firstService.Empty() {
		t.Fatal("revoked owner service still has targets")
	}
	if secondService.Empty() {
		t.Fatal("revocation closed another owner's service")
	}
	list, err := second.Unary(context.Background(), Request{Operation: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasTarget(list.Snapshot, target) {
		t.Fatalf("surviving owner target missing: %+v", list.Snapshot)
	}
}

func TestFollowDisconnectLeavesTargetRunning(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	registration := env.register(t, "follower")
	client := env.client(t, "follower", registration.Password)
	target := testTarget()
	if _, err := client.Unary(context.Background(), Request{Operation: "create", Target: target}); err != nil {
		t.Fatal(err)
	}
	connection, _, err := client.dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Version: ProtocolVersion, ID: "follow-1", Operation: "follow", Target: target}
	if err := connection.WriteJSON(request); err != nil {
		t.Fatal(err)
	}
	messageType, data, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("follow acknowledgement type = %d, want text", messageType)
	}
	ack, err := decodeProtocolResponse(data)
	if err != nil || ack.Error != nil {
		t.Fatalf("follow acknowledgement = %+v, err %v", ack, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return env.server.connectionCount() == 0 })
	list, err := client.Unary(context.Background(), Request{Operation: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasTarget(list.Snapshot, target) {
		t.Fatalf("follow disconnect closed target: %+v", list.Snapshot)
	}
}

func TestClientFollowCancellationLeavesTargetRunning(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	registration := env.register(t, "client-follow")
	client := env.client(t, "client-follow", registration.Password)
	target := testTarget()
	if _, err := client.Unary(context.Background(), Request{Operation: OperationCreate, Target: target}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- client.Follow(ctx, Request{Target: target}, io.Discard)
	}()
	waitFor(t, 2*time.Second, func() bool { return env.server.connectionCount() == 1 })
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Follow error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Follow did not return after cancellation")
	}
	list, err := client.Unary(context.Background(), Request{Operation: OperationList})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasTarget(list.Snapshot, target) {
		t.Fatalf("client follow cancellation closed target: %+v", list.Snapshot)
	}
}

func TestClientReadPreservesANSIOutput(t *testing.T) {
	want := "\x1b[31mred\x1b[0m\n\x1b[48;5;1m  \x1b[0mplain"
	runner := &remoteTestRunner{
		readOutput: want,
		readCounts: make(chan int, 1),
	}
	env := newRemoteRunnerTestEnv(t, runner)
	registration := env.register(t, "ansi-read")
	client := env.client(t, "ansi-read", registration.Password)
	if _, err := client.Unary(context.Background(), Request{Operation: OperationCreate, Target: testTarget()}); err != nil {
		t.Fatal(err)
	}

	response, err := client.Unary(context.Background(), Request{
		Operation: OperationRead,
		Target:    testTarget(),
		ReadCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Output != want {
		t.Fatalf("read output = %q, want %q", response.Output, want)
	}
	select {
	case count := <-runner.readCounts:
		if count != 2 {
			t.Fatalf("Read count = %d, want 2", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote read did not reach runner")
	}
}

func TestClientReadMaximumTranscriptOutputFitsTransport(t *testing.T) {
	want := strings.Repeat("\x1b", 128<<10)
	runner := &remoteTestRunner{
		readOutput: want,
		readCounts: make(chan int, 1),
	}
	env := newRemoteRunnerTestEnv(t, runner)
	registration := env.register(t, "bounded-read")
	client := env.client(t, "bounded-read", registration.Password)
	if _, err := client.Unary(context.Background(), Request{Operation: OperationCreate, Target: testTarget()}); err != nil {
		t.Fatal(err)
	}

	response, err := client.Unary(context.Background(), Request{
		Operation: OperationRead,
		Target:    testTarget(),
		ReadCount: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Output != want {
		t.Fatalf("read output length = %d, want %d", len(response.Output), len(want))
	}
}

func TestClientFollowPreservesFutureRawBytes(t *testing.T) {
	runner := &remoteTestRunner{
		readOutput:    "past-output",
		readCounts:    make(chan int, 1),
		followStarted: make(chan struct{}),
		followChunks:  make(chan []byte, 3),
	}
	env := newRemoteRunnerTestEnv(t, runner)
	registration := env.register(t, "raw-follow")
	client := env.client(t, "raw-follow", registration.Password)
	if _, err := client.Unary(context.Background(), Request{Operation: OperationCreate, Target: testTarget()}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture := &channelWriter{chunks: make(chan []byte, 3)}
	result := make(chan error, 1)
	go func() {
		result <- client.Follow(ctx, Request{Target: testTarget()}, capture)
	}()
	select {
	case <-runner.followStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("remote follow did not reach runner")
	}

	chunks := [][]byte{
		[]byte("\x1b]0;user@"),
		[]byte("host:/path\x07\x1b[41m  "),
		[]byte("\x1b[0mabc\rxy\b\n"),
	}
	for _, chunk := range chunks {
		runner.followChunks <- chunk
	}
	for i, want := range chunks {
		select {
		case got := <-capture.chunks:
			if string(got) != string(want) {
				t.Fatalf("follow chunk %d = %q, want %q", i, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("follow chunk %d was not delivered", i)
		}
	}
	select {
	case count := <-runner.readCounts:
		t.Fatalf("follow unexpectedly called Read(%d)", count)
	default:
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Follow error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Follow did not return after cancellation")
	}
}

func TestEnqueueResponseRejectsSerializedOutputBeyondQueueLimit(t *testing.T) {
	connection := newWSConnection(nil, nil, Principal{}, 32)
	connection.enqueueResponse(Response{
		Version: ProtocolVersion,
		Type:    ResponseTypeResponse,
		ID:      "read-1",
		Output:  strings.Repeat("x", 32),
	})

	select {
	case <-connection.done:
	default:
		t.Fatal("oversized response did not close the connection")
	}
	select {
	case request := <-connection.closeReq:
		if request.code != websocket.ClosePolicyViolation || request.reason != ErrorSlowConnection {
			t.Fatalf("close request = %+v", request)
		}
	default:
		t.Fatal("oversized response has no close request")
	}
}

func TestByteQueueIsBoundedByBytes(t *testing.T) {
	queue := newByteQueue(10)
	if !queue.tryPush(outboundMessage{data: make([]byte, 6)}) {
		t.Fatal("first message did not fit")
	}
	if queue.tryPush(outboundMessage{data: make([]byte, 5)}) {
		t.Fatal("queue accepted bytes beyond its limit")
	}
	message, ok := queue.pop()
	if !ok || len(message.data) != 6 {
		t.Fatalf("pop = %d bytes, %v", len(message.data), ok)
	}
	if !queue.tryPush(outboundMessage{data: make([]byte, 5)}) {
		t.Fatal("queue did not release byte capacity after pop")
	}
}

func TestFollowWriterIgnoresEmptyWrites(t *testing.T) {
	connection := newWSConnection(nil, nil, Principal{}, 1)
	writer := &followWriter{connection: connection}

	if n, err := writer.Write(nil); err != nil || n != 0 {
		t.Fatalf("Write(nil) = %d, %v; want 0, nil", n, err)
	}
	if _, ok := connection.queue.pop(); ok {
		t.Fatal("empty write queued a WebSocket message")
	}
}

func TestQueueOverflowDisconnectsOnlySlowConnection(t *testing.T) {
	principal := Principal{OwnerID: "owner", Name: "client", CredentialGeneration: 1}
	slow := newWSConnection(nil, nil, principal, 4)
	other := newWSConnection(nil, nil, principal, 4)
	writer := &followWriter{connection: slow}
	if _, err := writer.Write([]byte("12345")); err == nil {
		t.Fatal("oversized follow output did not overflow the queue")
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("slow connection was not disconnected")
	}
	select {
	case request := <-slow.closeReq:
		if request.reason != ErrorSlowConnection {
			t.Fatalf("close reason = %q, want %q", request.reason, ErrorSlowConnection)
		}
	default:
		t.Fatal("slow connection has no close request")
	}
	select {
	case <-other.done:
		t.Fatal("queue overflow disconnected another connection")
	default:
	}
	if !other.queue.tryPush(outboundMessage{data: []byte("1234")}) {
		t.Fatal("other connection queue was affected")
	}
}

func TestServerPreAuthConnectionLimitDefaultsAndValidation(t *testing.T) {
	registry := openTestRegistry(t)
	remoteServer, err := NewServer(ServerConfig{Token: testGlobalToken, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if remoteServer.maxPreAuthConnections != defaultMaxPreAuthConnections {
		t.Fatalf("maxPreAuthConnections = %d, want %d", remoteServer.maxPreAuthConnections, defaultMaxPreAuthConnections)
	}
	if _, err := NewServer(ServerConfig{Token: testGlobalToken, Registry: registry, MaxPreAuthConnections: -1}); err == nil {
		t.Fatal("negative pre-authentication connection limit was accepted")
	}
}

func TestPreAuthConnectionLimitRejectsAndReleasesRawTCPConnections(t *testing.T) {
	env := newRealRemoteTestEnv(t, ServerConfig{MaxPreAuthConnections: 1})
	first := dialRawRemote(t, env.address)
	defer first.Close()
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 1 })

	second := dialRawRemote(t, env.address)
	expectTCPConnectionClosed(t, second)
	_ = second.Close()
	if got := env.server.preAuthConnectionCount(); got != 1 {
		t.Fatalf("pre-auth connection count = %d, want 1", got)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 0 })
	third := dialRawRemote(t, env.address)
	defer third.Close()
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 1 })
}

func TestSuccessfulAuthenticationReleasesPreAuthConnection(t *testing.T) {
	env := newRealRemoteTestEnv(t, ServerConfig{MaxPreAuthConnections: 1})
	first := dialRawRemote(t, env.address)
	defer first.Close()
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 1 })

	response := rawHTTPGet(t, first, "/healthz", map[string]string{
		"Authorization": "Bearer " + testGlobalToken,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 0 })

	second := dialRawRemote(t, env.address)
	defer second.Close()
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 1 })
}

func TestFailedAuthenticationKeepsPreAuthConnectionOccupied(t *testing.T) {
	env := newRealRemoteTestEnv(t, ServerConfig{MaxPreAuthConnections: 1})
	first := dialRawRemote(t, env.address)
	defer first.Close()
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 1 })

	response := rawHTTPGet(t, first, "/healthz", map[string]string{
		"Authorization": "Bearer wrong-token",
	})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("health status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	if got := env.server.preAuthConnectionCount(); got != 1 {
		t.Fatalf("pre-auth connection count = %d, want 1", got)
	}

	second := dialRawRemote(t, env.address)
	expectTCPConnectionClosed(t, second)
	_ = second.Close()
}

func TestClientAuthenticationMustSucceedBeforePreAuthRelease(t *testing.T) {
	env := newRealRemoteTestEnv(t, ServerConfig{MaxPreAuthConnections: 1})
	principal, _, err := env.registry.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	first := dialRawRemote(t, env.address)
	defer first.Close()
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 1 })

	response := rawHTTPGet(t, first, "/v1/ws", map[string]string{
		"Authorization":            "Bearer " + testGlobalToken,
		"X-Ptymux-Client-Name":     principal.Name,
		"X-Ptymux-Client-Password": "wrong-password",
	})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("WebSocket authentication status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	if got := env.server.preAuthConnectionCount(); got != 1 {
		t.Fatalf("pre-auth connection count = %d, want 1", got)
	}

	second := dialRawRemote(t, env.address)
	expectTCPConnectionClosed(t, second)
	_ = second.Close()
}

func TestAuthenticatedWebSocketDoesNotOccupyPreAuthSlot(t *testing.T) {
	env := newRealRemoteTestEnv(t, ServerConfig{MaxPreAuthConnections: 1, MaxConnections: 1})
	principal, password, err := env.registry.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{
		"Authorization":            []string{"Bearer " + testGlobalToken},
		"X-Ptymux-Client-Name":     []string{principal.Name},
		"X-Ptymux-Client-Password": []string{password},
	}
	conn, response, err := websocket.DefaultDialer.Dial("ws://"+env.address+"/v1/ws", headers)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatal(err)
	}
	defer conn.Close()
	waitFor(t, time.Second, func() bool {
		return env.server.preAuthConnectionCount() == 0 && env.server.connectionCount() == 1
	})

	raw := dialRawRemote(t, env.address)
	defer raw.Close()
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 1 })
}

func TestRemoteShutdownClosesPreAuthConnections(t *testing.T) {
	env := newRealRemoteTestEnv(t, ServerConfig{MaxPreAuthConnections: 1})
	conn := dialRawRemote(t, env.address)
	defer conn.Close()
	waitFor(t, time.Second, func() bool { return env.server.preAuthConnectionCount() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := env.server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	expectTCPConnectionClosed(t, conn)
	if got := env.server.preAuthConnectionCount(); got != 0 {
		t.Fatalf("pre-auth connection count = %d, want 0", got)
	}
}

func TestNewHTTPServerUsesPlainHTTPWithTimeouts(t *testing.T) {
	env := newRemoteTestEnv(t, defaultOutgoingQueueBytes)
	httpServer := env.server.NewHTTPServer("")
	if httpServer.TLSConfig != nil {
		t.Fatal("HTTP server unexpectedly configured TLS")
	}
	if httpServer.ReadHeaderTimeout <= 0 || httpServer.ReadTimeout <= 0 || httpServer.WriteTimeout <= 0 || httpServer.IdleTimeout <= 0 {
		t.Fatalf("HTTP server timeouts are not configured: %+v", httpServer)
	}
	if httpServer.ConnContext == nil || httpServer.ConnState == nil {
		t.Fatal("HTTP server pre-authentication connection hooks are not configured")
	}
}

func TestLoadServerTokenFilePermissions(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "allowed")
	if err := os.WriteFile(allowed, []byte("secret\r\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(allowed, 0444); err != nil {
		t.Fatal(err)
	}
	if token, err := LoadServerTokenFile(allowed); err != nil || token != "secret" {
		t.Fatalf("LoadServerTokenFile = %q, %v", token, err)
	}

	writable := filepath.Join(dir, "writable")
	if err := os.WriteFile(writable, []byte("secret"), 0622); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0622); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServerTokenFile(writable); err == nil {
		t.Fatal("accepted group/other-writable token file")
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\r\n"), 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServerTokenFile(empty); err == nil {
		t.Fatal("accepted empty token")
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(allowed, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServerTokenFile(link); err == nil {
		t.Fatal("accepted symlink token file")
	}
}

type remoteTestRunner struct {
	server.Runner
	readOutput    string
	readCounts    chan int
	followStarted chan struct{}
	followChunks  chan []byte
}

func (r *remoteTestRunner) Read(count int) (server.RunResult, error) {
	if r.readCounts != nil {
		r.readCounts <- count
	}
	return server.RunResult{Output: r.readOutput}, nil
}

func (r *remoteTestRunner) Follow(output io.Writer, done <-chan struct{}) error {
	if r.followStarted != nil {
		close(r.followStarted)
	}
	for {
		select {
		case chunk, ok := <-r.followChunks:
			if !ok {
				return nil
			}
			if _, err := output.Write(chunk); err != nil {
				return err
			}
		case <-done:
			return nil
		}
	}
}

func (r *remoteTestRunner) Close() error {
	return nil
}

type channelWriter struct {
	chunks chan []byte
}

func (w *channelWriter) Write(data []byte) (int, error) {
	copyOfData := append([]byte(nil), data...)
	w.chunks <- copyOfData
	return len(data), nil
}

func newRemoteRunnerTestEnv(t *testing.T, runner server.Runner) *remoteTestEnv {
	t.Helper()
	target := testTarget()
	service := server.NewService("/bin/sh")
	service.Store().GetOrCreate(target.Session, target.Pane, target.Tab, func() server.Runner {
		return runner
	})
	return newRemoteTestEnvWithConfig(t, ServerConfig{
		OutgoingQueueBytes: defaultOutgoingQueueBytes,
		ServiceFactory: func(Principal) *server.Service {
			return service
		},
	})
}

func TestValidateRequestBounds(t *testing.T) {
	request := Request{
		Version:   ProtocolVersion,
		ID:        "req-1",
		Operation: OperationRead,
		Target:    testTarget(),
	}
	if err := validateRequest(request); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	request.ID = strings.Repeat("i", MaxRequestIDBytes+1)
	if err := validateRequest(request); err == nil || err.Code != ErrorBadRequest {
		t.Fatalf("oversized request ID error = %v", err)
	}
	request.ID = "req-1"
	request.Target.Session = strings.Repeat("s", server.MaxTargetComponentBytes+1)
	if err := validateRequest(request); err == nil || err.Code != ErrorBadRequest {
		t.Fatalf("oversized target error = %v", err)
	}
	request.Target = testTarget()
	request.ReadCount = server.MaxReadCount + 1
	if err := validateRequest(request); err == nil || err.Code != ErrorBadRequest {
		t.Fatalf("oversized read count error = %v", err)
	}
}

type remoteTestEnv struct {
	registry *Registry
	server   *Server
	http     *httptest.Server
	dialer   *websocket.Dialer
}

func newRemoteTestEnv(t *testing.T, queueBytes int) *remoteTestEnv {
	t.Helper()
	return newRemoteTestEnvWithConfig(t, ServerConfig{OutgoingQueueBytes: queueBytes})
}

func newRemoteTestEnvWithConfig(t *testing.T, config ServerConfig) *remoteTestEnv {
	t.Helper()
	registry := openTestRegistry(t)
	config.Token = testGlobalToken
	config.Registry = registry
	config.Shell = "/bin/sh"
	remoteServer, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(remoteServer.Handler())
	dialer := &websocket.Dialer{EnableCompression: true}
	env := &remoteTestEnv{registry: registry, server: remoteServer, http: httpServer, dialer: dialer}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := remoteServer.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown: %v", err)
		}
		httpServer.Close()
	})
	return env
}

func (e *remoteTestEnv) register(t *testing.T, name string) Registration {
	t.Helper()
	client := e.client(t, name, "")
	registration, err := client.Register(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return registration
}

func (e *remoteTestEnv) client(t *testing.T, name, password string) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		BaseURL:         e.http.URL,
		Token:           testGlobalToken,
		Name:            name,
		Password:        password,
		HTTPClient:      e.http.Client(),
		WebSocketDialer: e.dialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testTarget() Target {
	return Target{Session: "work", Pane: "default", Tab: "default"}
}

func snapshotHasTarget(snapshot server.Snapshot, target Target) bool {
	for _, session := range snapshot.Sessions {
		if session.Name != target.Session {
			continue
		}
		for _, pane := range session.Panes {
			if pane.Name != target.Pane {
				continue
			}
			for _, tab := range pane.Tabs {
				if tab.Name == target.Tab {
					return true
				}
			}
		}
	}
	return false
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

type realRemoteTestEnv struct {
	registry   *Registry
	server     *Server
	httpServer *http.Server
	address    string
	serveErr   chan error
}

func newRealRemoteTestEnv(t *testing.T, config ServerConfig) *realRemoteTestEnv {
	t.Helper()
	registry := openTestRegistry(t)
	config.Token = testGlobalToken
	config.Registry = registry
	config.Shell = "/bin/sh"
	remoteServer, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := remoteServer.NewHTTPServer(listener.Addr().String())
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	env := &realRemoteTestEnv{
		registry:   registry,
		server:     remoteServer,
		httpServer: httpServer,
		address:    listener.Addr().String(),
		serveErr:   serveErr,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := remoteServer.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown: %v", err)
		}
		_ = httpServer.Close()
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("HTTP server did not stop")
		}
	})
	return env
}

func dialRawRemote(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func rawHTTPGet(t *testing.T, conn net.Conn, path string, headers map[string]string) *http.Response {
	t.Helper()
	var request strings.Builder
	fmt.Fprintf(&request, "GET %s HTTP/1.1\r\nHost: ptymux.test\r\nConnection: keep-alive\r\n", path)
	for name, value := range headers {
		fmt.Fprintf(&request, "%s: %s\r\n", name, value)
	}
	request.WriteString("\r\n")
	if _, err := io.WriteString(conn, request.String()); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	return response
}

func expectTCPConnectionClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var data [1]byte
	_, err := conn.Read(data[:])
	if err == nil {
		t.Fatal("connection remained open")
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatal("connection was not closed before timeout")
	}
}
