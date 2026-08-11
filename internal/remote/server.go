package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"ptymux/internal/server"
)

const (
	defaultOutgoingQueueBytes      = 1 << 20
	defaultMaxPreAuthConnections   = 256
	defaultMaxConnections          = 256
	defaultMaxConnectionsPerClient = 16
	defaultMaxTargetsPerClient     = 64
	defaultAuthRate                = 5
	defaultAuthBurst               = 20
	maxHTTPBodyBytes               = 64 << 10
	maxWebSocketMessageBytes       = 1 << 20
	webSocketReadTimeout           = 60 * time.Second
	webSocketWriteTimeout          = 10 * time.Second
	webSocketPingInterval          = 25 * time.Second
)

type ServerConfig struct {
	Token                   string
	Registry                *Registry
	Shell                   string
	OutgoingQueueBytes      int
	MaxPreAuthConnections   int
	MaxConnections          int
	MaxConnectionsPerClient int
	MaxTargetsPerClient     int
	AuthRate                float64
	AuthBurst               int
	ServiceFactory          func(Principal) *server.Service
}

type Server struct {
	registry                *Registry
	tokenDigest             [sha256.Size]byte
	shell                   string
	queueBytes              int
	maxPreAuthConnections   int
	maxConnections          int
	maxConnectionsPerClient int
	maxTargetsPerClient     int
	serviceFactory          func(Principal) *server.Service
	upgrader                websocket.Upgrader
	closing                 uint32
	lifecycleMu             sync.RWMutex
	authLimiter             *keyedRateLimiter
	registerLimiter         *keyedRateLimiter

	preAuthMu          sync.Mutex
	preAuthConnections map[net.Conn]struct{}

	serviceMu  sync.Mutex
	services   map[string]*server.Service
	createMu   sync.Mutex
	createLock map[string]*sync.Mutex

	connMu             sync.Mutex
	connections        map[string]map[*wsConnection]struct{}
	pendingConnections map[string]int
	pendingTotal       int
	connChanged        chan struct{}
}

func NewServer(config ServerConfig) (*Server, error) {
	if config.Token == "" {
		return nil, errors.New("remote: empty global token")
	}
	if config.Registry == nil {
		return nil, errors.New("remote: nil registry")
	}
	queueBytes := config.OutgoingQueueBytes
	if queueBytes < 0 {
		return nil, errors.New("remote: outgoing queue limit must be positive")
	}
	if queueBytes == 0 {
		queueBytes = defaultOutgoingQueueBytes
	}
	maxPreAuthConnections := config.MaxPreAuthConnections
	if maxPreAuthConnections < 0 {
		return nil, errors.New("remote: pre-authentication connection limit must be positive")
	}
	if maxPreAuthConnections == 0 {
		maxPreAuthConnections = defaultMaxPreAuthConnections
	}
	maxConnections := config.MaxConnections
	if maxConnections < 0 {
		return nil, errors.New("remote: connection limit must be positive")
	}
	if maxConnections == 0 {
		maxConnections = defaultMaxConnections
	}
	maxConnectionsPerClient := config.MaxConnectionsPerClient
	if maxConnectionsPerClient < 0 {
		return nil, errors.New("remote: per-client connection limit must be positive")
	}
	if maxConnectionsPerClient == 0 {
		maxConnectionsPerClient = defaultMaxConnectionsPerClient
	}
	maxTargetsPerClient := config.MaxTargetsPerClient
	if maxTargetsPerClient < 0 {
		return nil, errors.New("remote: per-client target limit must be positive")
	}
	if maxTargetsPerClient == 0 {
		maxTargetsPerClient = defaultMaxTargetsPerClient
	}
	authRate := config.AuthRate
	if authRate < 0 {
		return nil, errors.New("remote: authentication rate must be positive")
	}
	if authRate == 0 {
		authRate = defaultAuthRate
	}
	authBurst := config.AuthBurst
	if authBurst < 0 {
		return nil, errors.New("remote: authentication burst must be positive")
	}
	if authBurst == 0 {
		authBurst = defaultAuthBurst
	}
	s := &Server{
		registry:                config.Registry,
		tokenDigest:             sha256.Sum256([]byte(config.Token)),
		shell:                   config.Shell,
		queueBytes:              queueBytes,
		maxPreAuthConnections:   maxPreAuthConnections,
		maxConnections:          maxConnections,
		maxConnectionsPerClient: maxConnectionsPerClient,
		maxTargetsPerClient:     maxTargetsPerClient,
		serviceFactory:          config.ServiceFactory,
		authLimiter:             newKeyedRateLimiter(authRate, authBurst),
		registerLimiter:         newKeyedRateLimiter(authRate, authBurst),
		preAuthConnections:      make(map[net.Conn]struct{}),
		services:                make(map[string]*server.Service),
		createLock:              make(map[string]*sync.Mutex),
		connections:             make(map[string]map[*wsConnection]struct{}),
		pendingConnections:      make(map[string]int),
		connChanged:             make(chan struct{}, 1),
	}
	s.upgrader = websocket.Upgrader{
		EnableCompression: false,
		CheckOrigin: func(r *http.Request) bool {
			return r.Header.Get("Origin") == ""
		},
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/v1/clients/register", s.handleRegister)
	mux.HandleFunc("/v1/clients/rotate", s.handleRotate)
	mux.HandleFunc("/v1/clients/revoke", s.handleRevoke)
	mux.HandleFunc("/v1/ws", s.handleWebSocket)
	return mux
}

type preAuthConnectionContextKey struct{}

func (s *Server) NewHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnContext:       s.preAuthConnectionContext,
		ConnState:         s.handleHTTPConnectionState,
	}
}

