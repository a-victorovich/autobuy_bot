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

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL.String(), header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial getgems websocket: status %s: %w", resp.Status, err)
		}
		return fmt.Errorf("dial getgems websocket: %w", err)
	}
	defer conn.Close()

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
	defer close(done)

	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("Websocket listener shutting down")
				return ctx.Err()
			}
			return fmt.Errorf("read getgems websocket event: %w", err)
		}

		slog.Info("Received websocket event",
			"type", websocketMessageType(messageType),
			"payload", string(payload),
		)
		m.handleWebsocketMessage(messageType, payload)
	}
}

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

func (m *Monitor) handleWebsocketMessage(messageType int, payload []byte) {
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
		m.handleWebsocketHistoryMessage(websocketHistoryMessage{
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

func (m *Monitor) handleWebsocketHistoryMessage(msg websocketHistoryMessage) {
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
