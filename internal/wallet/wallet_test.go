package wallet

import (
	"context"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	tonwallet "github.com/xssnick/tonutils-go/ton/wallet"
	"github.com/xssnick/tonutils-go/tvm/cell"

	"github.com/yourorg/nft-scanner/internal/config"
)

type stubTonAPI struct {
	block  *ton.BlockIDExt
	acc    *tlb.Account
	result *ton.ExecutionResult
}

func (s stubTonAPI) WaitForBlock(uint32) ton.APIClientWrapped {
	return stubAPIClientWrapped{
		acc:    s.acc,
		result: s.result,
	}
}

func (s stubTonAPI) CurrentMasterchainInfo(context.Context) (*ton.BlockIDExt, error) {
	return s.block, nil
}

func (s stubTonAPI) SendExternalMessage(context.Context, *tlb.ExternalMessage) error {
	return nil
}

func (s stubTonAPI) SendExternalMessageWaitTransaction(context.Context, *tlb.ExternalMessage) (*tlb.Transaction, *ton.BlockIDExt, []byte, error) {
	return nil, nil, nil, nil
}

func (s stubTonAPI) FindLastTransactionByInMsgHash(context.Context, *address.Address, []byte, ...int) (*tlb.Transaction, error) {
	return nil, nil
}

type stubAPIClientWrapped struct {
	acc    *tlb.Account
	result *ton.ExecutionResult
}

func (s stubAPIClientWrapped) Client() ton.LiteClient { return nil }
func (s stubAPIClientWrapped) GetTime(context.Context) (uint32, error) {
	return 0, nil
}
func (s stubAPIClientWrapped) GetLibraries(context.Context, ...[]byte) ([]*cell.Cell, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) LookupBlock(context.Context, int32, int64, uint32) (*ton.BlockIDExt, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) GetBlockData(context.Context, *ton.BlockIDExt) (*tlb.Block, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) GetBlockTransactionsV2(context.Context, *ton.BlockIDExt, uint32, ...*ton.TransactionID3) ([]ton.TransactionShortInfo, bool, error) {
	return nil, false, nil
}
func (s stubAPIClientWrapped) GetBlockShardsInfo(context.Context, *ton.BlockIDExt) ([]*ton.BlockIDExt, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) GetBlockchainConfig(context.Context, *ton.BlockIDExt, ...int32) (*ton.BlockchainConfig, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) GetMasterchainInfo(context.Context) (*ton.BlockIDExt, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) GetAccount(context.Context, *ton.BlockIDExt, *address.Address) (*tlb.Account, error) {
	return s.acc, nil
}
func (s stubAPIClientWrapped) SendExternalMessage(context.Context, *tlb.ExternalMessage) error {
	return nil
}
func (s stubAPIClientWrapped) SendExternalMessageWaitTransaction(context.Context, *tlb.ExternalMessage) (*tlb.Transaction, *ton.BlockIDExt, []byte, error) {
	return nil, nil, nil, nil
}
func (s stubAPIClientWrapped) RunGetMethod(context.Context, *ton.BlockIDExt, *address.Address, string, ...interface{}) (*ton.ExecutionResult, error) {
	return s.result, nil
}
func (s stubAPIClientWrapped) ListTransactions(context.Context, *address.Address, uint32, uint64, []byte) ([]*tlb.Transaction, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) GetTransaction(context.Context, *ton.BlockIDExt, *address.Address, uint64) (*tlb.Transaction, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) GetBlockProof(context.Context, *ton.BlockIDExt, *ton.BlockIDExt) (*ton.PartialBlockProof, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) CurrentMasterchainInfo(context.Context) (*ton.BlockIDExt, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) SubscribeOnTransactions(context.Context, *address.Address, uint64, chan<- *tlb.Transaction) {
}
func (s stubAPIClientWrapped) VerifyProofChain(context.Context, *ton.BlockIDExt, *ton.BlockIDExt) error {
	return nil
}
func (s stubAPIClientWrapped) WaitForBlock(uint32) ton.APIClientWrapped { return s }
func (s stubAPIClientWrapped) WithRetry(...int) ton.APIClientWrapped    { return s }
func (s stubAPIClientWrapped) WithTimeout(time.Duration) ton.APIClientWrapped {
	return s
}
func (s stubAPIClientWrapped) SetTrustedBlock(*ton.BlockIDExt) {}
func (s stubAPIClientWrapped) SetTrustedBlockFromConfig(*liteclient.GlobalConfig) {
}
func (s stubAPIClientWrapped) FindLastTransactionByInMsgHash(context.Context, *address.Address, []byte, ...int) (*tlb.Transaction, error) {
	return nil, nil
}
func (s stubAPIClientWrapped) FindLastTransactionByOutMsgHash(context.Context, *address.Address, []byte, ...int) (*tlb.Transaction, error) {
	return nil, nil
}

