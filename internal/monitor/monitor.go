package monitor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/yourorg/nft-scanner/internal/config"
	getgemsapi "github.com/yourorg/nft-scanner/internal/getgems/openapi"
	"github.com/yourorg/nft-scanner/internal/telegram"
	"github.com/yourorg/nft-scanner/internal/toncenter"
	toncenterapi "github.com/yourorg/nft-scanner/internal/toncenter/openapi"
	"github.com/yourorg/nft-scanner/internal/wallet"
)

const (
	initialCursorLimit = 1
	historyBatchLimit  = 100
	floorRefreshEvery  = 5 * time.Minute

	minTxPrice = 100_000_000
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
	toncenter  *toncenterapi.ClientWithResponses
	wallet     *wallet.Wallet
	floorCache map[string]int64
	mu         sync.RWMutex
	walletMu   sync.Mutex
	seqno      uint32
	balance    int64
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
	IsOffchain        bool
}

// New constructs a Monitor. Call Run to start the polling loop.
func New(cfg *config.Config, api *getgemsapi.ClientWithResponses, notifier *telegram.Notifier) *Monitor {
	cacheSize := len(cfg.Collections) + len(cfg.GiftCollections)
	return &Monitor{
		cfg:        cfg,
		api:        api,
		notifier:   notifier,
		toncenter:  toncenter.New(cfg.Toncenter.APIKey, cfg.Toncenter.BaseURL),
		floorCache: make(map[string]int64, cacheSize),
	}
}

// Run initialises floor prices and then polls for new listings until ctx is
// cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	if err := m.initWallet(ctx); err != nil {
		return err
	}

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

func (m *Monitor) initWallet(ctx context.Context) error {
	if !m.cfg.Scanner.PurchasesEnabled {
		return nil
	}
	if m.wallet != nil {
		return nil
	}

	w, err := wallet.New(m.cfg.Wallet)
	if err != nil {
		return fmt.Errorf("initialise wallet: %w", err)
	}

	m.wallet = w
	accountState, err := m.updateWalletBalanceAndSeqno(ctx)
	if err != nil {
		return fmt.Errorf("Failed get wallet balance and seqno")
	}

	slog.Info("Wallet initialised", "address", w.GetAddress(), "seqno", m.seqno, "accountState", accountState)
	if accountState == "uninitialized" {
		if m.balance < 100_000_000 {
			return fmt.Errorf("Empty wallet. It must have at least 0.1 TON")
		}

		boc, err := w.InitWalletBOC(ctx)
		if err != nil {
			return fmt.Errorf("initialize wallet boc: %w", err)
		}

		res, err := m.toncenter.SendBocReturnHashPostWithResponse(ctx, toncenterapi.SendBocRequest{
			Boc: base64.StdEncoding.EncodeToString(boc),
		})
		if err != nil {
			return fmt.Errorf("send wallet initialization boc: %w", err)
		}
		if res.JSON200 == nil || !res.JSON200.Ok {
			slog.Debug(string(res.Body))
			return fmt.Errorf("Failed send result InitWalletBOC, not 200")
		}
	}

	return nil
}

func (m *Monitor) refreshFloorPrices(ctx context.Context) error {
	for _, addr := range m.watchedCollections() {
		floorPriceNano, err := m.fetchCollectionFloorPriceNano(ctx, addr)
		if err != nil {
			slog.Warn("Failed to fetch floor price", "collection", addr, "err", err)
			continue
		}

		floorPrice, err := strconv.ParseInt(floorPriceNano, 10, 64)
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
			"collection", addr,
			"floorPriceNano", floorPrice,
		)
	}

	return nil
}

func (m *Monitor) floorPrice(addr string) (int64, bool) {
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
		"collection", collectionAddress,
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

	itemsToProcess := make([]getgemsapi.NftItemHistoryItem, 0, len(page.Items))
	for _, item := range page.Items {
		collectionAddress := stringValue(item.CollectionAddress)
		if !m.isWatchedGiftCollection(collectionAddress) {
			slog.Debug("Skipping NFT from unwatched collection",
				"nft", item.Address,
				"collection", collectionAddress,
			)
			continue
		}

		itemsToProcess = append(itemsToProcess, item)
	}
	m.processItemsWithWorkerPool(ctx, itemsToProcess, m.cfg.GiftCollections)

	return nextCursor(cursor, page), len(page.Items) == historyBatchLimit, nil
}

