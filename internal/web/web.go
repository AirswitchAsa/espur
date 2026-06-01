// Package web is the operator-facing admin UI. Server-rendered HTML built on
// the Spicadust design system shipped under static/. No JS build, no SPA —
// pages are plain html/template; the small static/js/app.js adds the
// interactive bits (theme, drawers, drag-reorder, cooldown countdowns).
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/punny/espur/internal/contextasm"
	"github.com/punny/espur/internal/opencode"
	"github.com/punny/espur/internal/providers"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
)

// Version is the espur version string surfaced in the status strip / Health
// page. Wired from cmd at startup; defaults to "dev" for tests.
var Version = "dev"

// startTime is captured once for uptime reporting.
var startTime = time.Now()

//go:embed static
var staticFS embed.FS

// AdapterHealth is the minimal contract the UI needs about each registered
// adapter. Implemented by every internal/adapter.Adapter.
type AdapterHealth interface {
	Platform() string
	Healthy() bool
}

// Server is the admin UI HTTP server.
type Server struct {
	db       *store.DB
	vault    *secrets.Vault
	pool     *vendor.Pool
	ts       *transcript.Store
	tmpl     *template.Template
	adapters []AdapterHealth
	bind     string
}

// New wires the admin server.
func New(db *store.DB, vault *secrets.Vault, pool *vendor.Pool, ts *transcript.Store) *Server {
	s := &Server{db: db, vault: vault, pool: pool, ts: ts}
	funcs := template.FuncMap{
		"inc": func(i int) int { return i + 1 },
	}
	tmpl := template.Must(template.New("layout").Funcs(funcs).Parse(layout))
	template.Must(tmpl.Parse(iconsTpl))
	template.Must(tmpl.Parse(homeTpl))
	template.Must(tmpl.Parse(vendorsTpl))
	template.Must(tmpl.Parse(addVendorDrawerTpl))
	template.Must(tmpl.Parse(threadsTpl))
	template.Must(tmpl.Parse(threadDetailTpl))
	template.Must(tmpl.Parse(oauthTpl))
	template.Must(tmpl.Parse(settingsTpl))
	template.Must(tmpl.Parse(healthTpl))
	s.tmpl = tmpl
	return s
}

// RegisterAdapter wires an adapter into status reporting. Boot-only.
func (s *Server) RegisterAdapter(a AdapterHealth) {
	s.adapters = append(s.adapters, a)
}

// Handler returns the http.Handler to mount on the admin port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	subFS, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(subFS))))

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /vendors", s.vendorsList)
	mux.HandleFunc("POST /vendors/add", s.vendorAdd)
	mux.HandleFunc("POST /vendors/{id}/key", s.vendorSetKey)
	mux.HandleFunc("POST /vendors/{id}/toggle", s.vendorToggle)
	mux.HandleFunc("POST /vendors/{id}/delete", s.vendorDelete)
	mux.HandleFunc("POST /vendors/{id}/clear-penalty", s.vendorClearPenalty)
	mux.HandleFunc("POST /vendors/reorder", s.vendorsReorder)        // legacy up/down
	mux.HandleFunc("POST /vendors/reorder-all", s.vendorsReorderAll) // drag full order
	mux.HandleFunc("GET /oauth", s.oauthPage)
	mux.HandleFunc("GET /threads", s.threads)
	mux.HandleFunc("GET /threads/{platform}/{enc_id}", s.threadDetail)
	mux.HandleFunc("POST /threads/{platform}/{enc_id}/wipe-memory", s.threadWipeMemory)
	mux.HandleFunc("POST /threads/{platform}/{enc_id}/delete", s.threadDelete)
	mux.HandleFunc("GET /settings", s.settings)
	mux.HandleFunc("GET /health", s.healthPage)
	return mux
}

// ---------- envelope + status strip ----------

// envelope is what every page hands to the layout template.
type envelope struct {
	Page  string
	Strip stripData
	Data  any
}

type stripAdapter struct {
	Platform string
	Up       bool
	Threads  int
}

type stripData struct {
	Adapters   []stripAdapter
	Eligible   int
	Cooldown   int
	Locked     int
	InRotation string
	Version    string
}

