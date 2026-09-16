package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"

	"avatars/internal/events"
)

type Writer interface {
	Append(event events.Envelope) error
	Flush() error
	Close() error
	Path() string
	SessionID() string
}

type JSONLWriter struct {
	file        *os.File
	writer      *bufio.Writer
	encoder     *json.Encoder
	path        string
	sessionID   string
	appendCount int // incremented on each Append; used in Close to detect 0-byte flush loss
}

func NewJSONLWriter(baseDir string, sessionID string) (*JSONLWriter, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}

	path := filepath.Join(baseDir, sessionID+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}

	buffered := bufio.NewWriter(file)
	encoder := json.NewEncoder(buffered)
	encoder.SetEscapeHTML(false)

	return &JSONLWriter{
		file:      file,
		writer:    buffered,
		encoder:   encoder,
		path:      path,
		sessionID: sessionID,
	}, nil
}

func (w *JSONLWriter) Append(event events.Envelope) error {
	if err := w.encoder.Encode(event); err != nil {
		return err
	}
	w.appendCount++
	// Transcript is the source of truth for resume. Flush the
	// bufio buffer to the file and fsync the file to disk before
	// returning so a crash or graceful shutdown that skips Close()
	// does not leave an empty 0-byte file on disk. The overhead of
	// one fsync per event is sub-millisecond on local SSD and is
	// the price of having a durable audit trail.
	if err := w.writer.Flush(); err != nil {
		return err
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	return nil
}

func (w *JSONLWriter) Flush() error {
	return w.writer.Flush()
}

func (w *JSONLWriter) Close() error {
	if err := w.Flush(); err != nil {
		_ = w.file.Close()
		return err
	}
	// Guard against silent 0-byte transcript files: if Append was called
	// at least once but the file on disk is still empty, fsync/flush
	// lost data and resume would silently start fresh.
	if w.appendCount > 0 {
		if info, statErr := w.file.Stat(); statErr == nil && info.Size() == 0 {
			_ = w.file.Close()
			return &os.PathError{Op: "transcript_close_stat", Path: w.path, Err: os.ErrInvalid}
		}
	}
	return w.file.Close()
}

func (w *JSONLWriter) Path() string {
	return w.path
}

func (w *JSONLWriter) SessionID() string {
	return w.sessionID
}