func (s *Server) preAuthConnectionContext(ctx context.Context, conn net.Conn) context.Context {
	if !s.admitPreAuthConnection(conn) {
		_ = conn.Close()
		return ctx
	}
	return context.WithValue(ctx, preAuthConnectionContextKey{}, conn)
}

func (s *Server) admitPreAuthConnection(conn net.Conn) bool {
	s.preAuthMu.Lock()
	defer s.preAuthMu.Unlock()
	if s.isClosing() || len(s.preAuthConnections) >= s.maxPreAuthConnections {
		return false
	}
	s.preAuthConnections[conn] = struct{}{}
	return true
}

func (s *Server) handleHTTPConnectionState(conn net.Conn, state http.ConnState) {
	if state == http.StateClosed || state == http.StateHijacked {
		s.releasePreAuthConnection(conn)
	}
}

func (s *Server) releasePreAuthRequest(r *http.Request) {
	conn, _ := r.Context().Value(preAuthConnectionContextKey{}).(net.Conn)
	if conn != nil {
		s.releasePreAuthConnection(conn)
	}
}

func (s *Server) releasePreAuthConnection(conn net.Conn) {
	s.preAuthMu.Lock()
	delete(s.preAuthConnections, conn)
	s.preAuthMu.Unlock()
}

func (s *Server) closePreAuthConnections() {
	s.preAuthMu.Lock()
	connections := make([]net.Conn, 0, len(s.preAuthConnections))
	for conn := range s.preAuthConnections {
		connections = append(connections, conn)
		delete(s.preAuthConnections, conn)
	}
	s.preAuthMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (s *Server) preAuthConnectionCount() int {
	s.preAuthMu.Lock()
	defer s.preAuthMu.Unlock()
	return len(s.preAuthConnections)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticateRequest(w, r, false); !ok {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.writeError(w, http.StatusMethodNotAllowed, ErrorBadRequest, "method not allowed")
		return
	}
	s.writeJSON(w, http.StatusOK, ManagementResponse{Version: ProtocolVersion})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticateRequest(w, r, false); !ok {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.writeError(w, http.StatusMethodNotAllowed, ErrorBadRequest, "method not allowed")
		return
	}
	if s.isClosing() {
		s.writeError(w, http.StatusServiceUnavailable, ErrorServerShuttingDown, "")
		return
	}
	s.writeJSON(w, http.StatusOK, ManagementResponse{Version: ProtocolVersion})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticateRequest(w, r, false); !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.writeError(w, http.StatusMethodNotAllowed, ErrorBadRequest, "method not allowed")
		return
	}
	if s.isClosing() {
		s.writeError(w, http.StatusServiceUnavailable, ErrorServerShuttingDown, "")
		return
	}
	if !s.registerLimiter.Allow(remoteAddressKey(r)) {
		s.writeError(w, http.StatusTooManyRequests, ErrorRateLimited, "")
		return
	}
	var request RegisterRequest
	if err := decodeHTTPJSON(r, &request); err != nil {
		s.writeError(w, http.StatusBadRequest, ErrorBadRequest, "invalid JSON request")
		return
	}
	record, password, err := s.registry.Register(request.Name)
	if err != nil && !registryMutationCommitted(err) {
		switch {
		case errors.Is(err, ErrClientNameUnavailable):
			s.writeError(w, http.StatusConflict, ErrorClientNameUnavailable, "")
		case errors.Is(err, ErrInvalidClientName):
			s.writeError(w, http.StatusBadRequest, ErrorBadRequest, "invalid client name")
		default:
			s.writeError(w, http.StatusInternalServerError, ErrorInternal, "")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusCreated, Registration{Version: ProtocolVersion, Client: record, Password: password})
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticateRequest(w, r, true)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.writeError(w, http.StatusMethodNotAllowed, ErrorBadRequest, "method not allowed")
		return
	}
	password := r.Header.Get("X-Ptymux-Client-Password")
	newPassword, rotated, err := s.registry.Rotate(principal.Name, password)
	if err != nil && !registryMutationCommitted(err) {
		if errors.Is(err, ErrAuthenticationFailed) {
			s.writeAuthenticationFailure(w)
		} else {
			s.writeError(w, http.StatusInternalServerError, ErrorInternal, "")
		}
		return
	}
	s.lifecycleMu.Lock()
	s.closeOwnerConnections(rotated.OwnerID, func(connection *wsConnection) bool {
		return connection.principal.CredentialGeneration < rotated.CredentialGeneration
	}, "credentials_rotated")
	s.lifecycleMu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, Rotation{Version: ProtocolVersion, Principal: rotated, Password: newPassword})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticateRequest(w, r, true)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.writeError(w, http.StatusMethodNotAllowed, ErrorBadRequest, "method not allowed")
		return
	}
	password := r.Header.Get("X-Ptymux-Client-Password")
	if err := s.registry.Revoke(principal.Name, password); err != nil && !registryMutationCommitted(err) {
		if errors.Is(err, ErrAuthenticationFailed) {
			s.writeAuthenticationFailure(w)
		} else {
			s.writeError(w, http.StatusInternalServerError, ErrorInternal, "")
		}
		return
	}
	s.lifecycleMu.Lock()
	s.closeOwnerConnections(principal.OwnerID, func(*wsConnection) bool { return true }, "client_revoked")
	service := s.removeService(principal.OwnerID)
	s.lifecycleMu.Unlock()
	if service != nil {
		_ = service.CloseAll()
	}
	s.writeJSON(w, http.StatusOK, ManagementResponse{Version: ProtocolVersion})
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authenticateRequest(w, r, true)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.writeError(w, http.StatusMethodNotAllowed, ErrorBadRequest, "method not allowed")
		return
	}
	if r.Header.Get("Origin") != "" {
		s.writeError(w, http.StatusForbidden, ErrorOriginNotAllowed, "")
		return
	}
	if s.isClosing() {
		s.writeError(w, http.StatusServiceUnavailable, ErrorServerShuttingDown, "")
		return
	}
	if code := s.reserveConnection(principal.OwnerID); code != "" {
		status := http.StatusTooManyRequests
		if code == ErrorServerShuttingDown {
			status = http.StatusServiceUnavailable
		}
		s.writeError(w, status, code, "")
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.releaseConnectionReservation(principal.OwnerID)
		return
	}

	s.lifecycleMu.RLock()
	current, authErr := s.registry.Authenticate(r.Header.Get("X-Ptymux-Client-Name"), r.Header.Get("X-Ptymux-Client-Password"))
	if authErr != nil || current != principal {
		s.lifecycleMu.RUnlock()
		s.releaseConnectionReservation(principal.OwnerID)
		_ = conn.SetWriteDeadline(time.Now().Add(webSocketWriteTimeout))
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, ErrorAuthenticationFailed))
		_ = conn.Close()
		return
	}
	connection := newWSConnection(s, conn, current, s.queueBytes)
	if !s.activateConnection(connection) {
		s.lifecycleMu.RUnlock()
		_ = conn.SetWriteDeadline(time.Now().Add(webSocketWriteTimeout))
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, ErrorServerShuttingDown))
		_ = conn.Close()
		return
	}
	s.lifecycleMu.RUnlock()
	defer s.removeConnection(connection)
	connection.run()
}

