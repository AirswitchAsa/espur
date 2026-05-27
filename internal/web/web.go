// Package web is the operator-facing admin UI per specs/webui.dog.md.
// Kept deliberately minimal in v0.1: plain html/template, no JS build, no
// htmx, no OAuth — BYO API key only. Pages: status home, vendors,
// threads list, thread detail peek.
package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
)

// Server is the admin UI HTTP server.
type Server struct {
	db    *store.DB
	vault *secrets.Vault
	pool  *vendor.Pool
	ts    *transcript.Store
	tmpl  *template.Template
}

// New wires the admin server. No auth — relies on reverse-proxy auth per spec.
func New(db *store.DB, vault *secrets.Vault, pool *vendor.Pool, ts *transcript.Store) *Server {
	s := &Server{db: db, vault: vault, pool: pool, ts: ts}
	s.tmpl = template.Must(template.New("").Funcs(template.FuncMap{
		"fmtTime": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			return t.Format("15:04 MST")
		},
		"untilNow": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			d := time.Until(*t)
			if d < 0 {
				return "expired"
			}
			if d < time.Minute {
				return "<1m"
			}
			if d < time.Hour {
				return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
			}
			return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
		},
	}).Parse(layout))
	template.Must(s.tmpl.Parse(homeTpl))
	template.Must(s.tmpl.Parse(vendorsTpl))
	template.Must(s.tmpl.Parse(threadsTpl))
	template.Must(s.tmpl.Parse(threadDetailTpl))
	return s
}

// Handler returns the http.Handler to mount on the admin port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /vendors", s.vendorsList)
	mux.HandleFunc("POST /vendors/add", s.vendorAdd)
	mux.HandleFunc("POST /vendors/{id}/key", s.vendorSetKey)
	mux.HandleFunc("POST /vendors/{id}/toggle", s.vendorToggle)
	mux.HandleFunc("POST /vendors/{id}/delete", s.vendorDelete)
	mux.HandleFunc("POST /vendors/{id}/clear-penalty", s.vendorClearPenalty)
	mux.HandleFunc("POST /vendors/reorder", s.vendorsReorder)
	mux.HandleFunc("GET /threads", s.threads)
	mux.HandleFunc("GET /threads/{platform}/{enc_id}", s.threadDetail)
	return mux
}

// ---- handlers ----

type homePage struct {
	NumVendors, NumEligible, NumCooldown, NumAuthLocked int
	NumThreads                                          int
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vs, _ := s.db.ListVendors(ctx)
	snaps, _ := s.pool.PenalizedSnapshotsAll(ctx)
	var elig, cd, auth int
	for _, sn := range snaps {
		switch sn.Status {
		case store.PenaltyAuthLocked:
			auth++
		case store.PenaltyCooldown:
			cd++
		default:
			elig++
		}
	}
	page := homePage{
		NumVendors:    len(vs),
		NumEligible:   elig,
		NumCooldown:   cd,
		NumAuthLocked: auth,
		NumThreads:    s.countThreads(),
	}
	s.render(w, "home", page)
}

type vendorRow struct {
	Vendor     store.Vendor
	CredStatus string
	Penalty    vendor.PenalizedSnapshot
}

func (s *Server) vendorsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vs, _ := s.db.ListVendors(ctx)
	rows := make([]vendorRow, 0, len(vs))
	for _, v := range vs {
		c, err := s.db.GetCredential(ctx, "vendor", v.VendorID)
		credStatus := "missing"
		if err == nil {
			credStatus = c.Status
		}
		pen, _ := s.db.GetPenalty(ctx, v.VendorID)
		rows = append(rows, vendorRow{
			Vendor: v, CredStatus: credStatus,
			Penalty: vendor.PenalizedSnapshot{
				VendorID: v.VendorID, Status: pen.Status, CooldownUntil: pen.CooldownUntil,
			},
		})
	}
	s.render(w, "vendors", rows)
}

