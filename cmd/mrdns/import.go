package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mmatfi/mrdns/internal/config"
	"github.com/mmatfi/mrdns/internal/store"
)

// runImport implements:  mrdns import [-config path] [-targets a,b] <zone> <file>
//
// It parses a BIND zone file and creates the zone (SOA settings + records) in
// the database, so existing zones can be migrated into mrdns.
func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	configPath := fs.String("config", config.DefaultConfigPath, "path to mrdns.yaml")
	targetsFlag := fs.String("targets", "", "comma-separated target server names")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mrdns import [-config path] [-targets ns1,ns2] <zone> <file>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("expected <zone> and <file>")
	}
	zoneName, file := fs.Arg(0), fs.Arg(1)

	cfg, err := config.LoadNoSecrets(*configPath)
	if err != nil {
		return err
	}

	var targets []string
	for _, t := range strings.Split(*targetsFlag, ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if _, ok := cfg.Servers[t]; !ok {
			return fmt.Errorf("unknown target server %q (not in %s)", t, *configPath)
		}
		targets = append(targets, t)
	}

	content, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath(), cfg.BackupKeep)
	if err != nil {
		return err
	}
	defer st.Close()

	n, err := st.ImportZone(zoneName, content, targets)
	if err != nil {
		return err
	}
	fmt.Printf("imported %s: %d records", zoneName, n)
	if len(targets) > 0 {
		fmt.Printf(" (targets: %s)", strings.Join(targets, ", "))
	}
	fmt.Println()
	return nil
}
