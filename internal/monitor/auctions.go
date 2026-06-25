package monitor

import (
	"context"
	"log/slog"

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
}
