package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/function"
	"telegram-interactive-bot/go-bot/internal/handler"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/service"
	"telegram-interactive-bot/go-bot/internal/store"
)

type botSession struct {
	bot        *bot.Bot
	transport  *http.Transport
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	me         *models.User
	groupTitle string
}

type Controller struct {
	root      context.Context
	cfg       *config.Manager
	store     store.Store
	jobs      *job.Scheduler
	service   *service.Services
	handlers  *handler.Handlers
	functions *function.Manager
	logger    *slog.Logger
	mu        sync.Mutex
	updateMu  sync.Mutex
	menuMu    sync.Mutex
	active    *botSession
}

func NewController(root context.Context, cfg *config.Manager, st store.Store, jobs *job.Scheduler, svc *service.Services, handlers *handler.Handlers, functions *function.Manager, logger *slog.Logger) *Controller {
	return &Controller{root: root, cfg: cfg, store: st, jobs: jobs, service: svc, handlers: handlers, functions: functions, logger: logger}
}

func (c *Controller) Start() error {
	session, err := c.build(c.cfg.Current())
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.active = session
	c.mu.Unlock()
	c.start(session)
	return nil
}
func (c *Controller) Stop() {
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	c.menuMu.Lock()
	defer c.menuMu.Unlock()
	c.mu.Lock()
	session := c.active
	c.active = nil
	c.mu.Unlock()
	if session != nil {
		c.jobs.Stop()
		session.cancel()
		<-session.done
		session.transport.CloseIdleConnections()
	}
}
func (c *Controller) Info() function.BotInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return function.BotInfo{}
	}
	return function.BotInfo{ID: c.active.me.ID, Username: c.active.me.Username, GroupTitle: c.active.groupTitle}
}

func (c *Controller) Update(ctx context.Context, updates map[string]string, reset bool) error {
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	old := c.cfg.Current()
	candidate, err := c.cfg.Preview(updates)
	if err != nil {
		return err
	}
	if candidate.DatabasePath != old.DatabasePath {
		return fmt.Errorf("DATABASE_PATH 修改后需要重启进程")
	}
	if candidate.LogPath != old.LogPath || candidate.LogMaxSizeMB != old.LogMaxSizeMB || candidate.LogMaxBackups != old.LogMaxBackups {
		return fmt.Errorf("日志路径或滚动参数修改后需要重启进程")
	}
	if candidate.BotToken != old.BotToken || candidate.AdminGroupID != old.AdminGroupID {
		if _, _, err := c.validate(ctx, candidate); err != nil {
			return err
		}
	}
	hash, err := c.cfg.Write(updates)
	if err != nil {
		return err
	}
	if reset || candidate.AdminGroupID != old.AdminGroupID {
		c.jobs.Stop()
		if err := c.store.ResetConversationRouting(); err != nil {
			rollbackHash, rollbackErr := c.cfg.Write(config.Values(old))
			if rollbackErr == nil {
				c.cfg.Apply(old, rollbackHash)
			}
			return fmt.Errorf("reset conversation routing: %w", err)
		}
	}
	c.cfg.Apply(candidate, hash)
	c.afterApply(old, candidate)
	return nil
}

func (c *Controller) Reload(ctx context.Context) error {
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	candidate, hash, err := c.cfg.ReadCandidate()
	if err != nil {
		c.cfg.MarkObserved(hash)
		return err
	}
	if !c.cfg.Changed(hash) {
		return nil
	}
	old := c.cfg.Current()
	if candidate.DatabasePath != old.DatabasePath {
		c.cfg.MarkObserved(hash)
		return fmt.Errorf("DATABASE_PATH 修改需要重启进程")
	}
	if candidate.LogPath != old.LogPath || candidate.LogMaxSizeMB != old.LogMaxSizeMB || candidate.LogMaxBackups != old.LogMaxBackups {
		c.cfg.MarkObserved(hash)
		return fmt.Errorf("日志路径或滚动参数修改需要重启进程")
	}
	if candidate.BotToken != old.BotToken || candidate.AdminGroupID != old.AdminGroupID {
		if _, _, err := c.validate(ctx, candidate); err != nil {
			c.cfg.MarkObserved(hash)
			return err
		}
	}
	if candidate.AdminGroupID != old.AdminGroupID {
		c.jobs.Stop()
		if err := c.store.ResetConversationRouting(); err != nil {
			return err
		}
	}
	c.cfg.Apply(candidate, hash)
	c.afterApply(old, candidate)
	return nil
}

