// Package config loads and validates the mrdns service configuration.
//
// Zones and records live in the SQLite database, not in this file. The config
// declares the target nameservers and service settings. Secrets (the access
// token and the cookie-signing key) are read from the environment at load time.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Defaults for the /opt/mrdns install layout.
const (
	DefaultConfigPath = "/opt/mrdns/etc/mrdns.yaml"
	DefaultListen     = "127.0.0.1:8080"
	DefaultDataDir    = "/opt/mrdns/var"
	DefaultTokenEnv   = "MRDNS_TOKEN"
	DefaultCookieEnv  = "MRDNS_COOKIE_KEY"
	DefaultCheckzone  = "named-checkzone"
	DefaultBackupKeep = 20
)

// Config is the top-level service configuration.
type Config struct {
	Listen        string            `yaml:"listen"`
	SecureCookies *bool             `yaml:"secure_cookies"`
	DataDir       string            `yaml:"data_dir"`
	AuditLog      string            `yaml:"audit_log"`
	AuthTokenEnv  string            `yaml:"auth_token_env"`
	CookieKeyEnv  string            `yaml:"cookie_key_env"`
	SerialPolicy  string            `yaml:"serial_policy"`
	BackupKeep    int               `yaml:"backup_keep"`
	Servers       map[string]Server `yaml:"servers"`

	// Resolved from the environment at load time; never serialized.
	AuthToken          string `yaml:"-"`
	CookieKey          []byte `yaml:"-"`
	CookieKeyEphemeral bool   `yaml:"-"`
}

// Server is a target BIND nameserver reachable over SSH.
type Server struct {
	Host          string `yaml:"host"`
	Port          int    `yaml:"port"`
	User          string `yaml:"user"`
	SSHKey        string `yaml:"ssh_key"`
	KnownHosts    string `yaml:"known_hosts"`
	RemoteZoneDir string `yaml:"remote_zone_dir"`
	CheckzoneCmd  string `yaml:"checkzone_cmd"`
	ReloadCmd     string `yaml:"reload_cmd"`
}

// Load reads, parses, resolves secrets for, and validates the config file.
func Load(path string) (*Config, error) {
	cfg, err := LoadNoSecrets(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.resolveSecrets(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadNoSecrets loads and validates the config without resolving the
// environment secrets. It is for CLI tools that only need the paths and
// servers, not the web auth token or cookie key.
func LoadNoSecrets(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if c.AuthTokenEnv == "" {
		c.AuthTokenEnv = DefaultTokenEnv
	}
	if c.CookieKeyEnv == "" {
		c.CookieKeyEnv = DefaultCookieEnv
	}
	if c.SerialPolicy == "" {
		c.SerialPolicy = "date"
	}
	if c.BackupKeep <= 0 {
		c.BackupKeep = DefaultBackupKeep
	}
	for name, s := range c.Servers {
		if s.Port == 0 {
			s.Port = 22
		}
		if s.CheckzoneCmd == "" {
			s.CheckzoneCmd = DefaultCheckzone
		}
		c.Servers[name] = s
	}
}

func (c *Config) resolveSecrets() error {
	c.AuthToken = os.Getenv(c.AuthTokenEnv)
	if c.AuthToken == "" {
		return fmt.Errorf("auth token env %q is empty; set it to a strong secret", c.AuthTokenEnv)
	}

	if raw := strings.TrimSpace(os.Getenv(c.CookieKeyEnv)); raw != "" {
		key, err := hex.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("decode %s (want hex): %w", c.CookieKeyEnv, err)
		}
		if len(key) < 32 {
			return fmt.Errorf("%s must be at least 32 bytes (64 hex chars)", c.CookieKeyEnv)
		}
		c.CookieKey = key
		return nil
	}

	// No key configured: generate an ephemeral one so the service still runs.
	c.CookieKey = make([]byte, 32)
	if _, err := rand.Read(c.CookieKey); err != nil {
		return fmt.Errorf("generate ephemeral cookie key: %w", err)
	}
	c.CookieKeyEphemeral = true
	return nil
}

// Validate checks structural invariants of the configuration.
func (c *Config) Validate() error {
	switch c.SerialPolicy {
	case "date", "unixtime", "increment":
	default:
		return fmt.Errorf("serial_policy %q must be one of date|unixtime|increment", c.SerialPolicy)
	}
	if c.DataDir == "" {
		return errors.New("data_dir must be set")
	}
	for name, s := range c.Servers {
		if s.Host == "" {
			return fmt.Errorf("server %q: host must be set", name)
		}
		if s.User == "" {
			return fmt.Errorf("server %q: user must be set", name)
		}
		if s.RemoteZoneDir == "" {
			return fmt.Errorf("server %q: remote_zone_dir must be set", name)
		}
	}
	return nil
}

// ServerNames returns the configured server names, sorted.
func (c *Config) ServerNames() []string {
	names := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		names = append(names, n)
	}
	// simple insertion sort to avoid importing sort here
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// SecureCookiesEnabled reports whether the session cookie should carry the
// Secure attribute. Defaults to true unless explicitly disabled for local dev.
func (c *Config) SecureCookiesEnabled() bool {
	return c.SecureCookies == nil || *c.SecureCookies
}

// DBPath is the SQLite database path under the data directory.
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "mrdns.db") }
