package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	getgemsapi "github.com/yourorg/nft-scanner/internal/getgems/openapi"

	"github.com/gorilla/websocket"
)

type websocketMessage struct {
	Type         string                        `json:"type"`
	Subscribe    []string                      `json:"subscribe"`
	HistoryEvent getgemsapi.NftItemHistoryItem `json:"historyEvent"`
	IsGiftEvent  bool                          `json:"isGiftEvent"`
}

type websocketSubscriptionsMessage struct {
	Type      string   `json:"type"`
	Subscribe []string `json:"subscribe"`
}

type websocketHistoryMessage struct {
	Type         string                        `json:"type"`
	HistoryEvent getgemsapi.NftItemHistoryItem `json:"historyEvent"`
	IsGiftEvent  bool                          `json:"isGiftEvent"`
}

func (m *Monitor) runWebsocketListener(ctx context.Context) error {
	if m.cfg.Getgems.WSURL == "" {
		return fmt.Errorf("getgems.ws_url is required when getgems.use_ws is true")
	}

	slog.Info("Starting websocket listener", "url", m.cfg.Getgems.WSURL)

	header := http.Header{}
	if m.cfg.Getgems.APIKey != "" {
		header.Set("Authorization", m.cfg.Getgems.APIKey)
	}

	wsURL, err := url.Parse(m.cfg.Getgems.WSURL)
	if err != nil {
		return fmt.Errorf("parse getgems websocket url: %w", err)
	}
	query := wsURL.Query()
	query.Set("subscriptions", "giftsPutUpForSale")
	wsURL.RawQuery = query.Encode()

	for {
		conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL.String(), header)
		if err != nil {
			m.setWebsocketConnected(false)
			if ctx.Err() != nil {
				slog.Info("Websocket listener shutting down")
				return ctx.Err()
			}
			if resp != nil {
				slog.Error("Failed to dial getgems websocket", "status", resp.Status, "err", err)
			} else {
				slog.Error("Failed to dial getgems websocket", "err", err)
			}

			if err := waitWebsocketReconnect(ctx); err != nil {
				slog.Info("Websocket listener shutting down")
				return err
			}
			continue
		}
		m.setWebsocketConnected(true)

		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "context cancelled"),
					time.Now().Add(time.Second),
				)
				_ = conn.Close()
			case <-done:
			}
		}()

		for {
			messageType, payload, err := conn.ReadMessage()
			if err != nil {
				close(done)
				_ = conn.Close()
				m.setWebsocketConnected(false)

				if ctx.Err() != nil {
					slog.Info("Websocket listener shutting down")
					return ctx.Err()
				}
				slog.Error("Failed to read getgems websocket event", "err", err)

				if err := waitWebsocketReconnect(ctx); err != nil {
					slog.Info("Websocket listener shutting down")
					return err
				}
				break
			}

			slog.Info("Received websocket event",
				"type", websocketMessageType(messageType),
				"payload", string(payload),
			)
			m.handleWebsocketMessage(ctx, messageType, payload)
		}
	}
}

func (m *Monitor) setWebsocketConnected(connected bool) {
	if m.notifier != nil {
		m.notifier.SetWebsocketConnected(connected)
	}
}

func waitWebsocketReconnect(ctx context.Context) error {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *Monitor) handleWebsocketMessage(ctx context.Context, messageType int, payload []byte) {
	if messageType != websocket.TextMessage {
		return
	}

	var msg websocketMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		slog.Error("Failed to parse websocket text message", "err", err, "message", string(payload))
		return
	}

	switch msg.Type {
	case "subscriptions":
		m.handleWebsocketSubscriptionsMessage(websocketSubscriptionsMessage{
			Type:      msg.Type,
			Subscribe: msg.Subscribe,
		})
	case "history":
		m.handleWebsocketHistoryMessage(ctx, websocketHistoryMessage{
			Type:         msg.Type,
			HistoryEvent: msg.HistoryEvent,
			IsGiftEvent:  msg.IsGiftEvent,
		})
	default:
		slog.Warn("Unsupported websocket text message type", "type", msg.Type, "message", string(payload))
	}
}

func (m *Monitor) handleWebsocketSubscriptionsMessage(msg websocketSubscriptionsMessage) {
	for _, subscription := range msg.Subscribe {
		if subscription == "giftsPutUpForSale" {
			slog.Info("Successfully connected to websocket giftsPutUpForSale subscription")
			return
		}
	}

	slog.Warn("Subscription does not have giftsPutUpForSale value", "subscribe", msg.Subscribe)
}

func (m *Monitor) handleWebsocketHistoryMessage(ctx context.Context, msg websocketHistoryMessage) {
	watchedCollections := m.cfg.Collections
	if msg.IsGiftEvent {
		watchedCollections = m.cfg.GiftCollections
	}

	m.processItemsWithWorkerPool(ctx, []getgemsapi.NftItemHistoryItem{msg.HistoryEvent}, watchedCollections)
}

func websocketMessageType(messageType int) string {
	switch messageType {
	case websocket.TextMessage:
		return "text"
	case websocket.BinaryMessage:
		return "binary"
	case websocket.CloseMessage:
		return "close"
	case websocket.PingMessage:
		return "ping"
	case websocket.PongMessage:
		return "pong"
	default:
		return strconv.Itoa(messageType)
	}
}
