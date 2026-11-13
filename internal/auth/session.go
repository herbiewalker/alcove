package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// Session lifetimes. Short on purpose (D-23): the server is on a home network and a phone browser
// is the main client, so a lost or shared phone should not stay signed in for days.
const (
	IdleTTL = 30 * time.Minute // rolling; bumped on each request
	MaxTTL  = 12 * time.Hour   // absolute, from issue
	// StepUpTTL is how long a fresh password re-entry stays valid for sensitive admin actions.
	StepUpTTL = 5 * time.Minute
)

// Session is one issued cookie's server-side record. Sessions live in process memory only: a
// restart signs everyone out, which is the safe direction.
type Session struct {
	ID         string
	UserID     string
	CSRF       string // per-session token every form carries
	RemoteIP   string
	CreatedAt  time.Time
	LastSeen   time.Time
	StepUpTill time.Time // zero unless the user re-entered their password recently
}

// SteppedUp reports whether the session re-authenticated within StepUpTTL.
func (s Session) SteppedUp(now time.Time) bool { return now.Before(s.StepUpTill) }

// Errors from the session manager.
var (
	ErrNoSession      = errors.New("auth: no such session")
	ErrSessionExpired = errors.New("auth: session expired")
)

// Sessions is the in-memory session table. Safe for concurrent use.
type Sessions struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*Session
}

// NewSessions makes a table. now may be nil (time.Now).
func NewSessions(now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{now: now, m: map[string]*Session{}}
}

// RandomToken returns 32 random bytes, base64url without padding.
func RandomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Issue creates a session for a user. remoteIP is kept for the account page, never bound into the
// cookie (a phone roaming between networks must not be logged out).
func (s *Sessions) Issue(userID, remoteIP string) (Session, error) {
	id, err := RandomToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := RandomToken()
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	sess := &Session{ID: id, UserID: userID, CSRF: csrf, RemoteIP: remoteIP, CreatedAt: now, LastSeen: now}
	s.mu.Lock()
	s.m[id] = sess
	s.mu.Unlock()
	return *sess, nil
}

// Get returns a copy of a live session and bumps its idle timer.
func (s *Sessions) Get(id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[id]
	if !ok {
		return Session{}, ErrNoSession
	}
	now := s.now()
	if now.Sub(sess.LastSeen) > IdleTTL || now.Sub(sess.CreatedAt) > MaxTTL {
		delete(s.m, id)
		return Session{}, ErrSessionExpired
	}
	sess.LastSeen = now
	return *sess, nil
}

// StepUp marks the session as freshly re-authenticated.
func (s *Sessions) StepUp(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.m[id]; ok {
		sess.StepUpTill = s.now().Add(StepUpTTL)
	}
}

// Delete ends one session (sign out).
func (s *Sessions) Delete(id string) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

// RevokeUser ends every session of one account (disable, password change).
func (s *Sessions) RevokeUser(userID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, sess := range s.m {
		if sess.UserID == userID {
			delete(s.m, id)
			n++
		}
	}
	return n
}

// RevokeUserExcept ends the account's other sessions, keeping one (password change).
func (s *Sessions) RevokeUserExcept(userID, keep string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, sess := range s.m {
		if sess.UserID == userID && id != keep {
			delete(s.m, id)
			n++
		}
	}
	return n
}

// GC drops expired sessions and returns how many.
func (s *Sessions) GC() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now, n := s.now(), 0
	for id, sess := range s.m {
		if now.Sub(sess.LastSeen) > IdleTTL || now.Sub(sess.CreatedAt) > MaxTTL {
			delete(s.m, id)
			n++
		}
	}
	return n
}

// Count is for tests and the health page.
func (s *Sessions) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