func (s *Server) vendorAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id := strings.TrimSpace(r.FormValue("vendor_id"))
	model := strings.TrimSpace(r.FormValue("model"))
	envKey := strings.TrimSpace(r.FormValue("env_key"))
	if id == "" || model == "" {
		http.Error(w, "vendor_id and model required", 400)
		return
	}
	ctx := r.Context()
	vs, _ := s.db.ListVendors(ctx)
	pos := len(vs)
	if err := s.db.UpsertVendor(ctx, store.Vendor{
		VendorID: id, Model: model, Enabled: true, Position: pos, CredKind: "byo_key",
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Seed an empty credential row so its env_keys are remembered (the key
	// itself is set via /vendors/{id}/key).
	if envKey != "" {
		_ = s.db.PutCredential(ctx, store.Credential{
			Scope: "vendor", ID: id, Kind: "byo_key", Status: "missing",
			EnvKeys: []string{envKey},
		})
	}
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

func (s *Server) vendorSetKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	plain := r.FormValue("key")
	envKey := strings.TrimSpace(r.FormValue("env_key"))
	if plain == "" {
		http.Error(w, "key required", 400)
		return
	}
	ctx := r.Context()
	blob, err := s.vault.Encrypt([]byte(plain))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	existing, err := s.db.GetCredential(ctx, "vendor", id)
	envKeys := []string{}
	if err == nil {
		envKeys = existing.EnvKeys
	}
	if envKey != "" {
		envKeys = []string{envKey}
	}
	if err := s.db.PutCredential(ctx, store.Credential{
		Scope: "vendor", ID: id, Kind: "byo_key", Status: "set",
		Blob: blob, EnvKeys: envKeys,
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Saving credentials clears auth_locked per spec (also returns vendor to eligible).
	_ = s.pool.ClearPenalty(ctx, id)
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

func (s *Server) vendorToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	vs, _ := s.db.ListVendors(ctx)
	for _, v := range vs {
		if v.VendorID == id {
			v.Enabled = !v.Enabled
			_ = s.db.UpsertVendor(ctx, v)
			break
		}
	}
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

func (s *Server) vendorDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.db.DeleteVendor(r.Context(), id)
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

func (s *Server) vendorClearPenalty(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.pool.ClearPenalty(r.Context(), id)
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

func (s *Server) vendorsReorder(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id := r.FormValue("vendor_id")
	dir := r.FormValue("dir") // "up" | "down"
	ctx := r.Context()
	vs, _ := s.db.ListVendors(ctx)
	idx := -1
	for i, v := range vs {
		if v.VendorID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		http.Redirect(w, r, "/vendors", http.StatusSeeOther)
		return
	}
	switch dir {
	case "up":
		if idx > 0 {
			vs[idx-1], vs[idx] = vs[idx], vs[idx-1]
		}
	case "down":
		if idx < len(vs)-1 {
			vs[idx+1], vs[idx] = vs[idx], vs[idx+1]
		}
	}
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.VendorID
	}
	_ = s.db.ReorderVendors(ctx, ids)
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

// ---- threads ----

type threadRow struct {
	Platform     string
	EncID        string
	RawID        string
	SizeBytes    int64
	LastActivity time.Time
}

func (s *Server) countThreads() int {
	root := filepath.Join(s.ts.BaseDir, "threads")
	count := 0
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.IsDir() {
			return nil
		}
		// depth: <BaseDir>/threads/<platform>/<encoded_id>/
		rel, _ := filepath.Rel(root, p)
		if strings.Count(rel, string(filepath.Separator)) == 1 {
			count++
		}
		return nil
	})
	return count
}

func (s *Server) listThreads() []threadRow {
	root := filepath.Join(s.ts.BaseDir, "threads")
	var rows []threadRow
	platforms, _ := os.ReadDir(root)
	for _, pf := range platforms {
		if !pf.IsDir() {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(root, pf.Name()))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, pf.Name(), e.Name())
			size, last := dirSize(dir)
			rows = append(rows, threadRow{
				Platform: pf.Name(), EncID: e.Name(),
				SizeBytes: size, LastActivity: last,
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].LastActivity.After(rows[j].LastActivity)
	})
	return rows
}

func (s *Server) threads(w http.ResponseWriter, r *http.Request) {
	s.render(w, "threads", s.listThreads())
}

type threadDetailPage struct {
	Platform string
	EncID    string
	Agents   string
	Facts    []factRow
	Tail     []transcript.Record
}

type factRow struct {
	Name string
	Size int64
}

func (s *Server) threadDetail(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	enc := r.PathValue("enc_id")
	dir := filepath.Join(s.ts.BaseDir, "threads", platform, enc)
	agentsBytes, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	entries, _ := os.ReadDir(dir)
	var facts []factRow
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), "fact_") || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		fi, _ := e.Info()
		facts = append(facts, factRow{Name: e.Name(), Size: fi.Size()})
	}
	// tail: read transcript.jsonl directly, we don't know raw id.
	tail, _ := tailJSONL(filepath.Join(dir, "transcript.jsonl"), 30)
	s.render(w, "thread_detail", threadDetailPage{
		Platform: platform, EncID: enc,
		Agents: string(agentsBytes), Facts: facts, Tail: tail,
	})
}

func tailJSONL(path string, n int) ([]transcript.Record, error) {
	// crude: read everything; transcripts are small in v0.1.
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var all []transcript.Record
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var r transcript.Record
		if jsonUnmarshal(line, &r) == nil {
			all = append(all, r)
		}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// jsonUnmarshal is a tiny wrapper so the rest of this file doesn't import
// encoding/json directly (keeps imports tight; only transcript.Record needs it).
func jsonUnmarshal(s string, out any) error {
	return jsonUnmarshalImpl([]byte(s), out)
}

// ---- rendering ----

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "layout", struct {
		Page string
		Data any
	}{Page: name, Data: data}); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func dirSize(dir string) (int64, time.Time) {
	var size int64
	var last time.Time
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		size += fi.Size()
		if fi.ModTime().After(last) {
			last = fi.ModTime()
		}
		return nil
	})
	return size, last
}

// ListenAndServe runs the admin UI on addr. Closes when ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
		return nil
	case err := <-errCh:
		return err
	}
}

// _ keeps strconv referenced if a future page wants to parse query ints.
var _ = strconv.Itoa
