package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

func (m *Monitor) runWebsocketListener(ctx context.Context) error {
	if m.cfg.Getgems.WSURL == "" {
		return fmt.Errorf("getgems.ws_url is required when getgems.use_ws is true")
	}

	slog.Info("Starting websocket listener", "url", m.cfg.Getgems.WSURL)

	header := http.Header{}
	if m.cfg.Getgems.APIKey != "" {
		header.Set("X-API-Key", m.cfg.Getgems.APIKey)
	}

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, m.cfg.Getgems.WSURL, header)
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
	}
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
