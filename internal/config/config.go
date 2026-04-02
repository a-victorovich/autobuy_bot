package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultGetgemsBaseURL = "https://api.getgems.io/public-api"
	DefaultGetgemsWebURL  = "https://getgems.io"
)

// Config is the root configuration structure loaded from config.yaml.
type Config struct {
	Telegram        TelegramConfig     `yaml:"telegram"`
	Getgems         GetgemsConfig      `yaml:"getgems"`
	Wallet          WalletConfig       `yaml:"wallet"`
	Scanner         ScannerConfig      `yaml:"scanner"`
	Collections     map[string]float64 `yaml:"collections"`      // collectionAddress -> discount percent
	GiftCollections map[string]float64 `yaml:"gift_collections"` // gift collectionAddress -> discount percent
}

// GetgemsConfig holds credentials for the Getgems public API.
type GetgemsConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	WebURL  string `yaml:"web_url"`
}

// TelegramConfig holds Telegram bot credentials and target chat.
type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   int64  `yaml:"chat_id"`
}

// WalletConfig holds TON wallet credentials.
type WalletConfig struct {
	SecretPhrase string `yaml:"secret_phrase"`
}

// ScannerConfig holds polling and behaviour settings.
type ScannerConfig struct {
	PollIntervalSeconds int `yaml:"poll_interval_seconds"`
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
	if cfg.Getgems.BaseURL == "" {
		cfg.Getgems.BaseURL = DefaultGetgemsBaseURL
	}
	if cfg.Getgems.WebURL == "" {
		cfg.Getgems.WebURL = DefaultGetgemsWebURL
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
	if c.Wallet.SecretPhrase != "" {
		if err := validateSecretPhrase(c.Wallet.SecretPhrase); err != nil {
			return fmt.Errorf("wallet.secret_phrase: %w", err)
		}
	}
	if len(c.Collections) == 0 && len(c.GiftCollections) == 0 {
		return fmt.Errorf("at least one of collections or gift_collections must be configured")
	}
	if err := validateCollections("collections", c.Collections); err != nil {
		return err
	}
	if err := validateCollections("gift_collections", c.GiftCollections); err != nil {
		return err
	}
	return nil
}

func validateCollections(field string, collections map[string]float64) error {
	for addr, pct := range collections {
		if addr == "" {
			return fmt.Errorf("%s contains an empty collection address", field)
		}
		if pct < 0 || pct > 100 {
			return fmt.Errorf("%s %q: percent must be between 0 and 100, got %v", field, addr, pct)
		}
	}
	return nil
}

func validateSecretPhrase(phrase string) error {
	words := splitSecretPhrase(phrase)
	if len(words) != 12 && len(words) != 24 {
		return fmt.Errorf("must contain 12 or 24 words, got %d", len(words))
	}
	return nil
}

func splitSecretPhrase(phrase string) []string {
	return strings.Fields(phrase)
}
