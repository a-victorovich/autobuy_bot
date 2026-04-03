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

const (
	initialCursorLimit = 1
	historyBatchLimit  = 100
	floorRefreshEvery  = 5 * time.Minute
)

var allowedKinds = map[getgemsapi.NftItemFullKind]struct{}{
	getgemsapi.NftItemFullKind("CollectionItem"): {},
	getgemsapi.NftItemFullKind("DnsItem"):        {},
	getgemsapi.NftItemFullKind("OffchainNft"):    {},
}

// Monitor orchestrates fetching NFTs on sale, comparing prices against the
// collection floor, and sending Telegram alerts when a deal is found.
type Monitor struct {
	cfg        *config.Config
	api        *getgemsapi.ClientWithResponses
	notifier   *telegram.Notifier
	floorCache map[string]float64
	mu         sync.RWMutex
}

type historyPage struct {
	Items  []getgemsapi.NftItemHistoryItem
	Cursor string
}

type listingEvent struct {
	Address           string
	CollectionAddress string
	PriceNano         string
	Currency          string
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
	lastFloorRefreshAt := time.Now()

	giftCursor, err := m.bootstrapGiftCursor(ctx)
	if err != nil {
		return fmt.Errorf("bootstrapping gift history cursor: %w", err)
	}

	collectionCursors, err := m.bootstrapNftCursors(ctx)
	if err != nil {
		return fmt.Errorf("bootstrapping collection history cursors: %w", err)
	}

	for {
		slog.Debug("Starting monitor iteration",
			"giftCursor", shorten(giftCursor),
			"collectionCursors", len(collectionCursors),
		)

		if time.Since(lastFloorRefreshAt) >= floorRefreshEvery {
			if err := m.refreshFloorPrices(ctx); err != nil {
				slog.Warn("Periodic floor price refresh failed", "err", err)
			} else {
				lastFloorRefreshAt = time.Now()
			}
		}

		immediate := false

		if m.hasGiftCollections() {
			nextCursor, shouldContinue, err := m.scanGiftHistoryBatch(ctx, giftCursor)
			if err != nil {
				slog.Error("Gift scan error", "err", err)
			} else {
				giftCursor = nextCursor
				immediate = immediate || shouldContinue
			}
		}

		for collectionAddress, cursor := range collectionCursors {
			nextCursor, shouldContinue, err := m.scanNftHistoryBatch(ctx, collectionAddress, cursor)
			if err != nil {
				slog.Error("Collection scan error",
					"collection", shorten(collectionAddress),
					"err", err,
				)
				continue
			}

			collectionCursors[collectionAddress] = nextCursor
			immediate = immediate || shouldContinue
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

func (m *Monitor) refreshFloorPrices(ctx context.Context) error {
	for _, addr := range m.watchedCollections() {
		floorPriceNano, err := m.fetchCollectionFloorPriceNano(ctx, addr)
		if err != nil {
			slog.Warn("Failed to fetch floor price", "collection", addr, "err", err)
			continue
		}

		floorPrice, err := strconv.ParseFloat(floorPriceNano, 64)
		if err != nil {
			slog.Warn("Failed to parse floor price nano",
				"collection", addr,
				"floorPriceNano", floorPriceNano,
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

func (m *Monitor) floorPrice(addr string) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	fp, ok := m.floorCache[addr]
	return fp, ok
}

func (m *Monitor) bootstrapGiftCursor(ctx context.Context) (string, error) {
	if !m.hasGiftCollections() {
		return "", nil
	}

	page, err := m.fetchGiftHistory(ctx, "", false, initialCursorLimit)
	if err != nil {
		return "", err
	}

	slog.Info("Bootstrapped gift history cursor",
		"items", len(page.Items),
		"cursor", page.Cursor,
	)

	return page.Cursor, nil
}

func (m *Monitor) bootstrapNftCursors(ctx context.Context) (map[string]string, error) {
	cursors := make(map[string]string, len(m.cfg.Collections))
	if !m.hasCollections() {
		return cursors, nil
	}

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
	page, err := m.fetchCollectionHistory(ctx, collectionAddress, "", false, initialCursorLimit)
	if err != nil {
		return "", fmt.Errorf("fetching initial collection history cursor for %s: %w", collectionAddress, err)
	}

	slog.Info("Bootstrapped collection history cursor",
		"collection", shorten(collectionAddress),
		"items", len(page.Items),
		"cursor", page.Cursor,
	)

	return page.Cursor, nil
}

func (m *Monitor) scanGiftHistoryBatch(ctx context.Context, cursor string) (string, bool, error) {
	page, err := m.fetchGiftHistory(ctx, cursor, true, historyBatchLimit)
	if err != nil {
		return cursor, false, fmt.Errorf("fetching gift history (cursor=%q): %w", cursor, err)
	}

	slog.Debug("Fetched gift history batch",
		"items", len(page.Items),
		"newCursor", page.Cursor,
		"after", cursor,
	)

	for _, item := range page.Items {
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

	return nextCursor(cursor, page), len(page.Items) == historyBatchLimit, nil
}

func (m *Monitor) scanNftHistoryBatch(ctx context.Context, collectionAddress, cursor string) (string, bool, error) {
	page, err := m.fetchCollectionHistory(ctx, collectionAddress, cursor, true, historyBatchLimit)
	if err != nil {
		return cursor, false, fmt.Errorf("fetching collection history (collection=%q, cursor=%q): %w", collectionAddress, cursor, err)
	}

	slog.Debug("Fetched collection history batch",
		"collection", shorten(collectionAddress),
		"items", len(page.Items),
		"newCursor", page.Cursor,
		"after", cursor,
	)

	for _, item := range page.Items {
		m.processItem(ctx, item, m.cfg.Collections)
	}

	return nextCursor(cursor, page), len(page.Items) == historyBatchLimit, nil
}

func (m *Monitor) processItem(ctx context.Context, item getgemsapi.NftItemHistoryItem, watchedCollections map[string]float64) {
	event, ok := decodeListingEvent(item)
	if !ok {
		return
	}

	discountPct, watched := discountThreshold(watchedCollections, event.CollectionAddress)
	if !watched {
		return
	}

	if event.Currency != "TON" {
		slog.Debug("Skipping non-TON sale",
			"currency", event.Currency,
			"nft", shorten(event.Address),
			"collection", shorten(event.CollectionAddress),
		)
		return
	}

	floorPrice, ok := m.floorPrice(event.CollectionAddress)
	if !ok || floorPrice <= 0 {
		slog.Warn("No floor price available for collection",
			"collection", shorten(event.CollectionAddress),
		)
		return
	}

	price, err := strconv.ParseFloat(event.PriceNano, 64)
	if err != nil {
		slog.Warn("Failed to parse sale price nano",
			"priceNano", event.PriceNano,
			"nft", shorten(event.Address),
		)
		return
	}
	if price <= 0 {
		return
	}

	threshold := calculateThreshold(floorPrice, discountPct)
	slog.Debug("Checking NFT",
		"nft", event.Address,
		"collection", event.CollectionAddress,
		"price", price,
		"floor", floorPrice,
		"threshold", threshold,
	)

	if price > threshold {
		return
	}

	discount := (1 - price/floorPrice) * 100
	message := formatAlert(m.cfg.Getgems.WebURL, event, floorPrice, price, discount, discountPct)

	slog.Info("Signal found",
		"nft", shorten(event.Address),
		"priceNano", price,
		"floorPriceNano", floorPrice,
		"discountPct", fmt.Sprintf("%.2f%%", discount),
	)
	if err := m.notifier.SendSignal(ctx, message); err != nil {
		slog.Error("Failed to send Telegram alert", "err", err)
		return
	}

	nftResp, err := m.fetchNft(ctx, event.Address)
	if err != nil {
		slog.Error("Failed to fetch NFT sale details",
			"nft", shorten(event.Address),
			"err", err,
		)
		return
	}

	ok, saleVersion := validateNftSaleDetails(event, nftResp)
	slog.Info("Validated NFT sale details",
		"nft", shorten(event.Address),
		"ok", ok,
		"saleVersion", saleVersion,
	)

	buyTx, err := m.createBuyTx(ctx, event.Address, saleVersion)
	if err != nil {
		slog.Error("Failed to create buy transaction",
			"nft", shorten(event.Address),
			"saleVersion", saleVersion,
			"err", err,
		)
		return
	}

	slog.Info("Created buy transaction",
		"nft", shorten(event.Address),
		"saleVersion", saleVersion,
		"buyTx", formatBuyTransactionLog(buyTx),
	)
}

func (m *Monitor) fetchCollectionFloorPriceNano(ctx context.Context, collectionAddress string) (string, error) {
	resp, err := m.api.V1GetCollectionStatsWithResponse(ctx, collectionAddress)
	if err != nil {
		return "", err
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		return "", err
	}
	if resp.JSON200 == nil || !resp.JSON200.Success || resp.JSON200.Response == nil || resp.JSON200.Response.FloorPriceNano == nil {
		return "", fmt.Errorf("empty floor price response")
	}

	return *resp.JSON200.Response.FloorPriceNano, nil
}

func (m *Monitor) fetchGiftHistory(ctx context.Context, cursor string, reverse bool, limit int) (historyPage, error) {
	resp, err := m.api.V1GetGiftsHistoryWithResponse(ctx, giftHistoryParams(cursor, reverse, limit))
	if err != nil {
		return historyPage{}, err
	}

	return unwrapHistoryPage(resp.StatusCode(), resp.JSON200, resp.JSON400, resp.Body)
}

func (m *Monitor) fetchCollectionHistory(ctx context.Context, collectionAddress, cursor string, reverse bool, limit int) (historyPage, error) {
	resp, err := m.api.V1GetNftCollectionHistoryWithResponse(ctx, collectionAddress, collectionHistoryParams(cursor, reverse, limit))
	if err != nil {
		return historyPage{}, err
	}

	return unwrapHistoryPage(resp.StatusCode(), resp.JSON200, resp.JSON400, resp.Body)
}

func (m *Monitor) fetchNft(ctx context.Context, nftAddress string) (*getgemsapi.V1GetNftByAddressResp, error) {
	resp, err := m.api.V1GetNftByAddressWithResponse(ctx, nftAddress, nil)
	if err != nil {
		return nil, err
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil || !resp.JSON200.Success || resp.JSON200.Response == nil {
		return nil, fmt.Errorf("empty nft response")
	}

	return resp, nil
}

func (m *Monitor) createBuyTx(ctx context.Context, nftAddress, version string) (*getgemsapi.V1BuyNftFixPriceResp, error) {
	resp, err := m.api.V1BuyNftFixPriceWithResponse(ctx, nftAddress, getgemsapi.V1BuyNftFixPriceJSONRequestBody{
		Version: version,
	})
	if err != nil {
		return nil, err
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		return nil, err
	}

	return resp, nil
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

	appendUnique := func(addr string) {
		if _, ok := seen[addr]; ok {
			return
		}
		seen[addr] = struct{}{}
		collections = append(collections, addr)
	}

	for addr := range m.cfg.Collections {
		appendUnique(addr)
	}
	for addr := range m.cfg.GiftCollections {
		appendUnique(addr)
	}

	return collections
}

func decodeListingEvent(item getgemsapi.NftItemHistoryItem) (listingEvent, bool) {
	typeData, err := item.TypeData.AsHistoryTypePutUpForSale()
	if err != nil {
		slog.Debug("Skipping unsupported history type payload",
			"nft", shorten(item.Address),
			"collection", shorten(stringValue(item.CollectionAddress)),
			"err", err,
		)
		return listingEvent{}, false
	}

	return listingEvent{
		Address:           item.Address,
		CollectionAddress: stringValue(item.CollectionAddress),
		PriceNano:         stringValue(typeData.PriceNano),
		Currency:          stringPtrValue(typeData.Currency),
	}, true
}

func nextCursor(current string, page historyPage) string {
	if page.Cursor != "" {
		return page.Cursor
	}
	if len(page.Items) > 0 {
		slog.Warn("API returned items without cursor; keeping previous cursor to avoid losing state")
	}
	return current
}

func unwrapHistoryPage(statusCode int, ok *getgemsapi.NftItemHistoryResponse, failed *getgemsapi.FailedResponse, body []byte) (historyPage, error) {
	if err := requireJSON200(statusCode, ok != nil, failed, body); err != nil {
		return historyPage{}, err
	}
	if ok == nil || !ok.Success {
		return historyPage{}, fmt.Errorf("empty history response")
	}

	return historyPage{
		Items:  ok.Response.Items,
		Cursor: stringValue(ok.Response.Cursor),
	}, nil
}

func discountThreshold(watchedCollections map[string]float64, collectionAddress string) (float64, bool) {
	discountPct, watched := watchedCollections[collectionAddress]
	return discountPct, watched
}

func calculateThreshold(floorPrice, discountPct float64) float64 {
	return floorPrice * (1 - discountPct/100)
}

func validateNftSaleDetails(event listingEvent, nft *getgemsapi.V1GetNftByAddressResp) (bool, string) {
	if nft == nil || nft.JSON200 == nil || !nft.JSON200.Success || nft.JSON200.Response == nil || nft.JSON200.Response.Sale == nil {
		return false, ""
	}

	sale, err := nft.JSON200.Response.Sale.AsFixPriceSale()
	if err != nil {
		return false, ""
	}
	if sale.Type != getgemsapi.FixPriceSaleType("FixPriceSale") {
		return false, sale.Version
	}
	if sale.FullPrice != event.PriceNano {
		return false, sale.Version
	}
	if string(sale.Currency) != event.Currency {
		return false, sale.Version
	}
	if _, ok := allowedKinds[nft.JSON200.Response.Kind]; !ok {
		return false, sale.Version
	}

	return true, sale.Version
}

func formatAlert(getgemsWebURL string, event listingEvent, floorPrice, salePrice, actualDiscount, configuredPct float64) string {
	nftURL := fmt.Sprintf(
		"%s/nft/%s",
		strings.TrimRight(getgemsWebURL, "/"),
		url.PathEscape(event.Address),
	)

	return fmt.Sprintf(
		"🚨 *NFT Deal Alert*\n\n"+
			"📦 *Collection:* `%s`\n"+
			"🎯 *NFT:* `%s`\n\n"+
			"💰 *Sale Price:* `%.2f TON`\n"+
			"📊 *Floor Price:* `%.2f TON`\n"+
			"📉 *Discount:* `%.2f%%` _(threshold: %.0f%%)_\n\n"+
			"🔗 [Open on Getgems](%s)",
		event.CollectionAddress,
		event.Address,
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
