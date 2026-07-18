package logging

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

var telegramTokenPattern = regexp.MustCompile(`[0-9]{5,}:[A-Za-z0-9_-]{20,}`)

type redactingHandler struct {
	next          slog.Handler
	tokenProvider func() string
}

func newRedactingHandler(next slog.Handler, tokenProvider func() string) slog.Handler {
	return &redactingHandler{next: next, tokenProvider: tokenProvider}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, h.redact(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(h.redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for index, attr := range attrs {
		clean[index] = h.redactAttr(attr)
	}
	return &redactingHandler{next: h.next.WithAttrs(clean), tokenProvider: h.tokenProvider}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), tokenProvider: h.tokenProvider}
}

func (h *redactingHandler) redactAttr(attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(h.redact(value.String()))
	case slog.KindAny:
		switch item := value.Any().(type) {
		case error:
			attr.Value = slog.StringValue(h.redact(item.Error()))
		case string:
			attr.Value = slog.StringValue(h.redact(item))
		}
	case slog.KindGroup:
		group := value.Group()
		clean := make([]slog.Attr, len(group))
		for index, child := range group {
			clean[index] = h.redactAttr(child)
		}
		attr.Value = slog.GroupValue(clean...)
	}
	return attr
}

func (h *redactingHandler) redact(text string) string {
	if h.tokenProvider != nil {
		if token := strings.TrimSpace(h.tokenProvider()); token != "" {
			text = strings.ReplaceAll(text, token, "[REDACTED]")
		}
	}
	return telegramTokenPattern.ReplaceAllString(text, "[REDACTED]")
}
