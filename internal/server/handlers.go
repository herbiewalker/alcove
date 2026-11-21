package server

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"

	"strconv"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/totp"
)

const timeFmt = "2006-01-02 15:04:05"

// entryFmt shows an entry time the way the Tryst app does (en-US medium date, short time).
const entryFmt = "Jan 2, 2006, 3:04 PM"

// tlsGate refuses sign-in pages over plain HTTP when require-TLS is on (N17). It reports true when
// it wrote the refusal.
func (q *rq) tlsGate() bool {
	if q.s.net.Load().TLSRequiredButMissing(q.c) {
		q.message(http.StatusForbidden, "Encrypted connection required",
			"This server only accepts sign-ins over HTTPS. Open it through its HTTPS address.", "")
		return true
	}
	return false
}

// ---- unlock ----

func (s *Server) getUnlock(q *rq) {
	if q.st != nil {
		q.redirect("/")
		return
	}
	q.render(http.StatusOK, "unlock", &page{Title: "Locked"})
}

func (s *Server) postUnlock(q *rq) {
	if q.st != nil {
		q.redirect("/")
		return
	}
	if !s.unlockLim.Allow(q.ip()) {
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a minute and try again.", "/unlock")
		return
	}
	if err := s.be.Unlock(q.r.PostForm.Get("passphrase")); err != nil {
		s.log.Warn("unlock failed", "ip", q.ip())
		q.render(http.StatusUnauthorized, "unlock", &page{Title: "Locked", Error: "That passphrase did not unlock the key."})
		return
	}
	s.log.Info("unlocked from the web page", "ip", q.ip())
	s.ensure()
	q.redirect("/")
}

// ---- first run ----

func (s *Server) getSetup(q *rq) {
	if s.hasUsers.Load() {
		q.notFound()
		return
	}
	if q.tlsGate() {
		return
	}
	q.render(http.StatusOK, "setup", &page{Title: "Welcome"})
}

// postSetup is the ONLY route that creates an account (constraint 11). It works once, with the
// one-time code from the server log, and only while there are no accounts at all.
func (s *Server) postSetup(q *rq) {
	if s.hasUsers.Load() {
		q.notFound()
		return
	}
	if q.tlsGate() {
		return
	}
	fail := func(status int, msg string) {
		q.render(status, "setup", &page{Title: "Welcome", Error: msg})
	}
	if !s.setupLim.Allow(q.ip()) {
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a minute and try again.", "/setup")
		return
	}
	f := q.r.PostForm
	s.mu.Lock()
	code := s.setupCode
	s.mu.Unlock()
	if code == "" || subtle.ConstantTimeCompare([]byte(normalizeCode(f.Get("code"))), []byte(normalizeCode(code))) != 1 {
		s.log.Warn("setup refused: wrong code", "ip", q.ip())
		fail(http.StatusUnauthorized, "That setup code is not right. Look in the server log for the one shown at start-up.")
		return
	}
	username, err := auth.NormalizeUsername(f.Get("username"))
	if err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	pw := f.Get("password")
	if pw != f.Get("confirm") {
		fail(http.StatusBadRequest, "The two passwords are not the same.")
		return
	}
	if err := auth.CheckPassword(pw, username); err != nil {
		fail(http.StatusBadRequest, strings.TrimPrefix(err.Error(), "password policy: "))
		return
	}
	hash, err := auth.Hash(pw, s.params)
	if err != nil {
		q.serverError("hash password", err)
		return
	}
	var u auth.User
	err = q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		n, err := auth.Count(tx)
		if err != nil {
			return err
		}
		if n > 0 {
			return errAlreadySetUp
		}
		if u, err = auth.Create(tx, s.now(), username, f.Get("display"), auth.RoleAdmin, hash); err != nil {
			return err
		}
		_, err = adminlog.Append(tx, s.now(), u.ID, "admin.first_run", "user", u.ID, "username,password,role")
		return err
	})
	switch {
	case errors.Is(err, auth.ErrBadDisplayName):
		fail(http.StatusBadRequest, "A name shown to others is 1 to 80 characters, with no control or direction-changing characters.")
		return
	case errors.Is(err, errAlreadySetUp):
		q.notFound()
		return
	case err != nil:
		q.serverError("first-run setup", err)
		return
	}
	s.mu.Lock()
	s.setupCode = ""
	s.mu.Unlock()
	s.hasUsers.Store(true)
	s.log.Info("first administrator created", "user", u.ID)
	s.signIn(q, u)
}

