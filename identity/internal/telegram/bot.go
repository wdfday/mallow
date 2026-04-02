package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	tgbot "github.com/go-telegram/bot"
	tgmodels "github.com/go-telegram/bot/models"
	"github.com/redis/go-redis/v9"
)

const (
	otpPrefix = "telegram:link:"
	otpTTL    = 5 * time.Minute
)

// Config holds Telegram bot configuration.
type Config struct {
	Token        string
	AllowedChats []int64
}

// Bot is the identity service's general-purpose Telegram bot.
// It handles onboarding (/start, /help) and account linking (/link).
type Bot struct {
	cfg Config
	bot *tgbot.Bot
	rdb *redis.Client
}

// New creates and wires the identity Telegram bot.
func New(cfg Config, rdb *redis.Client) (*Bot, error) {
	b := &Bot{cfg: cfg, rdb: rdb}

	tg, err := tgbot.New(cfg.Token,
		// "/start link" is sent when user opens https://t.me/bot?start=link deep link.
		// Must be registered before the plain "/start" handler (more specific wins).
		tgbot.WithMessageTextHandler("/start link", tgbot.MatchTypeExact, b.handleLink),
		tgbot.WithMessageTextHandler("/start", tgbot.MatchTypeExact, b.handleStart),
		tgbot.WithMessageTextHandler("/help", tgbot.MatchTypeExact, b.handleHelp),
		tgbot.WithMessageTextHandler("/link", tgbot.MatchTypeExact, b.handleLink),
		tgbot.WithDefaultHandler(b.handleDefault),
	)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}
	b.bot = tg
	return b, nil
}

// Start begins polling for updates. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) {
	slog.Info("identity telegram bot started")
	b.bot.Start(ctx)
}

// isAllowed returns true if the chat may interact with this bot.
func (b *Bot) isAllowed(chatID int64) bool {
	if len(b.cfg.AllowedChats) == 0 {
		return true
	}
	for _, id := range b.cfg.AllowedChats {
		if id == chatID {
			return true
		}
	}
	return false
}

func (b *Bot) handleStart(ctx context.Context, bot *tgbot.Bot, update *tgmodels.Update) {
	if update.Message == nil {
		return
	}
	chatID := update.Message.Chat.ID
	if !b.isAllowed(chatID) {
		return
	}

	text := "*Welcome to the Trading Platform*\n\n" +
		"This bot handles account setup\\.\n\n" +
		"*Commands:*\n" +
		"`/link` — Link your Telegram account to the platform\n" +
		"`/help` — Show this help\n\n" +
		"Once linked, use the *Strategist* bot for AI\\-powered trading\\."

	_, _ = bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: tgmodels.ParseModeMarkdown,
	})
}

func (b *Bot) handleHelp(ctx context.Context, bot *tgbot.Bot, update *tgmodels.Update) {
	if update.Message == nil {
		return
	}
	b.handleStart(ctx, bot, update)
}

func (b *Bot) handleLink(ctx context.Context, bot *tgbot.Bot, update *tgmodels.Update) {
	if update.Message == nil {
		return
	}
	chatID := update.Message.Chat.ID
	if !b.isAllowed(chatID) {
		return
	}

	otp, err := randomHex(3) // 6 hex chars
	if err != nil {
		slog.Error("identity bot: failed to generate OTP", "err", err)
		_, _ = bot.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:    chatID,
			Text:      "Failed to generate link code\\. Please try again later\\.",
			ParseMode: tgmodels.ParseModeMarkdown,
		})
		return
	}

	type linkPayload struct {
		ID       string `json:"id"`
		Username string `json:"username,omitempty"`
	}
	rawPayload, _ := json.Marshal(linkPayload{
		ID:       fmt.Sprintf("%d", chatID),
		Username: update.Message.From.Username,
	})
	key := otpPrefix + otp
	if err := b.rdb.Set(ctx, key, string(rawPayload), otpTTL).Err(); err != nil {
		slog.Error("identity bot: failed to store OTP", "err", err)
		_, _ = bot.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:    chatID,
			Text:      "Failed to generate link code\\. Please try again later\\.",
			ParseMode: tgmodels.ParseModeMarkdown,
		})
		return
	}

	text := fmt.Sprintf(
		"*Link your account*\n\n"+
			"Enter this code in the app under *Settings → Link Telegram*:\n\n"+
			"`%s`\n\n"+
			"⏱ Valid for 5 minutes\\.",
		otp,
	)
	_, _ = bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: tgmodels.ParseModeMarkdown,
	})
}

func (b *Bot) handleDefault(ctx context.Context, bot *tgbot.Bot, update *tgmodels.Update) {
	if update.Message == nil {
		return
	}
	chatID := update.Message.Chat.ID
	if !b.isAllowed(chatID) {
		return
	}

	_, _ = bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      "Use `/link` to link your account, or `/help` for more info\\.",
		ParseMode: tgmodels.ParseModeMarkdown,
	})
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
