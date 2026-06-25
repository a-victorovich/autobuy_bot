package monitor

import (
	"context"
	"log/slog"
	"strconv"

	getgemsapi "github.com/yourorg/nft-scanner/internal/getgems/openapi"
)

type auctionEvent struct {
	Address           string
	CollectionAddress string
	Owner             string
}

func decodeAuctionEvent(item getgemsapi.NftItemHistoryItem) (auctionEvent, bool) {
	typeData, err := item.TypeData.AsHistoryTypePutUpForAuction()
	if err != nil || typeData.Type != getgemsapi.PutUpForAuction {
		return auctionEvent{}, false
	}

	return auctionEvent{
		Address:           item.Address,
		CollectionAddress: stringValue(item.CollectionAddress),
		Owner:             stringValue(typeData.Owner),
	}, true
}

func (m *Monitor) processAuctionItem(ctx context.Context, event auctionEvent) {
	slog.InfoContext(ctx, "NFT put up for auction",
		"nft", event.Address,
		"collection", event.CollectionAddress,
		"owner", event.Owner,
	)

	attributeThresholds, hasAttributeThreshold := m.cfg.CollectionPriceThresholdByAttributes[event.CollectionAddress]
	if !hasAttributeThreshold {
		return
	}

	nftResp, err := m.fetchNft(ctx, event.Address)
	if err != nil {
		slog.WarnContext(ctx, "Failed to fetch NFT auction details",
			"nft", event.Address,
			"err", err,
		)
		return
	}
	ok, saleVersion, reason, sale := validateNftAuctionDetails(nftResp)
	if !ok {
		slog.WarnContext(ctx, "NFT auction details validation failed",
			"nft", event.Address,
			"sale_version", saleVersion,
			"reason", reason,
		)
		return
	}

	message := formatNewAuction(m.cfg.Getgems.WebURL, event)
	if notifyErr := m.notifier.SendSignal(ctx, message); notifyErr != nil {
		slog.Error("Failed to send Telegram newAuction message",
			"nft", event.Address,
			"err", notifyErr,
		)
	}
	

	slog.InfoContext(ctx, "NFT auction details validated",
		"nft", event.Address,
		"sale_version", saleVersion,
		"min_bid", sale.MinBid,
		"finish_at", sale.FinishAt,
	)

	var (
		threshold       int64
		thresholdSet    bool
		thresholdSource string
	)
	if hasAttributeThreshold {
		attributeThreshold, matched, err := m.calculateThresholdByAttribute(ctx, event.Address, attributeThresholds)
		if err != nil {
			slog.Warn("Failed to calculate attribute price threshold (processAuctionItem)",
				"nft", event.Address,
				"collection", event.CollectionAddress,
				"err", err,
			)
			return
		}
		if matched {
			threshold = attributeThreshold
			thresholdSet = true
			thresholdSource = "attribute"
		}
	}

	if !thresholdSet {
		slog.Debug("Skipping NFT because no attribute threshold matched (processAuctionItem)",
			"nft", event.Address,
			"collection", event.CollectionAddress,
		)
		return
	}

	bidAmount := sale.MinBid
	bidSource := "min_bid"
	if sale.LastBidAmount != nil {
		bidAmount = *sale.LastBidAmount
		bidSource = "last_bid_amount"
	}

	currentBid, err := strconv.ParseInt(bidAmount, 10, 64)
	if err != nil {
		slog.WarnContext(ctx, "Failed to parse NFT auction bid amount",
			"nft", event.Address,
			"amount", bidAmount,
			"source", bidSource,
			"err", err,
		)
		return
	}
	if currentBid >= threshold {
		slog.DebugContext(ctx, "Skipping NFT auction because bid amount is above threshold",
			"nft", event.Address,
			"amount", currentBid,
			"source", bidSource,
			"threshold", threshold,
			"thresholdSource", thresholdSource,
		)
		return
	}

	requiredAmount := threshold + 100_000_000
	if m.balance < requiredAmount {
		slog.Error("Wallet balance is too small",
			"balance", m.balance,
			"required amount", requiredAmount,
		)
		message := formatLowBalance(m.wallet.GetAddress(), m.balance, requiredAmount)
		m.notifier.SendSignal(ctx, message)
		return
	}

	amount := strconv.FormatInt(threshold, 10)
	resp, err := m.api.V1MakeNftActionBidWithResponse(ctx, event.Address, getgemsapi.V1MakeNftActionBidJSONRequestBody{
		Amount:  amount,
		Version: sale.Version,
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to create NFT auction bid",
			"nft", event.Address,
			"amount", amount,
			"err", err,
		)
		return
	}
	if err := requireJSON200(resp.StatusCode(), resp.JSON200 != nil, resp.JSON400, resp.Body); err != nil {
		slog.WarnContext(ctx, "Getgems rejected NFT auction bid",
			"nft", event.Address,
			"amount", amount,
			"err", err,
		)
		return
	}

	slog.InfoContext(ctx, "Created NFT auction bid",
		"nft", event.Address,
		"amount", threshold,
		"thresholdSource", thresholdSource,
	)

	if hash, err := m.sendSignedTransaction(ctx, listingEvent{
		Address:           event.Address,
		CollectionAddress: event.CollectionAddress,
		Owner:             event.Owner,
	}, saleVersion, resp.JSON200, true, true); err != nil {
		slog.Error("Failed to send signed auction bid transaction",
			"nft", event.Address,
			"hash", hash,
			"saleVersion", saleVersion,
			"err", err,
		)
	}

	m.balance -= requiredAmount
}
