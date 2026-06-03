package zone

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

// Add appends a record parsed from a single zone-file line. Relative owner
// names are interpreted against the zone origin, and a $TTL default is applied
// so a line may omit an explicit TTL. The SOA record cannot be added this way.
func (z *Zone) Add(line string) error {
	rr, err := newRRWithOrigin(line, z.Origin)
	if err != nil {
		return err
	}
	if rr.Header().Rrtype == dns.TypeSOA {
		return errors.New("the SOA record is managed automatically")
	}
	z.records = append(z.records, rr)
	return nil
}

// Remove deletes the first non-SOA record matching the FQDN name, type, and
// rdata, reporting whether one was removed.
func (z *Zone) Remove(name, rtype, data string) bool {
	for i, rr := range z.records {
		if rr.Header().Rrtype == dns.TypeSOA {
			continue
		}
		if recordMatches(rr, name, rtype, data) {
			z.records = append(z.records[:i], z.records[i+1:]...)
			return true
		}
	}
	return false
}

// Replace swaps the first non-SOA record matching (name, rtype, data) with one
// parsed from newLine, reporting whether a record was replaced.
func (z *Zone) Replace(name, rtype, data, newLine string) (bool, error) {
	rr, err := newRRWithOrigin(newLine, z.Origin)
	if err != nil {
		return false, err
	}
	if rr.Header().Rrtype == dns.TypeSOA {
		return false, errors.New("the SOA record is managed automatically")
	}
	for i, cur := range z.records {
		if cur.Header().Rrtype == dns.TypeSOA {
			continue
		}
		if recordMatches(cur, name, rtype, data) {
			z.records[i] = rr
			return true, nil
		}
	}
	return false, nil
}

func recordMatches(rr dns.RR, name, rtype, data string) bool {
	h := rr.Header()
	return h.Name == name &&
		dns.TypeToString[h.Rrtype] == rtype &&
		rdata(rr) == data
}

// newRRWithOrigin parses exactly one resource record from line, applying the
// zone origin and a default TTL. It rejects $INCLUDE and multi-record input.
func newRRWithOrigin(line, origin string) (dns.RR, error) {
	origin = dns.Fqdn(origin)
	text := "$ORIGIN " + origin + "\n$TTL 3600\n" + line + "\n"
	zp := dns.NewZoneParser(strings.NewReader(text), origin, "")
	zp.SetIncludeAllowed(false)

	rr, ok := zp.Next()
	if err := zp.Err(); err != nil {
		return nil, fmt.Errorf("invalid record: %w", err)
	}
	if !ok || rr == nil {
		return nil, errors.New("no record found")
	}
	if _, more := zp.Next(); more {
		return nil, errors.New("expected exactly one record")
	}
	return rr, nil
}
