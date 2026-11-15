// Package server is Alcove's HTTP surface: the mobile-first UI shell, sign-in, the admin console
// and the transport-security gate (M2b). Every real feature route arrives with its milestone.
//
// Request path, outermost first: panic guard, security headers, the network gate (constraint 2,
// D-23), the locked / first-run gates, then the route table. Every state-changing request needs a
// CSRF token and is refused when the browser says it is cross-site. Nothing here makes an
// outbound call, and nothing logs content (constraints 3 and 8).
package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/ingest"
	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/retention"
	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Backend is what the server needs from the running application.
type Backend interface {
	State() string // "locked" or "unlocked"
	Store() *store.Store
	Unlock(passphrase string) error
}

// Options configure New. Only Version is usually set; the rest are for tests.
type Options struct {
	Version   string
	Env       netsec.Env
	Logger    *slog.Logger
	Now       func() time.Time
	Params    auth.Params // zero means auth.DefaultParams
	AfterFunc func(time.Duration, func()) *time.Timer
	// BackupDir and BackupKeep configure the instance backup (D-28); TmpDir is the tmpfs the restore
	// drill opens its database in; AllowNonTmpfs is for tests and dev boxes only.
	BackupDir  string
	BackupKeep int
	TmpDir     string
	// MaxUploadBytes caps one uploaded backup (never above the Tryst reader bound, 512 MiB).
	MaxUploadBytes int64
	// UserRecoveryDays and AdminRecoveryDays are the soft-delete windows (zero means 10 and 45).
	UserRecoveryDays, AdminRecoveryDays int
	AllowNonTmpfs                       bool
}

// level is who may call a route.
type level int

const (
	public level = iota // anyone on an allowed network; CSRF by pre-session token
	anyone              // like public, but uses the session's CSRF token when signed in
	user                // a signed-in, active account
	admin               // a signed-in administrator; anyone else sees a 404
)

type route struct {
	pattern string // "METHOD /path"
	level   level
}

// Server implements http.Handler.
type Server struct {
	be     Backend
	opt    Options
	log    *slog.Logger
	now    func() time.Time
	params auth.Params

	mux      *http.ServeMux
	routes   []route
	tpl      map[string]*template.Template
	sessions *auth.Sessions
	csrfKey  []byte

	loginLim, setupLim, unlockLim, stepLim, refusedLog, joinLim *auth.Limiter

	net      atomic.Pointer[netsec.Manager]
	hasUsers atomic.Bool

	mu        sync.Mutex
	ready     bool // settings loaded from the unlocked store
	setupCode string

	jobs jobs

	ingMu    sync.Mutex
	ing      *ingest.Service
	ingStore *store.Store
	ret      *retention.Service
	retStore *store.Store
}

// New builds the server. It fails only on an invalid transport environment, so a typo in
// ALCOVE_ALLOWED_NETWORKS stops the start instead of silently opening the door.
func New(be Backend, opt Options) (*Server, error) {
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Params == (auth.Params{}) {
		opt.Params = auth.DefaultParams
	}
	s := &Server{be: be, opt: opt, log: opt.Logger, now: opt.Now, params: opt.Params,
		sessions: auth.NewSessions(opt.Now), tpl: map[string]*template.Template{},
		loginLim: auth.LoginLimiter(), setupLim: &auth.Limiter{Capacity: 10, RefillEvery: 30 * time.Second},
		unlockLim:  &auth.Limiter{Capacity: 5, RefillEvery: time.Minute},
		joinLim:    &auth.Limiter{Capacity: 10, RefillEvery: 30 * time.Second},
		stepLim:    &auth.Limiter{Capacity: 8, RefillEvery: 30 * time.Second},
		refusedLog: &auth.Limiter{Capacity: 1, RefillEvery: 10 * time.Minute},
	}
	s.csrfKey = make([]byte, 32)
	if _, err := rand.Read(s.csrfKey); err != nil {
		return nil, err
	}
	m, err := netsec.New(nil, opt.Env, s.netOptions())
	if err != nil {
		return nil, err
	}
	s.net.Store(m)
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes_()
	s.ensure()
	return s, nil
}

func (s *Server) netOptions() netsec.Options {
	return netsec.Options{Now: s.now, AfterFunc: s.opt.AfterFunc, OnRevert: s.onNetworksReverted}
}

