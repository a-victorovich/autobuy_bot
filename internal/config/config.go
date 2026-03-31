package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure loaded from config.yaml.
type Config struct {
	Telegram    TelegramConfig     `yaml:"telegram"`
	Getgems     GetgemsConfig      `yaml:"getgems"`
	Scanner     ScannerConfig      `yaml:"scanner"`
	Collections map[string]float64 `yaml:"collections"` // collectionAddress -> discount percent
}

// GetgemsConfig holds credentials for the Getgems public API.
type GetgemsConfig struct {
	APIKey string `yaml:"api_key"`
}

// TelegramConfig holds Telegram bot credentials and target chat.
type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   int64  `yaml:"chat_id"`
}

// ScannerConfig holds polling and behaviour settings.
type ScannerConfig struct {
	PollIntervalSeconds  int `yaml:"poll_interval_seconds"`
	PriceCheckThreshold  int `yaml:"price_check_threshold"`
}

// Load reads and parses the YAML config file at the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Apply defaults.
	if cfg.Scanner.PollIntervalSeconds == 0 {
		cfg.Scanner.PollIntervalSeconds = 30
	}
	if cfg.Scanner.PriceCheckThreshold == 0 {
		cfg.Scanner.PriceCheckThreshold = 100
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	if c.Getgems.APIKey == "" {
		return fmt.Errorf("getgems.api_key is required")
	}
	if c.Telegram.BotToken == "" {
		return fmt.Errorf("telegram.bot_token is required")
	}
	if c.Telegram.ChatID == 0 {
		return fmt.Errorf("telegram.chat_id is required")
	}
	if len(c.Collections) == 0 {
		return fmt.Errorf("at least one collection must be configured")
	}
	for addr, pct := range c.Collections {
		if addr == "" {
			return fmt.Errorf("empty collection address found")
		}
		if pct < 0 || pct > 100 {
			return fmt.Errorf("collection %q: percent must be between 0 and 100, got %v", addr, pct)
		}
	}
	return nil
}