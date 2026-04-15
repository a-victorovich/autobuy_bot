package wallet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
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
	words   []string
	version tonwallet.VersionConfig

	mu       sync.Mutex
	api      tonwallet.TonAPI
	instance *tonwallet.Wallet

	cft config.WalletConfig
}

type SignDataPayloadType string

const (
	SignDataPayloadTypeText   SignDataPayloadType = "text"
	SignDataPayloadTypeBinary SignDataPayloadType = "binary"
)

// New creates a wallet from config without eagerly connecting to lite servers.
func New(cfg config.WalletConfig) (*Wallet, error) {
	words, err := normalizeSecretPhrase(cfg.SecretPhrase)
	if err != nil {
		return nil, err
	}

	version := walletVersionConfig(cfg)

	instance, err := tonwallet.FromSeedWithOptions(nil, words, version)
	if err != nil {
		return nil, fmt.Errorf("create TON wallet from seed: %w", err)
	}

	return &Wallet{
		words:    append([]string(nil), words...),
		version:  version,
		instance: instance,
	}, nil
}

// GetAddress returns the current wallet address in non-bounceable form.
func (w *Wallet) GetAddress() string {
	return w.instance.WalletAddress().String()
}

func (w *Wallet) SignData(ctx context.Context, payloadType SignDataPayloadType, payload []byte, domain string, timestamp int64) (string, error) {
	_ = ctx

	if w == nil || w.instance == nil {
		return "", errors.New("wallet instance is not initialized")
	}
	if domain == "" {
		return "", errors.New("domain is required")
	}
	if timestamp <= 0 {
		return "", errors.New("timestamp must be positive unix time in seconds")
	}
	if payload == nil {
		payload = []byte{}
	}

	privateKey := w.instance.PrivateKey()
	if privateKey == nil {
		return "", errors.New("wallet private key is not set")
	}

	addr, err := address.ParseAddr(w.GetAddress())
	if err != nil {
		return "", fmt.Errorf("parse wallet address: %w", err)
	}
	addressHash := addr.Data()
	if len(addressHash) != 32 {
		return "", fmt.Errorf("unexpected wallet address hash length: %d", len(addressHash))
	}

	domainBytes := []byte(domain)

	workchainBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(workchainBuf, uint32(addr.Workchain()))

	domainLenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(domainLenBuf, uint32(len(domainBytes)))

	timestampBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(timestampBuf, uint64(timestamp))

	var payloadPrefix []byte
	switch payloadType {
	case SignDataPayloadTypeText:
		payloadPrefix = []byte("txt")
	case SignDataPayloadTypeBinary:
		payloadPrefix = []byte("bin")
	default:
		return "", fmt.Errorf("unsupported payload type: %s", payloadType)
	}

	payloadLenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(payloadLenBuf, uint32(len(payload)))

	preimage := bytes.Join([][]byte{
		{0xff, 0xff},
		[]byte("ton-connect/sign-data/"),
		workchainBuf,
		addressHash,
		domainLenBuf,
		domainBytes,
		timestampBuf,
		payloadPrefix,
		payloadLenBuf,
		payload,
	}, nil)

	digest := sha256.Sum256(preimage)
	signature := ed25519.Sign(privateKey, digest[:])
	return base64.StdEncoding.EncodeToString(signature), nil
}