var errAlreadySetUp = errors.New("already set up")

// signIn issues a session and goes home.
func (s *Server) signIn(q *rq, u auth.User) {
	sess, err := s.sessions.Issue(u.ID, q.ip())
	if err != nil {
		q.serverError("issue session", err)
		return
	}
	q.setSession(sess)
	q.redirect("/")
}

// ---- sign in and out ----

func (s *Server) getLogin(q *rq) {
	if q.id != nil {
		q.redirect("/")
		return
	}
	if q.tlsGate() {
		return
	}
	q.render(http.StatusOK, "login", &page{Title: "Sign in"})
}

func (s *Server) postLogin(q *rq) {
	if q.tlsGate() {
		return
	}
	if !s.loginLim.Allow(q.ip()) {
		s.log.Warn("sign-in rate limited", "ip", q.ip())
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a little and try again.", "/login")
		return
	}
	username, password := q.r.PostForm.Get("username"), q.r.PostForm.Get("password")
	var u auth.User
	var uerr error
	var twoStep bool
	if err := q.st.View(q.r.Context(), func(tx *sql.Tx) (e error) {
		if u, uerr = auth.ByUsername(tx, username); uerr == nil {
			twoStep, e = totp.Enabled(tx, u.ID)
		}
		return e
	}); err != nil {
		q.serverError("sign-in lookup", err)
		return
	}
	ok, rehash := false, false
	if uerr == nil && u.Status == auth.StatusActive {
		ok, rehash = auth.Verify(password, u.PWHash, s.params)
	} else {
		auth.DummyVerify(password, s.params) // same work, same answer, for a missing or disabled account
	}
	if !ok {
		s.log.Warn("sign-in failed", "ip", q.ip())
		q.render(http.StatusUnauthorized, "login", &page{Title: "Sign in", Error: "Wrong username or password."})
		return
	}
	var newHash string
	if rehash {
		newHash, _ = auth.Hash(password, s.params)
	}
	if twoStep {
		// A right password is only the first step (D-49): no session, no sign-in line in the log, until
		// the second step passes. The stronger hash, if one was due, is still saved now.
		if newHash != "" {
			if err := q.st.Update(q.r.Context(), false, func(tx *sql.Tx) error { return auth.SetPassword(tx, u.ID, newHash) }); err != nil {
				q.serverError("sign-in rehash", err)
				return
			}
		}
		s.startSecondStep(q, u)
		return
	}
	err := q.st.Update(q.r.Context(), false, func(tx *sql.Tx) error {
		if err := auth.TouchLogin(tx, u.ID, s.now()); err != nil {
			return err
		}
		if newHash != "" {
			if err := auth.SetPassword(tx, u.ID, newHash); err != nil {
				return err
			}
		}
		if u.Role == auth.RoleAdmin {
			_, err := adminlog.Append(tx, s.now(), u.ID, "session.login", "user", u.ID, "")
			return err
		}
		return nil
	})
	if err != nil {
		q.serverError("sign-in bookkeeping", err)
		return
	}
	s.loginLim.Reset(q.ip())
	s.signIn(q, u)
}

