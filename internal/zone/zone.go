// Package zone parses, renders, and validates BIND zone files and manages SOA
// serial numbers. It is built on github.com/miekg/dns.
package zone

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

// Zone is a parsed DNS zone: an origin plus its resource records.
type Zone struct {
	Origin  string
	records []dns.RR
}

// Record is a presentation-friendly view of a single resource record.
type Record struct {
	Name  string
	TTL   uint32
	Class string
	Type  string
	Data  string
}

// Parse parses zone file content for the given origin. $INCLUDE is disallowed
// so a zone file cannot pull in arbitrary files from disk.
func Parse(content []byte, origin string) (*Zone, error) {
	origin = dns.Fqdn(origin)
	zp := dns.NewZoneParser(bytes.NewReader(content), origin, "")
	zp.SetIncludeAllowed(false)

	var records []dns.RR
	for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
		records = append(records, rr)
	}
	if err := zp.Err(); err != nil {
		return nil, fmt.Errorf("parse zone %q: %w", origin, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("zone %q: no records", origin)
	}
	return &Zone{Origin: origin, records: records}, nil
}

// Render serializes the zone back to BIND zone file text.
func (z *Zone) Render() []byte {
	var b strings.Builder
	b.WriteString("$ORIGIN ")
	b.WriteString(z.Origin)
	b.WriteByte('\n')
	for _, rr := range z.records {
		b.WriteString(rr.String())
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// Records returns a presentation view of all resource records.
func (z *Zone) Records() []Record {
	out := make([]Record, 0, len(z.records))
	for _, rr := range z.records {
		h := rr.Header()
		out = append(out, Record{
			Name:  h.Name,
			TTL:   h.Ttl,
			Class: dns.ClassToString[h.Class],
			Type:  dns.TypeToString[h.Rrtype],
			Data:  rdata(rr),
		})
	}
	return out
}

// Len reports the number of resource records in the zone.
func (z *Zone) Len() int { return len(z.records) }

// rdata returns just the RDATA portion of a record's presentation form.
func rdata(rr dns.RR) string {
	return strings.TrimPrefix(rr.String(), rr.Header().String())
}

func (z *Zone) soa() (*dns.SOA, error) {
	for _, rr := range z.records {
		if soa, ok := rr.(*dns.SOA); ok {
			return soa, nil
		}
	}
	return nil, errors.New("zone has no SOA record")
}

// Serial returns the zone's current SOA serial.
func (z *Zone) Serial() (uint32, error) {
	soa, err := z.soa()
	if err != nil {
		return 0, err
	}
	return soa.Serial, nil
}