func (s *Server) authenticateRequest(w http.ResponseWriter, r *http.Request, requireClient bool) (Principal, bool) {
	key := remoteAddressKey(r)
	if !s.authLimiter.Allow(key) {
		s.writeAuthenticationFailure(w)
		return Principal{}, false
	}
	if !s.validGlobalAuthorization(r.Header.Get("Authorization")) {
		s.writeAuthenticationFailure(w)
		return Principal{}, false
	}
	if !requireClient {
		s.authLimiter.Refund(key)
		s.releasePreAuthRequest(r)
		return Principal{}, true
	}
	principal, err := s.registry.Authenticate(r.Header.Get("X-Ptymux-Client-Name"), r.Header.Get("X-Ptymux-Client-Password"))
	if err != nil {
		s.writeAuthenticationFailure(w)
		return Principal{}, false
	}
	s.authLimiter.Refund(key)
	s.releasePreAuthRequest(r)
	return principal, true
}

func (s *Server) validGlobalAuthorization(value string) bool {
	const prefix = "Bearer "
	provided := ""
	if strings.HasPrefix(value, prefix) {
		provided = strings.TrimPrefix(value, prefix)
	}
	digest := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(digest[:], s.tokenDigest[:]) == 1 && provided != ""
}

func (s *Server) writeAuthenticationFailure(w http.ResponseWriter) {
	s.writeError(w, http.StatusUnauthorized, ErrorAuthenticationFailed, "")
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	s.writeJSON(w, status, ManagementResponse{Version: ProtocolVersion, Error: &ProtocolError{Code: code, Message: message}})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) isClosing() bool {
	return atomic.LoadUint32(&s.closing) != 0
}

