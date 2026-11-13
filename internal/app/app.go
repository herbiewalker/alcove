// Package app is Alcove's startup sequence: validate the environment, load the master key, open
// the store. It is a separate package so the refuse-to-start rules are testable.
//
// Hard constraints enforced here (CLAUDE.md 4): the key file must live OUTSIDE the data directory,
// the live database must sit on tmpfs, and the server starts unattended (no human unlock) when the
// key file is a plain key. A passphrase-wrapped key file starts the server LOCKED; the unlock
// page that calls Unlock arrives with the web UI (M2b).
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/herbiewalker/alcove/internal/config"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

// State is whether the data can be read.
type State string

const (
	StateLocked   State = "locked"
	StateUnlocked State = "unlocked"
)

// Errors with messages meant to be read by an operator at a terminal.
var (
	ErrNoKeyFile = errors.New("no key file found: create one with `alcove keygen -out <path>` (keep a second copy somewhere safe, then check it with `alcove keycheck`)")
	ErrNotLocked = errors.New("app: not locked")
)

// Options tune Start for tests.
type Options struct {
	Logger *slog.Logger
	// AllowWeakKeyParams accepts weak Argon2id parameters in a wrapped key file (tests only).
	AllowWeakKeyParams bool
}

// App is a started instance.
type App struct {
	cfg  config.Config
	opt  Options
	log  *slog.Logger
	mu   sync.RWMutex
	st   *store.Store
	wrap bool // the key file is passphrase-wrapped
}

// Start checks the environment and opens the store. It returns an error, and starts nothing,
// whenever a hard constraint would be violated.
func Start(cfg config.Config, opt Options) (*App, error) {
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	a := &App{cfg: cfg, opt: opt, log: opt.Logger}

	// Constraint 4: the key lives apart from the data. Checked before the key is even read.
	if err := keyfile.CheckOutside(cfg.KeyFile, cfg.DataDir); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w (looked for %s)", ErrNoKeyFile, cfg.KeyFile)
		}
		return nil, fmt.Errorf("read key file: %w", err)
	}
	a.wrap = keyfile.IsWrapped(raw)
	if a.wrap {
		for i := range raw {
			raw[i] = 0
		}
		a.log.Info("key file is passphrase-wrapped: starting locked")
		return a, nil
	}
	key, err := keyfile.Parse(raw, "", keyfile.ParseOptions{})
	for i := range raw {
		raw[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("key file %s: %w", cfg.KeyFile, err)
	}
	if err := a.open(key); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *App) open(key keyfile.Key) error {
	defer key.Zero()
	st, err := store.Open(store.Config{
		DataDir: a.cfg.DataDir, TmpDir: a.cfg.TmpDir, Key: key,
		AllowNonTmpfsTmp: a.cfg.AllowNonTmpfsTmp, SealEvery: a.cfg.SealEvery, Logger: a.log,
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.st = st
	a.mu.Unlock()
	a.log.Info("store open", "state", string(StateUnlocked))
	return nil
}

// Unlock opens a passphrase-wrapped store. It is a no-op error for an already unlocked app.
func (a *App) Unlock(passphrase string) error {
	a.mu.RLock()
	unlocked := a.st != nil
	a.mu.RUnlock()
	if unlocked {
		return ErrNotLocked
	}
	key, err := keyfile.Load(a.cfg.KeyFile, passphrase, keyfile.ParseOptions{AllowWeakParams: a.opt.AllowWeakKeyParams})
	if err != nil {
		return err
	}
	return a.open(key)
}

// State reports whether the data is readable.
func (a *App) State() State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.st == nil {
		return StateLocked
	}
	return StateUnlocked
}

// Store returns the open store, or nil while locked.
func (a *App) Store() *store.Store {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.st
}

// Close seals and closes the store, wiping the key. Safe to call more than once.
func (a *App) Close() error {
	a.mu.Lock()
	st := a.st
	a.st = nil
	a.mu.Unlock()
	if st == nil {
		return nil
	}
	return st.Close()
}
