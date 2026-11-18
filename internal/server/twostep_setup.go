package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/herbiewalker/alcove/internal/totp"
)

// Setting up, using and turning off two-step sign-in (D-49, piece 3). Administrators only.
//
// Turning it on proves it first: Alcove makes a secret, keeps it only in memory for ten minutes, and
// saves it only after the person types a correct current code from their app. Then ten recovery codes
// are shown once. Turning it off and making new recovery codes both need a current code.

const setupTTL = 10 * time.Minute

type setupEntry struct {
	secret  []byte
	expires time.Time
}

// setupStore holds secrets that are waiting for their first code, keyed by session. Memory only.
type setupStore struct {
	mu sync.Mutex
	m  map[string]setupEntry
}

func (st *setupStore) put(session string, secret []byte, now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.m == nil || len(st.m) > 64 {
		st.m = map[string]setupEntry{}
	}
	st.m[session] = setupEntry{secret, now.Add(setupTTL)}
}

func (st *setupStore) get(session string, now time.Time) ([]byte, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e, ok := st.m[session]
	if !ok || !now.Before(e.expires) {
		delete(st.m, session)
		return nil, false
	}
	return e.secret, true
}

func (st *setupStore) drop(session string) {
	st.mu.Lock()
	delete(st.m, session)
	st.mu.Unlock()
}

const twoStepPath = "/account/twostep"

var errFactor = errors.New("could not check the code")

// renderTwoStep draws the page in whichever state the person is in.
func (s *Server) renderTwoStep(q *rq, status int, msg string) {
	uid := q.id.user.ID
	p := &page{Title: "Two-step sign-in", Active: "account", Error: msg}
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (e error) {
		if p.TwoOn, e = totp.Enabled(tx, uid); e != nil || !p.TwoOn {
			return e
		}
		if p.RecLeft, e = totp.RecoveryLeft(tx, uid); e != nil {
			return e
		}
		var at int64
		if e = tx.QueryRow(`SELECT enabled_at FROM user_totp WHERE user_id = ?`, uid).Scan(&at); e == nil {
			p.TwoSince = time.Unix(at, 0).UTC().Format("2006-01-02")
		}
		return e
	})
	if err != nil {
		q.serverError("two-step page", err)
		return
	}
	if !p.TwoOn {
		if secret, ok := s.setups.get(q.id.sess.ID, s.now()); ok {
			p.Setup = true
			p.QRText = totp.Base32(secret)
			p.QR = qrFor(totp.URI("Alcove", q.id.user.Username, secret), "QR code for your authenticator app")
		}
	}
	q.render(status, "twostep", p)
}

func (s *Server) getTwoStep(q *rq) { s.renderTwoStep(q, http.StatusOK, "") }

func (s *Server) postTwoStepStart(q *rq) {
	if q.needStepUp(twoStepPath) {
		return
	}
	var on bool
	if err := q.st.View(q.r.Context(), func(tx *sql.Tx) (e error) { on, e = totp.Enabled(tx, q.id.user.ID); return }); err != nil {
		q.serverError("two-step start", err)
		return
	}
	if on {
		s.renderTwoStep(q, http.StatusConflict, "Two-step sign-in is already on.")
		return
	}
	secret, err := totp.NewSecret()
	if err != nil {
		q.serverError("two-step secret", err)
		return
	}
	s.setups.put(q.id.sess.ID, secret, s.now())
	s.renderTwoStep(q, http.StatusOK, "")
}

// typedCode reads the code field, capped.
func typedCode(q *rq) string {
	c := strings.TrimSpace(q.r.PostForm.Get("code"))
	if len(c) > 64 {
		c = c[:64]
	}
	return c
}

// codeLock writes the one admin-log line for the start of a lock.
func (s *Server) codeLock(q *rq, uid string) {
	if s.pendings.Fail(uid) {
		if err := q.audit("session.2fa.locked", "user", uid, "", nil); err != nil {
			s.log.Error("log second-step lock", "err", err)
		}
	}
}

