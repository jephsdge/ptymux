package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type Daemon struct {
	service *Service
	store   *Store
	options DaemonOptions

	lifecycleMu sync.Mutex
	listener    *net.UnixListener
	connections map[net.Conn]bool
	stopping    bool
	stopAck     chan struct{}
	stopAckOnce sync.Once
	stopOnce    sync.Once
	stopped     chan struct{}
	handlers    sync.WaitGroup

	activityMu   sync.Mutex
	lastActivity time.Time
}

const (
	defaultMaxLocalConnections = 128
	defaultLocalRequestTimeout = 5 * time.Second
	defaultLocalWriteTimeout   = 5 * time.Second
)

type DaemonOptions struct {
	AutoRelease           AutoReleaseOptions
	MaxConnections        int
	InitialRequestTimeout time.Duration
	WriteTimeout          time.Duration
}

type AutoReleaseOptions struct {
	Enabled           bool
	TargetIdleTimeout time.Duration
	DaemonIdleTimeout time.Duration
	SweepInterval     time.Duration
}

func NewDaemon(shell string) *Daemon {
	return NewDaemonWithOptions(shell, DaemonOptions{})
}

func NewDaemonWithOptions(shell string, options DaemonOptions) *Daemon {
	if options.MaxConnections <= 0 {
		options.MaxConnections = defaultMaxLocalConnections
	}
	if options.InitialRequestTimeout <= 0 {
		options.InitialRequestTimeout = defaultLocalRequestTimeout
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = defaultLocalWriteTimeout
	}
	service := NewService(shell)
	return &Daemon{
		service:      service,
		store:        service.Store(),
		connections:  make(map[net.Conn]bool),
		stopped:      make(chan struct{}),
		options:      options,
		lastActivity: time.Now(),
	}
}

func (d *Daemon) Serve(socketPath string) (serveErr error) {
	if socketPath == "" {
		return errors.New("missing socket path")
	}
	if err := prepareSocketPath(socketPath); err != nil {
		return err
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return err
	}
	listener.SetUnlinkOnClose(false)
	createdSocket, err := os.Lstat(socketPath)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("inspect created socket: %w", err)
	}

	d.lifecycleMu.Lock()
	if d.stopping {
		d.lifecycleMu.Unlock()
		_ = listener.Close()
		return cleanupSocketPath(socketPath, createdSocket)
	}
	d.listener = listener
	d.lifecycleMu.Unlock()

	d.startCleanupLoop()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if d.Stopped() {
				break
			}
			serveErr = err
			d.requestStop(false)
			break
		}
		if !d.registerConnection(conn) {
			_ = conn.Close()
			continue
		}
		d.handlers.Add(1)
		go d.handleTracked(conn)
	}

	d.service.StopAdmission()
	d.closeReadingConnections()
	d.waitForStopAck()
	d.closeAcceptedConnections()
	if err := d.service.CloseAll(); err != nil && serveErr == nil {
		serveErr = err
	}
	d.service.WaitOperations()
	d.handlers.Wait()
	_ = listener.Close()
	if err := cleanupSocketPath(socketPath, createdSocket); err != nil && serveErr == nil {
		serveErr = err
	}
	return serveErr
}

func (d *Daemon) registerConnection(conn net.Conn) bool {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.stopping || len(d.connections) >= d.options.MaxConnections {
		return false
	}
	d.connections[conn] = false
	return true
}

func (d *Daemon) markConnectionActive(conn net.Conn) {
	d.lifecycleMu.Lock()
	if _, ok := d.connections[conn]; ok {
		d.connections[conn] = true
	}
	d.lifecycleMu.Unlock()
}

func (d *Daemon) removeConnection(conn net.Conn) {
	d.lifecycleMu.Lock()
	delete(d.connections, conn)
	d.lifecycleMu.Unlock()
}

func (d *Daemon) handleTracked(conn net.Conn) {
	defer d.handlers.Done()
	d.handle(conn)
}

func (d *Daemon) handle(conn net.Conn) {
	defer d.removeConnection(conn)
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(d.options.InitialRequestTimeout)); err != nil {
		return
	}
	var req Request
	if err := DecodeLocalRequest(conn, &req); err != nil {
		d.writeResponse(conn, Response{Error: err.Error()})
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		d.writeResponse(conn, Response{Error: err.Error()})
		return
	}
	d.markConnectionActive(conn)
	if req.Action == "stop" {
		ack := d.requestStop(true)
		d.writeResponse(conn, Response{})
		if ack != nil {
			d.stopAckOnce.Do(func() { close(ack) })
		}
		return
	}
	if req.Action == LocalStreamEnvelopeAction {
		d.markActivity()
		d.handleFramedStream(conn, req)
		return
	}
	if IsStreamRequest(req) {
		d.markActivity()
		if err := ValidateStreamRequest(req); err != nil {
			d.writeResponse(conn, Response{Error: err.Error()})
			return
		}
		d.handleStream(conn, req)
		return
	}

	clientDone := monitorDisconnect(conn)
	resp := d.HandleWithDone(req, clientDone)
	d.writeResponse(conn, resp)
}

