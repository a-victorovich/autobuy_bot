package wallet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	// "log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xssnick/tonutils-go/address"
	// "github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	// "github.com/xssnick/tonutils-go/ton"
	tonwallet "github.com/xssnick/tonutils-go/ton/wallet"
	"github.com/xssnick/tonutils-go/tvm/cell"

	"github.com/yourorg/nft-scanner/internal/config"
	getgemsapi "github.com/yourorg/nft-scanner/internal/getgems/openapi"
)

const defaultMessagesTTL = 3 * time.Minute

// SendTransactionRequest is the TON Connect 2.0 sendTransaction payload.
type SendTransactionRequest struct {
	From       string                   `json:"from,omitempty"`
	Network    string                   `json:"network,omitempty"`
	ValidUntil int64                    `json:"validUntil,omitempty"`
	Messages   []SendTransactionMessage `json:"messages"`
}

// SendTransactionMessage is a single outgoing internal message.
type SendTransactionMessage struct {
	Address   string `json:"address"`
	Amount    string `json:"amount"`
	Payload   string `json:"payload,omitempty"`
	StateInit string `json:"stateInit,omitempty"`
}

// Wallet wraps a TON wallet created from the configured mnemonic.
type Wallet struct {
	words            []string
	networkConfigURL string

	mu       sync.Mutex
	api      tonwallet.TonAPI
	instance *tonwallet.Wallet
}

// New creates a wallet from config without eagerly connecting to lite servers.
func New(cfg config.WalletConfig) (*Wallet, error) {
	words, err := normalizeSecretPhrase(cfg.SecretPhrase)
	if err != nil {
		return nil, err
	}

	instance, err := tonwallet.FromSeedWithOptions(nil, words, tonwallet.V4R2)
	if err != nil {
		return nil, fmt.Errorf("create TON wallet from seed: %w", err)
	}

	return &Wallet{
		words:            append([]string(nil), words...),
		networkConfigURL: cfg.NetworkConfigURL,
		instance:         instance,
	}, nil
}

// GetAddress returns the current wallet address in non-bounceable form.
func (w *Wallet) GetAddress() string {
	return w.instance.WalletAddress().String()
}

