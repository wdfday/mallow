package telegram

import (
	"context"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"

	"mallow/identity/internal/config"
)

// Module provides the identity Telegram bot to fx.
// If TELEGRAM_GENERAL_BOT_TOKEN is empty, the module is a no-op.
var Module = fx.Module("telegram",
	fx.Provide(newBot),
	fx.Invoke(startBot),
)

func newBot(cfg *config.Config, rdb *redis.Client) (*Bot, error) {
	if cfg.Telegram.Token == "" {
		return nil, nil
	}
	return New(Config{
		Token:        cfg.Telegram.Token,
		AllowedChats: parseChats(cfg.Telegram.AllowedChats),
	}, rdb)
}

func startBot(lc fx.Lifecycle, bot *Bot) {
	if bot == nil {
		return
	}
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go bot.Start(context.Background())
			return nil
		},
	})
}

func parseChats(s string) []int64 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}