func (s *Server) buildStrip(ctx context.Context) stripData {
	var sd stripData
	sd.Version = Version
	for _, a := range s.adapters {
		sd.Adapters = append(sd.Adapters, stripAdapter{Platform: a.Platform(), Up: a.Healthy()})
	}
	vs, _ := s.db.ListVendors(ctx)
	for _, v := range vs {
		pen, _ := s.db.GetPenalty(ctx, v.VendorID)
		switch pen.Status {
		case store.PenaltyAuthLocked:
			sd.Locked++
		case store.PenaltyCooldown:
			if pen.CooldownUntil != nil && pen.CooldownUntil.After(time.Now()) {
				sd.Cooldown++
			} else {
				sd.Eligible++
			}
		default:
			sd.Eligible++
		}
		if sd.InRotation == "" && v.Enabled {
			cred, cerr := s.db.GetCredential(ctx, "vendor", v.VendorID)
			ready := pen.Status == store.PenaltyEligible || (pen.Status == store.PenaltyCooldown && (pen.CooldownUntil == nil || pen.CooldownUntil.Before(time.Now())))
			credOK := v.CredKind == "oauth" || (cerr == nil && cred.Status == "set")
			if ready && credOK {
				sd.InRotation = v.VendorID
			}
		}
	}
	return sd
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "layout", envelope{
		Page:  name,
		Strip: s.buildStrip(context.Background()),
		Data:  data,
	}); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// ---------- flash cookie ----------
// flash sets a one-shot toast payload the JS reads on page load.
func setFlash(w http.ResponseWriter, kind, title, msg string) {
	b, _ := json.Marshal(map[string]string{"kind": kind, "title": title, "msg": msg})
	http.SetCookie(w, &http.Cookie{
		Name: "espur_flash", Value: url.QueryEscape(string(b)),
		Path: "/", MaxAge: 30,
	})
}

// ---------- home ----------

type homeAdapter struct {
	Platform string
	Up       bool
	Threads  int
}

type rotationRow struct {
	VendorID string
	Ready    bool
	Why      string
}

type feedItem struct {
	Platform    string
	EncID       string
	ThreadLabel string
	Vendor      string
	Ago         string
}

type homePage struct {
	NumVendors    int
	NumEnabled    int
	NumEligible   int
	NumCooldown   int
	NumAuthLocked int
	NumThreads    int
	NumCatalog    int
	Uptime        string
	Adapters      []homeAdapter
	Rotation      []rotationRow
	Feed          []feedItem
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vs, _ := s.db.ListVendors(ctx)
	var enabled, elig, cd, auth int
	var rotation []rotationRow
	now := time.Now()
	for _, v := range vs {
		if v.Enabled {
			enabled++
		}
		pen, _ := s.db.GetPenalty(ctx, v.VendorID)
		ready := false
		why := ""
		switch pen.Status {
		case store.PenaltyAuthLocked:
			auth++
			why = "auth-locked"
		case store.PenaltyCooldown:
			if pen.CooldownUntil != nil && pen.CooldownUntil.After(now) {
				cd++
				why = "cooldown"
			} else {
				elig++
				ready = true
			}
		default:
			elig++
			ready = true
		}
		if v.Enabled {
			if ready {
				cred, cerr := s.db.GetCredential(ctx, "vendor", v.VendorID)
				if v.CredKind != "oauth" && (cerr != nil || cred.Status != "set") {
					ready = false
					why = "no key"
				}
			}
			rotation = append(rotation, rotationRow{VendorID: v.VendorID, Ready: ready, Why: why})
		}
	}

	var adapters []homeAdapter
	threadsByPlatform := s.threadsByPlatform()
	for _, a := range s.adapters {
		adapters = append(adapters, homeAdapter{
			Platform: a.Platform(), Up: a.Healthy(), Threads: threadsByPlatform[a.Platform()],
		})
	}
	if len(adapters) == 0 {
		// surface even with no adapter wired so the cards section isn't empty in tests
		for plat, n := range threadsByPlatform {
			adapters = append(adapters, homeAdapter{Platform: plat, Up: false, Threads: n})
		}
	}

	page := homePage{
		NumVendors:    len(vs),
		NumEnabled:    enabled,
		NumEligible:   elig,
		NumCooldown:   cd,
		NumAuthLocked: auth,
		NumThreads:    s.countThreads(),
		NumCatalog:    len(providers.Catalog),
		Uptime:        humanDuration(time.Since(startTime)),
		Adapters:      adapters,
		Rotation:      rotation,
		Feed:          nil, // no invocation history yet — leave empty for honest UI
	}
	s.render(w, "home", page)
}

