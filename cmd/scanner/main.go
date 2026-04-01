package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourorg/nft-scanner/internal/config"
	"github.com/yourorg/nft-scanner/internal/getgems"
	"github.com/yourorg/nft-scanner/internal/monitor"
	"github.com/yourorg/nft-scanner/internal/telegram"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML config file")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	setupLogger(*logLevel)

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "err", err)
		os.Exit(1)
	}
	slog.Info("Config loaded",
		"collections", len(cfg.Collections),
		"giftCollections", len(cfg.GiftCollections),
		"pollInterval", cfg.Scanner.PollIntervalSeconds,
	)

	apiClient := getgems.New(cfg.Getgems.APIKey)

	notifier, err := telegram.New(cfg.Telegram.BotToken, cfg.Telegram.ChatID)
	if err != nil {
		slog.Error("Failed to initialise Telegram notifier", "err", err)
		os.Exit(1)
	}

	mon := monitor.New(cfg, apiClient, notifier)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mon.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("Monitor exited with error", "err", err)
		os.Exit(1)
	}

	slog.Info("Shutdown complete")
}

func setupLogger(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	slog.SetDefault(slog.New(handler))
}
