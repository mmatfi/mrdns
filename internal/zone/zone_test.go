package zone

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

const sampleZone = `$ORIGIN example.com.
$TTL 3600
@   IN SOA ns1.example.com. admin.example.com. (
        2026060301 ; serial
        7200       ; refresh
        3600       ; retry
        1209600    ; expire
        3600 )     ; minimum
@       IN NS  ns1.example.com.
@       IN NS  ns2.example.com.
@       IN A   192.0.2.1
www     IN A   192.0.2.2
mail    IN A   192.0.2.3
@       IN MX  10 mail.example.com.
`

func TestParseAndRecords(t *testing.T) {
	z, err := Parse([]byte(sampleZone), "example.com")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := z.Len(); got != 7 {
		t.Fatalf("records = %d, want 7", got)
	}
	if s, err := z.Serial(); err != nil || s != 2026060301 {
		t.Fatalf("serial = %d (err %v), want 2026060301", s, err)
	}

	var foundWWW bool
	for _, r := range z.Records() {
		if r.Name == "www.example.com." && r.Type == "A" {
			foundWWW = true
			if r.Data != "192.0.2.2" {
				t.Errorf("www A data = %q, want 192.0.2.2", r.Data)
			}
		}
	}
	if !foundWWW {
		t.Error("www A record not found in Records()")
	}
}

func TestRenderRoundTrip(t *testing.T) {
	z, err := Parse([]byte(sampleZone), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	z2, err := Parse(z.Render(), "example.com")
	if err != nil {
		t.Fatalf("reparse rendered output: %v", err)
	}
	if z.Len() != z2.Len() {
		t.Fatalf("record count changed across round trip: %d -> %d", z.Len(), z2.Len())
	}
	s1, _ := z.Serial()
	s2, _ := z2.Serial()
	if s1 != s2 {
		t.Fatalf("serial changed across round trip: %d -> %d", s1, s2)
	}
}

func TestNextSerial(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		policy  SerialPolicy
		current uint32
		want    uint32
	}{
		{"date behind today", SerialDate, 2020010100, 2026060300},
		{"date same day", SerialDate, 2026060305, 2026060306},
		{"date equals base", SerialDate, 2026060300, 2026060301},
		{"increment", SerialIncrement, 7, 8},
		{"unixtime normal", SerialUnixtime, 1, uint32(now.Unix())},
		{"unixtime ahead", SerialUnixtime, 4_000_000_000, 4_000_000_001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextSerial(tt.policy, tt.current, now); got != tt.want {
				t.Errorf("nextSerial(%s, %d) = %d, want %d", tt.policy, tt.current, got, tt.want)
			}
		})
	}
}

func TestBumpSerialMutatesSOA(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	z, err := Parse([]byte(sampleZone), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	old, nw, err := z.BumpSerial(SerialIncrement, now)
	if err != nil {
		t.Fatal(err)
	}
	if old != 2026060301 || nw != 2026060302 {
		t.Fatalf("bump returned old=%d new=%d, want 2026060301/2026060302", old, nw)
	}
	if s, _ := z.Serial(); s != 2026060302 {
		t.Errorf("serial after bump = %d, want 2026060302", s)
	}
}

func TestParseRejectsInclude(t *testing.T) {
	z := `$ORIGIN example.com.
$TTL 3600
$INCLUDE /etc/passwd
@ IN SOA ns1.example.com. admin.example.com. 1 2 3 4 5
`
	if _, err := Parse([]byte(z), "example.com"); err == nil {
		t.Fatal("expected $INCLUDE to be rejected, got nil error")
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse([]byte("   \n\n"), "example.com"); err == nil {
		t.Fatal("expected error for a zone with no records")
	}
}

func TestChecker(t *testing.T) {
	if _, err := exec.LookPath("named-checkzone"); err != nil {
		t.Skip("named-checkzone not installed; skipping")
	}
	c := Checker{}
	if out, err := c.Check(context.Background(), "example.com", []byte(sampleZone)); err != nil {
		t.Fatalf("valid zone rejected: %v\n%s", err, out)
	}
	if _, err := c.Check(context.Background(), "example.com", []byte("@ IN A not-an-ip\n")); err == nil {
		t.Fatal("invalid zone was accepted")
	}
}
