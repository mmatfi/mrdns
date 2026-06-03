package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mrdns.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const validBody = `
listen: "127.0.0.1:9999"
zones_dir: /tmp/mrdns
servers:
  ns1:
    host: ns1.example.net
    user: deploy
    remote_zone_dir: /etc/bind/zones
zones:
  example.com:
    file: example.com.zone
    targets: [ns1]
`

func TestLoadValidAppliesDefaults(t *testing.T) {
	t.Setenv("MRDNS_TOKEN", "secret")
	cfg, err := Load(writeConfig(t, validBody))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BackupKeep != DefaultBackupKeep {
		t.Errorf("backup_keep = %d, want default %d", cfg.BackupKeep, DefaultBackupKeep)
	}
	if cfg.Servers["ns1"].Port != 22 {
		t.Errorf("server port = %d, want default 22", cfg.Servers["ns1"].Port)
	}
	if cfg.Servers["ns1"].CheckzoneCmd != DefaultCheckzone {
		t.Errorf("checkzone_cmd = %q, want default", cfg.Servers["ns1"].CheckzoneCmd)
	}
	if !cfg.CookieKeyEphemeral {
		t.Error("expected an ephemeral cookie key when the env var is unset")
	}
	if !cfg.SecureCookiesEnabled() {
		t.Error("secure cookies should default to enabled")
	}
}

func TestLoadRequiresToken(t *testing.T) {
	t.Setenv("MRDNS_TOKEN", "")
	if _, err := Load(writeConfig(t, validBody)); err == nil {
		t.Fatal("expected an error when the token env var is empty")
	}
}

func TestLoadRejectsUnknownServer(t *testing.T) {
	t.Setenv("MRDNS_TOKEN", "secret")
	body := `
zones_dir: /tmp/mrdns
servers:
  ns1: { host: a, user: b, remote_zone_dir: /z }
zones:
  example.com: { file: e.zone, targets: [nsX] }
`
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("expected an error for a zone referencing an unknown server")
	}
}

func TestLoadRejectsBadSerialPolicy(t *testing.T) {
	t.Setenv("MRDNS_TOKEN", "secret")
	body := "zones_dir: /tmp/mrdns\nserial_policy: weekly\n"
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("expected an error for an invalid serial_policy")
	}
}

func TestCookieKeyFromEnv(t *testing.T) {
	t.Setenv("MRDNS_TOKEN", "secret")
	t.Setenv("MRDNS_COOKIE_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	cfg, err := Load(writeConfig(t, validBody))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CookieKeyEphemeral {
		t.Error("cookie key should come from the env, not be ephemeral")
	}
	if len(cfg.CookieKey) != 32 {
		t.Errorf("cookie key length = %d bytes, want 32", len(cfg.CookieKey))
	}
}
