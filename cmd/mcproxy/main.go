package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/FlameInTheDark/mcproxy/internal/config"
	"github.com/FlameInTheDark/mcproxy/internal/transport"
)

func main() {
	// Default config is mcp-proxy.yaml in the current working directory
	configPath := flag.String("config", "mcp-proxy.yaml", "Path to configuration file")
	transportMode := flag.String("transport", "", "Transport mode: http or stdio")
	flag.Parse()

	// Logger must write to Stderr to avoid interfering with Stdio transport
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("Failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}

	// Override config with flag if provided
	if *transportMode != "" {
		cfg.Server.Transport = *transportMode
	}

	srv, err := transport.NewServer(cfg, logger, transport.DefaultClientFactory)
	if err != nil {
		logger.Error("Failed to initialize server", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		logger.Info("Received signal, shutting down", "signal", sig)
		cancel()
	}()

	if err := srv.Start(ctx); err != nil {
		logger.Error("Server error", "error", err)
		os.Exit(1)
	}
}
