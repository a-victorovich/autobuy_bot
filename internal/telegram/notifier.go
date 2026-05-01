package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Notifier sends messages to a Telegram chat via a bot.
type Notifier struct {
	bot    *tgbotapi.BotAPI
	chatID int64
	useWS  bool
	wsUp   atomic.Bool
	runAt  atomic.Int64
}

// New authenticates the bot and returns a ready Notifier.
func New(token string, chatID int64, useWS bool) (*Notifier, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("initialising Telegram bot: %w", err)
	}
	slog.Info("Telegram bot authorised", "username", bot.Self.UserName)
	return &Notifier{bot: bot, chatID: chatID, useWS: useWS}, nil
}

// SendSignal sends a formatted alert message for an under-priced NFT.
// ctx is accepted for future cancellation support (the underlying library is synchronous).
func (n *Notifier) SendSignal(_ context.Context, msg string) error {
	return n.send(msg, tgbotapi.ModeMarkdown)
}

// SendPlain sends a message without Telegram entity parsing.
func (n *Notifier) SendPlain(_ context.Context, msg string) error {
	return n.send(msg, "")
}

func (n *Notifier) send(msg, parseMode string) error {
	mc := tgbotapi.NewMessage(n.chatID, msg)
	mc.ParseMode = parseMode
	if _, err := n.bot.Send(mc); err != nil {
		return fmt.Errorf("sending Telegram message: %w", err)
	}
	return nil
}

func (n *Notifier) SetWebsocketConnected(connected bool) {
	n.wsUp.Store(connected)
}

func (n *Notifier) SetRunAt(runAt time.Time) {
	n.runAt.Store(runAt.UnixNano())
}

func (n *Notifier) ProcessIncoming(ctx context.Context) error {
	uc := tgbotapi.NewUpdate(0)
	uc.Timeout = 60
	uc.AllowedUpdates = []string{"message", "channel_post"}

	updates := n.bot.GetUpdatesChan(uc)
	defer n.bot.StopReceivingUpdates()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return nil
			}

			msg := update.Message
			if msg == nil {
				msg = update.ChannelPost
			}
			if msg == nil || msg.Chat == nil || msg.Chat.ID != n.chatID || msg.Command() != "status" {
				continue
			}

			transport := "http"
			if n.useWS {
				transport = "ws connected"
			}
			if n.useWS && !n.wsUp.Load() {
				transport = "ws disconnected"
			}

			runAt := "never"
			if runAtNano := n.runAt.Load(); runAtNano > 0 {
				runAt = time.Unix(0, runAtNano).Format(time.RFC3339)
			}

			if err := n.SendSignal(ctx, fmt.Sprintf(
				"✅ *Bot Status*\n\n"+
					"*Transport:* `%s`\n"+
					"*Run at:* `%s`\n",
				transport,
				runAt,
			)); err != nil {
				slog.Warn("Failed to respond to Telegram status command", "err", err)
			}
		}
	}
}
