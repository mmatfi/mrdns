package web

import (
	"errors"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mmatfi/mrdns/internal/audit"
	"github.com/mmatfi/mrdns/internal/config"
	"github.com/mmatfi/mrdns/internal/deploy"
	"github.com/mmatfi/mrdns/internal/store"
	"github.com/mmatfi/mrdns/internal/zone"
)

// recordRow is a presentation row for the editor's record table.
type recordRow struct {
	Name, TTL, Type, Data string
	Managed               bool // SOA: shown read-only
	Editing               bool // rendered as an inline edit form
}

func recordRows(z *zone.Zone) []recordRow {
	recs := z.Records()
	rows := make([]recordRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, recordRow{
			Name:    r.Name,
			TTL:     strconv.FormatUint(uint64(r.TTL), 10),
			Type:    r.Type,
			Data:    r.Data,
			Managed: r.Type == "SOA",
		})
	}
	return rows
}

// markEditing flags the first row matching (name, rtype, data) for inline edit.
func markEditing(rows []recordRow, name, rtype, data string) {
	for i := range rows {
		if rows[i].Name == name && rows[i].Type == rtype && rows[i].Data == data {
			rows[i].Editing = true
			return
		}
	}
}

// zoneConfig resolves the zone from the path, writing a 404 if unknown.
func (srv *Server) zoneConfig(w http.ResponseWriter, r *http.Request) (string, config.Zone, bool) {
	name := r.PathValue("zone")
	zc, ok := srv.cfg.Zones[name]
	if !ok {
		http.Error(w, "unknown zone", http.StatusNotFound)
		return "", config.Zone{}, false
	}
	return name, zc, true
}

// currentContent returns the draft if present, otherwise the live content,
// reporting whether the content came from a draft.
func (srv *Server) currentContent(zc config.Zone) (content []byte, isDraft bool, err error) {
	if srv.store.HasDraft(zc.File) {
		b, err := srv.store.ReadDraft(zc.File)
		return b, true, err
	}
	b, err := srv.store.ReadLive(zc.File)
	return b, false, err
}

func (srv *Server) handleEditor(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	content, isDraft, err := srv.currentContent(zc)
	if err != nil {
		srv.render(w, "editor.html", srv.pageData(r, name, map[string]any{
			"Zone": name, "Targets": zc.Targets,
			"LoadErr": "No zone file yet (" + err.Error() + "). Use the raw editor to create one.",
		}))
		return
	}
	z, perr := zone.Parse(content, name)
	if perr != nil {
		srv.render(w, "editor.html", srv.pageData(r, name, map[string]any{
			"Zone": name, "Targets": zc.Targets, "IsDraft": isDraft,
			"ParseErr": perr.Error(),
		}))
		return
	}
	serial, _ := z.Serial()
	srv.render(w, "editor.html", srv.pageData(r, name, map[string]any{
		"Zone": name, "Targets": zc.Targets, "IsDraft": isDraft,
		"Serial": serial, "Records": recordRows(z),
		"Flash": r.URL.Query().Get("flash"),
	}))
}

// renderRecordsFragment re-renders the records table partial (htmx target).
func (srv *Server) renderRecordsFragment(w http.ResponseWriter, r *http.Request, zoneName string, rows []recordRow, recErr string) {
	s, _ := srv.currentSession(r)
	srv.render(w, "records", map[string]any{
		"Zone": zoneName, "CSRF": csrfOf(s), "Records": rows, "RecErr": recErr,
	})
}