func (s *Server) serviceFor(principal Principal, create bool) *server.Service {
	s.serviceMu.Lock()
	defer s.serviceMu.Unlock()
	service := s.services[principal.OwnerID]
	if service == nil && create && !s.isClosing() {
		if s.serviceFactory != nil {
			service = s.serviceFactory(principal)
		} else {
			service = server.NewService(s.shell)
		}
		if service != nil {
			s.services[principal.OwnerID] = service
		}
	}
	return service
}

func (s *Server) removeService(ownerID string) *server.Service {
	s.serviceMu.Lock()
	service := s.services[ownerID]
	delete(s.services, ownerID)
	s.serviceMu.Unlock()
	s.createMu.Lock()
	delete(s.createLock, ownerID)
	s.createMu.Unlock()
	return service
}

func (s *Server) createLockFor(ownerID string) *sync.Mutex {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	lock := s.createLock[ownerID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.createLock[ownerID] = lock
	}
	return lock
}

func (s *Server) reserveConnection(ownerID string) string {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.isClosing() {
		return ErrorServerShuttingDown
	}
	activeTotal := 0
	for _, ownerConnections := range s.connections {
		activeTotal += len(ownerConnections)
	}
	if activeTotal+s.pendingTotal >= s.maxConnections {
		return ErrorConnectionLimit
	}
	if len(s.connections[ownerID])+s.pendingConnections[ownerID] >= s.maxConnectionsPerClient {
		return ErrorConnectionLimit
	}
	s.pendingConnections[ownerID]++
	s.pendingTotal++
	return ""
}

