package zone

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

// SOAData holds the fields of a zone's SOA record, managed as zone settings
// rather than as an editable record row.
type SOAData struct {
	PrimaryNS string
	Mbox      string
	Refresh   uint32
	Retry     uint32
	Expire    uint32
	Minimum   uint32
	TTL       uint32
	Serial    uint32
}

// Build assembles a zone from its SOA settings and records, then parses it so
// the result is validated. Use Render on the returned Zone for BIND text.
func Build(origin string, soa SOAData, recs []Record) (*Zone, error) {
	o := dns.Fqdn(origin)
	ttl := soa.TTL
	if ttl == 0 {
		ttl = 3600
	}
	var b strings.Builder
	fmt.Fprintf(&b, "$ORIGIN %s\n$TTL %d\n", o, ttl)
	fmt.Fprintf(&b, "@ %d IN SOA %s %s ( %d %d %d %d %d )\n",
		ttl, dns.Fqdn(soa.PrimaryNS), dnsMbox(soa.Mbox),
		soa.Serial, soa.Refresh, soa.Retry, soa.Expire, soa.Minimum)
	for _, r := range recs {
		rttl := r.TTL
		if rttl == 0 {
			rttl = ttl
		}
		owner := r.Name
		if owner == "" {
			owner = "@"
		}
		fmt.Fprintf(&b, "%s %d IN %s %s\n", owner, rttl, r.Type, r.Data)
	}
	return Parse([]byte(b.String()), origin)
}

// SOA returns the zone's SOA fields.
func (z *Zone) SOA() (SOAData, error) {
	soa, err := z.soa()
	if err != nil {
		return SOAData{}, err
	}
	return SOAData{
		PrimaryNS: soa.Ns,
		Mbox:      soa.Mbox,
		Refresh:   soa.Refresh,
		Retry:     soa.Retry,
		Expire:    soa.Expire,
		Minimum:   soa.Minttl,
		TTL:       soa.Hdr.Ttl,
		Serial:    soa.Serial,
	}, nil
}

// DataRecords returns the non-SOA records (the editable rows).
func (z *Zone) DataRecords() []Record {
	all := z.Records()
	out := make([]Record, 0, len(all))
	for _, r := range all {
		if r.Type != "SOA" {
			out = append(out, r)
		}
	}
	return out
}

// NormalizeRecord validates a single record against the zone origin and returns
// it in canonical form (FQDN owner, normalized rdata). The SOA type is rejected.
func NormalizeRecord(origin, name string, ttl uint32, rtype, data string) (Record, error) {
	if name == "" {
		name = "@"
	}
	if ttl == 0 {
		ttl = 3600
	}
	line := fmt.Sprintf("%s %d IN %s %s", name, ttl, strings.ToUpper(strings.TrimSpace(rtype)), strings.TrimSpace(data))
	rr, err := newRRWithOrigin(line, origin)
	if err != nil {
		return Record{}, err
	}
	if rr.Header().Rrtype == dns.TypeSOA {
		return Record{}, errors.New("the SOA record is managed via zone settings")
	}
	h := rr.Header()
	return Record{
		Name:  h.Name,
		TTL:   h.Ttl,
		Class: "IN",
		Type:  dns.TypeToString[h.Rrtype],
		Data:  rdata(rr),
	}, nil
}

// dnsMbox converts an email-style address to the DNS SOA mailbox form
// (admin@example.com -> admin.example.com.).
func dnsMbox(s string) string {
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i] + "." + s[i+1:]
	}
	return dns.Fqdn(s)
}