func (s *Server) postLogout(q *rq) {
	if q.id != nil {
		s.sessions.Delete(q.id.sess.ID)
	}
	http.SetCookie(q.w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	q.redirect("/login")
}

func (s *Server) postTheme(q *rq) {
	c := &http.Cookie{Name: themeCookie, Path: "/", SameSite: http.SameSiteLaxMode, Secure: q.c.Secure, HttpOnly: true}
	switch v := q.r.PostForm.Get("theme"); v {
	case "light", "dark":
		c.Value, c.MaxAge = v, 365*24*3600
	default:
		c.Value, c.MaxAge = "", -1
	}
	http.SetCookie(q.w, c)
	next := "/"
	if n := q.r.PostForm.Get("next"); knownPaths[n] {
		next = n
	}
	q.redirect(next)
}

// ---- home and account ----

func (s *Server) getHome(q *rq) {
	p := &page{Title: "Home", Active: "home"}
	if list, err := s.ingestFor(q.st).List(q.r.Context(), q.id.user.ID); err == nil {
		for _, u := range list {
			if u.Head {
				p.Home.HasData, p.Home.Entries, p.Home.Media, p.Home.When = true, u.Encounters, u.Media, u.UploadedAt.UTC().Format(timeFmt)
				break
			}
		}
	}
	q.render(http.StatusOK, "home", p)
}

func (s *Server) getAccount(q *rq) {
	last := "this is your first"
	if !q.id.user.LastLoginAt.IsZero() {
		last = q.id.user.LastLoginAt.Format(timeFmt) + " UTC"
	}
	q.render(http.StatusOK, "account", &page{Title: "Account", Active: "account", LastLogin: last, ActivityOn: s.activityOn(q)})
}

func (s *Server) postAccount(q *rq) {
	f := q.r.PostForm
	again := func(status int, msg string) {
		q.render(status, "account", &page{Title: "Account", Active: "account", Error: msg, LastLogin: "-"})
	}
	if !s.stepLim.Allow(q.ip() + "|pw") {
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a little and try again.", "/account")
		return
	}
	if ok, _ := auth.Verify(f.Get("current"), q.id.user.PWHash, s.params); !ok {
		again(http.StatusUnauthorized, "The current password is not right.")
		return
	}
	pw := f.Get("password")
	if pw != f.Get("confirm") {
		again(http.StatusBadRequest, "The two new passwords are not the same.")
		return
	}
	if err := auth.CheckPassword(pw, q.id.user.Username); err != nil {
		again(http.StatusBadRequest, strings.TrimPrefix(err.Error(), "password policy: "))
		return
	}
	hash, err := auth.Hash(pw, s.params)
	if err != nil {
		q.serverError("hash password", err)
		return
	}
	u := q.id.user
	err = q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		if err := auth.ChangePassword(tx, u.ID, hash); err != nil {
			return err
		}
		if u.Role == auth.RoleAdmin {
			_, err := adminlog.Append(tx, s.now(), u.ID, "account.password.change", "user", u.ID, "password")
			return err
		}
		return nil
	})
	if err != nil {
		q.serverError("change password", err)
		return
	}
	s.sessions.RevokeUserExcept(u.ID, q.id.sess.ID)
	q.setFlash("pw")
	q.redirect("/account")
}

// ---- step-up ----

// stepUpPaths are the only places a step-up may return to, so the "next" field can never be an
// open redirect. The first is the default for an administrator, the second for everyone else.
var stepUpPaths = map[string]bool{
	"/admin": true, "/admin/log": true, "/admin/settings": true, "/admin/backup": true, "/admin/users": true, "/admin/deleted": true,
	"/uploads": true, "/uploads/deleted": true, "/account": true, "/spaces": true, "/admin/spaces": true, "/export": true, "/account/twostep": true,
}

func (q *rq) stepUpTarget(s string) string {
	if stepUpPaths[s] && (q.id.user.Role == auth.RoleAdmin || !strings.HasPrefix(s, "/admin")) {
		return s
	}
	if q.id.user.Role == auth.RoleAdmin {
		return "/admin"
	}
	return "/uploads"
}

func (q *rq) stepUpActive() string {
	if q.id.user.Role == auth.RoleAdmin {
		return "admin"
	}
	return "uploads"
}

func (s *Server) getStepUp(q *rq) {
	q.render(http.StatusOK, "stepup", &page{Title: "Confirm it is you", Active: q.stepUpActive(), Next: q.stepUpTarget(q.r.URL.Query().Get("next"))})
}

func (s *Server) postStepUp(q *rq) {
	next := q.stepUpTarget(q.r.PostForm.Get("next"))
	if !s.stepLim.Allow(q.ip() + "|stepup") {
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a little and try again.", next)
		return
	}
	if ok, _ := auth.Verify(q.r.PostForm.Get("password"), q.id.user.PWHash, s.params); !ok {
		q.render(http.StatusUnauthorized, "stepup", &page{Title: "Confirm it is you", Active: q.stepUpActive(), Next: next, Error: "That password is not right."})
		return
	}
	if err := q.audit("session.stepup", "user", q.id.user.ID, "", nil); err != nil {
		q.serverError("log step-up", err)
		return
	}
	s.sessions.StepUp(q.id.sess.ID)
	q.redirect(next)
}

