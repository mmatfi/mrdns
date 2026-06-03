package zone

import "time"

// SerialPolicy selects how the SOA serial is advanced on each deploy.
type SerialPolicy string

const (
	SerialDate      SerialPolicy = "date"     // YYYYMMDDnn
	SerialUnixtime  SerialPolicy = "unixtime" // seconds since the epoch
	SerialIncrement SerialPolicy = "increment"
)

// BumpSerial advances the zone's SOA serial per the policy and returns the old
// and new values. It always produces a strictly larger serial so BIND treats
// the zone as changed.
func (z *Zone) BumpSerial(policy SerialPolicy, now time.Time) (oldSerial, newSerial uint32, err error) {
	soa, err := z.soa()
	if err != nil {
		return 0, 0, err
	}
	oldSerial = soa.Serial
	newSerial = nextSerial(policy, oldSerial, now)
	soa.Serial = newSerial
	return oldSerial, newSerial, nil
}

func nextSerial(policy SerialPolicy, current uint32, now time.Time) uint32 {
	switch policy {
	case SerialUnixtime:
		n := uint32(now.Unix())
		if n <= current {
			return current + 1
		}
		return n
	case SerialIncrement:
		return current + 1
	default: // SerialDate
		base := dateSerial(now)
		if current < base {
			return base
		}
		return current + 1
	}
}

func dateSerial(now time.Time) uint32 {
	y, m, d := now.Date()
	return uint32(y)*1_000_000 + uint32(m)*10_000 + uint32(d)*100
}
