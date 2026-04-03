package wallet

import (
	"context"
	"strings"
	"testing"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	tonwallet "github.com/xssnick/tonutils-go/ton/wallet"
)

type stubTonAPI struct{}

func (stubTonAPI) WaitForBlock(uint32) ton.APIClientWrapped {
	return nil
}

func (stubTonAPI) CurrentMasterchainInfo(context.Context) (*ton.BlockIDExt, error) {
	return nil, nil
}

func (stubTonAPI) SendExternalMessage(context.Context, *tlb.ExternalMessage) error {
	return nil
}

func (stubTonAPI) SendExternalMessageWaitTransaction(context.Context, *tlb.ExternalMessage) (*tlb.Transaction, *ton.BlockIDExt, []byte, error) {
	return nil, nil, nil, nil
}

func (stubTonAPI) FindLastTransactionByInMsgHash(context.Context, *address.Address, []byte, ...int) (*tlb.Transaction, error) {
	return nil, nil
}

func TestNewWithAPI(t *testing.T) {
	t.Run("accepts valid 24-word phrase and creates tonutils wallet", func(t *testing.T) {
		words := tonwallet.NewSeed()
		phrase := strings.Join(words, " ")

		w, err := newWithAPI(phrase, words, stubTonAPI{})
		if err != nil {
			t.Fatalf("newWithAPI returned error: %v", err)
		}

		if got := w.SecretPhrase(); got != phrase {
			t.Fatalf("SecretPhrase() = %q, want %q", got, phrase)
		}
		if got := len(w.Words()); got != len(words) {
			t.Fatalf("len(Words()) = %d, want %d", got, len(words))
		}
		if w.Instance() == nil {
			t.Fatal("Instance() = nil, want tonutils wallet instance")
		}
		if got := w.Address(); got == "" {
			t.Fatal("Address() = empty, want wallet address")
		}
	})

	t.Run("normalizes extra spaces", func(t *testing.T) {
		words := tonwallet.NewSeed()
		phrase := strings.Join(words, " ")
		spacedPhrase := "  " + strings.Join(words[:12], "  ") + "   " + strings.Join(words[12:], "   ") + "  "

		normalizedWords, normalizedPhrase, err := normalizeSecretPhrase(spacedPhrase)
		if err != nil {
			t.Fatalf("normalizeSecretPhrase returned error: %v", err)
		}

		w, err := newWithAPI(normalizedPhrase, normalizedWords, stubTonAPI{})
		if err != nil {
			t.Fatalf("newWithAPI returned error: %v", err)
		}

		if got := w.SecretPhrase(); got != phrase {
			t.Fatalf("SecretPhrase() = %q, want %q", got, phrase)
		}
	})

	t.Run("rejects invalid word count", func(t *testing.T) {
		_, _, err := normalizeSecretPhrase("one two three")
		if err == nil {
			t.Fatal("normalizeSecretPhrase returned nil error, want validation error")
		}
	})
}