func (s *Server) parseTemplates() error {
	pages := []string{"message", "setup", "login", "unlock", "home", "account", "stepup", "admin", "adminlog", "settings", "backup", "uploads", "users", "usercreated", "forcepw", "mydeleted", "admindeleted", "spaces", "space", "join", "joinconsent", "codeshown", "adminspaces", "spaceentries", "myentries", "profile", "prefill", "calendar", "compare", "entry", "search", "gallery"}
	for _, p := range pages {
		t, err := template.ParseFS(assets, "templates/base.html", "templates/"+p+".html")
		if err != nil {
			return fmt.Errorf("server: template %s: %w", p, err)
		}
		s.tpl[p] = t
	}
	return nil
}

// ensure loads the saved transport settings and the first-run state once the store is open (it
// is not while the key is passphrase-wrapped and still locked). It returns the store, or nil.
func (s *Server) ensure() *store.Store {
	st := s.be.Store()
	if st == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return st
	}
	saved := map[string]string{}
	for _, k := range []string{"setting.allowed_networks", "setting.proxy_mode", "setting.trusted_proxies", "setting.require_tls"} {
		if v, ok, err := st.GetMeta(k); err == nil && ok {
			saved[k] = v
		}
	}
	m, err := netsec.New(saved, s.opt.Env, s.netOptions())
	if err != nil {
		s.log.Error("saved transport settings rejected; keeping the defaults", "err", err.Error())
	} else {
		s.net.Store(m)
	}
	var n int
	if err := st.View(context.Background(), func(tx *sqlTx) (err error) { n, err = auth.Count(tx); return }); err != nil {
		s.log.Error("cannot read accounts", "err", err.Error())
		return st
	}
	s.hasUsers.Store(n > 0)
	if n == 0 {
		s.setupCode = newSetupCode()
		s.log.Warn("first run: there are no accounts yet. Open /setup and enter this one-time setup code",
			"setup_code", s.setupCode)
	}
	s.ready = true
	return st
}

// newSetupCode is 10 characters from an alphabet with no look-alikes, shown as XXXXX-XXXXX.
func newSetupCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var b [10]byte
	_, _ = rand.Read(b[:])
	out := make([]byte, 0, 11)
	for i, c := range b {
		if i == 5 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(c)%len(alphabet)])
	}
	return string(out)
}

func normalizeCode(s string) string {
	s = strings.ToUpper(s)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// rq is one request with what the middleware learned about it.
type rq struct {
	s  *Server
	w  http.ResponseWriter
	r  *http.Request
	c  netsec.Client
	st *store.Store
	id *identity // nil unless signed in
}

type identity struct {
	sess auth.Session
	user auth.User
}

func (q *rq) ip() string { return q.c.IP.String() }

// ServeHTTP is the whole middleware chain.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("panic while serving", "path", r.URL.Path, "panic", fmt.Sprint(v))
			http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		}
	}()
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")

	st := s.ensure()
	nm := s.net.Load()
	c := nm.Resolve(r)
	if c.Secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	// Constraint 2 inside the app: refuse anything that is not from an allowed network. A direct
	// loopback peer is let through so the container healthcheck works whatever the list says.
	if !nm.Allowed(c) && !(netsec.LoopbackPeer(r) && c.IP.IsLoopback()) {
		if s.refusedLog.Allow(c.IP.String()) {
			s.log.Warn("refused a request from outside the allowed networks", "ip", c.IP.String())
		}
		http.Error(w, "This server only accepts connections from its own network.", http.StatusForbidden)
		return
	}
	q := &rq{s: s, w: w, r: r, c: c, st: st}

	p := r.URL.Path
	switch {
	case p == "/healthz" || strings.HasPrefix(p, "/static/") || p == "/favicon.ico" || p == "/theme":
		// always reachable
	case st == nil: // locked: only the unlock page works
		if p != "/unlock" {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/unlock", http.StatusSeeOther)
			} else {
				http.Error(w, "Locked.", http.StatusServiceUnavailable)
			}
			return
		}
	case !s.hasUsers.Load():
		s.refreshHasUsers(st)
		if !s.hasUsers.Load() && p != "/setup" {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
	}
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), rqKey{}, q)))
}

type rqKey struct{}

func (s *Server) refreshHasUsers(st *store.Store) {
	var n int
	if err := st.View(context.Background(), func(tx *sqlTx) (err error) { n, err = auth.Count(tx); return }); err == nil && n > 0 {
		s.hasUsers.Store(true)
	}
}

// handle registers a route. The wrapper identifies the user, enforces the level, and checks the
// CSRF token and fetch metadata on every POST before the handler runs.
func (s *Server) handle(pattern string, lv level, h func(*rq)) { s.register(pattern, lv, false, h) }

