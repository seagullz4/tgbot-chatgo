package function

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"telegram-interactive-bot/go-bot/internal/logging"
)

func TestCreateLogArchiveStreamsSnapshotFiles(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "bot.log")
	secondPath := filepath.Join(directory, "bot.log.1")
	if err := os.WriteFile(firstPath, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []logging.SnapshotFile{
		{Name: "bot.log", Path: firstPath, Size: 7},
		{Name: "bot.log.1", Path: secondPath, Size: 7},
	}
	archiveFile, err := createLogArchive(files)
	if err != nil {
		t.Fatal(err)
	}
	archiveName := archiveFile.Name()
	defer os.Remove(archiveName)
	defer archiveFile.Close()
	info, err := archiveFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(archiveFile, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 2 {
		t.Fatalf("archive entries = %d", len(reader.File))
	}
	for _, entry := range reader.File {
		opened, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(opened)
		closeErr := opened.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if string(data) != "current" && string(data) != "archive" {
			t.Fatalf("unexpected archive content %q", data)
		}
	}
}
