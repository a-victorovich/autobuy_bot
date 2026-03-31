package getgems

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	baseURL        = "https://api.getgems.io/public-api"
	defaultTimeout = 15 * time.Second
)

type APIResponse[T any] struct {
	Success  bool `json:"success"`
	Response T    `json:"response"`
}

// ----- Domain models --------------------------------------------------------

// SaleInfo holds pricing data for a listed NFT.
type SaleInfo struct {
	FixPrice float64 `json:"fix_price"`
}

// NftItem represents a single NFT returned by the on-sale endpoint.
type NftItem struct {
	Address           string   `json:"address"`
	CollectionAddress string   `json:"collectionAddress"`
	Sale              SaleInfo `json:"sale"`
}

// OnSaleResponse is the envelope returned by /v1/nfts/offchain/on-sale/gifts.
type OnSaleResponse struct {
	Items  []NftItem `json:"items"`
	Cursor string    `json:"cursor"`
}

// CollectionStats is returned by /v1/collection/stats/{collectionAddress}.
type CollectionStats struct {
	FloorPrice     float64 `json:"floorPrice"`
	FloorPriceNano string  `json:"floorPriceNano"`
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
func New(apiKey string) *Client {
	return &Client{
		http:    &http.Client{Timeout: defaultTimeout},
		baseURL: baseURL,
		apiKey:  apiKey,
	}
}

// GetOnSaleGifts fetches a page of on-sale NFT gifts.
// Pass an empty cursor to start from the beginning.
func (c *Client) GetOnSaleGifts(ctx context.Context, cursor string) (*OnSaleResponse, error) {
	endpoint := c.baseURL + "/v1/nfts/offchain/on-sale/gifts"

	if cursor != "" {
		params := url.Values{}
		params.Set("after", cursor)
		endpoint += "?" + params.Encode()
	}

	var result OnSaleResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("GetOnSaleGifts: %w", err)
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

// ----- internal helpers -----------------------------------------------------

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}


func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", c.apiKey)

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

	// 👉 ВАЖНО: используем обёртку
	var wrapped APIResponse[json.RawMessage]
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return fmt.Errorf("decoding API wrapper: %w", err)
	}

	if !wrapped.Success {
		return fmt.Errorf("api returned success=false: %s", truncate(string(wrapped.Response), 200))
	}

	// 👉 теперь декодируем вложенный response в нужный тип
	if err := json.Unmarshal(wrapped.Response, out); err != nil {
		return fmt.Errorf("decoding inner response: %w", err)
	}

	return nil
}
