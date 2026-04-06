package toncenter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/yourorg/nft-scanner/internal/config"
	"github.com/yourorg/nft-scanner/internal/toncenter/openapi"
)

const defaultTimeout = 15 * time.Second

func New(apiKey, baseURL string) *openapi.ClientWithResponses {
	if baseURL == "" {
		baseURL = config.DefaultToncenterBaseURL
	}

	client, err := openapi.NewClientWithResponses(
		baseURL,
		openapi.WithHTTPClient(&http.Client{Timeout: defaultTimeout}),
		openapi.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			req.Header.Set("Accept", "application/json")
			if apiKey != "" {
				req.Header.Set("X-API-Key", apiKey)
			}
			return nil
		}),
	)
	if err != nil {
		panic(fmt.Sprintf("create toncenter openapi client: %v", err))
	}

	return client
}
