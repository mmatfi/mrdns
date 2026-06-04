// Package store is the SQLite-backed source of truth for zones and their
// records. BIND zone files are generated from this data at deploy time; the
// deployed text of each zone is also snapshotted here for diff and rollback.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mmatfi/mrdns/internal/zone"
)

// ErrNotFound is returned when a zone, record, or snapshot does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db   *sql.DB
	keep int // snapshots retained per zone
}

// Zone is a managed zone: its SOA settings, current serial, and target servers.
type Zone struct {
	Name      string
	PrimaryNS string
	Mbox      string
	Refresh   uint32
	Retry     uint32
	Expire    uint32
	Minimum   uint32
	TTL       uint32
	Serial    uint32
	Targets   []string
}

// SOAData returns the zone's SOA settings.
func (z Zone) SOAData() zone.SOAData {
	return zone.SOAData{
		PrimaryNS: z.PrimaryNS, Mbox: z.Mbox,
		Refresh: z.Refresh, Retry: z.Retry, Expire: z.Expire, Minimum: z.Minimum,
		TTL: z.TTL, Serial: z.Serial,
	}
}

// Record is a stored resource record with its database id.
type Record struct {
	ID int64
	zone.Record
}

// Snapshot is the deployed text of a zone at a point in time.
type Snapshot struct {
	ID        int64
	Serial    uint32
	Content   string
	Size      int
	CreatedAt time.Time
}

