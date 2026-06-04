package web

import "strings"

// recordTypes are the DNS record types offered in the editor's type dropdown.
var recordTypes = []string{
	"A", "AAAA", "CNAME", "MX", "NS", "TXT", "SRV", "PTR", "CAA",
	"SSHFP", "TLSA", "NAPTR", "DNAME", "SPF",
}

type typeOption struct {
	Value    string
	Selected bool
}

// typeOptions returns the dropdown options for a record type. The current value
// is always included (so an uncommon imported type is never silently changed by
// the <select>) and marked selected.
func typeOptions(current string) []typeOption {
	current = strings.ToUpper(strings.TrimSpace(current))
	opts := make([]typeOption, 0, len(recordTypes)+1)
	found := false
	for _, t := range recordTypes {
		sel := t == current
		if sel {
			found = true
		}
		opts = append(opts, typeOption{Value: t, Selected: sel})
	}
	if current != "" && !found {
		opts = append([]typeOption{{Value: current, Selected: true}}, opts...)
	}
	return opts
}