func (m *Monitor) scanNftHistoryBatch(ctx context.Context, collectionAddress, cursor string) (string, bool, error) {
	page, err := m.fetchCollectionHistory(ctx, collectionAddress, cursor, true, historyBatchLimit)
	if err != nil {
		return cursor, false, fmt.Errorf("fetching collection history (collection=%q, cursor=%q): %w", collectionAddress, cursor, err)
	}

	slog.Debug("Fetched collection history batch",
		"collection", collectionAddress,
		"items", len(page.Items),
		"newCursor", page.Cursor,
		"after", cursor,
	)

	m.processItemsWithWorkerPool(ctx, page.Items, m.cfg.Collections)

	return nextCursor(cursor, page), len(page.Items) == historyBatchLimit, nil
}

func (m *Monitor) processItemsWithWorkerPool(
	ctx context.Context,
	items []getgemsapi.NftItemHistoryItem,
	watchedCollections map[string]float64,
) {
	if len(items) == 0 {
		return
	}

	workers := runtime.GOMAXPROCS(0) * 2
	if workers < 1 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}

	jobs := make(chan getgemsapi.NftItemHistoryItem, workers)
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for item := range jobs {
				m.processItem(ctx, item, watchedCollections)
			}
		}()
	}

	for _, item := range items {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		case jobs <- item:
		}
	}

	close(jobs)
	wg.Wait()
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
			"nft", event.Address,
			"collection", event.CollectionAddress,
		)
		return
	}

	floorPrice, ok := m.floorPrice(event.CollectionAddress)
	if !ok || floorPrice <= 0 {
		slog.Warn("No floor price available for collection",
			"collection", event.CollectionAddress,
		)
		return
	}

	price, err := strconv.ParseInt(event.PriceNano, 10, 64)
	if err != nil {
		slog.Warn("Failed to parse sale price nano",
			"priceNano", event.PriceNano,
			"nft", event.Address,
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

	discount := (1.0 - float64(price)/float64(floorPrice)) * 100.0
	if err := m.notifyMatchedListing(ctx, event, floorPrice, price, discount, discountPct); err != nil {
		slog.Error("Failed to send Telegram alert", "err", err)
		return
	}

	m.tryPurchaseMatchedListing(ctx, event, floorPrice, price)
}

func (m *Monitor) fetchWalletSeqnoAndBalance(ctx context.Context) (uint32, string, int64, error) {
	walletAddress := m.wallet.GetAddress()

	walletInfoResp, err := m.toncenter.GetWalletInformationGetWithResponse(ctx, &toncenterapi.GetWalletInformationGetParams{
		Address: walletAddress,
	})
	if err != nil {
		return 0, "", 0, fmt.Errorf("get wallet information: %w", err)
	}
	if walletInfoResp.JSON200 == nil || !walletInfoResp.JSON200.Ok {
		return 0, "", 0, fmt.Errorf("get wallet information: status=%d body=%s", walletInfoResp.StatusCode(), string(walletInfoResp.Body))
	}

	walletInfoObj, err := walletInfoResp.JSON200.Result.AsTonlibObject()
	if err != nil {
		return 0, "", 0, fmt.Errorf("decode wallet information result: %w", err)
	}

	walletInfo, err := walletInfoObj.AsWalletInformation()
	if err != nil {
		return 0, "", 0, fmt.Errorf("decode wallet information payload: %w", err)
	}

	balance, err := strconv.ParseInt(walletInfo.Balance, 10, 64)
	if err != nil {
		return 0, "", 0, fmt.Errorf("Failed ParseInt from balance: %w", err)
	}

	if walletInfo.Seqno == nil {
		return 0, string(walletInfo.AccountState), balance, nil
	}

	return uint32(*walletInfo.Seqno), string(walletInfo.AccountState), balance, nil
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

func (m *Monitor) createSaleTx(ctx context.Context, nftAddress, collectionAddress string, newPrice int64, currency getgemsapi.Currency) (*getgemsapi.V1PutUpNftForSaleFixPriceResp, error) {
	price := strconv.FormatInt(newPrice, 10)
	walletAddress := m.wallet.GetAddress()
	omitRoyalty := !m.hasRoyaltyCollection(collectionAddress)

	resp, err := m.api.V1PutUpNftForSaleFixPriceWithResponse(ctx, nftAddress, getgemsapi.V1PutUpNftForSaleFixPriceJSONRequestBody{
		OwnerAddress: &walletAddress,
		FullPrice:    &price,
		Currency:     &currency,
		OmitRoyalty:  Ptr(omitRoyalty),
	})
	if err != nil {
		return nil, err
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		return nil, err
	}

	return resp, nil
}

func (m *Monitor) notifyMatchedListing(
	ctx context.Context,
	event listingEvent,
	floorPrice, salePrice int64,
	actualDiscount, configuredPct float64,
) error {
	slog.Info("Signal found",
		"nft", event.Address,
		"priceNano", salePrice,
		"floorPriceNano", floorPrice,
		"discountPct", fmt.Sprintf("%.2f%%", actualDiscount),
	)

	message := formatSignalAlert(m.cfg.Getgems.WebURL, event, floorPrice, salePrice, actualDiscount, configuredPct)
	return m.notifier.SendSignal(ctx, message)
}

func (m *Monitor) tryPurchaseMatchedListing(ctx context.Context, event listingEvent, floorPrice int64, price int64) {
	if !m.cfg.Scanner.PurchasesEnabled {
		slog.Info("Buy flow is disabled; skipping buy transaction creation",
			"nft", event.Address,
		)
		return
	}

	maxPriceConfig := tonToNano(m.cfg.Scanner.MaxPrice)
	if maxPriceConfig > 0 && maxPriceConfig < price {
		slog.Info("Max price is lower that price; skipping buy transaction creation",
			"nft", event.Address,
		)

		message := formatMaxPriceIsLower(event.Address, maxPriceConfig, price)
		m.notifier.SendSignal(ctx, message)
		return
	}

	saleVersion, err := m.fetchValidatedSaleVersion(ctx, event)
	if err != nil {
		slog.Error("Failed to validate NFT sale details",
			"nft", event.Address,
			"err", err,
		)
		return
	}

	requiredAmount := price + minTxPrice
	if m.balance < requiredAmount {
		slog.Error("Wallet balance is too small",
			"balance", m.balance,
			"required amount", requiredAmount,
		)
		message := formatLowBalance(m.wallet.GetAddress(), m.balance, requiredAmount)
		m.notifier.SendSignal(ctx, message)
		return
	}

	buyTx, err := m.createBuyTx(ctx, event.Address, saleVersion)
	if err != nil {
		slog.Error("Failed to create buy transaction",
			"nft", event.Address,
			"saleVersion", saleVersion,
			"err", err,
		)
		return
	}
	slog.Info("Created buy transaction",
		"nft", event.Address,
		"saleVersion", saleVersion,
		"buyTx", formatTransactionLog(buyTx),
	)
	if hash, err := m.sendSignedBuyTransaction(ctx, event, saleVersion, buyTx); err != nil {
		slog.Error("Failed to send signed buy transaction",
			"nft", event.Address,
			"hash", hash,
			"saleVersion", saleVersion,
			"err", err,
		)
	}

	m.balance -= requiredAmount

	newPrice := calculateThreshold(floorPrice, m.resaleDiscountPct()) // fixme for falling price
	ready, err := m.waitBuyTransactionReady(ctx, event, saleVersion, buyTx)
	if err != nil {
		m.notifyPutUpForSaleResult(ctx, event.Address, newPrice, err)
		m.updateWalletBalanceAndSeqno(ctx)
		return
	}
	if ready {
		message := formatSuccessfullyBought(event.Address)
		if notifyErr := m.notifier.SendSignal(ctx, message); notifyErr != nil {
			slog.Error("Failed to send Telegram bought message",
				"nft", event.Address,
				"err", notifyErr,
			)
		}

		m.tryPutUpForSale(ctx, event, newPrice)
	}

	m.updateWalletBalanceAndSeqno(ctx)
}

func (m *Monitor) updateWalletBalanceAndSeqno(ctx context.Context) (string, error) {
	m.walletMu.Lock()
	defer m.walletMu.Unlock()

	seqno, accountState, balance, err := m.fetchWalletSeqnoAndBalance(ctx)
	if err != nil {
		return "", err
	}

	slog.Info("BALANCE",
		"seqno", seqno,
		"accountState", accountState,
		"balance", balance,
	)

	m.balance = balance
	m.seqno = seqno
	return accountState, nil
}

func (m *Monitor) fetchValidatedSaleVersion(ctx context.Context, event listingEvent) (string, error) {
	nftResp, err := m.fetchNft(ctx, event.Address)
	if err != nil {
		return "", fmt.Errorf("fetch NFT sale details: %w", err)
	}

	ok, saleVersion, reason := validateNftSaleDetails(event, nftResp)

	stringifiedNftResp := string(nftResp.Body)
	if stringifiedNftResp == "" {
		nftRespJSON, marshalErr := json.Marshal(nftResp.JSON200)
		if marshalErr != nil {
			stringifiedNftResp = fmt.Sprintf("%+v", nftResp)
		} else {
			stringifiedNftResp = string(nftRespJSON)
		}
	}

	slog.Info("Validated NFT sale details",
		"nft", event.Address,
		"ok", ok,
		"saleVersion", saleVersion,
		"reason", reason,
		"nftResp", stringifiedNftResp,
	)
	if !ok {
		message := formatInvalidVersion(event.Address, reason, stringifiedNftResp)
		if notifyErr := m.notifier.SendSignal(ctx, message); notifyErr != nil {
			slog.Error("Failed to send Telegram fetch sale version",
				"nft", event.Address,
				"err", notifyErr,
			)
		}

		return "", fmt.Errorf("sale details do not match listing event")
	}

	return saleVersion, nil
}

func (m *Monitor) tryPutUpForSale(ctx context.Context, event listingEvent, newPrice int64) {
	const (
		maxAttempts = 10
		retryDelay  = 30 * time.Second
		saleVersion = "put-up-for-sale"
	)

	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := waitForRetryDelay(ctx, retryDelay); err != nil {
			slog.Error("Stopped put up for sale retries",
				"nft", event.Address,
				"err", err,
			)

			m.notifyPutUpForSaleResult(ctx, event.Address, newPrice, err)
			return
		}

		var err error
		switch m.cfg.Scanner.Resale.Type {
		case "falling_price":
			if event.IsOffchain {
				err = m.putUpOffchainForFallingSaleAttempt(ctx, event, saleVersion, newPrice, attempt)
			} else {
				err = m.putUpForFallingSaleAttempt(ctx, event, saleVersion, newPrice, attempt)
			}
		default:
			if event.IsOffchain {
				err = m.putUpOffchainForSaleAttempt(ctx, event, saleVersion, newPrice, attempt)
			} else {
				err = m.putUpForSaleAttempt(ctx, event, saleVersion, newPrice, attempt)
			}
		}
		lastErr = err

		if err != nil {
			continue
		}

		m.notifyPutUpForSaleResult(ctx, event.Address, newPrice, nil)
		return
	}

	slog.Error("Failed to put up NFT for sale after all attempts",
		"nft", event.Address,
		"attempts", maxAttempts,
	)
	m.notifyPutUpForSaleResult(ctx, event.Address, newPrice, lastErr)
}

func (m *Monitor) resaleDiscountPct() float64 {
	if m.cfg.Scanner.Resale.Type == "falling_price" {
		return m.cfg.Scanner.Resale.MinDiscountPercent
	}
	return m.cfg.Scanner.Resale.ResaleDiscountPct
}

func (m *Monitor) waitBuyTransactionReady(
	ctx context.Context,
	event listingEvent,
	saleVersion string,
	buyTx *getgemsapi.V1BuyNftFixPriceResp,
) (bool, error) {
	const (
		maxAttempts = 10
		retryDelay  = 30 * time.Second
	)

	payload, ok := buildCheckTxPayload(buyTx)
	if !ok {
		slog.Warn("Buy transaction is missing data for tx status check",
			"nft", event.Address,
			"saleVersion", saleVersion,
			"buyTx", formatTransactionLog(buyTx),
		)
		return false, nil
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := waitForRetryDelay(ctx, retryDelay); err != nil {
			slog.Error("Stopped getting info about buy tx (retries)",
				"nft", event.Address,
				"err", err,
			)
			return false, err
		}

		slog.Info(fmt.Sprintf("Attemp #%d to get buy tx status", attempt), "nft", event.Address)

		checkResp, err := m.api.V1CheckTxStatusWithResponse(ctx, payload)
		if err != nil {
			slog.Error("Failed to check buy transaction status",
				"nft", event.Address,
				"saleVersion", saleVersion,
				"attempt", attempt,
				"err", err,
			)
			continue
		}
		if checkResp.JSON200 != nil {
			slog.Info("Checked buy transaction status",
				"nft", event.Address,
				"saleVersion", saleVersion,
				"attempt", attempt,
				"status", checkResp.JSON200.Response.State,
				"extra", checkResp.JSON200.Response.Extra,
			)
			if checkResp.JSON200.Response.State == "Ready" {
				return true, nil
			}

			if checkResp.JSON200.Response.State == "Failed" {
				return false, fmt.Errorf("buy transaction failed")
			}
			continue
		}

		slog.Warn("Buy transaction status check returned non-success response",
			"nft", event.Address,
			"saleVersion", saleVersion,
			"attempt", attempt,
			"statusCode", checkResp.StatusCode(),
			"failed", failureMessage(checkResp.JSON400),
			"body", string(checkResp.Body),
		)
	}

	return false, fmt.Errorf("buy transaction did not become ready after %d attempts", maxAttempts)
}

func (m *Monitor) putUpForSaleAttempt(
	ctx context.Context,
	event listingEvent,
	saleVersion string,
	newPrice int64,
	attempt int,
) error {
	slog.Info(fmt.Sprintf("Attempt #%d to put up for sale", attempt),
		"nft", event.Address,
		"priceNano", newPrice,
	)

	saleTx, err := m.createSaleTx(ctx, event.Address, event.CollectionAddress, newPrice, getgemsapi.Currency(event.Currency))
	if err != nil {
		slog.Error("Failed to create sale transaction",
			"nft", event.Address,
			"attempt", attempt,
			"err", err,
		)
		return err
	}

	slog.Info("Created sale transaction",
		"nft", event.Address,
		"attempt", attempt,
	)

	if _, err := m.sendSignedTransaction(ctx, event, saleVersion, saleTx.JSON200, false); err != nil {
		slog.Error("Failed to send signed sale transaction",
			"nft", event.Address,
			"attempt", attempt,
			"err", err,
		)
		return err
	}

	return nil
}

func (m *Monitor) putUpOffchainForSaleAttempt(
	ctx context.Context,
	event listingEvent,
	saleVersion string,
	newPrice int64,
	attempt int,
) error {
	slog.Info(fmt.Sprintf("Attempt #%d to put up offchain NFT for sale", attempt),
		"nft", event.Address,
		"saleVersion", saleVersion,
		"priceNano", newPrice,
	)

	price := strconv.FormatInt(newPrice, 10)
	walletAddress := m.wallet.GetAddress()
	omitRoyalty := !m.hasRoyaltyCollection(event.CollectionAddress)
	currency := getgemsapi.Currency(event.Currency)

	putResp, err := m.api.V1PutUpOffchainNftForSaleFixPriceWithResponse(ctx, getgemsapi.V1PutUpOffchainNftForSaleFixPriceJSONRequestBody{
		NftAddress:   event.Address,
		OwnerAddress: &walletAddress,
		FullPrice:    price,
		Currency:     &currency,
		OmitRoyalty:  Ptr(omitRoyalty),
		Lang:         getgemsapi.Lang("en"),
	})
	if err != nil {
		slog.Error("Failed to create offchain sale request",
			"nft", event.Address,
			"attempt", attempt,
			"err", err,
		)
		return err
	}
	if err := requireJSON200(putResp.StatusCode(), putResp.JSON200 != nil, putResp.JSON400, putResp.Body); err != nil {
		slog.Error("Getgems rejected offchain put-up request",
			"nft", event.Address,
			"attempt", attempt,
			"err", err,
		)
		return err
	}
	if putResp.JSON200 == nil || !putResp.JSON200.Success {
		return fmt.Errorf("empty offchain put-up response")
	}

	signText := putResp.JSON200.Response.SignatureData.Text
	if signText == "" {
		return fmt.Errorf("offchain put-up response does not contain signature text")
	}

	signDomain := getgemsSignDomain(m.cfg.Getgems.WebURL, m.cfg.Getgems.BaseURL)
	signTimestamp := time.Now().Unix()
	signature, err := m.wallet.SignData(ctx, wallet.SignDataPayloadTypeText, []byte(signText), signDomain, signTimestamp)
	if err != nil {
		return fmt.Errorf("sign offchain put-up payload: %w", err)
	}

	confirmResp, err := m.api.V1ConfirmPutUpOffchainNftForSaleFixPriceWithResponse(ctx, getgemsapi.V1ConfirmPutUpOffchainNftForSaleFixPriceJSONRequestBody{
		NftAddress:   event.Address,
		OwnerAddress: &walletAddress,
		FullPrice:    price,
		Currency:     &currency,
		OmitRoyalty:  Ptr(omitRoyalty),
		Lang:         getgemsapi.Lang("en"),
		SignatureData: &struct {
			Domain    *string `json:"domain,omitempty"`
			Signature *string `json:"signature,omitempty"`
			Text      *string `json:"text,omitempty"`
			Timestamp *int64  `json:"timestamp,omitempty"`
		}{
			Domain:    &signDomain,
			Signature: &signature,
			Text:      &signText,
			Timestamp: &signTimestamp,
		},
	})
	if err != nil {
		slog.Error("Failed to confirm offchain sale request",
			"nft", event.Address,
			"attempt", attempt,
			"err", err,
		)
		return err
	}
	if err := requireJSON200(confirmResp.StatusCode(), confirmResp.JSON200 != nil, confirmResp.JSON400, confirmResp.Body); err != nil {
		slog.Error("Getgems rejected offchain confirm request",
			"nft", event.Address,
			"attempt", attempt,
			"err", err,
		)
		return err
	}

	return nil
}

func (m *Monitor) putUpForFallingSaleAttempt(
	ctx context.Context,
	event listingEvent,
	saleVersion string,
	newPrice int64,
	attempt int,
) error {
	return m.putUpForSaleAttempt(ctx, event, saleVersion, newPrice, attempt)
}

func (m *Monitor) putUpOffchainForFallingSaleAttempt(
	ctx context.Context,
	event listingEvent,
	saleVersion string,
	newPrice int64,
	attempt int,
) error {
	return m.putUpOffchainForSaleAttempt(ctx, event, saleVersion, newPrice, attempt)
}

func (m *Monitor) notifyPutUpForSaleResult(ctx context.Context, nftAddress string, newPrice int64, saleErr error) {
	message := formatPutUpForSaleResult(nftAddress, newPrice, saleErr)
	if notifyErr := m.notifier.SendSignal(ctx, message); notifyErr != nil {
		slog.Error("Failed to send Telegram sale result",
			"nft", nftAddress,
			"err", notifyErr,
		)
	}
}

func (m *Monitor) sendSignedBuyTransaction(
	ctx context.Context,
	event listingEvent,
	saleVersion string,
	buyTx *getgemsapi.V1BuyNftFixPriceResp,
) (string, error) {
	return m.sendSignedTransaction(ctx, event, saleVersion, buyTx.JSON200, true)
}

func (m *Monitor) sendSignedTransaction(
	ctx context.Context,
	event listingEvent,
	saleVersion string,
	txResp *getgemsapi.TransactionResponse,
	notifyTelegram bool,
) (string, error) {
	signedBOC, err := m.buildSignedTxBoc(ctx, m.seqno, false, txResp)
	if err != nil {
		return "", fmt.Errorf("build signed transaction boc: %w", err)
	}

	slog.Info("Signed transaction was created", "nft", event.Address)

	sendBocResp, err := m.toncenter.SendBocReturnHashPostWithResponse(ctx, toncenterapi.SendBocRequest{
		Boc: base64.StdEncoding.EncodeToString(signedBOC),
	})
	m.seqno++

	if notifyTelegram == true {
		message := formatTxResult(event.Address, saleVersion, sendBocResp, err)
		// slog.Info("Message", message)
		if notifyErr := m.notifier.SendSignal(ctx, message); notifyErr != nil {
			slog.Error("Failed to send Telegram transaction result",
				"nft", event.Address,
				"saleVersion", saleVersion,
				"err", notifyErr,
			)
		}
	}

	if err != nil {
		return "", err
	}
	if sendBocResp.JSON200 == nil || !sendBocResp.JSON200.Ok {
		return "", fmt.Errorf("toncenter rejected signed buy transaction: status=%d body=%s", sendBocResp.StatusCode(), string(sendBocResp.Body))
	}

	resultJSON, err := sendBocResp.JSON200.Result.MarshalJSON()
	if err != nil {
		return "", fmt.Errorf("marshal toncenter sendBoc result: %w", err)
	}

	var msgInfo toncenterapi.ExtMessageInfo
	if err := json.Unmarshal(resultJSON, &msgInfo); err != nil {
		return "", fmt.Errorf("decode toncenter sendBoc result: %w", err)
	}
	if msgInfo.Hash == "" {
		return "", fmt.Errorf("toncenter sendBoc result does not contain hash: body=%s", string(sendBocResp.Body))
	}

	hash := msgInfo.Hash

	slog.Info("Signed transaction was sent",
		"nft", event.Address,
		"saleVersion", saleVersion,
		"Body", string(sendBocResp.Body),
	)
	return hash, nil
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

func (m *Monitor) hasRoyaltyCollection(collectionAddress string) bool {
	for _, addr := range m.cfg.RoyaltyCollections {
		if addr == collectionAddress {
			return true
		}
	}
	return false
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

func (m *Monitor) buildSignedTxBoc(ctx context.Context, seqno uint32, withStateInit bool, resp *getgemsapi.TransactionResponse) ([]byte, error) {
	if resp == nil || resp.Response == nil {
		return nil, fmt.Errorf("empty transaction response")
	}

	tx := resp.Response

	signedBOC, err := m.wallet.BuildSignedBOC(ctx, seqno, withStateInit, tx)
	if err != nil {
		slog.Error("Failed to sign transaction", "err", err)
		return nil, err
	}

	return signedBOC, nil
}

func (m *Monitor) InitWallet(ctx context.Context) error {
	return m.initWallet(ctx)
}

func (m *Monitor) PutUpOffchainForSaleAttempt(ctx context.Context, nftAddress string, newPrice int64) error {
	nftResp, err := m.fetchNft(ctx, nftAddress)
	if err != nil {
		return fmt.Errorf("fetch NFT before offchain put-up: %w", err)
	}
	if nftResp == nil || nftResp.JSON200 == nil || nftResp.JSON200.Response == nil {
		return fmt.Errorf("empty NFT response")
	}

	nft := nftResp.JSON200.Response
	collectionAddress := stringValue(nft.CollectionAddress)
	if collectionAddress == "" {
		return fmt.Errorf("NFT %s does not contain collectionAddress", nftAddress)
	}

	currency := "TON"
	if nft.Sale != nil {
		if sale, saleErr := nft.Sale.AsFixPriceSale(); saleErr == nil && sale.Currency != "" {
			currency = string(sale.Currency)
		}
	}

	event := listingEvent{
		Address:           nftAddress,
		CollectionAddress: collectionAddress,
		Currency:          currency,
		IsOffchain:        true,
	}

	return m.putUpOffchainForSaleAttempt(ctx, event, "put-up-for-sale", newPrice, 1)
}

func getgemsSignDomain(webURL, baseURL string) string {
	for _, raw := range []string{webURL, baseURL} {
		if raw == "" {
			continue
		}

		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if host := parsed.Hostname(); host != "" {
			return host
		}
	}

	return "getgems.io"
}