// ---------- vendors ----------

type vendorRow struct {
	Vendor            store.Vendor
	CredStatus        string
	EnvKey            string
	ProviderName      string
	ProviderShort     string
	PenaltyKind       string // "eligible" | "cooldown" | "auth_locked"
	CooldownUntilUnix int64
	CooldownRemaining string
}

type vendorsPage struct {
	Rows      []vendorRow
	Providers []providers.Provider
}

// providerLookupByID returns the catalog entry for an opencode provider id.
func providerLookupByID(id string) *providers.Provider {
	for i, p := range providers.Catalog {
		if p.ID == id {
			return &providers.Catalog[i]
		}
	}
	return nil
}

func providerShort(name string) string {
	switch strings.ToLower(name) {
	case "anthropic":
		return "ANTH"
	case "openai":
		return "OAI"
	case "google":
		return "GOOG"
	case "github copilot":
		return "COPILOT"
	case "xai":
		return "XAI"
	case "deepseek":
		return "DEEPSEEK"
	case "mistral":
		return "MISTRAL"
	case "groq":
		return "GROQ"
	case "cerebras":
		return "CEREBRAS"
	case "openrouter":
		return "OROUTE"
	}
	if len(name) > 6 {
		return strings.ToUpper(name[:6])
	}
	return strings.ToUpper(name)
}

func (s *Server) vendorsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vs, _ := s.db.ListVendors(ctx)
	now := time.Now()
	rows := make([]vendorRow, 0, len(vs))
	for _, v := range vs {
		c, err := s.db.GetCredential(ctx, "vendor", v.VendorID)
		credStatus := "missing"
		envKey := ""
		if err == nil {
			credStatus = c.Status
			if len(c.EnvKeys) > 0 {
				envKey = c.EnvKeys[0]
			}
		}
		// derive provider from model string
		prov, _ := providers.Lookup(v.Model)
		provName, provShort := "", ""
		if prov != nil {
			provName = prov.Name
			provShort = providerShort(prov.Name)
			if envKey == "" {
				envKey = prov.EnvKey
			}
		} else if i := strings.IndexByte(v.Model, '/'); i > 0 {
			provName = v.Model[:i]
			provShort = providerShort(provName)
		}

		pen, _ := s.db.GetPenalty(ctx, v.VendorID)
		row := vendorRow{
			Vendor: v, CredStatus: credStatus, EnvKey: envKey,
			ProviderName:  provName,
			ProviderShort: provShort,
		}
		switch pen.Status {
		case store.PenaltyAuthLocked:
			row.PenaltyKind = "auth_locked"
		case store.PenaltyCooldown:
			if pen.CooldownUntil != nil && pen.CooldownUntil.After(now) {
				row.PenaltyKind = "cooldown"
				row.CooldownUntilUnix = pen.CooldownUntil.Unix()
				left := time.Until(*pen.CooldownUntil)
				row.CooldownRemaining = fmt.Sprintf("%d:%02d", int(left.Minutes()), int(left.Seconds())%60)
			} else {
				row.PenaltyKind = "eligible"
			}
		default:
			row.PenaltyKind = "eligible"
		}
		rows = append(rows, row)
	}
	s.render(w, "vendors", vendorsPage{Rows: rows, Providers: providers.Catalog})
}