func monitorDisconnect(conn net.Conn) <-chan struct{} {
	clientDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(clientDone)
	}()
	return clientDone
}

type deadlineWriter struct {
	conn    net.Conn
	timeout time.Duration
}

func (w deadlineWriter) Write(data []byte) (int, error) {
	if err := w.conn.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil {
		return 0, err
	}
	return w.conn.Write(data)
}

func (d *Daemon) writeResponse(conn net.Conn, response Response) {
	_ = conn.SetWriteDeadline(time.Now().Add(d.options.WriteTimeout))
	_ = json.NewEncoder(conn).Encode(response)
}

func (d *Daemon) handleStream(conn net.Conn, req Request) {
	clientDone := monitorDisconnect(conn)
	writer := deadlineWriter{conn: conn, timeout: d.options.WriteTimeout}
	if err := d.service.Stream(req, writer, clientDone, true); err != nil {
		_, _ = io.WriteString(writer, err.Error()+"\n")
	}
}

func (d *Daemon) handleFramedStream(conn net.Conn, envelope Request) {
	writer := deadlineWriter{conn: conn, timeout: d.options.WriteTimeout}
	if err := WriteLocalStreamFrame(writer, LocalStreamFrameStarted, nil); err != nil {
		return
	}
	streamRequest := envelope
	streamRequest.Action = envelope.StreamAction
	streamRequest.StreamAction = ""
	streamRequest.StreamVersion = 0
	streamRequest.Follow = true

	var streamErr error
	if envelope.StreamVersion != LocalStreamVersion || !IsStreamRequest(streamRequest) {
		streamErr = errors.New("unsupported local stream request")
	} else {
		clientDone := monitorDisconnect(conn)
		streamErr = d.service.Stream(streamRequest, LocalStreamWriter{Writer: writer}, clientDone, true)
	}
	if streamErr != nil {
		payload := []byte(streamErr.Error())
		if len(payload) > MaxLocalStreamErrorBytes {
			payload = payload[:completeUTF8Prefix(payload[:MaxLocalStreamErrorBytes])]
		}
		if err := WriteLocalStreamFrame(writer, LocalStreamFrameError, payload); err != nil {
			return
		}
	}
	_ = WriteLocalStreamFrame(writer, LocalStreamFrameEnd, nil)
}

func (d *Daemon) Handle(req Request) Response {
	return d.HandleWithDone(req, nil)
}

func (d *Daemon) HandleWithDone(req Request, clientDone <-chan struct{}) Response {
	d.markActivity()
	if req.Action != "stop" {
		return d.service.HandleWithDone(req, true, clientDone)
	}
	d.requestStop(false)
	if err := d.service.CloseAll(); err != nil {
		return Response{Error: err.Error()}
	}
	d.service.WaitOperations()
	return Response{}
}

func (d *Daemon) startCleanupLoop() {
	if !d.options.AutoRelease.Enabled {
		return
	}
	interval := d.options.AutoRelease.SweepInterval
	if interval <= 0 {
		interval = time.Minute
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d.cleanupIdle(time.Now())
			case <-d.stopped:
				return
			}
		}
	}()
}

func (d *Daemon) cleanupIdle(now time.Time) {
	options := d.options.AutoRelease
	if !options.Enabled {
		return
	}
	if err := d.service.CloseIdleTargets(now, options.TargetIdleTimeout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
	}
	if options.DaemonIdleTimeout <= 0 || !d.service.Empty() {
		return
	}

	d.activityMu.Lock()
	idleFor := now.Sub(d.lastActivity)
	d.activityMu.Unlock()
	if idleFor >= options.DaemonIdleTimeout {
		d.requestStop(false)
	}
}

func (d *Daemon) markActivity() {
	d.activityMu.Lock()
	d.lastActivity = time.Now()
	d.activityMu.Unlock()
}

func (d *Daemon) Stopped() bool {
	select {
	case <-d.stopped:
		return true
	default:
		return false
	}
}

func (d *Daemon) requestStop(withAck bool) chan struct{} {
	d.service.StopAdmission()
	d.lifecycleMu.Lock()
	if !d.stopping {
		d.stopping = true
		if withAck {
			d.stopAck = make(chan struct{})
		}
	}
	ack := d.stopAck
	listener := d.listener
	d.lifecycleMu.Unlock()

	d.stopOnce.Do(func() { close(d.stopped) })
	if listener != nil {
		_ = listener.Close()
	}
	return ack
}

func (d *Daemon) waitForStopAck() {
	d.lifecycleMu.Lock()
	ack := d.stopAck
	d.lifecycleMu.Unlock()
	if ack != nil {
		<-ack
	}
}

func (d *Daemon) closeReadingConnections() {
	d.lifecycleMu.Lock()
	var connections []net.Conn
	for conn, active := range d.connections {
		if !active {
			connections = append(connections, conn)
		}
	}
	d.lifecycleMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (d *Daemon) closeAcceptedConnections() {
	d.lifecycleMu.Lock()
	connections := make([]net.Conn, 0, len(d.connections))
	for conn := range d.connections {
		connections = append(connections, conn)
	}
	d.lifecycleMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}
