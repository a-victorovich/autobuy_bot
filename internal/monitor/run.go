package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Run initialises shared monitor state, then starts the configured listing
// source until ctx is cancelled.
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

	if m.cfg.Getgems.UseWS {
		return m.runWebsocketListener(ctx)
	}

	return m.runHistoryPolling(ctx)
}

func (m *Monitor) runHistoryPolling(ctx context.Context) error {
	interval := time.Duration(m.cfg.Scanner.PollIntervalSeconds) * time.Second
	slog.Info("Starting history loops", "interval", interval)
	lastFloorRefreshAt := time.Now()
	lastBalanceRefreshAt := time.Now()

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

		if time.Since(lastBalanceRefreshAt) >= balanceRefreshEvery {
			_, err = m.updateWalletBalanceAndSeqno(ctx)
			if err != nil {
				slog.Warn("Periodic balance refresh failed", "err", err)
			} else {
				lastBalanceRefreshAt = time.Now()
			}
		}

		immediate := m.scanHistoryIteration(ctx, &giftCursor, collectionCursors)
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

func (m *Monitor) scanHistoryIteration(ctx context.Context, giftCursor *string, collectionCursors map[string]string) bool {
	immediate := false

	if m.hasGiftCollections() {
		nextCursor, shouldContinue, err := m.scanGiftHistoryBatch(ctx, *giftCursor)
		if err != nil {
			slog.Error("Gift scan error", "err", err)
		} else {
			*giftCursor = nextCursor
			immediate = shouldContinue
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

	return immediate
}
