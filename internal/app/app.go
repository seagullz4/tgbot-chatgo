package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"telegram-interactive-bot/go-bot/internal/config"
	functionpkg "telegram-interactive-bot/go-bot/internal/function"
	"telegram-interactive-bot/go-bot/internal/handler"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/logging"
	"telegram-interactive-bot/go-bot/internal/service"
	"telegram-interactive-bot/go-bot/internal/store/sqlite"
)

func Run() error {
	cfgManager, err := config.OpenManager()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg := cfgManager.Current()
	logWriter, err := logging.New(cfg.LogPath, cfg.LogMaxSizeMB, cfg.LogMaxBackups)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer logWriter.Close()
	logger := logging.NewLogger(logWriter, func() string { return cfgManager.Snapshot().BotToken })

	st, err := sqlite.Open(cfg.DatabasePath, cfg.Workers+2)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer st.Close()
	jobs := job.New()
	defer jobs.Stop()
	svc := service.New(cfgManager, st, jobs, logger)
	handlers := handler.New(svc, logger)
	functions := functionpkg.New(cfgManager, logWriter, logger)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	controller := NewController(ctx, cfgManager, st, jobs, svc, handlers, functions, logger)
	functions.SetController(controller)
	if err := controller.Start(); err != nil {
		return err
	}
	defer controller.Stop()
	logger.Info("bot started", "app", cfg.AppName, "db", cfg.DatabasePath, "env_file", cfgManager.Path())
	go watchConfig(ctx, cfgManager, controller, logger)
	<-ctx.Done()
	logger.Info("bot stopped")
	return nil
}

func watchConfig(ctx context.Context, manager *config.Manager, controller *Controller, logger interface{ Error(string, ...any) }) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			candidate, hash, err := manager.ReadCandidate()
			_ = candidate
			if err != nil {
				if manager.Changed(hash) {
					manager.MarkObserved(hash)
					logger.Error("reload env file", "err", err)
					controller.NotifyOwners(".env 热重载失败：" + err.Error())
				}
				continue
			}
			if !manager.Changed(hash) {
				continue
			}
			if err := controller.Reload(ctx); err != nil {
				logger.Error("apply env reload", "err", err)
				controller.NotifyOwners(".env 热重载失败：" + err.Error())
			}
		}
	}
}