func (s *Server) vendorAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id := strings.TrimSpace(r.FormValue("vendor_id"))
	model := strings.TrimSpace(r.FormValue("model"))
	credKind := strings.TrimSpace(r.FormValue("cred_kind"))
	if credKind == "" {
		credKind = "byo_key"
	}
	if credKind != "byo_key" && credKind != "oauth" {
		http.Error(w, "cred_kind must be byo_key or oauth", 400)
		return
	}
	if id == "" || model == "" {
		http.Error(w, "vendor_id and model required", 400)
		return
	}
	prov, _ := providers.Lookup(model)
	if prov == nil {
		http.Error(w, "model not in curated catalog: "+model, 400)
		return
	}
	if credKind == "oauth" && !prov.SupportsOAuth {
		http.Error(w, "provider "+prov.ID+" does not support OAuth", 400)
		return
	}
	envKey := ""
	if credKind == "byo_key" {
		if prov.EnvKey == "" {
			http.Error(w, "provider "+prov.ID+" is OAuth-only", 400)
			return
		}
		envKey = prov.EnvKey
	}
	ctx := r.Context()
	vs, _ := s.db.ListVendors(ctx)
	pos := len(vs)
	if err := s.db.UpsertVendor(ctx, store.Vendor{
		VendorID: id, Model: model, Enabled: true, Position: pos, CredKind: credKind,
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	switch credKind {
	case "byo_key":
		_ = s.db.PutCredential(ctx, store.Credential{
			Scope: "vendor", ID: id, Kind: "byo_key", Status: "missing",
			EnvKeys: []string{envKey},
		})
		setFlash(w, "info", "Vendor added", id+" needs a key before it can be used.")
	case "oauth":
		_ = s.db.PutCredential(ctx, store.Credential{
			Scope: "vendor", ID: id, Kind: "oauth", Status: "set",
		})
		setFlash(w, "ok", "Vendor added", id+" uses OAuth — credential resolved from the provider session.")
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
	_ = s.pool.ClearPenalty(ctx, id)
	setFlash(w, "ok", "Key set", "Credential stored for "+id+".")
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
	setFlash(w, "ok", "Vendor deleted", id+" removed from the pool.")
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

func (s *Server) vendorClearPenalty(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.pool.ClearPenalty(r.Context(), id)
	setFlash(w, "ok", "Penalty cleared", id+" is eligible again.")
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

func (s *Server) vendorsReorder(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id := r.FormValue("vendor_id")
	dir := r.FormValue("dir")
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

// vendorsReorderAll accepts the full ordered ids[] from the drag UI and
// persists it as the new priority order in one shot.
func (s *Server) vendorsReorderAll(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ids := r.Form["ids"]
	if len(ids) == 0 {
		http.Redirect(w, r, "/vendors", http.StatusSeeOther)
		return
	}
	_ = s.db.ReorderVendors(r.Context(), ids)
	setFlash(w, "ok", "Priority updated", "Rotation order saved.")
	http.Redirect(w, r, "/vendors", http.StatusSeeOther)
}

// ---------- threads ----------

type threadRow struct {
	Platform        string
	EncID           string
	SizeBytes       int64
	SizeFmt         string
	LastActivity    time.Time
	LastActivityFmt string
}

func (s *Server) countThreads() int {
	root := filepath.Join(s.ts.BaseDir, "threads")
	count := 0
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if strings.Count(rel, string(filepath.Separator)) == 1 {
			count++
		}
		return nil
	})
	return count
}

func (s *Server) threadsByPlatform() map[string]int {
	out := map[string]int{}
	root := filepath.Join(s.ts.BaseDir, "threads")
	platforms, _ := os.ReadDir(root)
	for _, pf := range platforms {
		if !pf.IsDir() {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(root, pf.Name()))
		for _, e := range entries {
			if e.IsDir() {
				out[pf.Name()]++
			}
		}
	}
	return out
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
				SizeBytes:       size,
				SizeFmt:         humanBytes(size),
				LastActivity:    last,
				LastActivityFmt: humanTime(last),
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

// ---------- thread detail ----------

type factRow struct {
	Name        string
	EscapedName string
	Size        int64
	SizeFmt     string
	Body        string
}

type workdirFile struct {
	Name       string
	IsDir      bool
	SizeFmt    string
	ModTimeFmt string
}

type tailItem struct {
	Kind   string
	TS     string
	TSFmt  string
	Author transcript.Author
	Body   string
}

type threadDetailPage struct {
	Platform     string
	EncID        string
	Workdir      string
	Turns        int
	Agents       string
	Facts        []factRow
	Tail         []tailItem
	WorkdirFiles []workdirFile
}

// escapeForData mirrors the data-attribute escaping used by the JS file picker.
func escapeForData(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteString(fmt.Sprintf("_%d", r))
		}
	}
	return b.String()
}

func (s *Server) threadDetail(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	enc := r.PathValue("enc_id")
	dir := filepath.Join(s.ts.BaseDir, "threads", platform, enc)
	agentsBytes, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))

	entries, _ := os.ReadDir(dir)
	var facts []factRow
	var wdFiles []workdirFile
	for _, e := range entries {
		fi, _ := e.Info()
		if e.IsDir() {
			wdFiles = append(wdFiles, workdirFile{Name: e.Name(), IsDir: true, ModTimeFmt: humanTime(fi.ModTime())})
			continue
		}
		wdFiles = append(wdFiles, workdirFile{
			Name: e.Name(), IsDir: false,
			SizeFmt: humanBytes(fi.Size()), ModTimeFmt: humanTime(fi.ModTime()),
		})
		if strings.HasPrefix(e.Name(), "fact_") && strings.HasSuffix(e.Name(), ".md") {
			body, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			facts = append(facts, factRow{
				Name: e.Name(), EscapedName: escapeForData(e.Name()),
				Size: fi.Size(), SizeFmt: humanBytes(fi.Size()),
				Body: string(body),
			})
		}
	}

	rawTail, _ := tailJSONL(filepath.Join(dir, "transcript.jsonl"), contextasm.DefaultTailN)
	tail := make([]tailItem, 0, len(rawTail))
	for _, t := range rawTail {
		ts, _ := time.Parse(time.RFC3339Nano, t.TS)
		tail = append(tail, tailItem{
			Kind: string(t.Kind), TS: t.TS, TSFmt: humanTime(ts),
			Author: t.Author, Body: t.Body,
		})
	}

	s.render(w, "thread_detail", threadDetailPage{
		Platform: platform, EncID: enc,
		Workdir: dir, Turns: len(tail),
		Agents: string(agentsBytes), Facts: facts, Tail: tail,
		WorkdirFiles: wdFiles,
	})
}

