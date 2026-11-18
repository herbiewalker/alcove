package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/totp"
)

// The second step of sign-in (D-49). A person who turned two-step sign-in on does not get a session
// from a correct password alone: they get a pending sign-in (an in-memory, single-use token in an
// HttpOnly cookie that only /login/code accepts) and a session only once a current code or an unused
// recovery code is presented. No code, secret or recovery code is ever written to a log.
const pendingCookie = "alcove_2fa"

func (q *rq) setPending(tok string) {
	http.SetCookie(q.w, &http.Cookie{Name: pendingCookie, Value: tok, Path: "/login", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: q.c.Secure, MaxAge: int(auth.PendingTTL.Seconds())})
}

func (q *rq) clearPending() {
	http.SetCookie(q.w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/login", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: q.c.Secure, MaxAge: -1})
}

func (q *rq) pendingToken() string {
	if c, err := q.r.Cookie(pendingCookie); err == nil {
		return c.Value
	}
	return ""
}

// startSecondStep begins a pending sign-in for a person whose password was just checked.
func (s *Server) startSecondStep(q *rq, u auth.User) {
	tok, err := s.pendings.Start(u.ID)
	if err != nil {
		q.serverError("start second step", err)
		return
	}
	q.setPending(tok)
	q.redirect("/login/code")
}

func (s *Server) getLoginCode(q *rq) {
	if q.id != nil {
		q.redirect("/")
		return
	}
	if q.tlsGate() {
		return
	}
	if _, ok := s.pendings.Peek(q.pendingToken()); !ok {
		q.redirect("/login")
		return
	}
	q.render(http.StatusOK, "logincode", &page{Title: "Second step"})
}

func (s *Server) postLoginCode(q *rq) {
	if q.tlsGate() {
		return
	}
	tok := q.pendingToken()
	uid, ok := s.pendings.Peek(tok)
	if !ok {
		q.redirect("/login")
		return
	}
	if !s.loginLim.Allow(q.ip()) {
		s.log.Warn("sign-in rate limited", "ip", q.ip())
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a little and try again.", "/login")
		return
	}
	if s.pendings.Locked(uid) {
		q.render(http.StatusTooManyRequests, "logincode", &page{Title: "Second step",
			Error: "Too many wrong codes. Wait about ten minutes and sign in again."})
		return
	}
	typed := q.r.PostForm.Get("code")
	if len(typed) > 64 {
		typed = typed[:64]
	}

	var u auth.User
	var good bool
	var how string
	now := s.now()
	err := q.st.Update(q.r.Context(), false, func(tx *sql.Tx) error {
		var e error
		if u, e = auth.ByID(tx, uid); e != nil {
			if errors.Is(e, auth.ErrNoUser) {
				return nil
			}
			return e
		}
		if u.Status != auth.StatusActive || u.Deleted() {
			return nil
		}
		secret, last, on, e := totp.Secret(tx, uid)
		if e != nil || !on {
			return e
		}
		if totp.LooksLikeRecovery(typed) {
			spent, left, e := totp.UseRecovery(tx, now, uid, typed)
			if e != nil || !spent {
				return e
			}
			how = "2fa:recovery,left:" + strconv.Itoa(left)
		} else {
			step, valid := totp.Verify(secret, typed, now, last)
			if !valid {
				return nil
			}
			if e := totp.AcceptStep(tx, uid, step); e != nil {
				if errors.Is(e, totp.ErrReplay) {
					return nil
				}
				return e
			}
			how = "2fa:totp"
		}
		if e := auth.TouchLogin(tx, uid, now); e != nil {
			return e
		}
		good = true
		if u.Role == auth.RoleAdmin {
			_, e = adminlog.Append(tx, now, u.ID, "session.login", "user", u.ID, how)
			return e
		}
		return nil
	})
	if err != nil {
		q.serverError("second step", err)
		return
	}
	if !good {
		s.log.Warn("second step failed", "ip", q.ip())
		if s.pendings.Fail(uid) {
			if err := q.audit("session.2fa.locked", "user", uid, "", nil); err != nil {
				s.log.Error("log second-step lock", "err", err)
			}
		}
		q.render(http.StatusUnauthorized, "logincode", &page{Title: "Second step", Error: "That code is not right."})
		return
	}
	if _, live := s.pendings.Finish(tok); !live {
		q.redirect("/login")
		return
	}
	s.pendings.Clear(uid)
	q.clearPending()
	s.loginLim.Reset(q.ip())
	s.signIn(q, u)
}
