package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
	cacheSize := len(cfg.Collections) + len(cfg.GiftCollections)
	return &Monitor{
		cfg:        cfg,
		api:        api,
		notifier:   notifier,
		floorCache: make(map[string]float64, cacheSize),
	}
}

const (
	initialCursorLimit = 1
	historyBatchLimit  = 10
)

// Run initialises floor prices and then polls for new listings until ctx is
// cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	slog.Info("Initialising floor prices",
		"collections", len(m.cfg.Collections),
		"giftCollections", len(m.cfg.GiftCollections),
	)
	if err := m.refreshFloorPrices(ctx); err != nil {
		return fmt.Errorf("initial floor price fetch: %w", err)
	}

	interval := time.Duration(m.cfg.Scanner.PollIntervalSeconds) * time.Second
	slog.Info("Starting history loops", "interval", interval)

	var giftCursor string
	if m.hasGiftCollections() {
		var err error
		giftCursor, err = m.bootstrapGiftCursor(ctx)
		if err != nil {
			return fmt.Errorf("bootstrapping gift history cursor: %w", err)
		}
	}

	collectionCursors := make(map[string]string, len(m.cfg.Collections))
	if m.hasCollections() {
		var err error
		collectionCursors, err = m.bootstrapNftCursors(ctx)
		if err != nil {
			return fmt.Errorf("bootstrapping collection history cursors: %w", err)
		}
	}

	for {
		slog.Debug("Starting monitor iteration",
			"giftCursor", shorten(giftCursor),
			"collectionCursors", len(collectionCursors),
		)

		immediate := false

		if m.hasGiftCollections() {
			nextCursor, giftImmediate, err := m.scanGiftHistoryBatch(ctx, giftCursor)
			if err != nil {
				slog.Error("Gift scan error", "err", err)
			} else if nextCursor != "" {
				giftCursor = nextCursor
			}
			immediate = immediate || giftImmediate
		}

		for collectionAddress, cursor := range collectionCursors {
			nextCursor, collectionImmediate, err := m.scanNftHistoryBatch(ctx, collectionAddress, cursor)
			if err != nil {
				slog.Error("Collection scan error",
					"collection", shorten(collectionAddress),
					"err", err,
				)
				continue
			}
			if nextCursor != "" {
				collectionCursors[collectionAddress] = nextCursor
			}
			immediate = immediate || collectionImmediate
		}

		if immediate {
			continue
		}

		select {
		case <-ctx.Done():
			slog.Info("Monitor shutting down")
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// ----- Floor prices ---------------------------------------------------------

// refreshFloorPrices fetches floor prices for all configured collections
// and stores them in the local cache.
func (m *Monitor) refreshFloorPrices(ctx context.Context) error {
	for _, addr := range m.watchedCollections() {
		stats, err := m.api.GetCollectionStats(ctx, addr)
		if err != nil {
			slog.Warn("Failed to fetch floor price", "collection", addr, "err", err)
			continue
		}

		floorPrice, err := strconv.ParseFloat(stats.FloorPriceNano, 64)
		if err != nil {
			slog.Warn("Failed to parse floor price nano",
				"collection", addr,
				"floorPriceNano", stats.FloorPriceNano,
				"err", err,
			)
			continue
		}

		m.mu.Lock()
		m.floorCache[addr] = floorPrice
		m.mu.Unlock()
		slog.Info("Floor price fetched",
			"collection", shorten(addr),
			"floorPriceNano", floorPrice,
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

func (m *Monitor) bootstrapGiftCursor(ctx context.Context) (string, error) {
	resp, err := m.api.GetGiftHistory(ctx, "", false, initialCursorLimit)
	if err != nil {
		return "", fmt.Errorf("fetching initial gift history cursor: %w", err)
	}

	slog.Info("Bootstrapped gift history cursor",
		"items", len(resp.Items),
		"cursor", resp.Cursor,
	)

	return resp.Cursor, nil
}

func (m *Monitor) bootstrapNftCursors(ctx context.Context) (map[string]string, error) {
	cursors := make(map[string]string, len(m.cfg.Collections))
	for collectionAddress := range m.cfg.Collections {
		cursor, err := m.bootstrapNftCursor(ctx, collectionAddress)
		if err != nil {
			return nil, err
		}
		cursors[collectionAddress] = cursor
	}
	return cursors, nil
}

func (m *Monitor) bootstrapNftCursor(ctx context.Context, collectionAddress string) (string, error) {
	resp, err := m.api.GetNftHistory(ctx, collectionAddress, "", false, initialCursorLimit)
	if err != nil {
		return "", fmt.Errorf("fetching initial collection history cursor for %s: %w", collectionAddress, err)
	}

	slog.Info("Bootstrapped collection history cursor",
		"collection", shorten(collectionAddress),
		"items", len(resp.Items),
		"cursor", resp.Cursor,
	)

	return resp.Cursor, nil
}

// scanGiftHistoryBatch fetches one incremental page after the current cursor
// and processes all received items. It returns the next cursor and whether the
// caller should immediately request another page.
func (m *Monitor) scanGiftHistoryBatch(ctx context.Context, cursor string) (string, bool, error) {
	resp, err := m.api.GetGiftHistory(ctx, cursor, true, historyBatchLimit)
	if err != nil {
		return cursor, false, fmt.Errorf("fetching gift history (cursor=%q): %w", cursor, err)
	}

	slog.Debug("Fetched gift history batch",
		"items", len(resp.Items),
		"new cursor", resp.Cursor,
		"after", cursor,
	)

	for _, item := range resp.Items {
		if !m.isWatchedGiftCollection(item.CollectionAddress) {
			slog.Debug("Skipping NFT from unwatched collection",
				"nft", shorten(item.Address),
				"collection", shorten(item.CollectionAddress),
			)
			continue
		}

		m.processItem(ctx, item, m.cfg.GiftCollections)
	}

	nextCursor := cursor
	if resp.Cursor != "" {
		nextCursor = resp.Cursor
	} else if len(resp.Items) > 0 {
		slog.Warn("API returned items without cursor; keeping previous cursor to avoid losing state")
	}

	return nextCursor, len(resp.Items) == historyBatchLimit, nil
}

// scanNftHistoryBatch fetches one incremental page for a specific collection
// and processes all received items. It returns the next cursor and whether the
// caller should immediately request another page.
func (m *Monitor) scanNftHistoryBatch(ctx context.Context, collectionAddress, cursor string) (string, bool, error) {
	resp, err := m.api.GetNftHistory(ctx, collectionAddress, cursor, true, historyBatchLimit)
	if err != nil {
		return cursor, false, fmt.Errorf("fetching collection history (collection=%q, cursor=%q): %w", collectionAddress, cursor, err)
	}

	slog.Debug("Fetched collection history batch",
		"collection", shorten(collectionAddress),
		"items", len(resp.Items),
		"newCursor", resp.Cursor,
		"after", cursor,
	)

	for _, item := range resp.Items {
		m.processItem(ctx, item, m.cfg.Collections)
	}

	nextCursor := cursor
	if resp.Cursor != "" {
		nextCursor = resp.Cursor
	} else if len(resp.Items) > 0 {
		slog.Warn("API returned collection items without cursor; keeping previous cursor to avoid losing state",
			"collection", shorten(collectionAddress),
		)
	}

	return nextCursor, len(resp.Items) == historyBatchLimit, nil
}

// processItem checks a single NFT against its collection floor price and
// fires an alert if the listing price is below the configured threshold.
func (m *Monitor) processItem(ctx context.Context, item getgems.NftItem, watchedCollections map[string]float64) {
	discountPct, watched := discountThreshold(watchedCollections, item.CollectionAddress)
	if !watched {
		return
	}

	if (item.TypeData.Currency != "TON") {
		slog.Debug("Skip non-TON sales", item.TypeData.Currency) // todo check is it a correct log?
		return
	}

	floorPrice, ok := m.floorPrice(item.CollectionAddress)
	if !ok || floorPrice <= 0 {
		slog.Warn("No floor price available for collection",
			"collection", shorten(item.CollectionAddress))
		return
	}

	threshold := calculateThreshold(floorPrice, discountPct)

	price, err := strconv.ParseFloat(item.TypeData.PriceNano, 64)
	if err != nil {
		slog.Warn("Failed parse float", item.TypeData.PriceNano, item.Address)
		return
	}

	if price <= 0 {
		return // invalid price — skip
	}

	slog.Debug("Checking NFT",
		"nft", item.Address,
		"collection", item.CollectionAddress,
		"price", price,
		"floor", floorPrice,
		"threshold", threshold,
	)

	if price <= threshold {
		discount := (1 - price/floorPrice) * 100
		msg := formatAlert(m.cfg.Getgems.WebURL, item, floorPrice, price, discount, discountPct)
		slog.Info("🔔 Signal found",
			"nft", shorten(item.Address),
			"priceNano", price,
			"floorPriceNano", floorPrice,
			"discountPct", fmt.Sprintf("%.2f%%", discount),
		)
		if err := m.notifier.SendSignal(ctx, msg); err != nil {
			slog.Error("Failed to send Telegram alert", "err", err)
		}
	}
}

func (m *Monitor) hasCollections() bool {
	return len(m.cfg.Collections) > 0
}

func (m *Monitor) hasGiftCollections() bool {
	return len(m.cfg.GiftCollections) > 0
}

func (m *Monitor) isWatchedGiftCollection(collectionAddress string) bool {
	_, watched := discountThreshold(m.cfg.GiftCollections, collectionAddress)
	return watched
}

func (m *Monitor) watchedCollections() []string {
	seen := make(map[string]struct{}, len(m.cfg.Collections)+len(m.cfg.GiftCollections))
	collections := make([]string, 0, len(m.cfg.Collections)+len(m.cfg.GiftCollections))

	for addr := range m.cfg.Collections {
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		collections = append(collections, addr)
	}

	for addr := range m.cfg.GiftCollections {
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		collections = append(collections, addr)
	}

	return collections
}

func discountThreshold(watchedCollections map[string]float64, collectionAddress string) (float64, bool) {
	discountPct, watched := watchedCollections[collectionAddress]
	return discountPct, watched
}

func calculateThreshold(floorPrice, discountPct float64) float64 {
	return floorPrice * (1 - discountPct/100)
}

// ----- Formatting -----------------------------------------------------------

func formatAlert(getgemsWebURL string, item getgems.NftItem, floorPrice, salePrice, actualDiscount, configuredPct float64) string {
	return fmt.Sprintf(
		"🚨 *NFT Deal Alert*\n\n"+
			"📦 *Collection:* `%s`\n"+
			"🎯 *NFT:* `%s`\n\n"+
			"💰 *Sale Price:* `%.2f TON`\n"+
			"📊 *Floor Price:* `%.2f TON`\n"+
			"📉 *Discount:* `%.2f%%` _(threshold: %.0f%%)_\n\n"+
			"🔗 %s/nft/%s",
		item.CollectionAddress,
		item.Address,
		tonFromNano(salePrice),
		tonFromNano(floorPrice),
		actualDiscount,
		configuredPct,
		strings.TrimRight(getgemsWebURL, "/"),
		item.Address,
	)
}

func tonFromNano(nano float64) float64 {
	return nano / 1_000_000_000
}

// shorten trims long addresses/cursors for readable log output.
func shorten(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "…" + s[len(s)-6:]
}
