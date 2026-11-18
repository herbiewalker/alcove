package auth

import (
	"sync"
	"time"
)

// Two-step sign-in (D-49). A correct password does not start a session for a person who has the
// second step on. It starts a pending sign-in: a random single-use token, held in memory only, that
// can be exchanged for a session by presenting the second factor. Nothing here is written to disk.
const (
	// PendingTTL is how long a person has to type the code after the password.
	PendingTTL = 5 * time.Minute
	// CodeFailLimit wrong codes inside CodeFailWindow lock the second step for CodeLockFor.
	CodeFailLimit  = 5
	CodeFailWindow = 10 * time.Minute
	CodeLockFor    = 10 * time.Minute
)

type pending struct {
	userID  string
	expires time.Time
}

type failures struct {
	n           int
	windowStart time.Time
	lockedUntil time.Time
}

// Pendings holds pending sign-ins and the per-account count of wrong second-step codes. The count is
// only reached by someone who already has the password, so unlike a per-account password lockout it
// cannot be used from outside to lock the real owner out (D-27, D-49).
type Pendings struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]pending
	bad map[string]*failures
}

// NewPendings makes the table. now may be nil (time.Now).
func NewPendings(now func() time.Time) *Pendings {
	if now == nil {
		now = time.Now
	}
	return &Pendings{now: now, m: map[string]pending{}, bad: map[string]*failures{}}
}

// Start makes a pending sign-in for a user whose password was just checked and returns its token.
func (p *Pendings) Start(userID string) (string, error) {
	tok, err := RandomToken()
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.m) >= maxKeys {
		p.gcLocked()
	}
	if len(p.m) >= maxKeys { // still full of live ones: drop an arbitrary one rather than grow
		for k := range p.m {
			delete(p.m, k)
			break
		}
	}
	p.m[tok] = pending{userID: userID, expires: p.now().Add(PendingTTL)}
	return tok, nil
}

// Peek returns whose pending sign-in a token is, without using it up.
func (p *Pendings) Peek(tok string) (userID string, ok bool) {
	if tok == "" {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, found := p.m[tok]
	if !found || !p.now().Before(e.expires) {
		delete(p.m, tok)
		return "", false
	}
	return e.userID, true
}

// Finish uses a token up. It reports whether the token was live, so two requests cannot both win.
func (p *Pendings) Finish(tok string) (userID string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, found := p.m[tok]
	delete(p.m, tok)
	if !found || !p.now().Before(e.expires) {
		return "", false
	}
	return e.userID, true
}

// Locked reports whether the second step is refusing this account right now.
func (p *Pendings) Locked(userID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.bad[userID]
	return f != nil && p.now().Before(f.lockedUntil)
}

// Fail counts a wrong code. justLocked is true on the attempt that starts a lock, so the caller can
// write that one event to the admin log once.
func (p *Pendings) Fail(userID string) (justLocked bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	f := p.bad[userID]
	if f == nil {
		if len(p.bad) >= maxKeys {
			for k := range p.bad {
				delete(p.bad, k)
				break
			}
		}
		f = &failures{windowStart: now}
		p.bad[userID] = f
	}
	if now.Sub(f.windowStart) > CodeFailWindow {
		f.n, f.windowStart = 0, now
	}
	if now.Before(f.lockedUntil) {
		return false
	}
	if f.n++; f.n >= CodeFailLimit {
		f.lockedUntil = now.Add(CodeLockFor)
		f.n = 0
		return true
	}
	return false
}

// Clear forgets an account's wrong codes after a good sign-in.
func (p *Pendings) Clear(userID string) {
	p.mu.Lock()
	delete(p.bad, userID)
	p.mu.Unlock()
}

// DropUser forgets every pending sign-in of an account (it was disabled, deleted or reset).
func (p *Pendings) DropUser(userID string) {
	p.mu.Lock()
	for k, e := range p.m {
		if e.userID == userID {
			delete(p.m, k)
		}
	}
	p.mu.Unlock()
}

// GC drops expired entries.
func (p *Pendings) GC() {
	p.mu.Lock()
	p.gcLocked()
	p.mu.Unlock()
}

func (p *Pendings) gcLocked() {
	now := p.now()
	for k, e := range p.m {
		if !now.Before(e.expires) {
			delete(p.m, k)
		}
	}
	for k, f := range p.bad {
		if now.Sub(f.windowStart) > CodeFailWindow && !now.Before(f.lockedUntil) {
			delete(p.bad, k)
		}
	}
}