// needStepUp redirects to the password prompt when the session has not re-authenticated lately.
func (q *rq) needStepUp(next string) bool {
	if q.id.sess.SteppedUp(q.s.now()) {
		return false
	}
	q.redirect("/stepup?next=" + next)
	return true
}

// ---- admin console ----

func (s *Server) getAdmin(q *rq) {
	p := &page{Title: "Admin", Active: "admin"}
	p.LastBackup, p.LastDrill = s.lastTimes(q)
	err := q.st.Update(q.r.Context(), false, func(tx *sql.Tx) error {
		if _, err := adminlog.Append(tx, s.now(), q.id.user.ID, "admin.console.view", "console", "home", ""); err != nil {
			return err
		}
		users, err := auth.Count(tx)
		if err != nil {
			return err
		}
		var entries int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM admin_log`).Scan(&entries); err != nil {
			return err
		}
		p.Stats.Users, p.Stats.LogEntries = users, entries
		return nil
	})
	if err != nil {
		q.serverError("admin console", err)
		return
	}
	if _, w, herr := s.health(q); herr == nil {
		p.HealthWarns = w
	}
	p.QRText = q.r.Host
	p.QR = qrFor(q.joinBase()+"/", "QR code for the address of this server")
	p.Stats.Sealed = "never"
	if t := q.st.LastSealed(); !t.IsZero() {
		p.Stats.Sealed = t.UTC().Format("15:04:05") + " UTC"
	}
	if v, err := q.st.SchemaVersionInDB(); err == nil {
		p.Stats.Schema = strconv.Itoa(v)
	}
	q.render(http.StatusOK, "admin", p)
}

const logPageSize = 50

func (s *Server) logPage(q *rq, p *page, page int, action string, in logIn) bool {
	page = min(page, maxPage)
	if page < 1 {
		page = 1
	}
	var entries []adminlog.Entry
	names := map[string]string{}
	var total int
	err := q.st.Update(q.r.Context(), false, func(tx *sql.Tx) error {
		if action != "" {
			if _, err := adminlog.Append(tx, s.now(), q.id.user.ID, action, "admin_log", "all", map[bool]string{true: "filtered", false: ""}[in.any()]); err != nil {
				return err
			}
		}
		var err error
		flt, problem := resolveLogFilter(tx, in)
		p.FilterProblem = problem
		if entries, err = adminlog.Find(tx, flt, logPageSize, (page-1)*logPageSize); err != nil {
			return err
		}
		if total, err = adminlog.Count(tx, flt); err != nil {
			return err
		}
		var all int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM admin_log`).Scan(&all); err != nil {
			return err
		}
		p.Stats.LogEntries = all
		rows, err := tx.Query(`SELECT id, username FROM user`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			names[id] = name
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		q.serverError("read admin log", err)
		return false
	}
	for _, e := range entries {
		who := e.Actor
		if n, ok := names[e.Actor]; ok {
			who = n
		}
		p.Entries = append(p.Entries, logRow{Seq: e.Seq, Time: e.Time.Format(timeFmt), Who: who, Action: e.Action,
			Target: strings.Trim(e.TargetType+" "+e.TargetID, " "), Fields: e.Fields})
	}
	p.Filter, p.FilterQuery, p.Filtered = in, template.URL(in.query()), in.any()
	if page > 1 {
		p.Newer = page - 1
	}
	if total > page*logPageSize {
		p.Older = page + 1
	}
	if v, ok, err := q.st.GetMeta("adminlog.head"); err == nil && ok {
		if i := strings.IndexByte(v, ':'); i >= 0 {
			p.Head = v[i+1:]
		}
	}
	return true
}

func (s *Server) getAdminLog(q *rq) {
	n, _ := strconv.Atoi(q.r.URL.Query().Get("page"))
	p := &page{Title: "Admin log", Active: "admin", SteppedUp: q.id.sess.SteppedUp(s.now())}
	if s.logPage(q, p, n, "adminlog.view", logInOf(q.r.URL.Query())) {
		q.render(http.StatusOK, "adminlog", p)
	}
}

func (s *Server) postVerifyLog(q *rq) {
	p := &page{Title: "Admin log", Active: "admin"}
	var res adminlog.Result
	var verr error
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		res, verr = adminlog.Verify(tx)
		outcome := "ok"
		if verr != nil {
			outcome = "failed"
		}
		_, err := adminlog.Append(tx, s.now(), q.id.user.ID, "adminlog.verify", "admin_log", "all", outcome)
		return err
	})
	if err != nil {
		q.serverError("verify admin log", err)
		return
	}
	status := http.StatusOK
	if verr != nil {
		s.log.Error("admin log verification FAILED", "reason", verr.Error())
		p.Error = "The chain does NOT verify: " + verr.Error() + ". Treat the log as tampered with."
		status = http.StatusConflict
	} else {
		p.Flash = fmt.Sprintf("The chain verifies: %d entries, each matching the one before.", res.Entries)
	}
	if s.logPage(q, p, 1, "", logIn{}) {
		q.render(status, "adminlog", p)
	}
}

