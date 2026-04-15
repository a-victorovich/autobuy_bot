package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/yourorg/nft-scanner/internal/config"
	"github.com/yourorg/nft-scanner/internal/getgems"
	"github.com/yourorg/nft-scanner/internal/monitor"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML config file")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	nftAddress := flag.String("nft-address", "", "NFT address")
	priceNano := flag.Int64("price-nano", 0, "new sale price in nano TON")
	flag.Parse()

	setupLogger(*logLevel)

	if *nftAddress == "" {
		slog.Error("nft-address is required")
		os.Exit(1)
	}
	if *priceNano <= 0 {
		slog.Error("price-nano must be greater than 0")
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "err", err)
		os.Exit(1)
	}

	apiClient := getgems.New(cfg.Getgems.APIKey, cfg.Getgems.BaseURL)
	mon := monitor.New(cfg, apiClient, nil)

	ctx := context.Background()

	if err := mon.InitWallet(ctx); err != nil {
		slog.Error("Failed to initialize wallet", "err", err)
		os.Exit(1)
	}

	if err := mon.PutUpOffchainForSaleAttempt(ctx, *nftAddress, *priceNano); err != nil {
		slog.Error("Failed to put up offchain NFT for sale", "nft", *nftAddress, "priceNano", *priceNano, "err", err)
		os.Exit(1)
	}

	slog.Info("Offchain put-up attempt finished", "nft", *nftAddress, "priceNano", *priceNano)
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