func (w *Wallet) BuildSignedBOC(ctx context.Context, seqno uint32, withStateInit bool, incomeTx *getgemsapi.Transaction) ([]byte, error) {
	if incomeTx == nil {
		return nil, errors.New("transaction is nil")
	}
	if w == nil || w.instance == nil {
		return nil, errors.New("wallet instance is not initialized")
	}
	if incomeTx.List == nil || len(*incomeTx.List) == 0 {
		return nil, errors.New("transaction has no messages")
	}
	if len(*incomeTx.List) > 4 {
		return nil, errors.New("for this type of wallet max 4 messages can be sent in the same time")
	}

	var validUntil int64
	if incomeTx.Timeout != nil && strings.TrimSpace(*incomeTx.Timeout) != "" {
		parsedValidUntil, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*incomeTx.Timeout))
		if err != nil {
			return nil, fmt.Errorf("parse transaction timeout: %w", err)
		}
		validUntil = parsedValidUntil.Unix()
	}

	expireAt, err := resolveExpireAt(validUntil, time.Now())
	if err != nil {
		return nil, err
	}

	payload := cell.BeginCell().
		MustStoreUInt(uint64(w.instance.GetSubwalletID()), 32).
		MustStoreUInt(uint64(expireAt), 32).
		MustStoreUInt(uint64(seqno), 32).
		MustStoreInt(0, 8)

	for i, item := range *incomeTx.List {
		if item.To == nil || strings.TrimSpace(*item.To) == "" {
			return nil, fmt.Errorf("message %d has empty destination", i)
		}
		if item.Amount == nil || strings.TrimSpace(*item.Amount) == "" {
			return nil, fmt.Errorf("message %d has empty amount", i)
		}

		dst, err := address.ParseAddr(strings.TrimSpace(*item.To))
		if err != nil {
			return nil, fmt.Errorf("parse message %d destination: %w", i, err)
		}

		amount, err := tlb.FromNanoTONStr(strings.TrimSpace(*item.Amount))
		if err != nil {
			return nil, fmt.Errorf("parse message %d amount: %w", i, err)
		}

		var body *cell.Cell
		if item.Payload != nil && strings.TrimSpace(*item.Payload) != "" {
			payloadBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*item.Payload))
			if err != nil {
				return nil, fmt.Errorf("decode message %d payload: %w", i, err)
			}

			body, err = cell.FromBOC(payloadBytes)
			if err != nil {
				return nil, fmt.Errorf("parse message %d payload BOC: %w", i, err)
			}
		}

		var stateInit *tlb.StateInit
		if item.StateInit != nil && strings.TrimSpace(*item.StateInit) != "" {
			stateInitCell, err := decodeCellBOC(strings.TrimSpace(*item.StateInit))
			if err != nil {
				return nil, fmt.Errorf("decode message %d state init: %w", i, err)
			}

			if stateInitCell != nil {
				var loadedStateInit tlb.StateInit
				if err := tlb.LoadFromCell(&loadedStateInit, stateInitCell.BeginParse()); err != nil {
					return nil, fmt.Errorf("parse message %d state init: %w", i, err)
				}
				stateInit = &loadedStateInit
			}
		}

		intMsg, err := tlb.ToCell(&tlb.InternalMessage{
			IHRDisabled: true,
			Bounce:      dst.IsBounceable(),
			DstAddr:     dst,
			Amount:      amount,
			StateInit:   stateInit,
			Body:        body,
		})
		if err != nil {
			return nil, fmt.Errorf("convert internal message %d to cell: %w", i, err)
		}

		payload.MustStoreUInt(uint64(tonwallet.PayGasSeparately+tonwallet.IgnoreErrors), 8).MustStoreRef(intMsg)
	}

	payloadCell := payload.EndCell()

	privateKey := w.instance.PrivateKey()
	if privateKey == nil {
		return nil, errors.New("wallet private key is not set")
	}

	signature := payloadCell.Sign(privateKey)
	body := cell.BeginCell().MustStoreSlice(signature, 512).MustStoreBuilder(payload).EndCell()

	var stateInit *tlb.StateInit
	if withStateInit {
		publicKey := privateKey.Public().(ed25519.PublicKey)
		stateInit, err = tonwallet.GetStateInit(publicKey, tonwallet.V4R2, w.instance.GetSubwalletID())
		if err != nil {
			return nil, fmt.Errorf("build wallet state init: %w", err)
		}
	}

	msgCell, err := tlb.ToCell(&tlb.ExternalMessage{
		DstAddr:   w.instance.WalletAddress(),
		StateInit: stateInit,
		Body:      body,
	})
	if err != nil {
		return nil, fmt.Errorf("serialize external message: %w", err)
	}

	return msgCell.ToBOC(), nil
}

func decodeStateInit(value string) (*tlb.StateInit, error) {
	cl, err := decodeCellBOC(value)
	if err != nil || cl == nil {
		return nil, err
	}

	var stateInit tlb.StateInit
	if err := tlb.LoadFromCell(&stateInit, cl.BeginParse()); err != nil {
		return nil, fmt.Errorf("parse state init: %w", err)
	}

	return &stateInit, nil
}

func decodeCellBOC(value string) (*cell.Cell, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	raw, err := decodeBase64(value)
	if err != nil {
		return nil, err
	}

	cl, err := cell.FromBOC(raw)
	if err != nil {
		return nil, fmt.Errorf("parse BOC: %w", err)
	}

	return cl, nil
}

func decodeBase64(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	decoders := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}

	for _, enc := range decoders {
		data, err := enc.DecodeString(trimmed)
		if err == nil {
			return data, nil
		}
	}

	return nil, errors.New("invalid base64 value")
}

func resolveExpireAt(validUntil int64, now time.Time) (uint32, error) {
	if validUntil == 0 {
		return uint32(now.Add(defaultMessagesTTL).Unix()), nil
	}

	if validUntil <= now.Unix() {
		return 0, errors.New("transaction is already expired")
	}

	return uint32(validUntil), nil
}

func normalizeSecretPhrase(secretPhrase string) ([]string, error) {
	words := strings.Fields(secretPhrase)
	if len(words) != 12 && len(words) != 24 {
		return nil, fmt.Errorf("secret phrase must contain 12 or 24 words, got %d", len(words))
	}

	return words, nil
}