// ---- network and security settings ----

func (s *Server) settingsPage(q *rq) *page {
	nm := s.net.Load()
	v := nm.View()
	p := &page{Title: "Network and security", Active: "admin",
		SteppedUp: q.id.sess.SteppedUp(s.now()), Pending: !v.PendingUntil.IsZero(),
		AllowedText: strings.ReplaceAll(netsec.FormatNets(v.AllowedNets), ",", "\n"),
		ProxiesText: strings.ReplaceAll(netsec.FormatNets(v.TrustedProxies), ",", "\n"),
		YourIP:      q.c.IP.String(), ProxyMode: v.ProxyMode, RequireTLS: v.RequireTLS,
		EnvNets: v.AllowedSrc == netsec.FromEnv, EnvProxy: v.ProxySrc == netsec.FromEnv, EnvTLS: v.TLSSrc == netsec.FromEnv,
		AllowedSource: sourceNote(v.AllowedSrc), ProxySource: sourceNote(v.ProxySrc), TLSSource: sourceNote(v.TLSSrc)}
	p.ActivityOn = s.activityOn(q)
	p.ZoneFallback = s.envZone
	p.DefaultTZ = share.DefaultZoneName()
	if q.st != nil {
		if v, ok, err := q.st.GetMeta(zoneKey); err == nil && ok {
			p.ZoneSaved = v
		}
	}
	if p.Pending {
		left := v.PendingUntil.Sub(s.now()).Round(time.Second)
		if left < 0 {
			left = 0
		}
		p.PendingLeft = fmt.Sprintf("%d seconds", int(left.Seconds()))
	}
	return p
}

func sourceNote(s netsec.Source) string {
	switch s {
	case netsec.FromEnv:
		return "Set by environment: change it there."
	case netsec.FromConsole:
		return "Set in this console."
	}
	return "Using the default."
}

func (s *Server) getSettings(q *rq) {
	if err := q.audit("settings.view", "settings", "network", "", nil); err != nil {
		q.serverError("log settings view", err)
		return
	}
	q.render(http.StatusOK, "settings", s.settingsPage(q))
}

func (q *rq) settingsError(status int, msg string) {
	p := q.s.settingsPage(q)
	p.Error = msg
	q.render(status, "settings", p)
}

