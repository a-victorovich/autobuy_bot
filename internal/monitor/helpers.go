package monitor

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"

	getgemsapi "github.com/yourorg/nft-scanner/internal/getgems/openapi"
)

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

func calculateThreshold(floorPrice int64, discountPct float64) int64 {
	result := float64(floorPrice) * (1.0 - discountPct/100.0)
	return int64(math.Ceil(result))
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

func tonFromNano(nano int64) float64 {
	return float64(nano) / 1_000_000_000
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

func formatTransactionLog[T *getgemsapi.V1BuyNftFixPriceResp | *getgemsapi.V1PutUpNftForSaleFixPriceResp](resp T) string {
	if resp == nil {
		return ""
	}

	var (
		body     []byte
		response *getgemsapi.TransactionResponse
	)

	switch v := any(resp).(type) {
	case *getgemsapi.V1BuyNftFixPriceResp:
		body = v.Body
		response = v.JSON200
	case *getgemsapi.V1PutUpNftForSaleFixPriceResp:
		body = v.Body
		response = v.JSON200
	default:
		return ""
	}

	if response == nil {
		return ""
	}

	payload, err := json.Marshal(response.Response)
	if err != nil {
		return string(body)
	}
	return string(payload)
}

func Ptr[T any](v T) *T {
	return &v
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}