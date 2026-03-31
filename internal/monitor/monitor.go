package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yourorg/nft-scanner/internal/config"
	"github.com/yourorg/nft-scanner/internal/getgems"
	"github.com/yourorg/nft-scanner/internal/telegram"
)

// Monitor orchestrates fetching NFTs on sale, comparing prices against the
// collection floor, and sending Telegram alerts when a deal is found.
type Monitor struct {
	cfg        *config.Config
	api        *getgems.Client
	notifier   *telegram.Notifier
	floorCache map[string]float64 // collectionAddress -> floor price
	mu         sync.RWMutex       // guards floorCache
}

// New constructs a Monitor. Call Run to start the polling loop.
func New(cfg *config.Config, api *getgems.Client, notifier *telegram.Notifier) *Monitor {
	return &Monitor{
		cfg:        cfg,
		api:        api,
		notifier:   notifier,
		floorCache: make(map[string]float64, len(cfg.Collections)),
	}
}

// Run initialises floor prices and then polls for new listings on the
// configured interval until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	slog.Info("Initialising floor prices", "collections", len(m.cfg.Collections))
	if err := m.refreshFloorPrices(ctx); err != nil {
		return fmt.Errorf("initial floor price fetch: %w", err)
	}

	interval := time.Duration(m.cfg.Scanner.PollIntervalSeconds) * time.Second
	slog.Info("Starting scan loop", "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately, then on each tick.
	if err := m.scanOnSaleNFTs(ctx); err != nil {
		slog.Error("Scan error", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("Monitor shutting down")
			return ctx.Err()
		case <-ticker.C:
			if err := m.scanOnSaleNFTs(ctx); err != nil {
				slog.Error("Scan error", "err", err)
			}
		}
	}
}

// ----- Floor prices ---------------------------------------------------------

// refreshFloorPrices fetches floor prices for all configured collections
// and stores them in the local cache.
func (m *Monitor) refreshFloorPrices(ctx context.Context) error {
	for addr := range m.cfg.Collections {
		stats, err := m.api.GetCollectionStats(ctx, addr)
		if err != nil {
			slog.Warn("Failed to fetch floor price", "collection", addr, "err", err)
			continue
		}
		m.mu.Lock()
		m.floorCache[addr] = stats.FloorPrice
		m.mu.Unlock()
		slog.Info("Floor price fetched",
			"collection", shorten(addr),
			"floorPrice", stats.FloorPrice,
		)
	}
	return nil
}

// floorPrice returns the cached floor price for a collection address.
// Returns 0 and false if not found.
func (m *Monitor) floorPrice(addr string) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fp, ok := m.floorCache[addr]
	return fp, ok
}

// ----- NFT scanning ---------------------------------------------------------

// scanOnSaleNFTs fetches all pages of on-sale NFTs for this polling cycle
// and processes each item.
func (m *Monitor) scanOnSaleNFTs(ctx context.Context) error {
	slog.Debug("Starting on-sale scan")

	var cursor string
	pageCount := 0

	for {
		resp, err := m.api.GetGiftHistory(ctx, cursor)
		if err != nil {
			return fmt.Errorf("fetching gift history (cursor=%q): %w", cursor, err)
		}

		pageCount++
		slog.Debug("Fetched page", "page", pageCount, "items", len(resp.Items), "cursor", shorten(resp.Cursor))

		for _, item := range resp.Items {
			if !m.isWatchedCollection(item.CollectionAddress) {
				slog.Debug("Skipping NFT from unwatched collection",
					"nft", shorten(item.Address),
					"collection", shorten(item.CollectionAddress),
				)
				continue
			}

			m.processItem(ctx, item)
		}

		// The API always returns a cursor. Stop when the item list is empty —
		// that signals the end of the current dataset.
		if len(resp.Items) == 0 {
			slog.Debug("Empty page received — scan complete", "pages", pageCount)
			break
		}

		if resp.Cursor == "" {
			slog.Warn("API returned non-empty items but no cursor — stopping to avoid infinite loop")
			break
		}

		cursor = resp.Cursor

		// Respect context cancellation between pages.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}

	return nil
}

// processItem checks a single NFT against its collection floor price and
// fires an alert if the listing price is below the configured threshold.
func (m *Monitor) processItem(ctx context.Context, item getgems.NftItem) {
	discountPct, watched := m.discountThreshold(item.CollectionAddress)
	if !watched {
		return
	}

	floorPrice, ok := m.floorPrice(item.CollectionAddress)
	if !ok || floorPrice <= 0 {
		slog.Warn("No floor price available for collection",
			"collection", shorten(item.CollectionAddress))
		return
	}

	// Threshold = floorPrice * (1 - discountPct/100)
	threshold := floorPrice * (1 - discountPct/100)
	price := item.Sale.FixPrice

	if price <= 0 {
		return // no valid price — skip
	}

	slog.Debug("Checking NFT",
		"nft", shorten(item.Address),
		"collection", shorten(item.CollectionAddress),
		"price", price,
		"floor", floorPrice,
		"threshold", threshold,
	)

	if price < threshold {
		discount := (1 - price/floorPrice) * 100
		msg := formatAlert(item, floorPrice, price, discount, discountPct)
		slog.Info("🔔 Signal found",
			"nft", shorten(item.Address),
			"price", price,
			"floor", floorPrice,
			"discountPct", fmt.Sprintf("%.2f%%", discount),
		)
		if err := m.notifier.SendSignal(ctx, msg); err != nil {
			slog.Error("Failed to send Telegram alert", "err", err)
		}
	}
}

func (m *Monitor) isWatchedCollection(collectionAddress string) bool {
	_, watched := m.discountThreshold(collectionAddress)
	return watched
}

func (m *Monitor) discountThreshold(collectionAddress string) (float64, bool) {
	discountPct, watched := m.cfg.Collections[collectionAddress]
	return discountPct, watched
}

// ----- Formatting -----------------------------------------------------------

func formatAlert(item getgems.NftItem, floorPrice, salePrice, actualDiscount, configuredPct float64) string {
	return fmt.Sprintf(
		"🚨 *NFT Deal Alert*\n\n"+
			"📦 *Collection:* `%s`\n"+
			"🎯 *NFT:* `%s`\n\n"+
			"💰 *Sale Price:* `%.2f TON`\n"+
			"📊 *Floor Price:* `%.2f TON`\n"+
			"📉 *Discount:* `%.2f%%` _(threshold: %.0f%%)_\n\n"+
			"🔗 https://getgems.io/nft/%s",
		item.CollectionAddress,
		item.Address,
		salePrice,
		floorPrice,
		actualDiscount,
		configuredPct,
		item.Address,
	)
}

// shorten trims long addresses/cursors for readable log output.
func shorten(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "…" + s[len(s)-6:]
}
