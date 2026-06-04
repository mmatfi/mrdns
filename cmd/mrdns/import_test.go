package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mmatfi/mrdns/internal/store"
)

func TestRunImport(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "var")
	cfgPath := filepath.Join(dir, "mrdns.yaml")
	cfg := "listen: \"127.0.0.1:0\"\ndata_dir: " + dataDir +
		"\nservers:\n  ns1: { host: ns1.example, user: u, remote_zone_dir: /z }\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	zoneFile := filepath.Join(dir, "example.com.zone")
	zoneText := "$ORIGIN example.com.\n$TTL 3600\n" +
		"@ IN SOA ns1.example.com. host.example.com. 2026010101 7200 3600 1209600 3600\n" +
		"@ IN NS ns1.example.com.\nwww IN A 192.0.2.2\n"
	if err := os.WriteFile(zoneFile, []byte(zoneText), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runImport([]string{"-config", cfgPath, "-targets", "ns1", "example.com", zoneFile}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	st, err := store.Open(filepath.Join(dataDir, "mrdns.db"), 5)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	z, err := st.GetZone("example.com")
	if err != nil {
		t.Fatalf("get zone: %v", err)
	}
	if z.Serial != 2026010101 || len(z.Targets) != 1 || z.Targets[0] != "ns1" {
		t.Errorf("imported zone = %+v", z)
	}
	if recs, _ := st.Records("example.com"); len(recs) != 2 {
		t.Errorf("records = %d, want 2", len(recs))
	}

	if err := runImport([]string{"-config", cfgPath, "-targets", "nope", "x.example", zoneFile}); err == nil {
		t.Error("expected an error for an unknown target server")
	}
}