// putMeta persists settings values inside the caller's transaction.
func putMeta(tx *sql.Tx, kv map[string]string, keys ...string) error {
	for _, k := range keys {
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, kv[k]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) postNetworks(q *rq) {
	if q.needStepUp("/admin/settings") {
		return
	}
	nets, err := netsec.ParseNets(q.r.PostForm.Get("networks"))
	if err != nil {
		q.settingsError(http.StatusBadRequest, err.Error())
		return
	}
	// Refuse the obvious self-lockout. The 60-second revert is the safety net, not the plan.
	covered := false
	for _, n := range nets {
		if n.Contains(q.c.IP) {
			covered = true
		}
	}
	if !covered && !q.c.IP.IsLoopback() {
		q.settingsError(http.StatusBadRequest, "That list does not include your own address ("+q.c.IP.String()+"), so you would lose access. Add it, then try again.")
		return
	}
	if _, err := s.net.Load().ProposeNetworks(nets); err != nil {
		q.settingsError(http.StatusBadRequest, errText(err))
		return
	}
	if err := q.audit("settings.networks.propose", "settings", "networks", "allowed_networks", nil); err != nil {
		s.net.Load().CancelNetworks()
		q.serverError("log network change", err)
		return
	}
	q.setFlash("net-prop")
	q.redirect("/admin/settings")
}

func (s *Server) postNetworksConfirm(q *rq) {
	if q.needStepUp("/admin/settings") {
		return
	}
	cur, err := s.net.Load().ConfirmNetworks()
	if err != nil {
		q.settingsError(http.StatusBadRequest, errText(err))
		return
	}
	m := cur.Marshal()
	if err := q.audit("settings.networks.confirm", "settings", "networks", "allowed_networks", func(tx *sql.Tx) error {
		return putMeta(tx, m, "setting.allowed_networks")
	}); err != nil {
		q.serverError("save network list", err)
		return
	}
	q.setFlash("net-ok")
	q.redirect("/admin/settings")
}

func (s *Server) postNetworksCancel(q *rq) {
	if q.needStepUp("/admin/settings") {
		return
	}
	s.net.Load().CancelNetworks()
	if err := q.audit("settings.networks.cancel", "settings", "networks", "allowed_networks", nil); err != nil {
		q.serverError("log network cancel", err)
		return
	}
	q.setFlash("net-undo")
	q.redirect("/admin/settings")
}

func (s *Server) postProxy(q *rq) {
	if q.needStepUp("/admin/settings") {
		return
	}
	proxies, err := netsec.ParseNets(q.r.PostForm.Get("proxies"))
	if err != nil {
		q.settingsError(http.StatusBadRequest, err.Error())
		return
	}
	cur, err := s.net.Load().SetProxy(q.r.PostForm.Get("proxy") == "1", proxies)
	if err != nil {
		q.settingsError(http.StatusBadRequest, errText(err))
		return
	}
	m := cur.Marshal()
	if err := q.audit("settings.proxy.change", "settings", "proxy", "proxy_mode,trusted_proxies", func(tx *sql.Tx) error {
		return putMeta(tx, m, "setting.proxy_mode", "setting.trusted_proxies")
	}); err != nil {
		q.serverError("save proxy settings", err)
		return
	}
	q.setFlash("proxy")
	q.redirect("/admin/settings")
}

func (s *Server) postTLS(q *rq) {
	if q.needStepUp("/admin/settings") {
		return
	}
	cur, err := s.net.Load().SetRequireTLS(q.r.PostForm.Get("tls") == "1", q.c)
	if err != nil {
		q.settingsError(http.StatusBadRequest, errText(err))
		return
	}
	m := cur.Marshal()
	if err := q.audit("settings.tls.change", "settings", "tls", "require_tls", func(tx *sql.Tx) error {
		return putMeta(tx, m, "setting.require_tls")
	}); err != nil {
		q.serverError("save HTTPS setting", err)
		return
	}
	q.setFlash("tls")
	q.redirect("/admin/settings")
}

// zoneKey is where the console's default time zone is kept. It beats the environment's ALCOVE_TZ,
// so a rebuilt container (which can reset its variables) cannot change what people see.
const zoneKey = "setting.default_tz"

func (s *Server) postZone(q *rq) {
	if q.needStepUp("/admin/settings") {
		return
	}
	name := strings.TrimSpace(q.r.PostForm.Get("tz"))
	if err := share.ValidZone(name); err != nil {
		q.settingsError(http.StatusBadRequest, "That is not a time zone name. Use one like America/New_York or Europe/Berlin.")
		return
	}
	if err := q.audit("settings.timezone.change", "settings", "timezone", "default_tz", func(tx *sql.Tx) error {
		if name == "" {
			_, err := tx.Exec(`DELETE FROM meta WHERE key = ?`, zoneKey)
			return err
		}
		return putMeta(tx, map[string]string{zoneKey: name}, zoneKey)
	}); err != nil {
		q.serverError("save default time zone", err)
		return
	}
	apply := name
	if apply == "" {
		apply = s.envZone
	}
	_ = share.SetDefaultZone(apply)
	q.setFlash("zone")
	q.redirect("/admin/settings")
}

func errText(err error) string {
	if errors.Is(err, netsec.ErrEnvLocked) {
		return "That value is set by the server's environment, so it cannot be changed here."
	}
	s := err.Error()
	return strings.ToUpper(s[:1]) + s[1:] + "."
}
