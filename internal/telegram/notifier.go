package telegram

import (
	"context"
	"fmt"
	"log/slog"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Notifier sends messages to a Telegram chat via a bot.
type Notifier struct {
	bot    *tgbotapi.BotAPI
	chatID int64
}

// New authenticates the bot and returns a ready Notifier.
func New(token string, chatID int64) (*Notifier, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("initialising Telegram bot: %w", err)
	}
	slog.Info("Telegram bot authorised", "username", bot.Self.UserName)
	return &Notifier{bot: bot, chatID: chatID}, nil
}

// SendSignal sends a formatted alert message for an under-priced NFT.
// ctx is accepted for future cancellation support (the underlying library is synchronous).
func (n *Notifier) SendSignal(_ context.Context, msg string) error {
	return n.send(msg, tgbotapi.ModeMarkdown)
}

func (n *Notifier) send(msg, parseMode string) error {
	mc := tgbotapi.NewMessage(n.chatID, msg)
	mc.ParseMode = parseMode
	if _, err := n.bot.Send(mc); err != nil {
		return fmt.Errorf("sending Telegram message: %w", err)
	}
	return nil
}
