package monitor

import (
	"fmt"
	"net/url"
	"strings"
)

func formatSignalAlert(
	getgemsWebURL string, event listingEvent,
	floorPrice, salePrice int64,
	actualDiscount, configuredPct float64,
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

func formatLowBalance (
	walletAddress string,
	balance, requiredBalance int64, 
) string {
	return fmt.Sprintf(
		"🚨🚨🚨 *Low Wallet Balance*\n\n"+
			"*WalletAddress:* `%s`\n"+
			"*Balance:* `%.2f TON`\n"+
			"*Required balance (for last signal):* `%.2f TON`\n",
		walletAddress,
		tonFromNano(balance),
		tonFromNano(requiredBalance),
	)
}