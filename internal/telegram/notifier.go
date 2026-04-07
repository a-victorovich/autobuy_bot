package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	toncenterapi "github.com/yourorg/nft-scanner/internal/toncenter/openapi"
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

// SendTransactionResult sends the result of a signed transaction submit.
func (n *Notifier) SendTransactionResult(_ context.Context, nftAddress, saleVersion string, resp *toncenterapi.SendBocPostResp, sendErr error) error {
	var b strings.Builder
	b.WriteString("Transaction send result\n")
	b.WriteString("NFT: ")
	b.WriteString(nftAddress)
	b.WriteString("\nSale version: ")
	b.WriteString(saleVersion)

	if sendErr != nil {
		b.WriteString("\nStatus: failed")
		b.WriteString("\nError: ")
		b.WriteString(sendErr.Error())
		return n.send(b.String(), "")
	}

	if resp == nil {
		b.WriteString("\nStatus: failed")
		b.WriteString("\nError: empty response")
		return n.send(b.String(), "")
	}

	if resp.JSON200 == nil || !resp.JSON200.Ok {
		b.WriteString("\nStatus: rejected")
		b.WriteString("\nHTTP status: ")
		b.WriteString(fmt.Sprintf("%d", resp.StatusCode()))
		if len(resp.Body) > 0 {
			b.WriteString("\nBody: ")
			b.WriteString(string(resp.Body))
		}
		return n.send(b.String(), "")
	}

	b.WriteString("\nStatus: sent")
	if result, err := resp.JSON200.Result.AsTonlibResponseResult0(); err == nil && strings.TrimSpace(string(result)) != "" {
		b.WriteString("\nResult: ")
		b.WriteString(string(result))
	}

	return n.send(b.String(), "")
}

func (n *Notifier) send(msg, parseMode string) error {
	mc := tgbotapi.NewMessage(n.chatID, msg)
	mc.ParseMode = parseMode
	if _, err := n.bot.Send(mc); err != nil {
		return fmt.Errorf("sending Telegram message: %w", err)
	}
	return nil
}