func (s *Server) releaseConnectionReservation(ownerID string) {
	s.connMu.Lock()
	s.releaseConnectionReservationLocked(ownerID)
	s.connMu.Unlock()
}

func (s *Server) releaseConnectionReservationLocked(ownerID string) {
	if s.pendingConnections[ownerID] <= 1 {
		delete(s.pendingConnections, ownerID)
	} else {
		s.pendingConnections[ownerID]--
	}
	if s.pendingTotal > 0 {
		s.pendingTotal--
	}
}

func (s *Server) activateConnection(connection *wsConnection) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.releaseConnectionReservationLocked(connection.principal.OwnerID)
	if s.isClosing() {
		return false
	}
	ownerConnections := s.connections[connection.principal.OwnerID]
	if ownerConnections == nil {
		ownerConnections = make(map[*wsConnection]struct{})
		s.connections[connection.principal.OwnerID] = ownerConnections
	}
	ownerConnections[connection] = struct{}{}
	return true
}

func (s *Server) removeConnection(connection *wsConnection) {
	s.connMu.Lock()
	ownerConnections := s.connections[connection.principal.OwnerID]
	delete(ownerConnections, connection)
	if len(ownerConnections) == 0 {
		delete(s.connections, connection.principal.OwnerID)
	}
	s.connMu.Unlock()
	s.notifyConnectionChange()
}

func (s *Server) closeOwnerConnections(ownerID string, match func(*wsConnection) bool, reason string) {
	s.connMu.Lock()
	var connections []*wsConnection
	for connection := range s.connections[ownerID] {
		if match(connection) {
			connections = append(connections, connection)
		}
	}
	s.connMu.Unlock()
	for _, connection := range connections {
		connection.signalClose(websocket.ClosePolicyViolation, reason)
	}
}

func (s *Server) notifyConnectionChange() {
	select {
	case s.connChanged <- struct{}{}:
	default:
	}
}

func (s *Server) connectionCount() int {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	count := 0
	for _, ownerConnections := range s.connections {
		count += len(ownerConnections)
	}
	return count
}

