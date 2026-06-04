package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mmatfi/mrdns/internal/audit"
	"github.com/mmatfi/mrdns/internal/deploy"
	"github.com/mmatfi/mrdns/internal/store"
	"github.com/mmatfi/mrdns/internal/zone"
)

// recordRow is a presentation row for the editor's record table.
type recordRow struct {
	ID                    int64
	Name, TTL, Type, Data string
	Editing               bool
}

func recordRows(recs []store.Record) []recordRow {
	rows := make([]recordRow, len(recs))
	for i, r := range recs {
		rows[i] = recordRow{
			ID:   r.ID,
			Name: r.Name,
			TTL:  strconv.FormatUint(uint64(r.TTL), 10),
			Type: r.Type,
			Data: r.Data,
		}
	}
	return rows
}

func markEditing(rows []recordRow, id int64) {
	for i := range rows {
		if rows[i].ID == id {
			rows[i].Editing = true
			return
		}
	}
}

type serverChoice struct {
	Name    string
	Checked bool
}

func (srv *Server) serverChoices(selected []string) []serverChoice {
	sel := map[string]bool{}
	for _, s := range selected {
		sel[s] = true
	}
	out := make([]serverChoice, 0, len(srv.cfg.Servers))
	for _, n := range srv.cfg.ServerNames() {
		out = append(out, serverChoice{Name: n, Checked: sel[n]})
	}
	return out
}

// ── new zone / settings ────────────────────────────────────────────────────

func (srv *Server) handleNewZoneForm(w http.ResponseWriter, r *http.Request) {
	z := store.Zone{TTL: 3600, Refresh: 7200, Retry: 3600, Expire: 1209600, Minimum: 3600}
	srv.render(w, "newzone.html", srv.pageData(r, "New zone", map[string]any{
		"Z": z, "Servers": srv.serverChoices(nil),
	}))
}

func (srv *Server) zoneFromForm(r *http.Request) store.Zone {
	_ = r.ParseForm()
	return store.Zone{
		Name:      strings.TrimSpace(r.PostFormValue("name")),
		PrimaryNS: strings.TrimSpace(r.PostFormValue("primary_ns")),
		Mbox:      strings.TrimSpace(r.PostFormValue("mbox")),
		TTL:       formUint(r, "ttl", 3600),
		Refresh:   formUint(r, "refresh", 7200),
		Retry:     formUint(r, "retry", 3600),
		Expire:    formUint(r, "expire", 1209600),
		Minimum:   formUint(r, "minimum", 3600),
		Targets:   srv.formTargets(r),
	}
}

func (srv *Server) handleCreateZone(w http.ResponseWriter, r *http.Request) {
	z := srv.zoneFromForm(r)
	reErr := func(msg string) {
		srv.render(w, "newzone.html", srv.pageData(r, "New zone", map[string]any{
			"Z": z, "Servers": srv.serverChoices(z.Targets), "Error": msg,
		}))
	}
	if z.Name == "" || z.PrimaryNS == "" || z.Mbox == "" {
		reErr("name, primary NS, and admin email are required")
		return
	}
	if ok, _ := srv.store.ZoneExists(z.Name); ok {
		reErr("a zone named " + z.Name + " already exists")
		return
	}
	if _, err := zone.Build(z.Name, z.SOAData(), nil); err != nil {
		reErr("invalid zone settings: " + err.Error())
		return
	}
	if err := srv.store.CreateZone(z); err != nil {
		reErr("create: " + err.Error())
		return
	}
	srv.audit.Log(audit.Event{Action: "zone_create", Actor: clientIP(r), Zone: z.Name})
	http.Redirect(w, r, "/zones/"+z.Name+"?flash=Zone+created", http.StatusSeeOther)
}

func (srv *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	srv.render(w, "import.html", srv.pageData(r, "Import zone", map[string]any{
		"Servers": srv.serverChoices(nil),
	}))
}