func (c *Controller) afterApply(old, candidate config.Config) {
	restart := old.BotToken != candidate.BotToken || old.Workers != candidate.Workers || old.PollTimeoutSeconds != candidate.PollTimeoutSeconds || old.HTTPMaxIdlePerHost != candidate.HTTPMaxIdlePerHost
	if restart {
		go func() {
			time.Sleep(300 * time.Millisecond)
			c.updateMu.Lock()
			defer c.updateMu.Unlock()
			current := c.cfg.Current()
			if !sameSessionConfig(current, candidate) {
				return
			}
			if err := c.restart(current); err != nil {
				c.logger.Error("restart bot session", "err", err)
				latest := c.cfg.Current()
				if !sameSessionConfig(latest, candidate) {
					return
				}
				latest, updates := rollbackSessionConfig(latest, old, candidate)
				if rollbackHash, rollbackErr := c.cfg.Write(updates); rollbackErr == nil {
					c.cfg.Apply(latest, rollbackHash)
					c.NotifyOwners("Bot 会话切换失败，已恢复旧配置：" + err.Error())
				}
			}
		}()
		return
	}
	if old.AdminGroupID != candidate.AdminGroupID || config.FormatIDs(old.AdminUserIDs) != config.FormatIDs(candidate.AdminUserIDs) || config.FormatIDs(old.OwnerUserIDs) != config.FormatIDs(candidate.OwnerUserIDs) {
		go c.refreshMenus(old)
	}
}

func redactTelegramError(err error, token string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if token != "" {
		message = strings.ReplaceAll(message, token, "[REDACTED]")
	}
	return errors.New(message)
}

func rollbackSessionConfig(latest, old, candidate config.Config) (config.Config, map[string]string) {
	latest.BotToken = old.BotToken
	latest.Workers = old.Workers
	latest.PollTimeoutSeconds = old.PollTimeoutSeconds
	latest.HTTPMaxIdlePerHost = old.HTTPMaxIdlePerHost
	updates := map[string]string{
		"BOT_TOKEN":              latest.BotToken,
		"BOT_WORKERS":            strconv.Itoa(latest.Workers),
		"POLL_TIMEOUT_SECONDS":   strconv.Itoa(latest.PollTimeoutSeconds),
		"HTTP_MAX_IDLE_PER_HOST": strconv.Itoa(latest.HTTPMaxIdlePerHost),
	}
	if old.BotToken != candidate.BotToken && latest.AdminGroupID != old.AdminGroupID {
		latest.AdminGroupID = old.AdminGroupID
		updates["ADMIN_GROUP_ID"] = strconv.FormatInt(old.AdminGroupID, 10)
	}
	return latest, updates
}

func sameSessionConfig(left, right config.Config) bool {
	return left.BotToken == right.BotToken && left.Workers == right.Workers && left.PollTimeoutSeconds == right.PollTimeoutSeconds && left.HTTPMaxIdlePerHost == right.HTTPMaxIdlePerHost
}

func (c *Controller) restart(cfg config.Config) error {
	session, err := c.build(cfg)
	if err != nil {
		return err
	}
	c.menuMu.Lock()
	defer c.menuMu.Unlock()
	c.mu.Lock()
	old := c.active
	c.mu.Unlock()
	if old != nil {
		c.jobs.Stop()
		old.cancel()
		<-old.done
		old.transport.CloseIdleConnections()
	}
	if err := c.root.Err(); err != nil {
		session.cancel()
		session.transport.CloseIdleConnections()
		return err
	}
	c.mu.Lock()
	c.active = session
	c.mu.Unlock()
	c.start(session)
	c.logger.Info("bot session reloaded", "bot_id", session.me.ID, "bot_username", session.me.Username, "admin_group", cfg.AdminGroupID)
	return nil
}
func (c *Controller) start(session *botSession) {
	go func() { defer close(session.done); session.bot.Start(session.ctx) }()
}

