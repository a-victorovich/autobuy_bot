package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultGetgemsBaseURL   = "https://api.getgems.io/public-api"
	DefaultGetgemsWebURL    = "https://getgems.io"
	DefaultToncenterBaseURL = "https://toncenter.com/api/v2"
	DefaultWalletNetwork    = "mainnet"
)

// Config is the root configuration structure loaded from config.yaml.
type Config struct {
	Telegram           TelegramConfig     `yaml:"telegram"`
	Getgems            GetgemsConfig      `yaml:"getgems"`
	Toncenter          ToncenterConfig    `yaml:"toncenter"`
	Wallet             WalletConfig       `yaml:"wallet"`
	Scanner            ScannerConfig      `yaml:"scanner"`
	Collections        map[string]float64 `yaml:"collections"`      // collectionAddress -> discount percent
	GiftCollections    map[string]float64 `yaml:"gift_collections"` // gift collectionAddress -> discount percent
	RoyaltyCollections []string           `yaml:"royalty_collections"`
}

// GetgemsConfig holds credentials for the Getgems public API.
type GetgemsConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	WebURL  string `yaml:"web_url"`
	WSURL   string `yaml:"ws_url"`
	UseWS   bool   `yaml:"use_ws"`
}

// ToncenterConfig holds credentials for the TON Center API v2.
type ToncenterConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

// TelegramConfig holds Telegram bot credentials and target chat.
type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   int64  `yaml:"chat_id"`
}

// WalletConfig holds TON wallet credentials.
type WalletConfig struct {
	SecretPhrase string `yaml:"secret_phrase"`
	Network      string `yaml:"network"`
	UseV5R1      bool   `yaml:"use_v5r1"`
}

// ScannerConfig holds polling and behaviour settings.
type ScannerConfig struct {
	PollIntervalSeconds     int      `yaml:"poll_interval_seconds"`
	PurchasesEnabled        bool     `yaml:"purchases_enabled"`
	Resale                  Resale   `yaml:"resale"`
	LegacyResaleDiscountPct *float64 `yaml:"resale_discount_pct"`
	MaxPrice                float64  `yaml:"max_price_ton"`
}

type Resale struct {
	Type               string  `yaml:"type"`
	ResaleDiscountPct  float64 `yaml:"resale_discount_pct"`
	MinDiscountPercent float64 `yaml:"min_discount_percent"`
	MaxDiscountPercent float64 `yaml:"max_discount_percent"`
	Every              string  `yaml:"every"`
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

	if err := cfg.validate(path); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Apply defaults.
	if cfg.Scanner.PollIntervalSeconds == 0 {
		cfg.Scanner.PollIntervalSeconds = 30
	}
	if cfg.Scanner.Resale.Type == "" {
		cfg.Scanner.Resale.Type = "fix_price"
	}
	if cfg.Getgems.BaseURL == "" {
		cfg.Getgems.BaseURL = DefaultGetgemsBaseURL
	}
	if cfg.Getgems.WebURL == "" {
		cfg.Getgems.WebURL = DefaultGetgemsWebURL
	}
	if cfg.Toncenter.BaseURL == "" {
		cfg.Toncenter.BaseURL = DefaultToncenterBaseURL
	}
	if cfg.Wallet.Network == "" {
		cfg.Wallet.Network = DefaultWalletNetwork
	}

	return &cfg, nil
}