func (s *Server) threadWipeMemory(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	enc := r.PathValue("enc_id")
	dir := filepath.Join(s.ts.BaseDir, "threads", platform, enc)
	// truncate (don't delete) so the file's existence stays consistent
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), nil, 0o644)
	setFlash(w, "ok", "AGENTS.md wiped", "Memory reset for this thread.")
	http.Redirect(w, r, "/threads/"+platform+"/"+enc, http.StatusSeeOther)
}

func (s *Server) threadDelete(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	enc := r.PathValue("enc_id")
	dir := filepath.Join(s.ts.BaseDir, "threads", platform, enc)
	// guardrail: refuse to delete anything outside threads/
	root := filepath.Join(s.ts.BaseDir, "threads")
	if !strings.HasPrefix(filepath.Clean(dir), filepath.Clean(root)+string(filepath.Separator)) {
		http.Error(w, "refuse to delete outside threads root", 400)
		return
	}
	_ = os.RemoveAll(dir)
	setFlash(w, "ok", "Workdir deleted", "Thread workdir and its files were removed.")
	http.Redirect(w, r, "/threads", http.StatusSeeOther)
}

// ---------- oauth ----------

func (s *Server) oauthPage(w http.ResponseWriter, _ *http.Request) {
	entries, err := opencode.ReadAuthEntries()
	if err != nil {
		http.Error(w, "read auth.json: "+err.Error(), 500)
		return
	}
	s.render(w, "oauth", struct {
		Entries  []opencode.AuthEntry
		AuthPath string
	}{Entries: entries, AuthPath: opencode.AuthFilePath()})
}

// ---------- settings ----------

type settingsAdapter struct {
	Platform string
	Up       bool
}

type settingsPage struct {
	TranscriptTail int
	MaxBytesFmt    string
	MaxAgentsMDFmt string
	Version        string
	DataDir        string
	AdminBind      string
	Adapters       []settingsAdapter
}