// Shutdown stops new upgrades, creates, and follows, asks active WebSocket
// clients to disconnect, waits until ctx expires, and then closes every owner
// service without holding connection or service-map locks during CloseAll.
func (s *Server) Shutdown(ctx context.Context) error {
	atomic.StoreUint32(&s.closing, 1)
	s.closePreAuthConnections()
	s.lifecycleMu.Lock()

	s.connMu.Lock()
	var connections []*wsConnection
	for _, ownerConnections := range s.connections {
		for connection := range ownerConnections {
			connections = append(connections, connection)
		}
	}
	s.connMu.Unlock()
	for _, connection := range connections {
		connection.signalClose(websocket.CloseGoingAway, "server_shutdown")
	}
	s.lifecycleMu.Unlock()

	var waitErr error
	for s.connectionCount() != 0 {
		select {
		case <-ctx.Done():
			waitErr = ctx.Err()
			for _, connection := range connections {
				connection.forceClose()
			}
			goto closeServices
		case <-s.connChanged:
		}
	}

closeServices:
	s.serviceMu.Lock()
	services := make([]*server.Service, 0, len(s.services))
	for _, service := range s.services {
		services = append(services, service)
	}
	s.services = make(map[string]*server.Service)
	s.serviceMu.Unlock()

	closeDone := make(chan error, 1)
	go func() {
		var closeErr error
		for _, service := range services {
			if err := service.CloseAll(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		closeDone <- closeErr
	}()
	select {
	case closeErr := <-closeDone:
		if waitErr != nil {
			return waitErr
		}
		return closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func decodeHTTPJSON(r *http.Request, value interface{}) error {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBodyBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxHTTPBodyBytes {
		return errors.New("remote: HTTP request body too large")
	}
	return decodeStrictJSON(bytes.NewReader(data), value)
}

type outboundMessage struct {
	messageType int
	data        []byte
}

type byteQueue struct {
	mu       sync.Mutex
	items    []outboundMessage
	bytes    int
	maxBytes int
	notify   chan struct{}
}

func newByteQueue(maxBytes int) *byteQueue {
	return &byteQueue{maxBytes: maxBytes, notify: make(chan struct{}, 1)}
}

func (q *byteQueue) tryPush(message outboundMessage) bool {
	q.mu.Lock()
	if len(message.data) > q.maxBytes-q.bytes {
		q.mu.Unlock()
		return false
	}
	q.items = append(q.items, message)
	q.bytes += len(message.data)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return true
}

func (q *byteQueue) pop() (outboundMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return outboundMessage{}, false
	}
	message := q.items[0]
	q.items[0] = outboundMessage{}
	q.items = q.items[1:]
	if len(q.items) == 0 {
		q.items = nil
	}
	q.bytes -= len(message.data)
	return message, true
}

type wsClose struct {
	code   int
	reason string
}

type wsConnection struct {
	server    *Server
	conn      *websocket.Conn
	principal Principal
	queue     *byteQueue
	done      chan struct{}
	closeReq  chan wsClose
	closeOnce sync.Once
	followMu  sync.Mutex
	following bool
}

func newWSConnection(remoteServer *Server, conn *websocket.Conn, principal Principal, queueBytes int) *wsConnection {
	return &wsConnection{
		server:    remoteServer,
		conn:      conn,
		principal: principal,
		queue:     newByteQueue(queueBytes),
		done:      make(chan struct{}),
		closeReq:  make(chan wsClose, 1),
	}
}

func (c *wsConnection) run() {
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c.writeLoop()
	}()
	c.readLoop()
	c.stop()
	<-writerDone
	_ = c.conn.Close()
}

func (c *wsConnection) readLoop() {
	c.conn.SetReadLimit(maxWebSocketMessageBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(webSocketReadTimeout))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(webSocketReadTimeout))
	})
	for {
		messageType, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			c.enqueueResponse(Response{Version: ProtocolVersion, Type: ResponseTypeResponse, Error: &ProtocolError{Code: ErrorBadRequest, Message: "requests must be text JSON"}})
			continue
		}
		request, err := decodeWebSocketRequest(data)
		if err != nil {
			c.enqueueResponse(Response{Version: ProtocolVersion, Type: ResponseTypeResponse, Error: &ProtocolError{Code: ErrorBadRequest, Message: "invalid JSON request"}})
			continue
		}
		if protocolErr := validateRequest(request); protocolErr != nil {
			id := request.ID
			if !validRequestID(id) {
				id = ""
			}
			c.enqueueResponse(Response{Version: ProtocolVersion, Type: ResponseTypeResponse, ID: id, Error: protocolErr})
			continue
		}
		c.handleRequest(request)
	}
}

