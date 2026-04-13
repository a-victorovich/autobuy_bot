package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/yourorg/nft-scanner/internal/config"
	"github.com/yourorg/nft-scanner/internal/getgems"
	"github.com/yourorg/nft-scanner/internal/getgems/openapi"
)

func main() {
	baseURL := flag.String("base-url", config.DefaultGetgemsBaseURL, "Getgems base URL")
	apiKey := flag.String("api-key", "", "optional Getgems API key")
	percent := flag.Float64("percent", 10, "discount percent for each collection")
	flag.Parse()

	if *percent < -100 || *percent > 100 {
		exitf("percent must be between -100 and 100, got %v", *percent)
	}

	client := getgems.New(*apiKey, *baseURL)

	addresses, err := fetchGiftCollectionAddresses(context.Background(), client)
	if err != nil {
		exitf("fetch gift collections: %v", err)
	}

	sort.Strings(addresses)

	fmt.Println("gift_collections:")
	for _, addr := range addresses {
		fmt.Printf("  %q: %s\n", addr, formatPercent(*percent))
	}
}

func fetchGiftCollectionAddresses(ctx context.Context, client *openapi.ClientWithResponses) ([]string, error) {
	limit := openapi.ParametersLimitParameter(100)

	uniq := make(map[string]struct{})
	var after *openapi.ParametersAfterParameter

	for {
		resp, err := client.V1GetGiftCollectionsWithResponse(ctx, &openapi.V1GetGiftCollectionsParams{
			After: after,
			Limit: &limit,
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode(), string(resp.Body))
		}

		for _, item := range resp.JSON200.Response.Items {
			if item.Address != "" {
				uniq[item.Address] = struct{}{}
			}
		}

		if resp.JSON200.Response.Cursor == nil || *resp.JSON200.Response.Cursor == "" {
			break
		}

		next := openapi.ParametersAfterParameter(*resp.JSON200.Response.Cursor)
		after = &next
	}

	addresses := make([]string, 0, len(uniq))
	for addr := range uniq {
		addresses = append(addresses, addr)
	}

	return addresses, nil
}

func formatPercent(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
