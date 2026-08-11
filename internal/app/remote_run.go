package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"ptymux/internal/remote"
	"ptymux/internal/server"
)

func runRemoteServer(cfg Config) (server.Response, error) {
	if cfg.Listen == "" {
		return server.Response{}, errors.New("server listen address is required")
	}
	if cfg.TokenFile == "" {
		return server.Response{}, errors.New("server token file is required")
	}
	if cfg.ClientRegistry == "" {
		return server.Response{}, errors.New("server client registry is required")
	}
	if cfg.ShutdownTimeout <= 0 {
		return server.Response{}, errors.New("server shutdown timeout must be positive")
	}

	token, err := remote.LoadServerTokenFile(cfg.TokenFile)
	if err != nil {
		return server.Response{}, err
	}
	registry, err := remote.OpenRegistry(cfg.ClientRegistry)
	if err != nil {
		return server.Response{}, err
	}
	defer registry.Close()

	remoteServer, err := remote.NewServer(remote.ServerConfig{
		Token:                   token,
		Registry:                registry,
		Shell:                   cfg.Shell,
		MaxPreAuthConnections:   cfg.MaxPreAuthConnections,
		MaxConnections:          cfg.MaxConnections,
		MaxConnectionsPerClient: cfg.MaxConnectionsPerClient,
		MaxTargetsPerClient:     cfg.MaxTargetsPerClient,
		AuthRate:                cfg.AuthRate,
		AuthBurst:               cfg.AuthBurst,
	})
	if err != nil {
		return server.Response{}, err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return server.Response{}, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	httpServer := remoteServer.NewHTTPServer(cfg.Listen)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	select {
	case err := <-serveErr:
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = remoteServer.Shutdown(shutdownContext)
		if errors.Is(err, http.ErrServerClosed) {
			return server.Response{}, nil
		}
		return server.Response{}, err
	case <-signalContext.Done():
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	remoteShutdown := make(chan error, 1)
	httpShutdown := make(chan error, 1)
	go func() { remoteShutdown <- remoteServer.Shutdown(shutdownContext) }()
	go func() { httpShutdown <- httpServer.Shutdown(shutdownContext) }()
	remoteErr := <-remoteShutdown
	httpErr := <-httpShutdown
	if httpErr != nil {
		_ = httpServer.Close()
	}
	serverErr := <-serveErr
	if remoteErr != nil && !errors.Is(remoteErr, context.Canceled) {
		return server.Response{}, remoteErr
	}
	if httpErr != nil && !errors.Is(httpErr, context.Canceled) {
		return server.Response{}, httpErr
	}
	if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
		return server.Response{}, serverErr
	}
	return server.Response{}, nil
}

func runRemoteClient(cfg Config) (server.Response, error) {
	return runRemoteClientContext(context.Background(), cfg)
}

func runRemoteClientContext(ctx context.Context, cfg Config) (server.Response, error) {
	remoteConfig := RemoteConfig{}
	if cfg.Alias != "" {
		var err error
		remoteConfig, err = LoadRemoteConfig()
		if err != nil {
			return server.Response{}, err
		}
	}
	cfg, err := ResolveRemoteConfig(cfg, remoteConfig)
	if err != nil {
		return server.Response{}, err
	}
	client, err := remote.NewClient(remote.ClientConfig{
		BaseURL:  cfg.URL,
		Token:    cfg.Token,
		Name:     cfg.ClientName,
		Password: cfg.Password,
	})
	if err != nil {
		return server.Response{}, err
	}

	switch cfg.Action {
	case ActionRegister:
		registration, err := client.Register(ctx)
		if err != nil {
			return server.Response{}, err
		}
		return server.Response{Output: registration.Password}, nil
	case ActionRotate:
		rotation, err := client.Rotate(ctx)
		if err != nil {
			return server.Response{}, err
		}
		return server.Response{Output: rotation.Password}, nil
	case ActionRevoke:
		return server.Response{}, client.Revoke(ctx)
	case ActionFollow:
		err := client.Follow(ctx, remoteRequest(cfg, remote.OperationFollow), os.Stdout)
		if errors.Is(err, context.Canceled) {
			return server.Response{}, context.Canceled
		}
		return server.Response{}, err
	default:
		operation, ok := remoteOperation(cfg.Action)
		if !ok {
			return server.Response{}, fmt.Errorf("unsupported remote action %q", cfg.Action)
		}
		request := remoteRequest(cfg, operation)
		response, err := client.Unary(ctx, request)
		if err != nil {
			return server.Response{}, err
		}
		return server.Response{
			Output:   response.Output,
			ExitCode: response.ExitCode,
			Snapshot: response.Snapshot,
		}, nil
	}
}

func remoteRequest(cfg Config, operation remote.Operation) remote.Request {
	return remote.Request{
		Operation: operation,
		Target: remote.Target{
			Session: cfg.Session,
			Pane:    cfg.Pane,
			Tab:     cfg.Tab,
		},
		Input:     cfg.Command,
		ReadCount: cfg.ReadCount,
	}
}

func remoteOperation(action Action) (remote.Operation, bool) {
	switch action {
	case ActionCreate:
		return remote.OperationCreate, true
	case ActionList:
		return remote.OperationList, true
	case ActionSend:
		return remote.OperationSend, true
	case ActionText:
		return remote.OperationText, true
	case ActionKeys:
		return remote.OperationKeys, true
	case ActionRead:
		return remote.OperationRead, true
	case ActionClose:
		return remote.OperationClose, true
	default:
		return "", false
	}
}
