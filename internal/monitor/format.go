package monitor

import (
	"fmt"
	"net/url"
	"strings"

	toncenterapi "github.com/yourorg/nft-scanner/internal/toncenter/openapi"
)

func formatSignalAlert(
	getgemsWebURL string, event listingEvent,
	floorPrice, salePrice int64,
	actualDiscount, configuredPct float64,
	thresholdSource string,
) string {
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
			"📊 *Floor Price (or threshold):* `%.2f TON (Type: %s)`\n"+
			"📉 *Discount:* `%.2f%%` _(threshold: %.0f%%)_\n\n"+
			"🔗 [Open on Getgems](%s)",
		event.CollectionAddress,
		event.Address,
		tonFromNano(salePrice),
		tonFromNano(floorPrice),
		thresholdSource,
		actualDiscount,
		configuredPct,
		nftURL,
	)
}

func formatLowBalance(
	walletAddress string,
	balance, requiredBalance int64,
) string {
	return fmt.Sprintf(
		"🆘 *Low Wallet Balance* 🆘\n\n"+
			"*WalletAddress:* `%s`\n"+
			"*Balance:* `%.2f TON`\n"+
			"*Required balance (for last signal):* `%.2f TON`\n",
		walletAddress,
		tonFromNano(balance),
		tonFromNano(requiredBalance),
	)
}

func formatTxResult(
	nftAddress, saleVersion string,
	resp *toncenterapi.SendBocReturnHashPostResp, sendErr error,
) string {
	return formatTxResultWithTitle("⚠️ *Attempt to buy* ⚠️", nftAddress, saleVersion, resp, sendErr)
}

func formatAuctionTxResult(
	nftAddress, saleVersion string,
	resp *toncenterapi.SendBocReturnHashPostResp, sendErr error,
) string {
	return formatTxResultWithTitle("⚠️ *Made bid on auction* ⚠️", nftAddress, saleVersion, resp, sendErr)
}

func formatTxResultWithTitle(
	title, nftAddress, saleVersion string,
	resp *toncenterapi.SendBocReturnHashPostResp, sendErr error,
) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(
		"%s\n\n"+
			"*NFT:* `%s`\n"+
			"*Version:* `%s`\n",
		title,
		nftAddress,
		saleVersion,
	))
	b.WriteString("\n")

	if sendErr != nil {
		b.WriteString(fmt.Sprintf("*Status* failed ❗️\n"))
		b.WriteString("\nError: ")
		b.WriteString(sendErr.Error())
		return b.String()
	}

	if resp == nil {
		b.WriteString(fmt.Sprintf("*Status* failed ❗️\n"))
		b.WriteString("\nError: empty response")
		return b.String()
	}

	if resp.JSON200 == nil || !resp.JSON200.Ok {
		b.WriteString(fmt.Sprintf("*Status* rejected ❗️\n"))
		b.WriteString("\nHTTP status: ")
		b.WriteString(fmt.Sprintf("%d", resp.StatusCode()))
		if len(resp.Body) > 0 {
			b.WriteString("\nBody: ")
			b.WriteString(string(resp.Body))
		}
		return b.String()
	}

	b.WriteString("\nStatus: sent")
	if result, err := resp.JSON200.Result.AsTonlibResponseResult0(); err == nil && strings.TrimSpace(string(result)) != "" {
		b.WriteString("\nResult: ")
		b.WriteString(string(result))
	}

	return b.String()
}

func formatPutUpForSaleResult(
	nftAddress string, newPrice int64, resultErr error,
) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("💲*Put up for sale*\n"))
	b.WriteString(fmt.Sprintf(
		"*NFT:* `%s`\n",
		nftAddress,
	))
	b.WriteString("\n\n")

	if resultErr != nil {
		b.WriteString(fmt.Sprintf("*Status* failed ❗️\n"))
		b.WriteString("\nError: ")
		b.WriteString(resultErr.Error())
		return b.String()
	}

	b.WriteString(fmt.Sprintf("*Status* success ✅\n"))
	b.WriteString(fmt.Sprintf("*New price:* `%.2f TON`\n", tonFromNano(newPrice)))

	return b.String()
}

func formatMaxPriceIsLower(
	nftAddress string,
	maxPrice, price int64,
) string {
	return fmt.Sprintf(
		"🗿 *Max price* is lower than the actual price\n\n"+
			"*NFT:* `%s`\n"+
			"*MaxPrice:* `%.2f TON`\n"+
			"*Price:* `%.2f TON`\n",
		nftAddress,
		tonFromNano(maxPrice),
		tonFromNano(price),
	)
}

func formatSuccessfullyBought(nftAddress string) string {
	return fmt.Sprintf(
		"✅ *Successfully* bought\n\n"+
			"*NFT:* `%s`\n",
		nftAddress,
	)
}

func formatInvalidVersion(nftAddress string, reason string, nftResp ...string) string {
	message := fmt.Sprintf(
		"*Failed* fetch sale data\n\n"+
			"*NFT:* `%s`\n"+
			"*Reason:* `%s`\n",
		nftAddress,
		reason,
	)

	if len(nftResp) == 0 {
		return message
	}

	return fmt.Sprintf(
		"%s*GG response:* `%s`\n",
		message,
		nftResp[0],
	)
}

func formatFailedCreateTx(nftAddress string, errorMsg string) string {
	return fmt.Sprintf(
		"🚨 *Failed* create buy tx\n\n"+
			"*NFT:* `%s`\n"+
			"Error: `%s`\n",
		nftAddress,
		errorMsg,
	)
}
