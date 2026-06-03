package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLoggerWritesJSONL(t *testing.T) {
	var buf bytes.Buffer
	l := NewWriter(&buf)
	l.Log(Event{Action: "deploy", Zone: "example.com", Status: "success", NewSerial: 2026060302})
	l.Log(Event{Action: "login", Actor: "127.0.0.1"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}
	var e Event
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Action != "deploy" || e.Zone != "example.com" || e.NewSerial != 2026060302 {
		t.Errorf("unexpected event: %+v", e)
	}
	if e.Time.IsZero() {
		t.Error("time was not populated")
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Log(Event{Action: "noop"}) // must not panic
	if err := l.Close(); err != nil {
		t.Errorf("Close on nil logger: %v", err)
	}
}

func TestEmptyPathDiscards(t *testing.T) {
	l, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	l.Log(Event{Action: "x"}) // discarded, no panic
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
