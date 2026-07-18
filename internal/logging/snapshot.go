package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SnapshotFile describes one immutable file in a disk-backed log snapshot.
type SnapshotFile struct {
	Name string
	Path string
	Size int64
}

// Snapshot is a consistent point-in-time copy of the current and archived logs.
type Snapshot struct {
	directory string
	files     []SnapshotFile
}

func (s *Snapshot) Files() []SnapshotFile {
	return append([]SnapshotFile(nil), s.files...)
}

func (s *Snapshot) Close() error {
	if s == nil || s.directory == "" {
		return nil
	}
	directory := s.directory
	s.directory = ""
	s.files = nil
	return os.RemoveAll(directory)
}

// Snapshot copies logs to temporary files while holding the writer lock.
// Compression and upload then proceed without blocking normal log writes.
func (w *RotatingWriter) Snapshot() (*Snapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		if err := w.file.Sync(); err != nil {
			return nil, err
		}
	}
	directory, err := os.MkdirTemp("", "go-bot-logs-*")
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{directory: directory, files: make([]SnapshotFile, 0, w.backups+1)}
	cleanup := func(err error) (*Snapshot, error) {
		_ = snapshot.Close()
		return nil, err
	}

	for index := 0; index <= w.backups; index++ {
		sourcePath := w.path
		if index > 0 {
			sourcePath = fmt.Sprintf("%s.%d", w.path, index)
		}
		source, err := os.Open(sourcePath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return cleanup(err)
		}
		name := filepath.Base(sourcePath)
		targetPath := filepath.Join(directory, name)
		target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			source.Close()
			return cleanup(err)
		}
		size, copyErr := io.Copy(target, source)
		closeSourceErr := source.Close()
		closeTargetErr := target.Close()
		if copyErr != nil {
			return cleanup(copyErr)
		}
		if closeSourceErr != nil {
			return cleanup(closeSourceErr)
		}
		if closeTargetErr != nil {
			return cleanup(closeTargetErr)
		}
		snapshot.files = append(snapshot.files, SnapshotFile{Name: name, Path: targetPath, Size: size})
	}
	return snapshot, nil
}
