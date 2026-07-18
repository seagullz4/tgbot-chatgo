package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingWriterRotatesSnapshotsAndClears(t *testing.T) {
	writer, err := New(filepath.Join(t.TempDir(), "bot.log"), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	payload := bytes.Repeat([]byte("x"), 700*1024)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}

	snapshot, err := writer.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	files := snapshot.Files()
	if len(files) < 2 {
		t.Fatalf("rotation files = %d", len(files))
	}
	for _, file := range files {
		info, err := os.Stat(file.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != file.Size {
			t.Fatalf("snapshot size for %s = %d, want %d", file.Name, info.Size(), file.Size)
		}
	}
	directory := snapshot.directory
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("snapshot directory was not removed: %v", err)
	}

	if err := writer.Clear(); err != nil {
		t.Fatal(err)
	}
	snapshot, err = writer.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	files = snapshot.Files()
	if len(files) != 1 || files[0].Size != 0 {
		t.Fatalf("files after clear = %#v", files)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("after-clear")); err != nil {
		t.Fatal(err)
	}
}
func TestClearReportsArchiveRemovalFailure(t *testing.T) {
	directory := t.TempDir()
	writer, err := New(filepath.Join(directory, "bot.log"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	blockedArchive := filepath.Join(directory, "bot.log.1")
	if err := os.Mkdir(blockedArchive, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedArchive, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.Clear(); err == nil {
		t.Fatal("expected archive removal error")
	}
}
