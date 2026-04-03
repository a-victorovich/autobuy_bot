package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/nft-scanner/internal/config"
	getgemsapi "github.com/yourorg/nft-scanner/internal/getgems/openapi"
	"github.com/yourorg/nft-scanner/internal/telegram"
)

// Monitor orchestrates fetching NFTs on sale, comparing prices against the
// collection floor, and sending Telegram alerts when a deal is found.
type Monitor struct {
	cfg        *config.Config
	api        *getgemsapi.ClientWithResponses
	notifier   *telegram.Notifier
	floorCache map[string]float64 // collectionAddress -> floor price
	mu         sync.RWMutex       // guards floorCache
}

// New constructs a Monitor. Call Run to start the polling loop.
func New(cfg *config.Config, api *getgemsapi.ClientWithResponses, notifier *telegram.Notifier) *Monitor {
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
		statsResp, err := m.api.V1GetCollectionStatsWithResponse(ctx, addr)
		if err != nil {
			slog.Warn("Failed to fetch floor price", "collection", addr, "err", err)
			continue
		}
		if err := requireJSON200(statsResp.StatusCode(), statsResp.JSON200 != nil, statsResp.JSON400, statsResp.Body); err != nil {
			slog.Warn("Failed to fetch floor price", "collection", addr, "err", err)
			continue
		}
		if statsResp.JSON200 == nil || !statsResp.JSON200.Success || statsResp.JSON200.Response == nil || statsResp.JSON200.Response.FloorPriceNano == nil {
			slog.Warn("Floor price response is empty", "collection", addr)
			continue
		}

		floorPrice, err := strconv.ParseFloat(*statsResp.JSON200.Response.FloorPriceNano, 64)
		if err != nil {
			slog.Warn("Failed to parse floor price nano",
				"collection", addr,
				"floorPriceNano", *statsResp.JSON200.Response.FloorPriceNano,
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
	resp, err := m.api.V1GetGiftsHistoryWithResponse(ctx, giftHistoryParams("", false, initialCursorLimit))
	if err != nil {
		return "", fmt.Errorf("fetching initial gift history cursor: %w", err)
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		return "", fmt.Errorf("fetching initial gift history cursor: %w", err)
	}

	slog.Info("Bootstrapped gift history cursor",
		"items", len(resp.JSON200.Response.Items),
		"cursor", stringValue(resp.JSON200.Response.Cursor),
	)

	return stringValue(resp.JSON200.Response.Cursor), nil
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
	resp, err := m.api.V1GetNftCollectionHistoryWithResponse(ctx, collectionAddress, collectionHistoryParams("", false, initialCursorLimit))
	if err != nil {
		return "", fmt.Errorf("fetching initial collection history cursor for %s: %w", collectionAddress, err)
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		return "", fmt.Errorf("fetching initial collection history cursor for %s: %w", collectionAddress, err)
	}

	slog.Info("Bootstrapped collection history cursor",
		"collection", shorten(collectionAddress),
		"items", len(resp.JSON200.Response.Items),
		"cursor", stringValue(resp.JSON200.Response.Cursor),
	)

	return stringValue(resp.JSON200.Response.Cursor), nil
}

// scanGiftHistoryBatch fetches one incremental page after the current cursor
// and processes all received items. It returns the next cursor and whether the
// caller should immediately request another page.
func (m *Monitor) scanGiftHistoryBatch(ctx context.Context, cursor string) (string, bool, error) {
	resp, err := m.api.V1GetGiftsHistoryWithResponse(ctx, giftHistoryParams(cursor, true, historyBatchLimit))
	if err != nil {
		return cursor, false, fmt.Errorf("fetching gift history (cursor=%q): %w", cursor, err)
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		return cursor, false, fmt.Errorf("fetching gift history (cursor=%q): %w", cursor, err)
	}

	slog.Debug("Fetched gift history batch",
		"items", len(resp.JSON200.Response.Items),
		"new cursor", stringValue(resp.JSON200.Response.Cursor),
		"after", cursor,
	)

	for _, item := range resp.JSON200.Response.Items {
		collectionAddress := stringValue(item.CollectionAddress)
		if !m.isWatchedGiftCollection(collectionAddress) {
			slog.Debug("Skipping NFT from unwatched collection",
				"nft", shorten(item.Address),
				"collection", shorten(collectionAddress),
			)
			continue
		}

		m.processItem(ctx, item, m.cfg.GiftCollections)
	}

	nextCursor := cursor
	if resp.JSON200.Response.Cursor != nil {
		nextCursor = *resp.JSON200.Response.Cursor
	} else if len(resp.JSON200.Response.Items) > 0 {
		slog.Warn("API returned items without cursor; keeping previous cursor to avoid losing state")
	}

	return nextCursor, len(resp.JSON200.Response.Items) == historyBatchLimit, nil
}

// scanNftHistoryBatch fetches one incremental page for a specific collection
// and processes all received items. It returns the next cursor and whether the
// caller should immediately request another page.
func (m *Monitor) scanNftHistoryBatch(ctx context.Context, collectionAddress, cursor string) (string, bool, error) {
	resp, err := m.api.V1GetNftCollectionHistoryWithResponse(ctx, collectionAddress, collectionHistoryParams(cursor, true, historyBatchLimit))
	if err != nil {
		return cursor, false, fmt.Errorf("fetching collection history (collection=%q, cursor=%q): %w", collectionAddress, cursor, err)
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		return cursor, false, fmt.Errorf("fetching collection history (collection=%q, cursor=%q): %w", collectionAddress, cursor, err)
	}

	slog.Debug("Fetched collection history batch",
		"collection", shorten(collectionAddress),
		"items", len(resp.JSON200.Response.Items),
		"newCursor", stringValue(resp.JSON200.Response.Cursor),
		"after", cursor,
	)

	for _, item := range resp.JSON200.Response.Items {
		m.processItem(ctx, item, m.cfg.Collections)
	}

	nextCursor := cursor
	if resp.JSON200.Response.Cursor != nil {
		nextCursor = *resp.JSON200.Response.Cursor
	} else if len(resp.JSON200.Response.Items) > 0 {
		slog.Warn("API returned collection items without cursor; keeping previous cursor to avoid losing state",
			"collection", shorten(collectionAddress),
		)
	}

	return nextCursor, len(resp.JSON200.Response.Items) == historyBatchLimit, nil
}

// processItem checks a single NFT against its collection floor price and
// fires an alert if the listing price is below the configured threshold.
func (m *Monitor) processItem(ctx context.Context, item getgemsapi.NftItemHistoryItem, watchedCollections map[string]float64) {
	collectionAddress := stringValue(item.CollectionAddress)
	typeData, err := item.TypeData.AsHistoryTypePutUpForSale()
	if err != nil {
		slog.Debug("Skipping unsupported history type payload",
			"nft", shorten(item.Address),
			"collection", shorten(collectionAddress),
			"err", err,
		)
		return
	}

	discountPct, watched := discountThreshold(watchedCollections, collectionAddress)
	if !watched {
		return
	}

	currency := stringPtrValue(typeData.Currency)
	if currency != "TON" {
		slog.Debug("Skipping non-TON sale",
			"currency", currency,
			"nft", shorten(item.Address),
			"collection", shorten(collectionAddress),
		)
		return
	}

	floorPrice, ok := m.floorPrice(collectionAddress)
	if !ok || floorPrice <= 0 {
		slog.Warn("No floor price available for collection",
			"collection", shorten(collectionAddress))
		return
	}

	threshold := calculateThreshold(floorPrice, discountPct)

	price, err := strconv.ParseFloat(stringValue(typeData.PriceNano), 64)
	if err != nil {
		slog.Warn("Failed to parse sale price nano",
			"priceNano", stringValue(typeData.PriceNano),
			"nft", shorten(item.Address),
		)
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
			return
		}

		nft, err := m.api.V1GetNftByAddressWithResponse(ctx, item.Address, nil)
		if err != nil {
			slog.Error("Failed to fetch NFT sale details",
				"nft", shorten(item.Address),
				"err", err,
			)
			return
		}
		if err := requireJSON200(nft.StatusCode(), nft.JSON200 != nil, nft.JSON400, nft.Body); err != nil {
			slog.Error("Failed to fetch NFT sale details",
				"nft", shorten(item.Address),
				"err", err,
			)
			return
		}

		ok, saleVersion := validateNftSaleDetails(item, nft)
		slog.Info("Validated NFT sale details",
			"nft", shorten(item.Address),
			"ok", ok,
			"saleVersion", saleVersion,
		)

		buyTx, err := m.api.V1BuyNftFixPriceWithResponse(ctx, item.Address, getgemsapi.V1BuyNftFixPriceJSONRequestBody{
			Version: saleVersion,
		})
		if err != nil {
			slog.Error("Failed to create buy transaction",
				"nft", shorten(item.Address),
				"saleVersion", saleVersion,
				"err", err,
			)
			return
		}
		if err := requireJSON200(buyTx.StatusCode(), buyTx.JSON200 != nil, buyTx.JSON400, buyTx.Body); err != nil {
			slog.Error("Failed to create buy transaction",
				"nft", shorten(item.Address),
				"saleVersion", saleVersion,
				"err", err,
			)
			return
		}

		slog.Info("Created buy transaction",
			"nft", shorten(item.Address),
			"saleVersion", saleVersion,
			"buyTx", formatBuyTransactionLog(buyTx),
		)
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

func validateNftSaleDetails(item getgemsapi.NftItemHistoryItem, nft *getgemsapi.V1GetNftByAddressResp) (bool, string) {
	if nft == nil || nft.JSON200 == nil || !nft.JSON200.Success || nft.JSON200.Response == nil || nft.JSON200.Response.Sale == nil {
		return false, ""
	}

	typeData, err := item.TypeData.AsHistoryTypePutUpForSale()
	if err != nil {
		return false, ""
	}

	sale, err := nft.JSON200.Response.Sale.AsFixPriceSale()
	if err != nil {
		return false, ""
	}

	if sale.Type != getgemsapi.FixPriceSaleType(getgemsapi.FixPriceSaleType("FixPriceSale")) {
		return false, sale.Version
	}

	if sale.FullPrice != stringValue(typeData.PriceNano) {
		return false, sale.Version
	}

	if string(sale.Currency) != stringPtrValue(typeData.Currency) {
		return false, sale.Version
	}

	allowedKinds := map[getgemsapi.NftItemFullKind]struct{}{
		getgemsapi.NftItemFullKind("CollectionItem"): {},
		getgemsapi.NftItemFullKind("DnsItem"):        {},
		getgemsapi.NftItemFullKind("OffchainNft"):    {},
	}
	if _, ok := allowedKinds[nft.JSON200.Response.Kind]; !ok {
		return false, sale.Version
	}

	return true, sale.Version
}

// ----- Formatting -----------------------------------------------------------

func formatAlert(getgemsWebURL string, item getgemsapi.NftItemHistoryItem, floorPrice, salePrice, actualDiscount, configuredPct float64) string {
	nftURL := fmt.Sprintf(
		"%s/nft/%s",
		strings.TrimRight(getgemsWebURL, "/"),
		url.PathEscape(item.Address),
	)

	return fmt.Sprintf(
		"🚨 *NFT Deal Alert*\n\n"+
			"📦 *Collection:* `%s`\n"+
			"🎯 *NFT:* `%s`\n\n"+
			"💰 *Sale Price:* `%.2f TON`\n"+
			"📊 *Floor Price:* `%.2f TON`\n"+
			"📉 *Discount:* `%.2f%%` _(threshold: %.0f%%)_\n\n"+
			"🔗 [Open on Getgems](%s)",
		stringValue(item.CollectionAddress),
		item.Address,
		tonFromNano(salePrice),
		tonFromNano(floorPrice),
		actualDiscount,
		configuredPct,
		nftURL,
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

func giftHistoryParams(cursor string, reverse bool, limit int) *getgemsapi.V1GetGiftsHistoryParams {
	params := &getgemsapi.V1GetGiftsHistoryParams{
		Reverse: &reverse,
		Types:   &[]getgemsapi.HistoryType{getgemsapi.PutUpForSale},
	}
	if cursor != "" {
		after := getgemsapi.ParametersAfterParameter(cursor)
		params.After = &after
	}
	if limit > 0 {
		l := getgemsapi.ParametersLimitParameter(limit)
		params.Limit = &l
	}
	return params
}

func collectionHistoryParams(cursor string, reverse bool, limit int) *getgemsapi.V1GetNftCollectionHistoryParams {
	params := &getgemsapi.V1GetNftCollectionHistoryParams{
		Reverse: &reverse,
		Types:   &[]getgemsapi.HistoryType{getgemsapi.PutUpForSale},
	}
	if cursor != "" {
		after := getgemsapi.ParametersAfterParameter(cursor)
		params.After = &after
	}
	if limit > 0 {
		l := getgemsapi.ParametersLimitParameter(limit)
		params.Limit = &l
	}
	return params
}

func requireJSON200(statusCode int, ok bool, failed *getgemsapi.FailedResponse, body []byte) error {
	if statusCode == 200 && ok {
		return nil
	}
	if failed != nil {
		return fmt.Errorf("unexpected status %d: %s", statusCode, failureMessage(failed))
	}
	return fmt.Errorf("unexpected status %d: %s", statusCode, truncate(string(body), 200))
}

func failureMessage(failed *getgemsapi.FailedResponse) string {
	if failed == nil {
		return ""
	}
	messages := make([]string, 0, len(failed.Errors))
	for _, entry := range failed.Errors {
		if entry.Message != nil && *entry.Message != "" {
			messages = append(messages, *entry.Message)
		}
	}
	if len(messages) > 0 {
		return strings.Join(messages, "; ")
	}
	return failed.Name
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func stringPtrValue[T ~string](v *T) string {
	if v == nil {
		return ""
	}
	return string(*v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func formatBuyTransactionLog(resp *getgemsapi.V1BuyNftFixPriceResp) string {
	if resp == nil || resp.JSON200 == nil {
		return ""
	}
	body, err := json.Marshal(resp.JSON200.Response)
	if err != nil {
		return string(resp.Body)
	}
	return string(body)
}