func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	var adapters []settingsAdapter
	for _, a := range s.adapters {
		adapters = append(adapters, settingsAdapter{Platform: a.Platform(), Up: a.Healthy()})
	}
	bind := s.bind
	if bind == "" {
		bind = "(not set)"
	}
	s.render(w, "settings", settingsPage{
		TranscriptTail: contextasm.DefaultTailN,
		MaxBytesFmt:    humanBytes(contextasm.MaxBytes),
		MaxAgentsMDFmt: humanBytes(contextasm.MaxAgentsMDBytes),
		Version:        Version,
		DataDir:        s.ts.BaseDir,
		AdminBind:      bind,
		Adapters:       adapters,
	})
}

// SetBind lets cmd record the admin bind address for the Settings page.
func (s *Server) SetBind(addr string) { s.bind = addr }

// ---------- health page (human-readable) ----------

type healthPage struct {
	OK               bool
	Version          string
	Uptime           string
	NumAdaptersUp    int
	NumAdaptersTotal int
	AllAdaptersUp    bool
	AdapterList      string
	DBStatus         string
	NumEligible      int
	NumCooldown      int
	NumAuthLocked    int
	NumThreads       int
	FreeDisk         string
	RawJSON          string
}

func (s *Server) healthPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var up, total int
	var labels []string
	for _, a := range s.adapters {
		total++
		if a.Healthy() {
			up++
			labels = append(labels, a.Platform())
		} else {
			labels = append(labels, a.Platform()+" (down)")
		}
	}
	vs, _ := s.db.ListVendors(ctx)
	var elig, cd, lock int
	now := time.Now()
	for _, v := range vs {
		pen, _ := s.db.GetPenalty(ctx, v.VendorID)
		switch pen.Status {
		case store.PenaltyAuthLocked:
			lock++
		case store.PenaltyCooldown:
			if pen.CooldownUntil != nil && pen.CooldownUntil.After(now) {
				cd++
			} else {
				elig++
			}
		default:
			elig++
		}
	}
	dbOK := "ok"
	if err := s.db.SQL().PingContext(ctx); err != nil {
		dbOK = "down: " + err.Error()
	}

	page := healthPage{
		OK:               up == total,
		Version:          Version,
		Uptime:           humanDuration(time.Since(startTime)),
		NumAdaptersUp:    up,
		NumAdaptersTotal: total,
		AllAdaptersUp:    up == total,
		AdapterList:      strings.Join(labels, " · "),
		DBStatus:         dbOK,
		NumEligible:      elig,
		NumCooldown:      cd,
		NumAuthLocked:    lock,
		NumThreads:       s.countThreads(),
		FreeDisk:         humanBytes(freeDiskBytes(s.ts.BaseDir)),
	}
	page.RawJSON = healthzJSON(s.adapters)
	s.render(w, "health", page)
}

func freeDiskBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

func healthzJSON(adapters []AdapterHealth) string {
	type entry struct {
		Platform string `json:"platform"`
		Healthy  bool   `json:"healthy"`
	}
	out := struct {
		OK       bool    `json:"ok"`
		Version  string  `json:"version"`
		Uptime   string  `json:"uptime"`
		Go       string  `json:"go"`
		Adapters []entry `json:"adapters"`
	}{OK: true, Version: Version, Uptime: humanDuration(time.Since(startTime)), Go: runtime.Version()}
	for _, a := range adapters {
		h := a.Healthy()
		out.Adapters = append(out.Adapters, entry{Platform: a.Platform(), Healthy: h})
		if !h {
			out.OK = false
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}

// ---------- helpers ----------

func tailJSONL(path string, n int) ([]transcript.Record, error) {
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
		if json.Unmarshal([]byte(line), &r) == nil {
			all = append(all, r)
		}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
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

func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	const k = 1024
	if n < k {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / k
	u := 0
	for v >= k && u < len(units)-1 {
		v /= k
		u++
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) - days*24
	return fmt.Sprintf("%dd %dh", days, hours)
}

func humanTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		return t.Format("2006-01-02 15:04")
	}
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
	}
	if d < 7*24*time.Hour {
		return fmt.Sprintf("%d days ago", int(d.Hours())/24)
	}
	return t.Format("2006-01-02")
}

// ListenAndServe runs the admin UI on addr. Closes when ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	s.bind = addr
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