func (s *Server) postTwoStepEnable(q *rq) {
	if q.needStepUp(twoStepPath) {
		return
	}
	uid := q.id.user.ID
	secret, ok := s.setups.get(q.id.sess.ID, s.now())
	if !ok {
		s.renderTwoStep(q, http.StatusConflict, "That setup timed out. Start again.")
		return
	}
	if !s.stepLim.Allow(q.ip()+"|2fa") || s.pendings.Locked(uid) {
		s.renderTwoStep(q, http.StatusTooManyRequests, "Too many tries. Wait a few minutes.")
		return
	}
	step, valid := totp.Verify(secret, typedCode(q), s.now(), 0)
	if !valid {
		s.codeLock(q, uid)
		s.renderTwoStep(q, http.StatusUnauthorized, "That code is not right. If it keeps failing, check that the phone's clock is automatic, then type the next code.")
		return
	}
	codes, hashes, err := totp.NewRecoveryCodes()
	if err != nil {
		q.serverError("recovery codes", err)
		return
	}
	now := s.now()
	if err := q.audit("totp.enable", "user", uid, "", func(tx *sqlTx) error {
		return totp.Enable(tx, now, uid, secret, step, hashes)
	}); err != nil {
		q.serverError("turn on two-step", err)
		return
	}
	s.setups.drop(q.id.sess.ID)
	s.pendings.Clear(uid)
	s.sessions.RevokeUserExcept(uid, q.id.sess.ID)
	q.render(http.StatusOK, "recoverycodes", &page{Title: "Your recovery codes", Active: "account", Codes: codes, Back: twoStepPath,
		Message: "Two-step sign-in is on. Other devices were signed out."})
}

// requireFactor checks the current code (or a recovery code) typed on a page that changes the second
// step. It returns 0 when the code was good, or the status and the words to show.
func (s *Server) requireFactor(q *rq) (int, string) {
	uid := q.id.user.ID
	if !s.stepLim.Allow(q.ip() + "|2fa") {
		return http.StatusTooManyRequests, "Too many tries. Wait a few minutes."
	}
	if s.pendings.Locked(uid) {
		return http.StatusTooManyRequests, "Too many wrong codes. Wait about ten minutes."
	}
	var passed bool
	err := q.st.Update(q.r.Context(), false, func(tx *sql.Tx) (e error) {
		_, passed, e = checkFactor(tx, s.now(), uid, typedCode(q))
		return e
	})
	if err != nil {
		q.s.log.Error("check code", "err", err.Error())
		return http.StatusInternalServerError, ""
	}
	if !passed {
		s.codeLock(q, uid)
		return http.StatusUnauthorized, "That code is not right."
	}
	s.pendings.Clear(uid)
	return 0, ""
}

func (s *Server) postTwoStepRecovery(q *rq) {
	if q.needStepUp(twoStepPath) {
		return
	}
	if st, msg := s.requireFactor(q); st != 0 {
		if st == http.StatusInternalServerError {
			q.serverError("new recovery codes", errFactor)
			return
		}
		s.renderTwoStep(q, st, msg)
		return
	}
	uid := q.id.user.ID
	codes, hashes, err := totp.NewRecoveryCodes()
	if err != nil {
		q.serverError("recovery codes", err)
		return
	}
	if err := q.audit("totp.recovery.new", "user", uid, "", func(tx *sqlTx) error { return totp.SetRecovery(tx, uid, hashes) }); err != nil {
		q.serverError("new recovery codes", err)
		return
	}
	q.render(http.StatusOK, "recoverycodes", &page{Title: "Your new recovery codes", Active: "account", Codes: codes, Back: twoStepPath,
		Message: "The old recovery codes no longer work."})
}

func (s *Server) postTwoStepDisable(q *rq) {
	if q.needStepUp(twoStepPath) {
		return
	}
	if st, msg := s.requireFactor(q); st != 0 {
		if st == http.StatusInternalServerError {
			q.serverError("turn off two-step", errFactor)
			return
		}
		s.renderTwoStep(q, st, msg)
		return
	}
	uid := q.id.user.ID
	if err := q.audit("totp.disable", "user", uid, "", func(tx *sqlTx) error { return totp.Disable(tx, uid) }); err != nil {
		q.serverError("turn off two-step", err)
		return
	}
	s.setups.drop(q.id.sess.ID)
	s.pendings.DropUser(uid)
	s.sessions.RevokeUserExcept(uid, q.id.sess.ID)
	q.setFlash("2fa-off")
	q.redirect(twoStepPath)
}