func (c *Config) validate(configPath string) error {
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
	if c.Wallet.Network != "" && c.Wallet.Network != "mainnet" && c.Wallet.Network != "testnet" {
		return fmt.Errorf("wallet.network must be either \"mainnet\" or \"testnet\"")
	}
	if len(c.Collections) == 0 && len(c.GiftCollections) == 0 {
		return fmt.Errorf("at least one of collections or gift_collections must be configured")
	}
	switch c.Scanner.Resale.Type {
	case "", "fix_price":
		if c.Scanner.Resale.Type == "" {
			c.Scanner.Resale.Type = "fix_price"
			if c.Scanner.LegacyResaleDiscountPct != nil {
				c.Scanner.Resale.ResaleDiscountPct = *c.Scanner.LegacyResaleDiscountPct
			}
		}
		if c.Scanner.Resale.ResaleDiscountPct < -100 || c.Scanner.Resale.ResaleDiscountPct > 100 {
			return fmt.Errorf("scanner.resale.resale_discount_pct must be between -100 and 100, got %v", c.Scanner.Resale.ResaleDiscountPct)
		}
	case "falling_price":
		if c.Scanner.Resale.MinDiscountPercent < -100 || c.Scanner.Resale.MinDiscountPercent > 100 {
			return fmt.Errorf("scanner.resale.min_discount_percent must be between -100 and 100, got %v", c.Scanner.Resale.MinDiscountPercent)
		}
		if c.Scanner.Resale.MaxDiscountPercent < -100 || c.Scanner.Resale.MaxDiscountPercent > 100 {
			return fmt.Errorf("scanner.resale.max_discount_percent must be between -100 and 100, got %v", c.Scanner.Resale.MaxDiscountPercent)
		}
		if c.Scanner.Resale.MinDiscountPercent > c.Scanner.Resale.MaxDiscountPercent {
			return fmt.Errorf("scanner.resale.min_discount_percent must be <= scanner.resale.max_discount_percent")
		}
		if c.Scanner.Resale.Every == "" {
			return fmt.Errorf("scanner.resale.every is required for falling_price")
		}
		if _, err := time.ParseDuration(c.Scanner.Resale.Every); err != nil {
			return fmt.Errorf("scanner.resale.every must be a valid duration, got %q: %w", c.Scanner.Resale.Every, err)
		}
	default:
		return fmt.Errorf("scanner.resale.type must be one of \"fix_price\" or \"falling_price\", got %q", c.Scanner.Resale.Type)
	}

	if allValue, hasAll := c.GiftCollections["all"]; hasAll {
		allGiftCollectionsPath, err := resolveGiftCollectionsPath(configPath)
		if err != nil {
			return fmt.Errorf("resolving gift_collections.yaml: %w", err)
		}

		allGiftCollections, err := loadGiftCollectionsFromYAML(allGiftCollectionsPath)
		if err != nil {
			return fmt.Errorf("expanding gift_collections all: %w", err)
		}
		for collectionAddress := range allGiftCollections {
			if collectionAddress == "all" {
				continue
			}
			if _, exists := c.GiftCollections[collectionAddress]; exists {
				continue
			}
			c.GiftCollections[collectionAddress] = allValue
		}
		delete(c.GiftCollections, "all")

		logConfiguredCollections("gift_collections", c.GiftCollections)
	}

	if err := validateCollections("collections", c.Collections); err != nil {
		return err
	}
	if err := validateCollections("gift_collections", c.GiftCollections); err != nil {
		return err
	}
	if err := validateCollectionList("royalty_collections", c.RoyaltyCollections); err != nil {
		return err
	}

	return nil
}

func validateCollections(field string, collections map[string]float64) error {
	for addr, pct := range collections {
		if addr == "" {
			return fmt.Errorf("%s contains an empty collection address", field)
		}
		if pct < -100 || pct > 100 {
			return fmt.Errorf("%s %q: percent must be between -100 and 100, got %v", field, addr, pct)
		}
	}
	return nil
}

func validateCollectionList(field string, collections []string) error {
	for _, addr := range collections {
		if addr == "" {
			return fmt.Errorf("%s contains an empty collection address", field)
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

func resolveGiftCollectionsPath(configPath string) (string, error) {
	candidates := make([]string, 0, 3)
	if configPath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(configPath), "gift_collections.yaml"))
	}

	exePath, err := os.Executable()
	if err == nil && exePath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(exePath), "gift_collections.yaml"))
	}

	candidates = append(candidates, "gift_collections.yaml")

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", os.ErrNotExist
}

func loadGiftCollectionsFromYAML(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}

	var cfg struct {
		GiftCollections map[string]float64 `yaml:"gift_collections"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %q: %w", path, err)
	}

	if cfg.GiftCollections == nil {
		cfg.GiftCollections = make(map[string]float64)
	}
	return cfg.GiftCollections, nil
}

func logConfiguredCollections(field string, collections map[string]float64) {
	if len(collections) == 0 {
		return
	}

	keys := make([]string, 0, len(collections))
	for collectionAddress := range collections {
		keys = append(keys, collectionAddress)
	}
	sort.Strings(keys)

	for _, collectionAddress := range keys {
		slog.Info("Configured collection discount",
			"field", field,
			"collection", collectionAddress,
			"percent", collections[collectionAddress],
		)
	}
}