func (srv *Server) handleAddRecord(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	rname := strings.TrimSpace(r.PostFormValue("name"))
	ttl := strings.TrimSpace(r.PostFormValue("ttl"))
	rtype := strings.ToUpper(strings.TrimSpace(r.PostFormValue("type")))
	data := strings.TrimSpace(r.PostFormValue("data"))

	content, _, err := srv.currentContent(zc)
	if err != nil {
		http.Error(w, "load zone: "+err.Error(), http.StatusInternalServerError)
		return
	}
	z, err := zone.Parse(content, name)
	if err != nil {
		http.Error(w, "parse zone: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if rname == "" || rtype == "" || data == "" {
		srv.renderRecordsFragment(w, r, name, recordRows(z), "name, type, and data are required")
		return
	}
	if ttl == "" {
		ttl = "3600"
	}
	line := rname + " " + ttl + " IN " + rtype + " " + data
	if err := z.Add(line); err != nil {
		srv.renderRecordsFragment(w, r, name, recordRows(z), err.Error())
		return
	}
	if err := srv.store.WriteDraft(zc.File, z.Render()); err != nil {
		http.Error(w, "save draft: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "record_add", Actor: clientIP(r), Zone: name})
	srv.renderRecordsFragment(w, r, name, recordRows(z), "")
}

func (srv *Server) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	content, _, err := srv.currentContent(zc)
	if err != nil {
		http.Error(w, "load zone: "+err.Error(), http.StatusInternalServerError)
		return
	}
	z, err := zone.Parse(content, name)
	if err != nil {
		http.Error(w, "parse zone: "+err.Error(), http.StatusInternalServerError)
		return
	}
	z.Remove(r.PostFormValue("name"), r.PostFormValue("type"), r.PostFormValue("data"))
	if err := srv.store.WriteDraft(zc.File, z.Render()); err != nil {
		http.Error(w, "save draft: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "record_delete", Actor: clientIP(r), Zone: name})
	srv.renderRecordsFragment(w, r, name, recordRows(z), "")
}

// handleRecordsFragment returns the records table (used to refresh or to cancel
// an inline edit).
func (srv *Server) handleRecordsFragment(w http.ResponseWriter, r *http.Request) {
	name, z, ok := srv.loadEditableZone(w, r)
	if !ok {
		return
	}
	srv.renderRecordsFragment(w, r, name, recordRows(z), "")
}

// handleEditRecordForm returns the records table with one row switched to an
// inline edit form.
func (srv *Server) handleEditRecordForm(w http.ResponseWriter, r *http.Request) {
	name, z, ok := srv.loadEditableZone(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	rows := recordRows(z)
	markEditing(rows, q.Get("name"), q.Get("type"), q.Get("data"))
	srv.renderRecordsFragment(w, r, name, rows, "")
}

// handleUpdateRecord replaces a record (identified by its old name/type/data)
// with edited values, writing the result to the draft.
func (srv *Server) handleUpdateRecord(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	oldName := r.PostFormValue("old_name")
	oldType := r.PostFormValue("old_type")
	oldData := r.PostFormValue("old_data")
	newName := strings.TrimSpace(r.PostFormValue("name"))
	ttl := strings.TrimSpace(r.PostFormValue("ttl"))
	newType := strings.ToUpper(strings.TrimSpace(r.PostFormValue("type")))
	data := strings.TrimSpace(r.PostFormValue("data"))

	content, _, err := srv.currentContent(zc)
	if err != nil {
		http.Error(w, "load zone: "+err.Error(), http.StatusInternalServerError)
		return
	}
	z, err := zone.Parse(content, name)
	if err != nil {
		http.Error(w, "parse zone: "+err.Error(), http.StatusInternalServerError)
		return
	}

	editErr := func(msg string) {
		rows := recordRows(z)
		markEditing(rows, oldName, oldType, oldData)
		srv.renderRecordsFragment(w, r, name, rows, msg)
	}
	if newName == "" || newType == "" || data == "" {
		editErr("name, type, and data are required")
		return
	}
	if ttl == "" {
		ttl = "3600"
	}
	line := newName + " " + ttl + " IN " + newType + " " + data
	replaced, err := z.Replace(oldName, oldType, oldData, line)
	if err != nil {
		editErr(err.Error())
		return
	}
	if !replaced {
		editErr("record not found (it may have changed); the table was reloaded")
		return
	}
	if err := srv.store.WriteDraft(zc.File, z.Render()); err != nil {
		http.Error(w, "save draft: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "record_update", Actor: clientIP(r), Zone: name})
	srv.renderRecordsFragment(w, r, name, recordRows(z), "")
}

// loadEditableZone resolves and parses the current draft-or-live content of the
// path's zone, writing an HTTP error and returning ok=false on failure.
func (srv *Server) loadEditableZone(w http.ResponseWriter, r *http.Request) (string, *zone.Zone, bool) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return "", nil, false
	}
	content, _, err := srv.currentContent(zc)
	if err != nil {
		http.Error(w, "load zone: "+err.Error(), http.StatusInternalServerError)
		return "", nil, false
	}
	z, err := zone.Parse(content, name)
	if err != nil {
		http.Error(w, "parse zone: "+err.Error(), http.StatusInternalServerError)
		return "", nil, false
	}
	return name, z, true
}

func (srv *Server) handleRawForm(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	content, isDraft, err := srv.currentContent(zc)
	if err != nil {
		content = []byte("$ORIGIN " + name + ".\n")
		isDraft = false
	}
	srv.render(w, "raw.html", srv.pageData(r, "Raw "+name, map[string]any{
		"Zone": name, "Content": string(content), "IsDraft": isDraft,
		"Err": r.URL.Query().Get("error"),
	}))
}

func (srv *Server) handleRawSave(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	content := r.PostFormValue("content")
	if _, err := zone.Parse([]byte(content), name); err != nil {
		srv.render(w, "raw.html", srv.pageData(r, "Raw "+name, map[string]any{
			"Zone": name, "Content": content, "Err": "Zone does not parse: " + err.Error(),
		}))
		return
	}
	if err := srv.store.WriteDraft(zc.File, []byte(content)); err != nil {
		http.Error(w, "save draft: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "raw_save", Actor: clientIP(r), Zone: name})
	http.Redirect(w, r, "/zones/"+name+"?flash=Draft+saved", http.StatusSeeOther)
}

func (srv *Server) handleDiscard(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	if err := srv.store.DiscardDraft(zc.File); err != nil {
		http.Error(w, "discard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "discard", Actor: clientIP(r), Zone: name})
	http.Redirect(w, r, "/zones/"+name+"?flash=Draft+discarded", http.StatusSeeOther)
}

func (srv *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	if !srv.store.HasDraft(zc.File) {
		srv.render(w, "diff.html", srv.pageData(r, "Diff "+name, map[string]any{"Zone": name, "HasDraft": false}))
		return
	}
	draft, _ := srv.store.ReadDraft(zc.File)
	var liveStr string
	if live, err := srv.store.ReadLive(zc.File); err == nil {
		liveStr = string(live)
	}
	added, removed := computeDiff(liveStr, string(draft))
	srv.render(w, "diff.html", srv.pageData(r, "Diff "+name, map[string]any{
		"Zone": name, "HasDraft": true, "Added": added, "Removed": removed,
	}))
}

func (srv *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	content, _, err := srv.currentContent(zc)
	if err != nil {
		srv.render(w, "validate_result", map[string]any{"OK": false, "Detail": err.Error()})
		return
	}
	z, perr := zone.Parse(content, name)
	if perr != nil {
		srv.render(w, "validate_result", map[string]any{"OK": false, "Detail": perr.Error()})
		return
	}
	out, cerr := (zone.Checker{}).Check(r.Context(), name, z.Render())
	if cerr != nil {
		var exitErr *exec.ExitError
		if errors.As(cerr, &exitErr) {
			srv.render(w, "validate_result", map[string]any{"OK": false, "Detail": strings.TrimSpace(out)})
			return
		}
		// Couldn't run named-checkzone locally; the parse already succeeded.
		srv.render(w, "validate_result", map[string]any{"OK": true, "Msg": "Parsed OK (named-checkzone unavailable locally; targets re-validate on deploy)."})
		return
	}
	srv.render(w, "validate_result", map[string]any{"OK": true, "Msg": "Zone is valid."})
}

func (srv *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	name, _, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	res, err := srv.deployer.Deploy(r.Context(), name)
	data := srv.pageData(r, "Deploy "+name, map[string]any{"Zone": name})
	if err != nil {
		data["Error"] = err.Error()
		if res != nil {
			data["Result"] = res
		}
		srv.audit.Log(audit.Event{Action: "deploy", Actor: clientIP(r), Zone: name, Status: "error", Detail: err.Error()})
	} else {
		data["Result"] = res
		srv.metrics.ObserveDeploy(string(res.Status))
		srv.audit.Log(audit.Event{
			Action: "deploy", Actor: clientIP(r), Zone: name,
			Status: string(res.Status), OldSerial: res.OldSerial, NewSerial: res.NewSerial,
			Servers: serverSummaries(res.Servers),
		})
	}
	srv.render(w, "deploy_result.html", data)
}

// serverSummaries renders each per-server result as "name:state" for the audit
// log, where state is the furthest stage reached or the failing stage.
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

func (srv *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	type row struct {
		ID, When string
		Size     int64
	}
	var rows []row
	backups, err := srv.store.ListBackups(zc.File)
	if err == nil {
		for _, b := range backups {
			rows = append(rows, row{ID: b.ID, When: b.CreatedAt.Format("2006-01-02 15:04:05 MST"), Size: b.Size})
		}
	}
	srv.render(w, "history.html", srv.pageData(r, "History "+name, map[string]any{
		"Zone": name, "Backups": rows, "Flash": r.URL.Query().Get("flash"),
	}))
}

func (srv *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	name, zc, ok := srv.zoneConfig(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	id := r.PostFormValue("id")
	if err := srv.store.Restore(zc.File, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown backup", http.StatusNotFound)
			return
		}
		http.Error(w, "rollback: "+err.Error(), http.StatusInternalServerError)
		return
	}
	srv.audit.Log(audit.Event{Action: "rollback", Actor: clientIP(r), Zone: name, Detail: id})
	http.Redirect(w, r, "/zones/"+name+"?flash=Restored+into+draft;+review+and+deploy", http.StatusSeeOther)
}
