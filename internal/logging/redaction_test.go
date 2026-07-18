package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactingHandlerRemovesTelegramTokens(t *testing.T) {
	var output bytes.Buffer
	shortToken := "123456:short-secret"
	logger := slog.New(newRedactingHandler(slog.NewTextHandler(&output, nil), func() string { return shortToken }))
	longToken := "123456:" + "ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"
	logger.Error("request "+shortToken, "err", errors.New("Post https://api.telegram.org/bot"+longToken+"/getMe: EOF"))
	text := output.String()
	if strings.Contains(text, shortToken) || strings.Contains(text, longToken) {
		t.Fatalf("log contains token: %s", text)
	}
	if strings.Count(text, "[REDACTED]") < 2 {
		t.Fatalf("redaction marker missing: %s", text)
	}
}
