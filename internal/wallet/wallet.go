package wallet

import (
	"context"
	"fmt"
	"strings"

	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"
	tonwallet "github.com/xssnick/tonutils-go/ton/wallet"

	"github.com/yourorg/nft-scanner/internal/config"
)

// Wallet stores TON wallet credentials together with a tonutils-go wallet
// instance ready for future blockchain operations.
type Wallet struct {
	secretPhrase string
	words        []string
	api          tonwallet.TonAPI
	instance     *tonwallet.Wallet
}

// New constructs a Wallet from a raw secret phrase string and TON network
// config URL.
func New(ctx context.Context, secretPhrase, networkConfigURL string) (*Wallet, error) {
	words, normalizedPhrase, err := normalizeSecretPhrase(secretPhrase)
	if err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, networkConfigURL)
	if err != nil {
		return nil, err
	}

	return newWithAPI(normalizedPhrase, words, api)
}

// NewFromConfig constructs a Wallet from application config.
func NewFromConfig(ctx context.Context, cfg config.WalletConfig) (*Wallet, error) {
	return New(ctx, cfg.SecretPhrase, cfg.NetworkConfigURL)
}

// SecretPhrase returns the normalized secret phrase.
func (w *Wallet) SecretPhrase() string {
	return w.secretPhrase
}

// Words returns a copy of the parsed mnemonic words.
func (w *Wallet) Words() []string {
	return append([]string(nil), w.words...)
}

// Instance returns the underlying tonutils-go wallet instance.
func (w *Wallet) Instance() *tonwallet.Wallet {
	return w.instance
}

// Address returns the wallet address in standard non-bounce format.
func (w *Wallet) Address() string {
	return w.instance.WalletAddress().String()
}

func newAPI(ctx context.Context, networkConfigURL string) (tonwallet.TonAPI, error) {
	client := liteclient.NewConnectionPool()
	if err := client.AddConnectionsFromConfigUrl(ctx, networkConfigURL); err != nil {
		return nil, fmt.Errorf("add TON lite servers from config %q: %w", networkConfigURL, err)
	}

	return ton.NewAPIClient(client).WithRetry(), nil
}

func newWithAPI(secretPhrase string, words []string, api tonwallet.TonAPI) (*Wallet, error) {
	instance, err := tonwallet.FromSeedWithOptions(api, words, tonwallet.V3)
	if err != nil {
		return nil, fmt.Errorf("create TON wallet from seed: %w", err)
	}

	return &Wallet{
		secretPhrase: secretPhrase,
		words:        append([]string(nil), words...),
		api:          api,
		instance:     instance,
	}, nil
}

func normalizeSecretPhrase(secretPhrase string) ([]string, string, error) {
	words := strings.Fields(secretPhrase)
	if len(words) != 12 && len(words) != 24 {
		return nil, "", fmt.Errorf("secret phrase must contain 12 or 24 words, got %d", len(words))
	}

	return words, strings.Join(words, " "), nil
}
