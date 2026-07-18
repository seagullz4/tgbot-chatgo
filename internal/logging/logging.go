package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
}

func New(path string, maxSizeMB, backups int) (*RotatingWriter, error) {
	if maxSizeMB < 1 {
		maxSizeMB = 10
	}
	if backups < 1 {
		backups = 5
	}
	writer := &RotatingWriter{path: path, maxBytes: int64(maxSizeMB) * 1024 * 1024, backups: backups}
	if err := writer.open(); err != nil {
		return nil, err
	}
	return writer, nil
}

func NewLogger(writer *RotatingWriter, tokenProviders ...func() string) *slog.Logger {
	var tokenProvider func() string
	if len(tokenProviders) > 0 {
		tokenProvider = tokenProviders[0]
	}
	handler := slog.NewTextHandler(io.MultiWriter(os.Stdout, writer), &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(newRedactingHandler(handler, tokenProvider))
}

func (w *RotatingWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *RotatingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size+int64(len(data)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := w.file.Write(data)
	w.size += int64(written)
	return written, err
}

func (w *RotatingWriter) rotate() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	for index := w.backups - 1; index >= 1; index-- {
		source := fmt.Sprintf("%s.%d", w.path, index)
		target := fmt.Sprintf("%s.%d", w.path, index+1)
		if err := removeIfExists(target); err != nil {
			return err
		}
		if _, err := os.Stat(source); err == nil {
			if err := os.Rename(source, target); err != nil {
				return err
			}
		}
	}
	if err := removeIfExists(w.path + ".1"); err != nil {
		return err
	}
	if _, err := os.Stat(w.path); err == nil {
		if err := os.Rename(w.path, w.path+".1"); err != nil {
			return err
		}
	}
	return w.open()
}

func (w *RotatingWriter) Clear() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	for index := 1; index <= w.backups; index++ {
		if err := removeIfExists(fmt.Sprintf("%s.%d", w.path, index)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(w.path, nil, 0o600); err != nil {
		return err
	}
	return w.open()
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