func (c *wsConnection) writeLoop() {
	defer c.conn.Close()
	ticker := time.NewTicker(webSocketPingInterval)
	defer ticker.Stop()
	for {
		select {
		case request := <-c.closeReq:
			_ = c.writeClose(request)
			return
		default:
		}
		if message, ok := c.queue.pop(); ok {
			if err := c.writeMessage(message.messageType, message.data); err != nil {
				return
			}
			continue
		}
		select {
		case request := <-c.closeReq:
			_ = c.writeClose(request)
			return
		case <-c.done:
			return
		case <-c.queue.notify:
		case <-ticker.C:
			if err := c.writeMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *wsConnection) handleRequest(request Request) {
	switch request.Operation {
	case OperationCreate:
		c.server.lifecycleMu.RLock()
		if c.server.isClosing() || c.isDone() {
			c.server.lifecycleMu.RUnlock()
			c.enqueueResponse(errorResponse(request.ID, ErrorServerShuttingDown, ""))
			return
		}
		createLock := c.server.createLockFor(c.principal.OwnerID)
		createLock.Lock()
		service := c.server.serviceFor(c.principal, true)
		if service == nil {
			createLock.Unlock()
			c.server.lifecycleMu.RUnlock()
			c.enqueueResponse(errorResponse(request.ID, ErrorInternal, ""))
			return
		}
		if !service.TargetExists(request.Target.Session, request.Target.Pane, request.Target.Tab) && service.TargetCount() >= c.server.maxTargetsPerClient {
			createLock.Unlock()
			c.server.lifecycleMu.RUnlock()
			c.enqueueResponse(errorResponse(request.ID, ErrorTargetLimit, ""))
			return
		}
		response := service.Create(serverRequest(request))
		createLock.Unlock()
		c.server.lifecycleMu.RUnlock()
		c.enqueueServiceResponse(request.ID, response)
	case OperationList:
		service := c.server.serviceFor(c.principal, false)
		if service == nil {
			c.enqueueResponse(Response{Version: ProtocolVersion, Type: ResponseTypeResponse, ID: request.ID, Snapshot: server.Snapshot{}})
			return
		}
		c.enqueueServiceResponse(request.ID, service.Handle(serverRequest(request), false))
	case OperationSend, OperationText, OperationKeys, OperationRead:
		service := c.server.serviceFor(c.principal, false)
		if service == nil {
			c.enqueueResponse(errorResponse(request.ID, ErrorTargetNotFound, ""))
			return
		}
		c.enqueueServiceResponse(request.ID, service.Handle(serverRequest(request), false))
	case OperationClose:
		service := c.server.serviceFor(c.principal, false)
		if service == nil {
			c.enqueueResponse(errorResponse(request.ID, ErrorTargetNotFound, ""))
			return
		}
		c.enqueueServiceResponse(request.ID, service.CloseExisting(serverRequest(request)))
	case OperationFollow:
		c.startFollow(request)
	default:
		c.enqueueResponse(errorResponse(request.ID, ErrorInvalidOperation, ""))
	}
}

func (c *wsConnection) startFollow(request Request) {
	c.server.lifecycleMu.RLock()
	if c.server.isClosing() || c.isDone() {
		c.server.lifecycleMu.RUnlock()
		c.enqueueResponse(errorResponse(request.ID, ErrorServerShuttingDown, ""))
		return
	}
	service := c.server.serviceFor(c.principal, false)
	if service == nil || !service.TargetExists(request.Target.Session, request.Target.Pane, request.Target.Tab) {
		c.server.lifecycleMu.RUnlock()
		c.enqueueResponse(errorResponse(request.ID, ErrorTargetNotFound, ""))
		return
	}
	c.followMu.Lock()
	if c.following {
		c.followMu.Unlock()
		c.server.lifecycleMu.RUnlock()
		c.enqueueResponse(errorResponse(request.ID, ErrorBadRequest, "follow already active"))
		return
	}
	c.following = true
	c.followMu.Unlock()

	c.enqueueResponse(Response{Version: ProtocolVersion, Type: ResponseTypeFollowStarted, ID: request.ID})
	go func() {
		err := service.Stream(serverRequest(request), &followWriter{connection: c}, c.done, false)
		c.followMu.Lock()
		c.following = false
		c.followMu.Unlock()
		switch {
		case errors.Is(err, server.ErrTargetNotFound):
			c.enqueueResponse(followEndResponse(request.ID, ErrorTargetNotFound, ""))
		case err != nil:
			c.enqueueResponse(followEndResponse(request.ID, ErrorInternal, "follow ended"))
		default:
			c.enqueueResponse(Response{Version: ProtocolVersion, Type: ResponseTypeFollowEnded, ID: request.ID})
		}
	}()
	c.server.lifecycleMu.RUnlock()
}

func (c *wsConnection) enqueueServiceResponse(id string, response server.Response) {
	if response.Error != "" {
		code := ErrorInternal
		if response.Error == server.ErrTargetNotFound.Error() {
			code = ErrorTargetNotFound
		}
		c.enqueueResponse(errorResponse(id, code, response.Error))
		return
	}
	c.enqueueResponse(Response{
		Version:  ProtocolVersion,
		Type:     ResponseTypeResponse,
		ID:       id,
		Output:   response.Output,
		ExitCode: response.ExitCode,
		Snapshot: response.Snapshot,
	})
}

func (c *wsConnection) enqueueResponse(response Response) {
	data, err := json.Marshal(response)
	if err != nil {
		c.signalClose(websocket.CloseInternalServerErr, ErrorInternal)
		return
	}
	if !c.enqueue(outboundMessage{messageType: websocket.TextMessage, data: data}) {
		c.signalClose(websocket.ClosePolicyViolation, ErrorSlowConnection)
	}
}

func (c *wsConnection) enqueue(message outboundMessage) bool {
	if c.isDone() {
		return false
	}
	return c.queue.tryPush(message)
}

func (c *wsConnection) isDone() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *wsConnection) signalClose(code int, reason string) {
	c.closeOnce.Do(func() {
		select {
		case c.closeReq <- wsClose{code: code, reason: reason}:
		default:
		}
		close(c.done)
		select {
		case c.queue.notify <- struct{}{}:
		default:
		}
	})
}

func (c *wsConnection) stop() {
	c.closeOnce.Do(func() { close(c.done) })
}

func (c *wsConnection) forceClose() {
	_ = c.conn.Close()
}

func (c *wsConnection) writeMessage(messageType int, data []byte) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(webSocketWriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(messageType, data)
}

func (c *wsConnection) writeClose(request wsClose) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(webSocketWriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(request.code, request.reason))
}

type followWriter struct {
	connection *wsConnection
}

func (w *followWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	copyOfData := append([]byte(nil), data...)
	if !w.connection.enqueue(outboundMessage{messageType: websocket.BinaryMessage, data: copyOfData}) {
		w.connection.signalClose(websocket.ClosePolicyViolation, ErrorSlowConnection)
		return 0, errors.New(ErrorSlowConnection)
	}
	return len(data), nil
}

func decodeWebSocketRequest(data []byte) (Request, error) {
	var request Request
	if err := decodeStrictJSON(bytes.NewReader(data), &request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func serverRequest(request Request) server.Request {
	return server.Request{
		Action:    string(request.Operation),
		Session:   request.Target.Session,
		Pane:      request.Target.Pane,
		Tab:       request.Target.Tab,
		Command:   request.Input,
		ReadCount: request.ReadCount,
	}
}

func errorResponse(id, code, message string) Response {
	return Response{Version: ProtocolVersion, Type: ResponseTypeResponse, ID: id, Error: &ProtocolError{Code: code, Message: message}}
}

func followEndResponse(id, code, message string) Response {
	return Response{Version: ProtocolVersion, Type: ResponseTypeFollowEnded, ID: id, Error: &ProtocolError{Code: code, Message: message}}
}