func TestNewAndGetAddress(t *testing.T) {
	words := tonwallet.NewSeed()

	w, err := New(config.WalletConfig{
		SecretPhrase: strings.Join(words, " "),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if got := w.GetAddress(); got == "" {
		t.Fatal("GetAddress returned empty string")
	}
}

func TestSignTransaction(t *testing.T) {
	words := tonwallet.NewSeed()
	instance, err := tonwallet.FromSeedWithOptions(nil, words, tonwallet.V4R2)
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	req := SendTransactionRequest{
		From:       instance.WalletAddress().String(),
		ValidUntil: time.Now().Add(time.Minute).Unix(),
		Messages: []SendTransactionMessage{
			{
				Address: instance.WalletAddress().String(),
				Amount:  "123456789",
				Payload: base64.StdEncoding.EncodeToString(cell.BeginCell().MustStoreUInt(0, 32).EndCell().ToBOC()),
			},
		},
	}

	w := &Wallet{
		words:    append([]string(nil), words...),
		instance: instance,
		api: stubTonAPI{
			block: &ton.BlockIDExt{SeqNo: 1},
			acc: &tlb.Account{
				IsActive: true,
				State: &tlb.AccountState{
					AccountStorage: tlb.AccountStorage{Status: tlb.AccountStatusActive},
				},
			},
			result: ton.NewExecutionResult([]any{big.NewInt(7)}),
		},
	}

	boc, err := w.SignTransaction(context.Background(), req)
	if err != nil {
		t.Fatalf("SignTransaction returned error: %v", err)
	}

	msgCell, err := cell.FromBOC(boc)
	if err != nil {
		t.Fatalf("cell.FromBOC returned error: %v", err)
	}

	var ext tlb.ExternalMessage
	if err := tlb.LoadFromCell(&ext, msgCell.BeginParse()); err != nil {
		t.Fatalf("LoadFromCell returned error: %v", err)
	}

	if ext.DstAddr.StringRaw() != instance.WalletAddress().StringRaw() {
		t.Fatalf("destination = %s, want %s", ext.DstAddr.StringRaw(), instance.WalletAddress().StringRaw())
	}

	if ext.StateInit != nil {
		t.Fatal("expected no state init for initialized wallet")
	}
}

func TestSignTransactionIncludesStateInitForUninitializedWallet(t *testing.T) {
	words := tonwallet.NewSeed()
	instance, err := tonwallet.FromSeedWithOptions(nil, words, tonwallet.V4R2)
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	w := &Wallet{
		words:    append([]string(nil), words...),
		instance: instance,
		api: stubTonAPI{
			block: &ton.BlockIDExt{SeqNo: 1},
			acc:   &tlb.Account{},
		},
	}

	boc, err := w.SignTransaction(context.Background(), SendTransactionRequest{
		Messages: []SendTransactionMessage{
			{
				Address: instance.WalletAddress().String(),
				Amount:  "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("SignTransaction returned error: %v", err)
	}

	msgCell, err := cell.FromBOC(boc)
	if err != nil {
		t.Fatalf("cell.FromBOC returned error: %v", err)
	}

	var ext tlb.ExternalMessage
	if err := tlb.LoadFromCell(&ext, msgCell.BeginParse()); err != nil {
		t.Fatalf("LoadFromCell returned error: %v", err)
	}

	if ext.StateInit == nil {
		t.Fatal("expected state init for uninitialized wallet")
	}
}

func TestResolveExpireAtRejectsExpired(t *testing.T) {
	_, err := resolveExpireAt(time.Now().Add(-time.Second).Unix(), time.Now())
	if err == nil {
		t.Fatal("expected expired transaction error")
	}
}
