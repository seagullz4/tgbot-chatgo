package function

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/logging"
)

const safeTelegramDocumentSize = 45 * 1024 * 1024

func (m *Manager) Logs(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !m.ownerPrivate(update.Message) {
		return
	}
	m.sendLogs(ctx, b, update.Message.From.ID)
}

func (m *Manager) sendLogs(ctx context.Context, b *bot.Bot, userID int64) {
	snapshot, err := m.logs.Snapshot()
	if err != nil {
		m.reply(ctx, b, userID, "读取日志失败："+err.Error())
		return
	}
	defer snapshot.Close()

	files := snapshot.Files()
	var totalBytes int64
	for _, file := range files {
		totalBytes += file.Size
	}
	if totalBytes == 0 {
		m.reply(ctx, b, userID, "暂无日志")
		return
	}

	archive, err := createLogArchive(files)
	if err != nil {
		m.reply(ctx, b, userID, "打包日志失败："+err.Error())
		return
	}
	archiveName := archive.Name()
	defer func() {
		archive.Close()
		_ = os.Remove(archiveName)
	}()

	info, err := archive.Stat()
	if err != nil {
		m.reply(ctx, b, userID, "读取日志压缩包失败："+err.Error())
		return
	}
	if info.Size() <= safeTelegramDocumentSize {
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			m.reply(ctx, b, userID, "读取日志压缩包失败："+err.Error())
			return
		}
		if _, err := b.SendDocument(ctx, &bot.SendDocumentParams{ChatID: userID, Document: &models.InputFileUpload{Filename: "bot-logs.zip", Data: archive}}); err != nil {
			m.reply(ctx, b, userID, "发送日志失败："+err.Error())
		}
		return
	}
	if err := sendSnapshotFiles(ctx, b, userID, files); err != nil {
		m.reply(ctx, b, userID, "发送日志失败："+err.Error())
	}
}

func createLogArchive(files []logging.SnapshotFile) (*os.File, error) {
	archiveFile, err := os.CreateTemp("", "go-bot-logs-*.zip")
	if err != nil {
		return nil, err
	}
	cleanup := func(err error) (*os.File, error) {
		name := archiveFile.Name()
		archiveFile.Close()
		_ = os.Remove(name)
		return nil, err
	}
	if err := archiveFile.Chmod(0o600); err != nil {
		return cleanup(err)
	}
	archive := zip.NewWriter(archiveFile)
	for _, file := range files {
		source, err := os.Open(file.Path)
		if err != nil {
			_ = archive.Close()
			return cleanup(err)
		}
		entry, err := archive.Create(file.Name)
		if err == nil {
			_, err = io.Copy(entry, source)
		}
		closeErr := source.Close()
		if err != nil {
			_ = archive.Close()
			return cleanup(err)
		}
		if closeErr != nil {
			_ = archive.Close()
			return cleanup(closeErr)
		}
	}
	if err := archive.Close(); err != nil {
		return cleanup(err)
	}
	if err := archiveFile.Sync(); err != nil {
		return cleanup(err)
	}
	return archiveFile, nil
}

func sendSnapshotFiles(ctx context.Context, b *bot.Bot, userID int64, files []logging.SnapshotFile) error {
	for _, file := range files {
		source, err := os.Open(file.Path)
		if err != nil {
			return err
		}
		_, sendErr := b.SendDocument(ctx, &bot.SendDocumentParams{ChatID: userID, Document: &models.InputFileUpload{Filename: file.Name, Data: source}})
		closeErr := source.Close()
		if sendErr != nil {
			return sendErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (m *Manager) ClearLogs(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !m.ownerPrivate(update.Message) {
		return
	}
	m.mu.Lock()
	m.confirmations[update.Message.From.ID] = confirmation{clearLogs: true, expires: time.Now().Add(time.Minute)}
	m.mu.Unlock()
	m.sendConfirm(ctx, b, update.Message.From.ID, "确认清除当前及所有归档日志？")
}