func (c *Controller) build(cfg config.Config) (*botSession, error) {
	b, me, title, transport, err := c.newBot(c.root, cfg, true)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(c.root)
	return &botSession{bot: b, transport: transport, ctx: ctx, cancel: cancel, done: make(chan struct{}), me: me, groupTitle: title}, nil
}
func (c *Controller) validate(ctx context.Context, cfg config.Config) (*models.User, string, error) {
	_, me, title, transport, err := c.newBot(ctx, cfg, false)
	if transport != nil {
		transport.CloseIdleConnections()
	}
	return me, title, err
}
func (c *Controller) newBot(ctx context.Context, cfg config.Config, register bool) (*bot.Bot, *models.User, string, *http.Transport, error) {
	poll := time.Duration(cfg.PollTimeoutSeconds) * time.Second
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: cfg.HTTPMaxIdlePerHost * 4, MaxIdleConnsPerHost: cfg.HTTPMaxIdlePerHost, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
	client := &http.Client{Timeout: poll + 15*time.Second, Transport: transport}
	succeeded := false
	defer func() {
		if !succeeded {
			transport.CloseIdleConnections()
		}
	}()
	options := []bot.Option{bot.WithSkipGetMe(), bot.WithHTTPClient(poll, client), bot.WithAllowedUpdates(bot.AllowedUpdates{models.AllowedUpdateMessage, models.AllowedUpdateEditedMessage, models.AllowedUpdateCallbackQuery}), bot.WithUpdatesChannelCap(cfg.Workers * 64), bot.WithWorkers(cfg.Workers), bot.WithNotAsyncHandlers(), bot.WithErrorsHandler(func(err error) {
		c.logger.Error("telegram polling error", "err", redactTelegramError(err, cfg.BotToken))
	})}
	if register {
		options = append(options, bot.WithDefaultHandler(c.handlers.Default))
	}
	b, err := bot.New(cfg.BotToken, options...)
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("create bot: %w", redactTelegramError(err, cfg.BotToken))
	}
	me, err := b.GetMe(ctx)
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("verify bot token: %w", redactTelegramError(err, cfg.BotToken))
	}
	chat, err := b.GetChat(ctx, &bot.GetChatParams{ChatID: cfg.AdminGroupID})
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("bot cannot access admin group %d: %w", cfg.AdminGroupID, redactTelegramError(err, cfg.BotToken))
	}
	if chat.Type != models.ChatTypeSupergroup || !chat.IsForum {
		return nil, nil, "", nil, fmt.Errorf("ADMIN_GROUP_ID must be a supergroup with Topics enabled")
	}
	member, err := b.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: cfg.AdminGroupID, UserID: me.ID})
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("check bot group permissions: %w", redactTelegramError(err, cfg.BotToken))
	}
	if member.Type != models.ChatMemberTypeOwner && (member.Administrator == nil || !member.Administrator.CanManageTopics) {
		return nil, nil, "", nil, fmt.Errorf("bot must be an administrator with topic management permission")
	}
	if register {
		c.handlers.Register(b, me.Username)
		c.functions.Register(b, me.Username)
		c.service.RegisterCommandMenus(ctx, b)
	}
	succeeded = true
	return b, me, chat.Title, transport, nil
}
func (c *Controller) refreshMenus(previous config.Config) {
	c.menuMu.Lock()
	defer c.menuMu.Unlock()
	current := c.cfg.Current()
	c.mu.Lock()
	session := c.active
	c.mu.Unlock()
	if session == nil {
		return
	}
	c.service.RefreshCommandMenus(c.root, session.bot, previous, current)
}
func (c *Controller) NotifyOwners(text string) {
	cfg := c.cfg.Current()
	c.mu.Lock()
	session := c.active
	c.mu.Unlock()
	if session == nil {
		return
	}
	for owner := range cfg.OwnerUserIDs {
		_, _ = session.bot.SendMessage(c.root, &bot.SendMessageParams{ChatID: owner, Text: text})
	}
}
