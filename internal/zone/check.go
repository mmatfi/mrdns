package zone

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/miekg/dns"
)

// Checker validates zone content authoritatively by running named-checkzone.
type Checker struct {
	Bin string // path to named-checkzone; defaults to "named-checkzone" in PATH
}

// Check writes content to a temp file and runs named-checkzone against it. It
// returns the tool's combined output and a non-nil error if validation fails.
func (c Checker) Check(ctx context.Context, origin string, content []byte) (string, error) {
	bin := c.Bin
	if bin == "" {
		bin = "named-checkzone"
	}
	f, err := os.CreateTemp("", "mrdns-checkzone-*.zone")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(content); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, dns.Fqdn(origin), f.Name())
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("named-checkzone: %w", err)
	}
	return out.String(), nil
}