func (srv *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	name := strings.TrimSpace(r.PostFormValue("name"))
	content := r.PostFormValue("content")
	targets := srv.formTargets(r)
	reErr := func(msg string) {
		srv.render(w, "import.html", srv.pageData(r, "Import zone", map[string]any{
			"Servers": srv.serverChoices(targets), "Name": name, "Content": content, "Error": msg,
		}))
	}
	if name == "" || strings.TrimSpace(content) == "" {
		reErr("zone name and file content are required")
		return
	}
	n, err := srv.store.ImportZone(name, []byte(content), targets)
	if err != nil {
		reErr(err.Error())
		return
	}
	srv.audit.Log(audit.Event{Action: "zone_import", Actor: clientIP(r), Zone: name, Detail: fmt.Sprintf("%d records", n)})
	http.Redirect(w, r, "/zones/"+name+"?flash="+url.QueryEscape(fmt.Sprintf("Imported %d records", n)), http.StatusSeeOther)
}

func (srv *Server) handleSettingsForm(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	z, err := srv.store.GetZone(name)
	if !srv.zoneOK(w, err) {
		return
	}
	srv.render(w, "settings.html", srv.pageData(r, "Settings "+name, map[string]any{
		"Z": z, "Servers": srv.serverChoices(z.Targets),
	}))
}

func (srv *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	z := srv.zoneFromForm(r)
	z.Name = name
	reErr := func(msg string) {
		srv.render(w, "settings.html", srv.pageData(r, "Settings "+name, map[string]any{
			"Z": z, "Servers": srv.serverChoices(z.Targets), "Error": msg,
		}))
	}
	if z.PrimaryNS == "" || z.Mbox == "" {
		reErr("primary NS and admin email are required")
		return
	}
	if _, err := zone.Build(name, z.SOAData(), nil); err != nil {
		reErr("invalid zone settings: " + err.Error())
		return
	}
	if err := srv.store.UpdateZoneSettings(z); err != nil {
		reErr("update: " + err.Error())
		return
	}
	srv.audit.Log(audit.Event{Action: "zone_settings", Actor: clientIP(r), Zone: name})
	http.Redirect(w, r, "/zones/"+name+"?flash=Settings+saved", http.StatusSeeOther)
}

func (srv *Server) handleDeleteZone(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	if err := srv.store.DeleteZone(name); err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "zone_delete", Actor: clientIP(r), Zone: name})
	http.Redirect(w, r, "/?flash=Zone+deleted", http.StatusSeeOther)
}

// ── editor ──────────────────────────────────────────────────────────────────

func (srv *Server) handleEditor(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	z, err := srv.store.GetZone(name)
	if !srv.zoneOK(w, err) {
		return
	}
	recs, _ := srv.store.Records(name)
	dirty, _ := srv.store.Dirty(name)
	_, published, _ := srv.store.LastSnapshot(name)
	srv.render(w, "editor.html", srv.pageData(r, name, map[string]any{
		"Zone": name, "Z": z, "Serial": z.Serial, "Targets": z.Targets,
		"Records": recordRows(recs), "Dirty": dirty, "Published": published,
		"Flash": r.URL.Query().Get("flash"),
	}))
}

func (srv *Server) renderRecords(w http.ResponseWriter, r *http.Request, zoneName string, rows []recordRow, recErr string) {
	s, _ := srv.currentSession(r)
	srv.render(w, "records", map[string]any{
		"Zone": zoneName, "CSRF": csrfOf(s), "Records": rows, "RecErr": recErr,
	})
}

func (srv *Server) handleRecordsFragment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	if ok, _ := srv.store.ZoneExists(name); !ok {
		http.Error(w, "unknown zone", http.StatusNotFound)
		return
	}
	recs, _ := srv.store.Records(name)
	srv.renderRecords(w, r, name, recordRows(recs), "")
}

func (srv *Server) handleEditRecordForm(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	recs, _ := srv.store.Records(name)
	rows := recordRows(recs)
	markEditing(rows, id)
	srv.renderRecords(w, r, name, rows, "")
}