// Open opens (creating if needed) the SQLite database and applies migrations.
// keep is the number of snapshots retained per zone.
func Open(path string, keep int) (*Store, error) {
	if keep <= 0 {
		keep = 20
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // serialize access; this is a low-traffic admin tool
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db, keep: keep}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS zones (
  name       TEXT PRIMARY KEY,
  primary_ns TEXT NOT NULL,
  mbox       TEXT NOT NULL,
  refresh    INTEGER NOT NULL,
  retry      INTEGER NOT NULL,
  expire     INTEGER NOT NULL,
  minimum    INTEGER NOT NULL,
  ttl        INTEGER NOT NULL,
  serial     INTEGER NOT NULL,
  targets    TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS records (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  zone TEXT NOT NULL REFERENCES zones(name) ON DELETE CASCADE,
  name TEXT NOT NULL,
  ttl  INTEGER NOT NULL,
  type TEXT NOT NULL,
  data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_records_zone ON records(zone);
CREATE TABLE IF NOT EXISTS snapshots (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  zone       TEXT NOT NULL REFERENCES zones(name) ON DELETE CASCADE,
  serial     INTEGER NOT NULL,
  content    TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_snapshots_zone ON snapshots(zone);
`)
	return err
}

// ── zones ────────────────────────────────────────────────────────────────

func (s *Store) CreateZone(z Zone) error {
	z.applyDefaults()
	_, err := s.db.Exec(
		`INSERT INTO zones(name,primary_ns,mbox,refresh,retry,expire,minimum,ttl,serial,targets,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		z.Name, z.PrimaryNS, z.Mbox, z.Refresh, z.Retry, z.Expire, z.Minimum, z.TTL, z.Serial,
		joinTargets(z.Targets), time.Now().UTC().Format(time.RFC3339))
	return err
}

func (z *Zone) applyDefaults() {
	if z.Refresh == 0 {
		z.Refresh = 7200
	}
	if z.Retry == 0 {
		z.Retry = 3600
	}
	if z.Expire == 0 {
		z.Expire = 1209600
	}
	if z.Minimum == 0 {
		z.Minimum = 3600
	}
	if z.TTL == 0 {
		z.TTL = 3600
	}
}

const zoneCols = `name,primary_ns,mbox,refresh,retry,expire,minimum,ttl,serial,targets`

func scanZone(sc interface{ Scan(...any) error }) (Zone, error) {
	var z Zone
	var targets string
	err := sc.Scan(&z.Name, &z.PrimaryNS, &z.Mbox, &z.Refresh, &z.Retry, &z.Expire, &z.Minimum, &z.TTL, &z.Serial, &targets)
	z.Targets = splitTargets(targets)
	return z, err
}

func (s *Store) GetZone(name string) (Zone, error) {
	z, err := scanZone(s.db.QueryRow(`SELECT `+zoneCols+` FROM zones WHERE name=?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Zone{}, ErrNotFound
	}
	return z, err
}

func (s *Store) ZoneExists(name string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM zones WHERE name=?`, name).Scan(&n)
	return n > 0, err
}

func (s *Store) ListZones() ([]Zone, error) {
	rows, err := s.db.Query(`SELECT ` + zoneCols + ` FROM zones ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Zone
	for rows.Next() {
		z, err := scanZone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// UpdateZoneSettings updates the SOA settings and targets (not the serial).
func (s *Store) UpdateZoneSettings(z Zone) error {
	z.applyDefaults()
	res, err := s.db.Exec(
		`UPDATE zones SET primary_ns=?,mbox=?,refresh=?,retry=?,expire=?,minimum=?,ttl=?,targets=? WHERE name=?`,
		z.PrimaryNS, z.Mbox, z.Refresh, z.Retry, z.Expire, z.Minimum, z.TTL, joinTargets(z.Targets), z.Name)
	return notFoundIfNoRows(res, err)
}

func (s *Store) SetSerial(name string, serial uint32) error {
	res, err := s.db.Exec(`UPDATE zones SET serial=? WHERE name=?`, serial, name)
	return notFoundIfNoRows(res, err)
}

func (s *Store) DeleteZone(name string) error {
	res, err := s.db.Exec(`DELETE FROM zones WHERE name=?`, name)
	return notFoundIfNoRows(res, err)
}

// ImportZone parses BIND zone file content and creates a new zone — its SOA
// settings become the zone settings and the remaining records are imported.
// It fails if a zone with that name already exists. Returns the record count.
func (s *Store) ImportZone(name string, content []byte, targets []string) (int, error) {
	if ok, err := s.ZoneExists(name); err != nil {
		return 0, err
	} else if ok {
		return 0, fmt.Errorf("zone %q already exists", name)
	}
	z, err := zone.Parse(content, name)
	if err != nil {
		return 0, fmt.Errorf("parse zone: %w", err)
	}
	soa, err := z.SOA()
	if err != nil {
		return 0, err
	}
	recs := z.DataRecords()

	zr := Zone{
		Name: name, PrimaryNS: soa.PrimaryNS, Mbox: soa.Mbox,
		Refresh: soa.Refresh, Retry: soa.Retry, Expire: soa.Expire, Minimum: soa.Minimum,
		TTL: soa.TTL, Serial: soa.Serial, Targets: targets,
	}
	zr.applyDefaults()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO zones(name,primary_ns,mbox,refresh,retry,expire,minimum,ttl,serial,targets,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		zr.Name, zr.PrimaryNS, zr.Mbox, zr.Refresh, zr.Retry, zr.Expire, zr.Minimum, zr.TTL, zr.Serial,
		joinTargets(zr.Targets), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return 0, err
	}
	for _, r := range recs {
		if _, err := tx.Exec(`INSERT INTO records(zone,name,ttl,type,data) VALUES(?,?,?,?,?)`,
			name, r.Name, r.TTL, r.Type, r.Data); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(recs), nil
}

// ── records ──────────────────────────────────────────────────────────────

func (s *Store) Records(zoneName string) ([]Record, error) {
	rows, err := s.db.Query(`SELECT id,name,ttl,type,data FROM records WHERE zone=? ORDER BY name,type,id`, zoneName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Name, &r.TTL, &r.Type, &r.Data); err != nil {
			return nil, err
		}
		r.Class = "IN"
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) AddRecord(zoneName string, r zone.Record) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO records(zone,name,ttl,type,data) VALUES(?,?,?,?,?)`,
		zoneName, r.Name, r.TTL, r.Type, r.Data)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateRecord(zoneName string, id int64, r zone.Record) error {
	res, err := s.db.Exec(`UPDATE records SET name=?,ttl=?,type=?,data=? WHERE id=? AND zone=?`,
		r.Name, r.TTL, r.Type, r.Data, id, zoneName)
	return notFoundIfNoRows(res, err)
}

func (s *Store) DeleteRecord(zoneName string, id int64) error {
	res, err := s.db.Exec(`DELETE FROM records WHERE id=? AND zone=?`, id, zoneName)
	return notFoundIfNoRows(res, err)
}

// ── rendering & dirty state ────────────────────────────────────────────────

// Build renders the zone from the database into a validated zone.Zone.
func (s *Store) Build(name string) (*zone.Zone, error) {
	z, err := s.GetZone(name)
	if err != nil {
		return nil, err
	}
	recs, err := s.Records(name)
	if err != nil {
		return nil, err
	}
	zr := make([]zone.Record, len(recs))
	for i, r := range recs {
		zr[i] = r.Record
	}
	return zone.Build(z.Name, z.SOAData(), zr)
}

// Dirty reports whether the zone has changes since its last deploy.
func (s *Store) Dirty(name string) (bool, error) {
	snap, ok, err := s.LastSnapshot(name)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil // never deployed
	}
	z, err := s.Build(name)
	if err != nil {
		return true, nil
	}
	return string(z.Render()) != snap.Content, nil
}

// ── snapshots ──────────────────────────────────────────────────────────────

// Publish records the deployed content as the zone's latest snapshot, updates
// the stored serial, and prunes old snapshots.
func (s *Store) Publish(name, content string, serial uint32) error {
	if err := s.SetSerial(name, serial); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO snapshots(zone,serial,content,created_at) VALUES(?,?,?,?)`,
		name, serial, content, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM snapshots WHERE zone=? AND id NOT IN (SELECT id FROM snapshots WHERE zone=? ORDER BY id DESC LIMIT ?)`,
		name, name, s.keep)
	return err
}

func (s *Store) LastSnapshot(name string) (Snapshot, bool, error) {
	row := s.db.QueryRow(`SELECT id,serial,content,created_at FROM snapshots WHERE zone=? ORDER BY id DESC LIMIT 1`, name)
	snap, err := scanSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	return snap, err == nil, err
}

func (s *Store) ListSnapshots(name string) ([]Snapshot, error) {
	rows, err := s.db.Query(`SELECT id,serial,content,created_at FROM snapshots WHERE zone=? ORDER BY id DESC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		snap, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// RestoreSnapshot replaces the zone's settings and records with the parsed
// contents of a snapshot, making it the current (unpublished) working state.
func (s *Store) RestoreSnapshot(name string, id int64) error {
	var content string
	err := s.db.QueryRow(`SELECT content FROM snapshots WHERE id=? AND zone=?`, id, name).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	z, err := zone.Parse([]byte(content), name)
	if err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}
	soa, err := z.SOA()
	if err != nil {
		return err
	}
	recs := z.DataRecords()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE zones SET primary_ns=?,mbox=?,refresh=?,retry=?,expire=?,minimum=?,ttl=?,serial=? WHERE name=?`,
		soa.PrimaryNS, soa.Mbox, soa.Refresh, soa.Retry, soa.Expire, soa.Minimum, soa.TTL, soa.Serial, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM records WHERE zone=?`, name); err != nil {
		return err
	}
	for _, r := range recs {
		if _, err := tx.Exec(`INSERT INTO records(zone,name,ttl,type,data) VALUES(?,?,?,?,?)`,
			name, r.Name, r.TTL, r.Type, r.Data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanSnapshot(sc interface{ Scan(...any) error }) (Snapshot, error) {
	var snap Snapshot
	var created string
	if err := sc.Scan(&snap.ID, &snap.Serial, &snap.Content, &created); err != nil {
		return Snapshot{}, err
	}
	snap.Size = len(snap.Content)
	snap.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return snap, nil
}

// ── helpers ────────────────────────────────────────────────────────────────

func notFoundIfNoRows(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func joinTargets(t []string) string { return strings.Join(t, ",") }

func splitTargets(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
