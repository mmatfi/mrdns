// Package audit writes an append-only JSONL trail of security- and
// change-relevant actions (logins, deploys, rollbacks, draft edits).
package audit

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is a single audit record. Empty fields are omitted from the JSON line.
type Event struct {
	Time      time.Time `json:"time"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor,omitempty"` // source IP (single-token auth)
	Zone      string    `json:"zone,omitempty"`
	Status    string    `json:"status,omitempty"`
	OldSerial uint32    `json:"old_serial,omitempty"`
	NewSerial uint32    `json:"new_serial,omitempty"`
	Servers   []string  `json:"servers,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// Logger appends events as JSON lines. It is safe for concurrent use, and its
// methods are safe to call on a nil *Logger (they no-op).
type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	c   io.Closer
	now func() time.Time
}

// New opens (or creates) the audit file for appending. An empty path yields a
// logger that discards events.
func New(path string) (*Logger, error) {
	if path == "" {
		return &Logger{w: io.Discard, now: time.Now}, nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	return &Logger{w: f, c: f, now: time.Now}, nil
}

// NewWriter returns a logger that writes to w (used in tests).
func NewWriter(w io.Writer) *Logger {
	return &Logger{w: w, now: time.Now}
}

// Log appends an event. A zero Time is filled with the current UTC time.
func (l *Logger) Log(e Event) {
	if l == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = l.now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	l.mu.Lock()
	_, _ = l.w.Write(b)
	l.mu.Unlock()
}

// Close closes the underlying file, if any.
func (l *Logger) Close() error {
	if l == nil || l.c == nil {
		return nil
	}
	return l.c.Close()
}
