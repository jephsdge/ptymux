package app

import (
	"context"
	"fmt"

	"ptymux/internal/server"
)

func Run(cfg Config) (server.Response, error) {
	return RunContext(context.Background(), cfg)
}

func RunContext(ctx context.Context, cfg Config) (server.Response, error) {
	switch cfg.Mode {
	case "", ModeLocal:
		return RunLocalContext(ctx, cfg)
	case ModeServer:
		return RunServer(cfg)
	case ModeClient:
		return RunClientContext(ctx, cfg)
	default:
		return server.Response{}, fmt.Errorf("unknown mode %q", cfg.Mode)
	}
}

func RunLocal(cfg Config) (server.Response, error) {
	return RunLocalContext(context.Background(), cfg)
}

func RunLocalContext(ctx context.Context, cfg Config) (server.Response, error) {
	return runLocalContext(ctx, cfg)
}

func RunClient(cfg Config) (server.Response, error) {
	return RunClientContext(context.Background(), cfg)
}

func RunClientContext(ctx context.Context, cfg Config) (server.Response, error) {
	return runRemoteClientContext(ctx, cfg)
}

func RunServer(cfg Config) (server.Response, error) {
	return runRemoteServer(cfg)
}
