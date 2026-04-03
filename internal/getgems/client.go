package getgems

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/yourorg/nft-scanner/internal/config"
)

const (
	defaultTimeout = 15 * time.Second
)

type APIResponse[T any] struct {
	Success  bool `json:"success"`
	Response T    `json:"response"`
}

// ----- Domain models --------------------------------------------------------

// PutUpForSaleTypeData holds pricing data for a listed NFT.
type PutUpForSaleTypeData struct {
	PriceNano string `json:"priceNano"`
	Currency  string `json:"currency"`
}

type NftKind string

const (
	NftKindCollectionItem NftKind = "CollectionItem"
	NftKindDNSItem        NftKind = "DnsItem"
	NftKindOffchainNFT    NftKind = "OffchainNft"
)

// NftItemHistoryEvent represents a single NFT history event returned by the API.
type NftItemHistoryEvent struct {
	Address           string               `json:"address"`
	CollectionAddress string               `json:"collectionAddress"`
	TypeData          PutUpForSaleTypeData `json:"typeData"`
}

// GiftHistoryResponse is the envelope returned by /v1/nfts/history/gifts.
type GiftHistoryResponse struct {
	Items  []NftItemHistoryEvent `json:"items"`
	Cursor string                `json:"cursor"`
}

// CollectionStats is returned by /v1/collection/stats/{collectionAddress}.
type CollectionStats struct {
	FloorPrice     float64 `json:"floorPrice"`
	FloorPriceNano string  `json:"floorPriceNano"`
}

type NftSaleType string

const (
	NftSaleTypeFixPriceSale NftSaleType = "FixPriceSale"
	NftSaleTypeAuction      NftSaleType = "Auction"
)

// NftResponse is returned by /v1/nft/{nftAddress}.
type NftResponse struct {
	Kind NftKind `json:"kind"`
	Sale NftSale `json:"sale"`
}

// NftSale stores fields shared across supported sale payloads.
type NftSale struct {
	Type         NftSaleType `json:"type"`
	FullPrice    string      `json:"fullPrice"`
	Currency     string      `json:"currency"`
	Version      string      `json:"version"`
	ContractType string      `json:"contractType"`
}

type createBuyTxRequest struct {
	Version string `json:"version"`
}

// ----- Client ---------------------------------------------------------------

// Client is a thin HTTP wrapper around the Getgems public API.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
}

// New creates a Client with a sensible timeout.
// apiKey is sent as the Authorization header on every request.
func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = config.DefaultGetgemsBaseURL
	}

	return &Client{
		http:    &http.Client{Timeout: defaultTimeout},
		baseURL: baseURL,
		apiKey:  apiKey,
	}
}

// GetGiftHistory fetches a page of gift history records.
// Pass an empty cursor to omit the after parameter.
func (c *Client) GetGiftHistory(ctx context.Context, cursor string, reverse bool, limit int) (*GiftHistoryResponse, error) {
	params := url.Values{}
	params.Set("reverse", fmt.Sprintf("%t", reverse))
	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}
	params.Set("types[]", "putUpForSale")
	if cursor != "" {
		params.Set("after", cursor)
	}

	endpoint := fmt.Sprintf("%s/v1/nfts/history/gifts?%s", c.baseURL, params.Encode())

	var result GiftHistoryResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("GetGiftHistory: %w", err)
	}
	return &result, nil
}

// GetNftHistory fetches a page of history records for a collection.
// Pass an empty cursor to omit the after parameter.
func (c *Client) GetNftHistory(ctx context.Context, collectionAddress, cursor string, reverse bool, limit int) (*GiftHistoryResponse, error) {
	params := url.Values{}
	params.Set("reverse", fmt.Sprintf("%t", reverse))
	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}
	params.Set("types[]", "putUpForSale")
	if cursor != "" {
		params.Set("after", cursor)
	}

	endpoint := fmt.Sprintf(
		"%s/v1/collection/history/%s?%s",
		c.baseURL,
		url.PathEscape(collectionAddress),
		params.Encode(),
	)

	var result GiftHistoryResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("GetNftHistory(%s): %w", collectionAddress, err)
	}
	return &result, nil
}

// GetCollectionStats fetches floor-price stats for a collection.
func (c *Client) GetCollectionStats(ctx context.Context, collectionAddress string) (*CollectionStats, error) {
	endpoint := fmt.Sprintf("%s/v1/collection/stats/%s", c.baseURL, url.PathEscape(collectionAddress))

	var result CollectionStats
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("GetCollectionStats(%s): %w", collectionAddress, err)
	}
	return &result, nil
}

// GetNft fetches NFT details for a single NFT address.
func (c *Client) GetNft(ctx context.Context, nftAddress string) (*NftResponse, error) {
	endpoint := fmt.Sprintf("%s/v1/nft/%s", c.baseURL, url.PathEscape(nftAddress))

	var result NftResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("GetNft(%s): %w", nftAddress, err)
	}
	return &result, nil
}

// CreateBuyTx creates a transaction payload for buying a fixed-price NFT.
func (c *Client) CreateBuyTx(ctx context.Context, nftAddress, version string) (json.RawMessage, error) {
	endpoint := fmt.Sprintf("%s/v1/nfts/buy-fix-price/%s", c.baseURL, url.PathEscape(nftAddress))

	body := createBuyTxRequest{
		Version: version,
	}

	var result json.RawMessage
	if err := c.post(ctx, endpoint, body, &result); err != nil {
		return nil, fmt.Errorf("CreateBuyTx(%s): %w", nftAddress, err)
	}
	return result, nil
}

// ----- internal helpers -----------------------------------------------------

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	return c.do(ctx, http.MethodGet, endpoint, nil, out)
}

func (c *Client) post(ctx context.Context, endpoint string, payload, out any) error {
	return c.do(ctx, http.MethodPost, endpoint, payload, out)
}

func (c *Client) do(ctx context.Context, method, endpoint string, payload, out any) error {
	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", c.apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var wrapped APIResponse[json.RawMessage]
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return fmt.Errorf("decoding API wrapper: %w", err)
	}

	if !wrapped.Success {
		return fmt.Errorf("api returned success=false: %s", truncate(string(wrapped.Response), 200))
	}

	if err := json.Unmarshal(wrapped.Response, out); err != nil {
		return fmt.Errorf("decoding inner response: %w", err)
	}

	return nil
}