// handleStream is handle for a route that reads its own request body as a stream (an upload): the
// wrapper still identifies the user and refuses cross-site requests, but it does not parse the
// form or cap the body, and the handler MUST check the CSRF token itself before reading any file.
func (s *Server) handleStream(pattern string, lv level, h func(*rq)) {
	s.register(pattern, lv, true, h)
}

func (s *Server) register(pattern string, lv level, stream bool, h func(*rq)) {
	s.routes = append(s.routes, route{pattern, lv})
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		q, _ := r.Context().Value(rqKey{}).(*rq)
		if q == nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		q.r = r // the mux passes a context-carrying copy; parse and read from that one
		if lv >= anyone && q.st != nil {
			q.id = s.identify(q)
		}
		if lv >= user && q.id != nil && q.id.user.MustChange && r.URL.Path != "/account/password" {
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		if lv >= user && q.id == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if lv == admin && q.id.user.Role != auth.RoleAdmin {
			q.notFound()
			return
		}
		if r.Method == http.MethodPost {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				q.message(http.StatusForbidden, "Request refused", "This form was sent from another site, so it was not accepted.", "/")
				return
			}
			if stream {
				h(q)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				q.message(http.StatusRequestEntityTooLarge, "Too much data", "That form was larger than expected.", "/")
				return
			}
			if !q.csrfOK(r.PostForm.Get("csrf")) {
				q.message(http.StatusForbidden, "That page expired", "For safety the form was not accepted. Go back, reload the page and try again.", "/")
				return
			}
		}
		h(q)
	})
}

// identify returns the signed-in account, or nil. A disabled account loses its sessions here.
func (s *Server) identify(q *rq) *identity {
	ck, err := q.r.Cookie(sessionCookie)
	if err != nil || ck.Value == "" {
		return nil
	}
	sess, err := s.sessions.Get(ck.Value)
	if err != nil {
		return nil
	}
	var u auth.User
	if err := q.st.View(q.r.Context(), func(tx *sqlTx) (err error) { u, err = auth.ByID(tx, sess.UserID); return }); err != nil || u.Status != auth.StatusActive {
		s.sessions.Delete(sess.ID)
		return nil
	}
	return &identity{sess: sess, user: u}
}

const (
	sessionCookie = "alcove_session"
	preCookie     = "alcove_pre"
	themeCookie   = "alcove_theme"
	flashCookie   = "alcove_flash"
)

