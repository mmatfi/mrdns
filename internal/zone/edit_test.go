package zone

import "testing"

func TestAddRecord(t *testing.T) {
	z, err := Parse([]byte(sampleZone), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	n := z.Len()
	if err := z.Add("ftp IN A 192.0.2.9"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if z.Len() != n+1 {
		t.Fatalf("len = %d, want %d", z.Len(), n+1)
	}
	var found bool
	for _, r := range z.Records() {
		if r.Name == "ftp.example.com." && r.Type == "A" && r.Data == "192.0.2.9" {
			found = true
		}
	}
	if !found {
		t.Error("added record not present after Add")
	}
	// The result must re-parse cleanly.
	if _, err := Parse(z.Render(), "example.com"); err != nil {
		t.Fatalf("re-parse after add: %v", err)
	}
}

func TestAddRejectsInvalidAndSOA(t *testing.T) {
	z, _ := Parse([]byte(sampleZone), "example.com")
	if err := z.Add("this is not a record"); err == nil {
		t.Error("expected error for malformed record")
	}
	if err := z.Add("@ IN SOA ns1.example.com. admin.example.com. 1 2 3 4 5"); err == nil {
		t.Error("expected SOA add to be rejected")
	}
	// Newline injection (a second record smuggled into the data field).
	if err := z.Add("x IN TXT \"a\"\nevil IN A 10.0.0.1"); err == nil {
		t.Error("expected multi-record input to be rejected")
	}
}

func TestRemoveAndReplace(t *testing.T) {
	z, _ := Parse([]byte(sampleZone), "example.com")

	if !z.Remove("www.example.com.", "A", "192.0.2.2") {
		t.Fatal("Remove reported no match for www A")
	}
	for _, r := range z.Records() {
		if r.Name == "www.example.com." && r.Type == "A" && r.Data == "192.0.2.2" {
			t.Fatal("record still present after Remove")
		}
	}
	if z.Remove("nope.example.com.", "A", "1.2.3.4") {
		t.Error("Remove reported a match for a nonexistent record")
	}

	ok, err := z.Replace("mail.example.com.", "A", "192.0.2.3", "mail IN A 192.0.2.30")
	if err != nil || !ok {
		t.Fatalf("Replace: ok=%v err=%v", ok, err)
	}
	var replaced bool
	for _, r := range z.Records() {
		if r.Name == "mail.example.com." && r.Type == "A" && r.Data == "192.0.2.30" {
			replaced = true
		}
	}
	if !replaced {
		t.Error("replacement record not present")
	}
}

func TestRemoveWontTouchSOA(t *testing.T) {
	z, _ := Parse([]byte(sampleZone), "example.com")
	if z.Remove("example.com.", "SOA", rdataOfSOA(t, z)) {
		t.Error("SOA should not be removable")
	}
}

func rdataOfSOA(t *testing.T, z *Zone) string {
	t.Helper()
	for _, r := range z.Records() {
		if r.Type == "SOA" {
			return r.Data
		}
	}
	t.Fatal("no SOA in sample zone")
	return ""
}
