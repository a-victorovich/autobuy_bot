package wallet

import (
	"fmt"
	"strings"

	"github.com/yourorg/nft-scanner/internal/config"
)

// Wallet stores TON wallet credentials and parsed mnemonic words.
// It is a lightweight foundation for future transaction creation and
// subscription flows.
type Wallet struct {
	secretPhrase string
	words        []string
}

// New constructs a Wallet from a raw secret phrase string.
func New(secretPhrase string) (*Wallet, error) {
	words := strings.Fields(secretPhrase)
	if len(words) != 12 && len(words) != 24 {
		return nil, fmt.Errorf("secret phrase must contain 12 or 24 words, got %d", len(words))
	}

	return &Wallet{
		secretPhrase: strings.Join(words, " "),
		words:        words,
	}, nil
}

// NewFromConfig constructs a Wallet from application config.
func NewFromConfig(cfg config.WalletConfig) (*Wallet, error) {
	return New(cfg.SecretPhrase)
}
