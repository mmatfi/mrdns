package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func newTestStore(t *testing.T, keep int) *Store {
	t.Helper()
	d := t.TempDir()
	s, err := New(
		filepath.Join(d, "live"),
		filepath.Join(d, "drafts"),
		filepath.Join(d, "backups"),
		filepath.Join(d, "locks"),
		keep,
	)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestDraftLifecycle(t *testing.T) {
	s := newTestStore(t, 5)
	const file = "example.com.zone"

	if err := s.WriteDraft(file, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if !s.HasDraft(file) {
		t.Fatal("expected a draft to exist")
	}
	b, err := s.ReadDraft(file)
	if err != nil || string(b) != "v1" {
		t.Fatalf("read draft = %q (err %v), want v1", b, err)
	}
	if err := s.DiscardDraft(file); err != nil {
		t.Fatal(err)
	}
	if s.HasDraft(file) {
		t.Fatal("draft should be gone after discard")
	}
	if _, err := s.ReadDraft(file); !errors.Is(err, ErrNoDraft) {
		t.Fatalf("read missing draft = %v, want ErrNoDraft", err)
	}
}

func TestPromoteCreatesBackup(t *testing.T) {
	s := newTestStore(t, 5)
	const file = "example.com.zone"

	if err := os.WriteFile(s.livePath(file), []byte("v1"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteDraft(file, []byte("v2")); err != nil {
		t.Fatal(err)
	}

	bk, err := s.Promote(file, []byte("v2"))
	if err != nil {
		t.Fatal(err)
	}
	if bk.ID == "" {
		t.Fatal("expected a backup of the prior live file")
	}
	if live, _ := s.ReadLive(file); string(live) != "v2" {
		t.Fatalf("live = %q, want v2", live)
	}
	if s.HasDraft(file) {
		t.Fatal("draft should be cleared after promote")
	}
	backups, _ := s.ListBackups(file)
	if len(backups) != 1 {
		t.Fatalf("backups = %d, want 1", len(backups))
	}
	if old, err := s.ReadBackup(file, backups[0].ID); err != nil || string(old) != "v1" {
		t.Fatalf("backup content = %q (err %v), want v1", old, err)
	}
}

func TestRestoreToDraft(t *testing.T) {
	s := newTestStore(t, 5)
	const file = "z.zone"

	if err := os.WriteFile(s.livePath(file), []byte("v1"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Promote(file, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	backups, _ := s.ListBackups(file)
	if len(backups) == 0 {
		t.Fatal("expected a backup")
	}
	if err := s.Restore(file, backups[0].ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.ReadDraft(file); string(d) != "v1" {
		t.Fatalf("restored draft = %q, want v1", d)
	}
}

func TestPruneKeepsMostRecent(t *testing.T) {
	s := newTestStore(t, 2)
	const file = "z.zone"

	if err := os.WriteFile(s.livePath(file), []byte("v0"), 0o640); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := s.Promote(file, []byte("v"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	backups, _ := s.ListBackups(file)
	if len(backups) > 2 {
		t.Fatalf("backups = %d, want <= 2 (keep)", len(backups))
	}
}

func TestReadBackupRejectsTraversal(t *testing.T) {
	s := newTestStore(t, 5)
	const file = "z.zone"

	if _, err := s.ReadBackup(file, "../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("traversal id = %v, want ErrNotFound", err)
	}
	if _, err := s.ReadBackup(file, "other.zone.20260101T000000.000000000Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-prefix id = %v, want ErrNotFound", err)
	}
}
