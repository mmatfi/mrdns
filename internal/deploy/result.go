package deploy

import "time"

// Status is the overall outcome of a deploy.
type Status string

const (
	// StatusSuccess: content staged, validated, committed, and reloaded on
	// every target.
	StatusSuccess Status = "success"
	// StatusPartial: validated everywhere, but at least one target failed to
	// commit or reload. The content may be live on some servers.
	StatusPartial Status = "partial"
	// StatusFailed: aborted before any target was changed (local validation or
	// the stage+validate phase failed somewhere).
	StatusFailed Status = "failed"
)

// Result is the outcome of a deploy, including per-server detail.
type Result struct {
	Zone      string
	OldSerial uint32
	NewSerial uint32
	Status    Status
	Promoted  bool // whether the draft was promoted to the local live copy
	Servers   []ServerResult
	StartedAt time.Time
	EndedAt   time.Time
}

// ServerResult records how far a single target progressed and any error.
type ServerResult struct {
	Name     string
	Staged   bool // file uploaded to a temp path
	Checked  bool // named-checkzone passed on the staged file
	Moved    bool // staged file moved into place
	Reloaded bool // rndc reload succeeded
	Verified bool // SOA serial confirmed live via DNS
	Output   string
	Err      string
}

// AllMoved reports whether the content reached every target (so the caller can
// safely record it as the new published state).
func (r *Result) AllMoved() bool {
	if len(r.Servers) == 0 {
		return false
	}
	for i := range r.Servers {
		if !r.Servers[i].Moved {
			return false
		}
	}
	return true
}