func (w *Wallet) InitWalletBOC(ctx context.Context) ([]byte, error) {
	_ = ctx

	if w == nil || w.instance == nil {
		return nil, errors.New("wallet instance is not initialized")
	}

	privateKey := w.instance.PrivateKey()
	if privateKey == nil {
		return nil, errors.New("wallet private key is not set")
	}

	publicKey := privateKey.Public().(ed25519.PublicKey)
	stateInit, err := tonwallet.GetStateInit(publicKey, w.version, w.instance.GetSubwalletID())
	if err != nil {
		return nil, err
	}

	body, err := w.buildInitBody()
	if err != nil {
		return nil, err
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

func (w *Wallet) buildInitBody() (*cell.Cell, error) {
	privateKey := w.instance.PrivateKey()
	if privateKey == nil {
		return nil, errors.New("wallet private key is not set")
	}

	expireAt := uint64(time.Now().Add(defaultMessagesTTL).UTC().Unix())

	switch v := w.version.(type) {
	case tonwallet.ConfigV5R1Final:
		actions, err := tonwallet.PackV5OutActions([]*tonwallet.Message{})
		if err != nil {
			return nil, fmt.Errorf("build empty v5 action list: %w", err)
		}

		walletID := tonwallet.V5R1ID{
			NetworkGlobalID: v.NetworkGlobalID,
			WorkChain:       v.Workchain,
			SubwalletNumber: uint16(w.instance.GetSubwalletID()),
			WalletVersion:   0,
		}

		payload := cell.BeginCell().
			MustStoreUInt(0x7369676e, 32).
			MustStoreUInt(uint64(walletID.Serialized()), 32).
			MustStoreUInt(expireAt, 32).
			MustStoreUInt(0, 32).
			MustStoreBuilder(actions)

		signature := payload.EndCell().Sign(privateKey)
		return cell.BeginCell().
			MustStoreBuilder(payload).
			MustStoreSlice(signature, 512).
			EndCell(), nil
	default:
		payload := cell.BeginCell().
			MustStoreUInt(uint64(w.instance.GetSubwalletID()), 32).
			MustStoreUInt(expireAt, 32).
			MustStoreUInt(0, 32).
			MustStoreInt(0, 8)

		signature := payload.EndCell().Sign(privateKey)
		return cell.BeginCell().
			MustStoreSlice(signature, 512).
			MustStoreBuilder(payload).
			EndCell(), nil
	}
}

func (w *Wallet) BuildSignedBOC(ctx context.Context, seqno uint32, withStateInit bool, incomeTx *getgemsapi.Transaction) ([]byte, error) {
	switch v := w.version.(type) {
	case tonwallet.ConfigV5R1Final:
		return w.BuildSignedBOCV5(ctx, seqno, withStateInit, incomeTx, v)
	default:
		return w.BuildSignedBOCV4(ctx, seqno, withStateInit, incomeTx)
	}
}

func (w *Wallet) BuildSignedBOCV4(ctx context.Context, seqno uint32, withStateInit bool, incomeTx *getgemsapi.Transaction) ([]byte, error) {
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

	expireAt, err := transactionExpireAt(incomeTx, time.Now())
	if err != nil {
		return nil, err
	}

	payload := cell.BeginCell().
		MustStoreUInt(uint64(w.instance.GetSubwalletID()), 32).
		MustStoreUInt(uint64(expireAt), 32).
		MustStoreUInt(uint64(seqno), 32).
		MustStoreInt(0, 8)

	intMsgs, err := buildInternalMessageCells(incomeTx)
	if err != nil {
		return nil, err
	}

	for _, intMsg := range intMsgs {
		payload.MustStoreUInt(uint64(tonwallet.PayGasSeparately+tonwallet.IgnoreErrors), 8).MustStoreRef(intMsg)
	}

	body, err := w.signV4Payload(payload)
	if err != nil {
		return nil, err
	}

	return w.buildExternalMessageBOC(body, withStateInit)
}

func (w *Wallet) BuildSignedBOCV5(ctx context.Context, seqno uint32, withStateInit bool, incomeTx *getgemsapi.Transaction, cfg tonwallet.ConfigV5R1Final) ([]byte, error) {
	if incomeTx == nil {
		return nil, errors.New("transaction is nil")
	}
	if w == nil || w.instance == nil {
		return nil, errors.New("wallet instance is not initialized")
	}
	if incomeTx.List == nil || len(*incomeTx.List) == 0 {
		return nil, errors.New("transaction has no messages")
	}
	if len(*incomeTx.List) > 255 {
		return nil, errors.New("for this type of wallet max 255 messages can be sent at the same time")
	}

	expireAt, err := transactionExpireAt(incomeTx, time.Now())
	if err != nil {
		return nil, err
	}

	intMsgs, err := buildInternalMessageCells(incomeTx)
	if err != nil {
		return nil, err
	}

	actionsList := cell.BeginCell().EndCell()
	for _, intMsg := range intMsgs {
		action := cell.BeginCell().
			MustStoreUInt(0x0ec3c86d, 32).
			MustStoreUInt(uint64(tonwallet.PayGasSeparately+tonwallet.IgnoreErrors), 8).
			MustStoreRef(intMsg)

		actionsList = cell.BeginCell().MustStoreRef(actionsList).MustStoreBuilder(action).EndCell()
	}

	actions := cell.BeginCell().MustStoreUInt(1, 1).MustStoreRef(actionsList).MustStoreUInt(0, 1)

	walletID := tonwallet.V5R1ID{
		NetworkGlobalID: cfg.NetworkGlobalID,
		WorkChain:       cfg.Workchain,
		SubwalletNumber: uint16(w.instance.GetSubwalletID()),
		WalletVersion:   0,
	}

	payload := cell.BeginCell().
		MustStoreUInt(0x7369676e, 32).
		MustStoreUInt(uint64(walletID.Serialized()), 32).
		MustStoreUInt(uint64(expireAt), 32).
		MustStoreUInt(uint64(seqno), 32).
		MustStoreBuilder(actions)

	privateKey := w.instance.PrivateKey()
	if privateKey == nil {
		return nil, errors.New("wallet private key is not set")
	}

	signature := payload.EndCell().Sign(privateKey)
	body := cell.BeginCell().MustStoreBuilder(payload).MustStoreSlice(signature, 512).EndCell()

	return w.buildExternalMessageBOC(body, withStateInit)
}

func buildInternalMessageCells(incomeTx *getgemsapi.Transaction) ([]*cell.Cell, error) {
	intMsgs := make([]*cell.Cell, 0, len(*incomeTx.List))
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

		intMsgs = append(intMsgs, intMsg)
	}

	return intMsgs, nil
}

func transactionExpireAt(incomeTx *getgemsapi.Transaction, now time.Time) (uint32, error) {
	var validUntil int64
	if incomeTx.Timeout != nil && strings.TrimSpace(*incomeTx.Timeout) != "" {
		parsedValidUntil, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*incomeTx.Timeout))
		if err != nil {
			return 0, fmt.Errorf("parse transaction timeout: %w", err)
		}
		validUntil = parsedValidUntil.Unix()
	}

	return resolveExpireAt(validUntil, now)
}

