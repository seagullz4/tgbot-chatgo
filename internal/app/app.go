package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/handler"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/service"
	"telegram-interactive-bot/go-bot/internal/store/sqlite"
)

// Run boots config, store, services, bot polling.
func Run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.ValidateRuntime(); err != nil {
		return fmt.Errorf("validate runtime config: %w", err)
	}

	st, err := sqlite.Open(cfg.DatabasePath, cfg.Workers+2)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer st.Close()

	jobs := job.New()
	defer jobs.Stop()

	svc := service.New(cfg, st, jobs, logger)
	h := handler.New(svc, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pollTimeout := time.Duration(cfg.PollTimeoutSeconds) * time.Second
	httpClient := &http.Client{
		Timeout: pollTimeout + 15*time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          cfg.HTTPMaxIdlePerHost * 4,
			MaxIdleConnsPerHost:   cfg.HTTPMaxIdlePerHost,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}

	opts := []bot.Option{
		bot.WithDefaultHandler(h.Default),
		bot.WithHTTPClient(pollTimeout, httpClient),
		bot.WithAllowedUpdates(bot.AllowedUpdates{
			models.AllowedUpdateMessage,
			models.AllowedUpdateEditedMessage,
			models.AllowedUpdateCallbackQuery,
		}),
		bot.WithUpdatesChannelCap(cfg.Workers * 64),
		bot.WithErrorsHandler(func(err error) {
			logger.Error("telegram polling error", "err", err)
		}),
		bot.WithWorkers(cfg.Workers),
		bot.WithNotAsyncHandlers(),
	}

	b, err := bot.New(cfg.BotToken, opts...)
	if err != nil {
		return fmt.Errorf("create bot: %w", err)
	}

	me, err := b.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("verify bot token: %w", err)
	}
	adminChat, err := b.GetChat(ctx, &bot.GetChatParams{ChatID: cfg.AdminGroupID})
	if err != nil {
		return fmt.Errorf("verify ADMIN_GROUP_ID %d: bot cannot access chat: %w", cfg.AdminGroupID, err)
	}
	if adminChat.Type != models.ChatTypeSupergroup {
		return fmt.Errorf("verify ADMIN_GROUP_ID %d: expected supergroup, got %s", cfg.AdminGroupID, adminChat.Type)
	}
	if !adminChat.IsForum {
		return fmt.Errorf("verify ADMIN_GROUP_ID %d: group topics are not enabled", cfg.AdminGroupID)
	}
	h.Register(b)
	svc.RegisterCommandMenus(ctx, b)

	logger.Info("bot starting (long polling)",
		"app", cfg.AppName,
		"bot_id", me.ID,
		"bot_username", me.Username,
		"admin_group", cfg.AdminGroupID,
		"admin_group_title", adminChat.Title,
		"db", cfg.DatabasePath,
		"verification_disabled", cfg.DisableVerification,
		"workers", cfg.Workers,
		"poll_timeout_seconds", cfg.PollTimeoutSeconds,
		"http_idle_per_host", cfg.HTTPMaxIdlePerHost,
	)

	// Start blocks until ctx is cancelled.
	b.Start(ctx)
	logger.Info("bot stopped")
	return nil
}