// preToken derives the pre-session CSRF token from the browser's random pre-session cookie. It
// is the "signed double submit" pattern: an attacker's page can neither read nor set our cookie.
func (s *Server) preToken(cookie string) string {
	m := hmac.New(sha256.New, s.csrfKey)
	m.Write([]byte("alcove-pre|" + cookie))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// csrf returns the token to embed in a form on this page.
func (q *rq) csrf() string {
	if q.id != nil {
		return q.id.sess.CSRF
	}
	ck, err := q.r.Cookie(preCookie)
	if err != nil || len(ck.Value) < 20 {
		v, err := auth.RandomToken()
		if err != nil {
			return ""
		}
		http.SetCookie(q.w, &http.Cookie{Name: preCookie, Value: v, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteStrictMode, Secure: q.c.Secure})
		return q.s.preToken(v)
	}
	return q.s.preToken(ck.Value)
}

func (q *rq) csrfOK(got string) bool {
	var want string
	if q.id != nil {
		want = q.id.sess.CSRF
	} else if ck, err := q.r.Cookie(preCookie); err == nil && len(ck.Value) >= 20 {
		want = q.s.preToken(ck.Value)
	}
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// page is what every template receives.
type page struct {
	Title, Version, Path, Active, Theme, ThemeAttr, CSRF string
	User                                                 *auth.User
	IsAdmin, Insecure                                    bool
	Flash, Error                                         string
	// per page
	Message, Back, Next, LastLogin, Head, AllowedSource, ProxySource, TLSSource string
	AllowedText, ProxiesText, YourIP, PendingLeft                               string
	Stats                                                                       struct {
		Users, LogEntries int
		Sealed, Schema    string
	}
	Entries                   []logRow
	Newer, Older              int
	SteppedUp, Pending        bool
	EnvNets, EnvProxy, EnvTLS bool
	ProxyMode, RequireTLS     bool
	// backup page
	Backups     []backupRow
	Uploads     []uploadRow
	HasPassword bool
	MaxMB       int
	Home        struct {
		HasData        bool
		Entries, Media int
		When, Name     string
	}
	BackupOK, JobRunning           bool
	Refresh                        bool
	JobText, LastBackup, LastDrill string
	JobSince                       int64
	// accounts, deleted items, password change (D-30)
	Users, DeletedUsers       []userRow
	DeletedUploads            []delRow
	MyDeleted                 []myDelRow
	NewUsername, TempPassword string
	Created, Forced           bool
	UserDays, AdminDays       int
	// spaces and joining (D-31)
	Spaces, DeletedSpaces              []spaceRow
	Space                              *spaceView
	Consent                            []string
	Code, JoinURL, CodeNote, JoinName  string
	JoinIsServer, JoinIsCode, SignedIn bool
	ServerInvites                      []inviteRow
	// shared entries (D-32)
	Shared             []entryRow
	EntryTotal, PageNo int
	SpaceID, SpaceName string
	// profiles (D-33)
	ProfileName   string
	ProfileFields []profileField
	Offers        []offerRow
	Cal           *calView
	Compare       *compareView
	Detail        *share.Detail
	// search (D-38)
	SearchScopes         []scopeOption
	SearchQ              string
	Tiles                []tile
	Results              []resultRow
	Searched, SearchFull bool
	DetailBack           string
	DetailBackLabel      string
	ProfileTZ            string
	DefaultTZ            string
}

type logRow struct {
	Seq                       int64
	Time, Who, Action, Target string
	Fields                    string
}

var knownPaths = map[string]bool{"/": true, "/login": true, "/setup": true, "/unlock": true, "/account": true,
	"/admin": true, "/admin/log": true, "/admin/settings": true, "/admin/backup": true, "/uploads": true, "/admin/users": true, "/admin/deleted": true, "/uploads/deleted": true, "/account/password": true, "/spaces": true, "/join": true, "/admin/spaces": true, "/profile": true}

// render writes a page with a status code. It builds the whole page first so a template error
// never leaves half a page on the wire.
func (q *rq) render(status int, name string, p *page) {
	p.Version = q.s.opt.Version
	p.CSRF = q.csrf()
	p.Theme = "auto"
	if ck, err := q.r.Cookie(themeCookie); err == nil && (ck.Value == "light" || ck.Value == "dark") {
		p.Theme, p.ThemeAttr = ck.Value, ck.Value
	}
	p.Path = "/"
	if knownPaths[q.r.URL.Path] {
		p.Path = q.r.URL.Path
	}
	if q.id != nil {
		u := q.id.user
		p.User, p.IsAdmin = &u, u.Role == auth.RoleAdmin
		p.Insecure = p.IsAdmin && !q.c.Secure
	}
	if p.Flash == "" {
		p.Flash = q.takeFlash()
	}
	var buf bytes.Buffer
	if err := q.s.tpl[name].ExecuteTemplate(&buf, "base", p); err != nil {
		q.s.log.Error("template failed", "page", name, "err", err.Error())
		http.Error(q.w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	h := q.w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	q.w.WriteHeader(status)
	_, _ = q.w.Write(buf.Bytes())
}

func (q *rq) message(status int, title, msg, back string) {
	q.render(status, "message", &page{Title: title, Message: msg, Back: back})
}

func (q *rq) notFound() {
	q.message(http.StatusNotFound, "Not found", "There is nothing at this address.", "/")
}

func (q *rq) serverError(what string, err error) {
	q.s.log.Error(what, "err", err.Error())
	q.message(http.StatusInternalServerError, "Something went wrong", "The change was not made. Try again, and check the server log if it keeps happening.", "/")
}

// Flash messages travel as a short key in a cookie, never as text, so a link cannot make the
// page say anything.
var flashes = map[string]string{
	"pw":              "Password changed. Other devices were signed out.",
	"net-prop":        "The new network list is live. Confirm it below within a minute, or it reverts.",
	"net-ok":          "Network list saved.",
	"net-undo":        "Network list reverted.",
	"proxy":           "Proxy settings saved.",
	"tls":             "HTTPS setting saved.",
	"signedout":       "Signed out.",
	"uploaded":        "Backup uploaded and checked. It is now your current data.",
	"done":            "Done.",
	"purged":          "Expired items were removed for good.",
	"restored":        "Restored.",
	"deleted":         "Moved to Recently deleted. You can restore it from there.",
	"deleted-empty":   "Moved to Recently deleted. You have no current data now; upload a backup to start again.",
	"profile":         "Profile saved.",
	"profile-filled":  "Imported what you ticked. Nothing is shared until you tick Share on a field.",
	"deleted-nocache": "Moved to Recently deleted. Your current data could not be rebuilt because no backup password is remembered, so it is empty until you upload again.",
}

func (q *rq) setFlash(key string) {
	http.SetCookie(q.w, &http.Cookie{Name: flashCookie, Value: key, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: q.c.Secure, MaxAge: 60})
}

func (q *rq) takeFlash() string {
	ck, err := q.r.Cookie(flashCookie)
	if err != nil {
		return ""
	}
	http.SetCookie(q.w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1})
	return flashes[ck.Value]
}

func (q *rq) redirect(path string) { http.Redirect(q.w, q.r, path, http.StatusSeeOther) }

func (q *rq) setSession(sess auth.Session) {
	http.SetCookie(q.w, &http.Cookie{Name: sessionCookie, Value: sess.ID, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: q.c.Secure})
}

// audit appends one admin-log entry and runs extra in the same transaction, so the action and its
// log line commit together or not at all (N5). durable seals the database before returning.
func (q *rq) audit(action, targetType, targetID, fields string, extra func(*sqlTx) error) error {
	actor := adminlog.System
	if q.id != nil {
		actor = q.id.user.ID
	}
	return q.st.Update(q.r.Context(), true, func(tx *sqlTx) error {
		if extra != nil {
			if err := extra(tx); err != nil {
				return err
			}
		}
		_, err := adminlog.Append(tx, q.s.now(), actor, action, targetType, targetID, fields)
		return err
	})
}

// onNetworksReverted writes the system's log line when an unconfirmed network change undoes itself.
func (s *Server) onNetworksReverted(netsec.Settings) {
	st := s.be.Store()
	if st == nil {
		return
	}
	err := st.Update(context.Background(), true, func(tx *sqlTx) error {
		_, err := adminlog.Append(tx, s.now(), adminlog.System, "settings.networks.revert", "settings", "networks", "allowed_networks")
		return err
	})
	if err != nil {
		s.log.Error("could not log the network revert", "err", err.Error())
	}
	s.log.Warn("an unconfirmed allowed-networks change reverted itself")
}

// ---- static files and health ----

func (s *Server) routes_() {
	s.mux = http.NewServeMux()
	sub, _ := fs.Sub(assets, "static")
	serveStatic := func(name, ctype string) {
		b, err := fs.ReadFile(sub, name)
		if err != nil {
			panic(err) // embedded at build time; a missing file is a build error
		}
		// Revalidate every time (a cheap 304) so an upgrade shows at once, never a stale stylesheet.
		sum := sha256.Sum256(b)
		etag := `"` + base64.RawURLEncoding.EncodeToString(sum[:12]) + `"`
		serve := func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write(b)
		}
		s.mux.HandleFunc("GET /static/"+name, serve)
		if name == "mark-32.png" {
			s.mux.HandleFunc("GET /favicon.ico", serve)
		}
	}
	serveStatic("app.css", "text/css; charset=utf-8")
	serveStatic("ui.js", "text/javascript; charset=utf-8")
	for _, n := range []string{"mark-32.png", "mark-96.png", "mark-192.png"} {
		serveStatic(n, "image/png")
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "state": s.be.State(), "version": s.opt.Version})
	})

	s.handle("GET /unlock", public, s.getUnlock)
	s.handle("POST /unlock", public, s.postUnlock)
	s.handle("GET /setup", public, s.getSetup)
	s.handle("POST /setup", public, s.postSetup)
	s.handle("GET /login", public, s.getLogin)
	s.handle("POST /login", public, s.postLogin)
	s.handle("POST /logout", anyone, s.postLogout)
	s.handle("POST /theme", anyone, s.postTheme)
	s.handle("GET /{$}", user, s.getHome)
	s.handle("GET /uploads", user, s.getUploads)
	s.handleStream("POST /uploads", user, s.postUploads)
	s.handle("POST /uploads/delete", user, s.postUploadDelete)
	s.handle("GET /uploads/deleted", user, s.getMyDeleted)
	s.handle("POST /uploads/restore", user, s.postUploadRestore)
	s.handle("POST /uploads/download", user, s.postDownload)
	s.handle("GET /account/password", user, s.getPassword)
	s.handle("POST /account/password", user, s.postPassword)
	s.handle("GET /spaces", user, s.getSpaces)
	s.handle("POST /spaces", user, s.postSpaceCreate)
	s.handle("GET /profile", user, s.getProfile)
	s.handle("POST /profile", user, s.postProfile)
	s.handle("GET /profile/prefill", user, s.getPrefill)
	s.handle("POST /profile/prefill", user, s.postPrefill)
	s.handle("GET /search", user, s.getSearch)
	s.handle("POST /search", user, s.postSearch)
	s.handle("GET /entries", user, s.getMyEntries)
	s.handle("GET /entries/{id}", user, s.getMyEntry)
	s.handle("GET /photos/{id}", user, s.getMyPhoto)
	s.handle("GET /gallery", user, s.getMyGallery)
	s.handle("GET /spaces/{id}/gallery", user, s.getSpaceGallery)
	s.handle("POST /entries/private", user, s.postEntryPrivate)
	s.handle("GET /spaces/{id}", user, s.getSpace)
	s.handle("GET /spaces/{id}/entries", user, s.getSpaceEntries)
	s.handle("GET /spaces/{id}/entry/{owner}/{entry}", user, s.getSpaceEntry)
	s.handle("GET /spaces/{id}/photo/{owner}/{media}", user, s.getSpacePhoto)
	s.handle("GET /spaces/{id}/calendar", user, s.getSpaceCalendar)
	s.handle("GET /spaces/{id}/compare", user, s.getSpaceCompare)
	s.handle("POST /spaces/{id}/restore", user, s.postSpaceRestore)
	s.handle("POST /spaces/{id}/{action}", user, s.postSpaceAction)
	s.handle("GET /join", anyone, s.getJoin)
	s.handle("POST /join/code", anyone, s.postJoinCode)
	s.handle("POST /join/accept", anyone, s.postJoinAccept)
	s.handle("GET /account", user, s.getAccount)
	s.handle("POST /account", user, s.postAccount)
	s.handle("GET /stepup", user, s.getStepUp)
	s.handle("POST /stepup", user, s.postStepUp)
	s.handle("GET /admin", admin, s.getAdmin)
	s.handle("GET /admin/log", admin, s.getAdminLog)
	s.handle("POST /admin/log/verify", admin, s.postVerifyLog)
	s.handle("GET /admin/users", admin, s.getUsers)
	s.handle("POST /admin/users/create", admin, s.postUserCreate)
	for _, k := range []string{"disable", "enable", "delete", "restore", "reset"} {
		s.handle("POST /admin/users/"+k, admin, s.userAction(k))
	}
	s.handle("GET /admin/deleted", admin, s.getAdminDeleted)
	s.handle("POST /admin/deleted/restore", admin, s.postAdminRestoreUpload)
	s.handle("POST /admin/deleted/purge", admin, s.postAdminPurge)
	s.handle("GET /admin/spaces", admin, s.getAdminSpaces)
	s.handle("POST /admin/spaces/action", admin, s.postAdminSpaceAction)
	s.handle("POST /admin/invites/create", admin, s.postServerInvite)
	s.handle("POST /admin/invites/revoke", admin, s.postServerInviteRevoke)
	s.handle("GET /admin/backup", admin, s.getBackup)
	s.handle("POST /admin/backup/run", admin, s.postBackupRun)
	s.handle("POST /admin/backup/verify", admin, s.postBackupVerify)
	s.handle("POST /admin/backup/drill", admin, s.postBackupDrill)
	s.handle("POST /admin/backup/rebuild", admin, s.postRebuild)
	s.handle("GET /admin/settings", admin, s.getSettings)
	s.handle("POST /admin/settings/networks", admin, s.postNetworks)
	s.handle("POST /admin/settings/networks/confirm", admin, s.postNetworksConfirm)
	s.handle("POST /admin/settings/networks/cancel", admin, s.postNetworksCancel)
	s.handle("POST /admin/settings/proxy", admin, s.postProxy)
	s.handle("POST /admin/settings/tls", admin, s.postTLS)

	// Everything else is a 404 page, never a directory listing or a hint.
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q, _ := r.Context().Value(rqKey{}).(*rq)
		if q == nil {
			http.NotFound(w, r)
			return
		}
		if q.st != nil {
			q.id = s.identify(q)
		}
		q.notFound()
	})
}

// Routes lists the registered routes and their levels, for the route-security tests.
func (s *Server) Routes() []string {
	out := make([]string, len(s.routes))
	for i, r := range s.routes {
		out[i] = fmt.Sprintf("%s [%d]", r.pattern, r.level)
	}
	return out
}

// sqlTx is database/sql's Tx, aliased to keep signatures short.
type sqlTx = sql.Tx