func (w *Wallet) signV4Payload(payload *cell.Builder) (*cell.Cell, error) {
	payloadCell := payload.EndCell()

	privateKey := w.instance.PrivateKey()
	if privateKey == nil {
		return nil, errors.New("wallet private key is not set")
	}

	signature := payloadCell.Sign(privateKey)
	return cell.BeginCell().MustStoreSlice(signature, 512).MustStoreBuilder(payload).EndCell(), nil
}

func (w *Wallet) buildExternalMessageBOC(body *cell.Cell, withStateInit bool) ([]byte, error) {
	privateKey := w.instance.PrivateKey()
	if privateKey == nil {
		return nil, errors.New("wallet private key is not set")
	}

	var stateInit *tlb.StateInit
	if withStateInit {
		publicKey := privateKey.Public().(ed25519.PublicKey)
		var err error
		stateInit, err = tonwallet.GetStateInit(publicKey, w.version, w.instance.GetSubwalletID())
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

func walletVersionConfig(cfg config.WalletConfig) tonwallet.VersionConfig {
	if !cfg.UseV5R1 {
		return tonwallet.V4R2
	}

	//  -239 is mainnet, -3 is testnet
	networkID := tonwallet.MainnetGlobalID
	if strings.EqualFold(cfg.Network, "testnet") {
		networkID = tonwallet.TestnetGlobalID
	}

	return tonwallet.ConfigV5R1Final{
		NetworkGlobalID: int32(networkID),
	}
}