func (srv *Server) handleAddRecord(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	_ = r.ParseForm()
	rec, err := zone.NormalizeRecord(name, r.PostFormValue("name"), formUint(r, "ttl", 3600), r.PostFormValue("type"), r.PostFormValue("data"))
	if err != nil {
		recs, _ := srv.store.Records(name)
		srv.renderRecords(w, r, name, recordRows(recs), err.Error())
		return
	}
	if _, err := srv.store.AddRecord(name, rec); err != nil {
		http.Error(w, "add record: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "record_add", Actor: clientIP(r), Zone: name})
	recs, _ := srv.store.Records(name)
	srv.renderRecords(w, r, name, recordRows(recs), "")
}

func (srv *Server) handleUpdateRecord(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	rec, err := zone.NormalizeRecord(name, r.PostFormValue("name"), formUint(r, "ttl", 3600), r.PostFormValue("type"), r.PostFormValue("data"))
	if err != nil {
		recs, _ := srv.store.Records(name)
		rows := recordRows(recs)
		markEditing(rows, id)
		srv.renderRecords(w, r, name, rows, err.Error())
		return
	}
	if err := srv.store.UpdateRecord(name, id, rec); err != nil {
		http.Error(w, "update record: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "record_update", Actor: clientIP(r), Zone: name})
	recs, _ := srv.store.Records(name)
	srv.renderRecords(w, r, name, recordRows(recs), "")
}

func (srv *Server) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err := srv.store.DeleteRecord(name, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "delete record: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "record_delete", Actor: clientIP(r), Zone: name})
	recs, _ := srv.store.Records(name)
	srv.renderRecords(w, r, name, recordRows(recs), "")
}

// ── preview / diff / validate ───────────────────────────────────────────────

func (srv *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	z, err := srv.store.Build(name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown zone", http.StatusNotFound)
			return
		}
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.render(w, "preview.html", srv.pageData(r, "Preview "+name, map[string]any{
		"Zone": name, "Content": string(z.Render()),
	}))
}

func (srv *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	z, err := srv.store.Build(name)
	if !srv.zoneOK(w, err) {
		return
	}
	snap, published, _ := srv.store.LastSnapshot(name)
	if !published {
		srv.render(w, "diff.html", srv.pageData(r, "Diff "+name, map[string]any{"Zone": name, "Published": false}))
		return
	}
	added, removed := computeDiff(snap.Content, string(z.Render()))
	srv.render(w, "diff.html", srv.pageData(r, "Diff "+name, map[string]any{
		"Zone": name, "Published": true, "Added": added, "Removed": removed,
	}))
}

func (srv *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	z, err := srv.store.Build(name)
	if err != nil {
		srv.render(w, "validate_result", map[string]any{"OK": false, "Detail": err.Error()})
		return
	}
	out, cerr := (zone.Checker{}).Check(r.Context(), name, z.Render())
	if cerr != nil {
		if isCheckzoneFailure(cerr) {
			srv.render(w, "validate_result", map[string]any{"OK": false, "Detail": strings.TrimSpace(out)})
			return
		}
		srv.render(w, "validate_result", map[string]any{"OK": true, "Msg": "Parsed OK (named-checkzone unavailable locally; targets re-validate on deploy)."})
		return
	}
	srv.render(w, "validate_result", map[string]any{"OK": true, "Msg": "Zone is valid."})
}

// ── deploy ──────────────────────────────────────────────────────────────────

func (srv *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	z, err := srv.store.GetZone(name)
	if !srv.zoneOK(w, err) {
		return
	}
	built, err := srv.store.Build(name)
	if err != nil {
		srv.render(w, "deploy_result.html", srv.pageData(r, "Deploy "+name, map[string]any{"Zone": name, "Error": err.Error()}))
		return
	}
	old, newSerial, err := built.BumpSerial(zone.SerialPolicy(srv.cfg.SerialPolicy), time.Now())
	if err != nil {
		srv.render(w, "deploy_result.html", srv.pageData(r, "Deploy "+name, map[string]any{"Zone": name, "Error": err.Error()}))
		return
	}
	content := built.Render()

	res, derr := srv.deployer.Deploy(r.Context(), deploy.Request{
		Zone: name, Content: content, OldSerial: old, NewSerial: newSerial, Targets: z.Targets,
	})
	data := srv.pageData(r, "Deploy "+name, map[string]any{"Zone": name})
	if derr != nil {
		data["Error"] = derr.Error()
		if res != nil {
			data["Result"] = res
		}
		srv.audit.Log(audit.Event{Action: "deploy", Actor: clientIP(r), Zone: name, Status: "error", Detail: derr.Error()})
		srv.render(w, "deploy_result.html", data)
		return
	}
	srv.metrics.ObserveDeploy(string(res.Status))
	if res.AllMoved() {
		if err := srv.store.Publish(name, string(content), newSerial); err != nil {
			srv.log.Error("publish snapshot failed", "zone", name, "err", err)
		} else {
			res.Promoted = true
		}
	}
	srv.audit.Log(audit.Event{
		Action: "deploy", Actor: clientIP(r), Zone: name,
		Status: string(res.Status), OldSerial: res.OldSerial, NewSerial: res.NewSerial,
		Servers: serverSummaries(res.Servers),
	})
	data["Result"] = res
	srv.render(w, "deploy_result.html", data)
}

func serverSummaries(srs []deploy.ServerResult) []string {
	out := make([]string, len(srs))
	for i, sr := range srs {
		state := "reloaded"
		switch {
		case sr.Err != "":
			state = strings.SplitN(sr.Err, ":", 2)[0]
		case !sr.Verified:
			state = "unverified"
		}
		out[i] = sr.Name + ":" + state
	}
	return out
}

// ── history / rollback ──────────────────────────────────────────────────────

func (srv *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	if ok, _ := srv.store.ZoneExists(name); !ok {
		http.Error(w, "unknown zone", http.StatusNotFound)
		return
	}
	type row struct {
		ID     int64
		Serial uint32
		When   string
		Size   int
	}
	snaps, _ := srv.store.ListSnapshots(name)
	rows := make([]row, 0, len(snaps))
	for _, s := range snaps {
		rows = append(rows, row{ID: s.ID, Serial: s.Serial, When: s.CreatedAt.Format("2006-01-02 15:04:05 MST"), Size: s.Size})
	}
	srv.render(w, "history.html", srv.pageData(r, "History "+name, map[string]any{
		"Zone": name, "Snapshots": rows, "Flash": r.URL.Query().Get("flash"),
	}))
}

func (srv *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("zone")
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err := srv.store.RestoreSnapshot(name, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown snapshot", http.StatusNotFound)
			return
		}
		http.Error(w, "rollback: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "rollback", Actor: clientIP(r), Zone: name, Detail: strconv.FormatInt(id, 10)})
	http.Redirect(w, r, "/zones/"+name+"?flash=Restored;+review+and+deploy", http.StatusSeeOther)
}

// ── helpers ─────────────────────────────────────────────────────────────────

// zoneOK writes a 404/500 and returns false if err is non-nil.
func (srv *Server) zoneOK(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown zone", http.StatusNotFound)
		return false
	}
	http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
	return false
}

func (srv *Server) formTargets(r *http.Request) []string {
	var out []string
	for _, t := range r.PostForm["targets"] {
		if _, ok := srv.cfg.Servers[t]; ok {
			out = append(out, t)
		}
	}
	return out
}

// isCheckzoneFailure reports whether the error is named-checkzone rejecting the
// zone (a non-zero exit) versus being unable to run at all.
func isCheckzoneFailure(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

func formUint(r *http.Request, key string, def uint32) uint32 {
	v := strings.TrimSpace(r.PostFormValue(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return def
	}
	return uint32(n)
}
