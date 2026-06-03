// Package store manages BIND zone files on local disk as three views: the live
// (last-deployed) copy, an editable draft, and timestamped backups. Mutating
// operations are serialized per file with an in-process mutex plus an advisory
// file lock.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors returned by the store.
var (
	ErrNoDraft  = errors.New("no draft for zone")
	ErrNotFound = errors.New("backup not found")
)

// Store is a flat-file zone store rooted at the configured directories.
type Store struct {
	liveDir   string
	draftDir  string
	backupDir string
	lockDir   string
	keep      int
	locks     keyedMutex
}

// New creates the store directories and returns a Store. keep is the number of
// backups retained per zone (defaults to 20 if non-positive).
func New(liveDir, draftDir, backupDir, lockDir string, keep int) (*Store, error) {
	if keep <= 0 {
		keep = 20
	}
	for _, d := range []string{liveDir, draftDir, backupDir, lockDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return &Store{
		liveDir:   liveDir,
		draftDir:  draftDir,
		backupDir: backupDir,
		lockDir:   lockDir,
		keep:      keep,
	}, nil
}

// Backup describes a retained previous version of a zone's live file.
type Backup struct {
	ID        string
	CreatedAt time.Time
	Size      int64
}

func (s *Store) livePath(file string) string  { return filepath.Join(s.liveDir, filepath.Base(file)) }
func (s *Store) draftPath(file string) string { return filepath.Join(s.draftDir, filepath.Base(file)) }

// ReadLive returns the current live (last-deployed) content.
func (s *Store) ReadLive(file string) ([]byte, error) {
	return os.ReadFile(s.livePath(file))
}

// HasDraft reports whether an editable draft exists.
func (s *Store) HasDraft(file string) bool {
	_, err := os.Stat(s.draftPath(file))
	return err == nil
}

// ReadDraft returns the draft content, or ErrNoDraft if none exists.
func (s *Store) ReadDraft(file string) ([]byte, error) {
	b, err := os.ReadFile(s.draftPath(file))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoDraft
	}
	return b, err
}

// WriteDraft writes editable draft content.
func (s *Store) WriteDraft(file string, content []byte) error {
	return s.withLock(file, func() error {
		return atomicWrite(s.draftPath(file), content, 0o640)
	})
}

// DiscardDraft removes the draft if present.
func (s *Store) DiscardDraft(file string) error {
	return s.withLock(file, func() error {
		if err := os.Remove(s.draftPath(file)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// Promote makes content the new live file: it backs up the existing live copy
// (if any), writes the new content atomically, removes the draft, and prunes
// old backups. The created backup (zero value if there was no prior live file)
// is returned.
func (s *Store) Promote(file string, content []byte) (Backup, error) {
	var bk Backup
	err := s.withLock(file, func() error {
		if cur, err := os.ReadFile(s.livePath(file)); err == nil {
			b, err := s.writeBackup(file, cur)
			if err != nil {
				return err
			}
			bk = b
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := atomicWrite(s.livePath(file), content, 0o640); err != nil {
			return err
		}
		if err := os.Remove(s.draftPath(file)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return s.prune(file)
	})
	return bk, err
}

// ListBackups returns the backups for a zone, newest first.
func (s *Store) ListBackups(file string) ([]Backup, error) {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		return nil, err
	}
	prefix := filepath.Base(file) + "."
	var out []Backup
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{ID: e.Name(), CreatedAt: info.ModTime().UTC(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// ReadBackup returns the content of a specific backup.
func (s *Store) ReadBackup(file, id string) ([]byte, error) {
	if !validBackupID(file, id) {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(s.backupDir, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// Restore copies a backup into the draft so it can be reviewed and deployed.
func (s *Store) Restore(file, id string) error {
	return s.withLock(file, func() error {
		b, err := s.ReadBackup(file, id)
		if err != nil {
			return err
		}
		return atomicWrite(s.draftPath(file), b, 0o640)
	})
}

func (s *Store) writeBackup(file string, content []byte) (Backup, error) {
	base := filepath.Base(file)
	ts := time.Now().UTC().Format("20060102T150405.000000000") + "Z"
	name := base + "." + ts
	path := filepath.Join(s.backupDir, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s.%s-%d", base, ts, i)
		path = filepath.Join(s.backupDir, name)
	}
	if err := atomicWrite(path, content, 0o640); err != nil {
		return Backup{}, err
	}
	return Backup{ID: name, CreatedAt: time.Now().UTC(), Size: int64(len(content))}, nil
}

func (s *Store) prune(file string) error {
	backups, err := s.ListBackups(file)
	if err != nil {
		return err
	}
	for i := s.keep; i < len(backups); i++ {
		if err := os.Remove(filepath.Join(s.backupDir, backups[i].ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) withLock(file string, fn func() error) error {
	mu := s.locks.get(filepath.Base(file))
	mu.Lock()
	defer mu.Unlock()

	release, err := flockFile(filepath.Join(s.lockDir, filepath.Base(file)+".lock"))
	if err != nil {
		return err
	}
	defer release()

	return fn()
}

// validBackupID guards against path traversal and cross-zone access.
func validBackupID(file, id string) bool {
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	return strings.HasPrefix(id, filepath.Base(file)+".")
}

// atomicWrite writes content to a temp file in the same directory and renames
// it into place, so readers never see a partial file.
func atomicWrite(path string, content []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// keyedMutex hands out a distinct mutex per key (here, per zone file).
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (k *keyedMutex) get(key string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = make(map[string]*sync.Mutex)
	}
	mu := k.m[key]
	if mu == nil {
		mu = &sync.Mutex{}
		k.m[key] = mu
	}
	return mu
}
