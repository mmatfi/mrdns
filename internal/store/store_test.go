package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmatfi/mrdns/internal/zone"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedZone(t *testing.T, s *Store) {
	t.Helper()
	if err := s.CreateZone(Zone{Name: "example.com", PrimaryNS: "ns1.example.com", Mbox: "admin@example.com", Targets: []string{"ns1"}}); err != nil {
		t.Fatal(err)
	}
}

func rec(t *testing.T, name, typ, data string) zone.Record {
	t.Helper()
	r, err := zone.NormalizeRecord("example.com", name, 3600, typ, data)
	if err != nil {
		t.Fatalf("normalize %s %s %q: %v", name, typ, data, err)
	}
	return r
}

func TestZoneLifecycle(t *testing.T) {
	s := openTest(t)
	seedZone(t, s)

	z, err := s.GetZone("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if z.Refresh == 0 || z.TTL == 0 {
		t.Error("SOA defaults were not applied")
	}
	if len(z.Targets) != 1 || z.Targets[0] != "ns1" {
		t.Errorf("targets = %v", z.Targets)
	}
	if zones, _ := s.ListZones(); len(zones) != 1 {
		t.Fatalf("ListZones = %d, want 1", len(zones))
	}
	if _, err := s.GetZone("nope"); err != ErrNotFound {
		t.Errorf("GetZone(nope) = %v, want ErrNotFound", err)
	}
	if err := s.DeleteZone("example.com"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ZoneExists("example.com"); ok {
		t.Error("zone should be gone after delete")
	}
}

func TestRecordsAndRender(t *testing.T) {
	s := openTest(t)
	seedZone(t, s)

	id, err := s.AddRecord("example.com", rec(t, "www", "A", "192.0.2.2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRecord("example.com", rec(t, "@", "MX", "10 mail.example.com.")); err != nil {
		t.Fatal(err)
	}

	if recs, _ := s.Records("example.com"); len(recs) != 2 {
		t.Fatalf("Records = %d, want 2", len(recs))
	}

	z, err := s.Build("example.com")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out := string(z.Render())
	if !strings.Contains(out, "192.0.2.2") || !strings.Contains(out, "SOA") || !strings.Contains(out, "MX") {
		t.Errorf("render missing content:\n%s", out)
	}

	if err := s.UpdateRecord("example.com", id, rec(t, "www", "A", "192.0.2.9")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRecord("example.com", id); err != nil {
		t.Fatal(err)
	}
	if recs, _ := s.Records("example.com"); len(recs) != 1 {
		t.Fatalf("after delete Records = %d, want 1", len(recs))
	}
	if err := s.DeleteRecord("example.com", 99999); err != ErrNotFound {
		t.Errorf("delete missing record = %v, want ErrNotFound", err)
	}
}

func TestPublishDirtyRestore(t *testing.T) {
	s := openTest(t)
	seedZone(t, s)
	s.AddRecord("example.com", rec(t, "www", "A", "192.0.2.2"))

	if dirty, _ := s.Dirty("example.com"); !dirty {
		t.Error("a never-deployed zone should be dirty")
	}

	z, _ := s.Build("example.com")
	_, newSerial, _ := z.BumpSerial(zone.SerialIncrement, time.Now())
	content := string(z.Render())
	if err := s.Publish("example.com", content, newSerial); err != nil {
		t.Fatal(err)
	}
	if dirty, _ := s.Dirty("example.com"); dirty {
		t.Error("zone should be clean immediately after publish")
	}

	snaps, _ := s.ListSnapshots("example.com")
	if len(snaps) != 1 {
		t.Fatalf("ListSnapshots = %d, want 1", len(snaps))
	}

	s.AddRecord("example.com", rec(t, "ftp", "A", "192.0.2.3"))
	if dirty, _ := s.Dirty("example.com"); !dirty {
		t.Error("zone should be dirty after an edit")
	}

	if err := s.RestoreSnapshot("example.com", snaps[0].ID); err != nil {
		t.Fatal(err)
	}
	recs, _ := s.Records("example.com")
	for _, r := range recs {
		if strings.HasPrefix(r.Name, "ftp") {
			t.Error("ftp record should be gone after restore")
		}
	}
	if dirty, _ := s.Dirty("example.com"); dirty {
		t.Error("restoring the latest snapshot should leave the zone clean")
	}
}

const importSample = `$ORIGIN example.com.
$TTL 3600
@ IN SOA ns1.example.com. hostmaster.example.com. 2026010101 7200 3600 1209600 3600
@ IN NS ns1.example.com.
@ IN A 192.0.2.1
www IN A 192.0.2.2
@ IN MX 10 mail.example.com.
`

func TestImportZone(t *testing.T) {
	s := openTest(t)

	n, err := s.ImportZone("example.com", []byte(importSample), []string{"ns1"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n != 4 { // NS + A + A + MX (SOA excluded)
		t.Fatalf("imported records = %d, want 4", n)
	}
	z, err := s.GetZone("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if z.Serial != 2026010101 {
		t.Errorf("serial = %d, want 2026010101", z.Serial)
	}
	if z.PrimaryNS != "ns1.example.com." {
		t.Errorf("primary_ns = %q", z.PrimaryNS)
	}
	if len(z.Targets) != 1 || z.Targets[0] != "ns1" {
		t.Errorf("targets = %v", z.Targets)
	}
	if recs, _ := s.Records("example.com"); len(recs) != 4 {
		t.Fatalf("stored records = %d, want 4", len(recs))
	}
	if _, err := s.Build("example.com"); err != nil {
		t.Fatalf("build after import: %v", err)
	}

	if _, err := s.ImportZone("example.com", []byte(importSample), nil); err == nil {
		t.Error("re-importing an existing zone should fail")
	}
	if _, err := s.ImportZone("bad.example", []byte("not a zone file"), nil); err == nil {
		t.Error("importing invalid content should fail")
	}
}

func TestSnapshotPruneKeep(t *testing.T) {
	s := openTest(t) // keep = 3
	seedZone(t, s)
	s.AddRecord("example.com", rec(t, "www", "A", "192.0.2.2"))
	for i := 0; i < 5; i++ {
		z, _ := s.Build("example.com")
		_, ns, _ := z.BumpSerial(zone.SerialIncrement, time.Now())
		if err := s.Publish("example.com", string(z.Render()), ns); err != nil {
			t.Fatal(err)
		}
	}
	if snaps, _ := s.ListSnapshots("example.com"); len(snaps) != 3 {
		t.Fatalf("snapshots retained = %d, want 3 (keep)", len(snaps))
	}
}
