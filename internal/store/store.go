// Package store is Alcove's structured storage: one SQLite database that lives on tmpfs and is
// sealed to the data directory as an AES-256-GCM blob (ADR D-16).
//
// The live database is plaintext, but only in RAM (tmpfs). What reaches the data volume is the
// sealed blob, written on a short timer whenever something changed, immediately after an important
// write, and on shutdown, with one previous generation kept. A crash can lose at most the writes
// since the last seal, which is why uploads and edits ask for a durable (sealed) commit.
//
// Hard constraint 4: decrypted data exists only in memory or tmpfs. `scripts/guards.sh no-plaintext`
// runs a test that stores sentinel data and scans the data directory for it.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/seal"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, static binary (ADR D-13)
)

// Config configures Open.
type Config struct {
	// DataDir holds the sealed database, the manifest and the blobs. It is ciphertext only.
	DataDir string
	// TmpDir is where the live database sits. It must be a tmpfs mount on Linux.
	TmpDir string
	// Key is the master key. Open takes a copy; the caller may zero its own.
	Key keyfile.Key
	// AllowNonTmpfsTmp lets TmpDir be an ordinary directory (a dev box or tests). Never in production.
	AllowNonTmpfsTmp bool
	// SealEvery is how often a changed database is sealed in the background (default 10s).
	SealEvery time.Duration
	Now       func() time.Time
	Logger    *slog.Logger
}

// Errors.
var (
	ErrTmpNotTmpfs     = errors.New("store: TmpDir is not on a tmpfs/ramfs mount, so decrypted data could reach disk (set ALCOVE_ALLOW_NONTMPFS_TMPDIR=true only on a dev box)")
	ErrTmpInsideData   = errors.New("store: TmpDir must not be inside DataDir")
	ErrManifestMissing = errors.New("store: a sealed database exists but manifest.json is missing; restore it from a backup of the data directory")
	ErrClosed          = errors.New("store: closed")
	ErrLocked          = errors.New("store: another Alcove process is using this data directory (stop the server first); nothing was changed")
)

// Store is an open, unsealed database.
type Store struct {
	cfg      Config
	key      [32]byte
	binding  []byte
	dbPath   string
	db       *sql.DB
	log      *slog.Logger
	now      func() time.Time
	dirty    atomic.Bool
	sealMu   sync.Mutex
	closed   atomic.Bool
	stop     context.CancelFunc
	wg       sync.WaitGroup
	lastSeal atomic.Int64 // unix nano of the last successful seal
	lock     *dirLock
}

// manifest.json sits in the data directory in the clear. It holds no secret: it names the store
// so a seal cannot be moved between stores unnoticed (it is the seal's AAD binding).
type manifest struct {
	StoreID string `json:"store_id"`
	Layout  int    `json:"layout"`
	Created string `json:"created"`
}

const (
	manifestName = "manifest.json"
	lockName     = ".lock"
)
const layoutVersion = 1

// Open unseals (or creates) the database and starts the background sealer.
func Open(cfg Config) (*Store, error) {
	if cfg.DataDir == "" || cfg.TmpDir == "" {
		return nil, errors.New("store: DataDir and TmpDir are required")
	}
	if cfg.Key.IsZero() {
		return nil, errors.New("store: a master key is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.SealEvery <= 0 {
		cfg.SealEvery = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if err := keyfileOutside(cfg.TmpDir, cfg.DataDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	// Lock first: a second process must not touch the sealed database or the live copy on tmpfs.
	lk, err := lockDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			lk.release()
		}
	}()
	if err := os.MkdirAll(cfg.TmpDir, 0o700); err != nil {
		return nil, err
	}
	if !cfg.AllowNonTmpfsTmp {
		ok, err := isTmpfsBacked(cfg.TmpDir)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrTmpNotTmpfs
		}
	}

	s := &Store{cfg: cfg, log: cfg.Logger, now: cfg.Now, dbPath: filepath.Join(cfg.TmpDir, "alcove.db")}
	copy(s.key[:], cfg.Key.Bytes())

	// A leftover live database from a crash is plaintext on tmpfs and older than the last seal:
	// discard it. The sealed copy is the source of truth.
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		_ = os.Remove(s.dbPath + suffix)
	}

	// A crash in the middle of a seal can leave the plaintext snapshot that serialize makes.
	sweepSnapshots(cfg.TmpDir)

	man, err := s.loadOrCreateManifest()
	if err != nil {
		return nil, err
	}
	s.binding = []byte(fmt.Sprintf("alcove-manifest|%s|%d", man.StoreID, man.Layout))

	if seal.Exists(cfg.DataDir) {
		plain, err := seal.ReadFile(cfg.DataDir, s.key[:], s.binding)
		if err != nil {
			s.wipeKey()
			return nil, fmt.Errorf("store: unseal: %w", err)
		}
		if err := os.WriteFile(s.dbPath, plain, 0o600); err != nil {
			s.wipeKey()
			return nil, err
		}
		for i := range plain {
			plain[i] = 0
		}
	}
	if err := s.openDB(); err != nil {
		s.wipeKey()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = s.db.Close()
		s.wipeKey()
		return nil, err
	}
	// A brand-new store is sealed at once, so "seal exists" is true from the first moment.
	if err := s.SealNow(true); err != nil {
		_ = s.db.Close()
		s.wipeKey()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	s.wg.Add(1)
	go s.sealLoop(ctx)
	s.lock = lk
	opened = true
	return s, nil
}

func keyfileOutside(tmp, data string) error {
	// Reuse the key file's path canonicalisation: TmpDir inside DataDir would put the live
	// plaintext database on the data volume.
	if err := keyfile.CheckOutside(tmp, data); err != nil {
		if errors.Is(err, keyfile.ErrInsideData) {
			return ErrTmpInsideData
		}
		return err
	}
	return nil
}

func (s *Store) loadOrCreateManifest() (manifest, error) {
	path := filepath.Join(s.cfg.DataDir, manifestName)
	raw, err := os.ReadFile(path)
	if err == nil {
		var m manifest
		if err := json.Unmarshal(raw, &m); err != nil || m.StoreID == "" {
			return manifest{}, fmt.Errorf("store: manifest.json is unreadable: %v", err)
		}
		return m, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return manifest{}, err
	}
	if seal.Exists(s.cfg.DataDir) {
		return manifest{}, ErrManifestMissing
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return manifest{}, err
	}
	m := manifest{StoreID: hex.EncodeToString(id[:]), Layout: layoutVersion, Created: s.now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return manifest{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return manifest{}, err
	}
	return m, nil
}

func (s *Store) openDB() error {
	dsn := "file:" + filepath.ToSlash(s.dbPath) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	// One connection serialises everything. At household scale that is simpler and safer than
	// reasoning about concurrent writers, and VACUUM INTO needs a consistent view.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return fmt.Errorf("store: open database: %w (a wrong key was caught earlier; this is a corrupt database)", err)
	}
	s.db = db
	return nil
}

// MarkDirty records that the database changed and needs sealing.
func (s *Store) MarkDirty() { s.dirty.Store(true) }

// SealNow seals the database to the data directory. If force is false it does nothing when
// nothing changed.
func (s *Store) SealNow(force bool) error {
	s.sealMu.Lock()
	defer s.sealMu.Unlock()
	return s.sealLocked(force)
}

// SealedSnapshot seals the database and calls fn with the data directory while no other seal can
// replace the files in it, so fn sees a consistent manifest and sealed database (D-28). fn must
// not call back into the store.
func (s *Store) SealedSnapshot(fn func(dataDir string) error) error {
	s.sealMu.Lock()
	defer s.sealMu.Unlock()
	if err := s.sealLocked(true); err != nil {
		return err
	}
	return fn(s.cfg.DataDir)
}

func (s *Store) sealLocked(force bool) error {
	if s.db == nil {
		return ErrClosed
	}
	if !force && !s.dirty.Load() {
		return nil
	}
	// Clear the flag before serialising: a write that lands during the seal marks it dirty again
	// and is picked up by the next one.
	s.dirty.Store(false)
	plain, err := s.serialize()
	if err != nil {
		s.dirty.Store(true)
		return err
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	if err := seal.WriteFile(s.cfg.DataDir, s.key[:], s.binding, plain); err != nil {
		s.dirty.Store(true)
		return fmt.Errorf("store: seal: %w", err)
	}
	s.lastSeal.Store(s.now().UnixNano())
	return nil
}

// serialize writes a compact copy of the database to a temp file on tmpfs and reads it back.
func (s *Store) serialize() ([]byte, error) {
	tmp, err := os.CreateTemp(s.cfg.TmpDir, "snapshot.*.sqlite")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)
	if _, err := s.db.Exec("VACUUM INTO " + quote(path)); err != nil {
		return nil, fmt.Errorf("store: VACUUM INTO: %w", err)
	}
	return os.ReadFile(path)
}

func quote(p string) string { return "'" + strings.ReplaceAll(filepath.ToSlash(p), "'", "''") + "'" }

func (s *Store) sealLoop(ctx context.Context) {
	defer s.wg.Done()
	t := time.NewTicker(s.cfg.SealEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.SealNow(false); err != nil && !errors.Is(err, ErrClosed) {
				// The error text names no data: file paths and driver messages only.
				s.log.Error("background seal failed", "err", err.Error())
			}
		}
	}
}

// LastSealed returns when the database was last sealed (zero if never).
func (s *Store) LastSealed() time.Time {
	n := s.lastSeal.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Update runs fn in a transaction and marks the store dirty. If durable is true the new state is
// sealed before Update returns; use it for uploads, edits and anything not cheap to redo.
func (s *Store) Update(ctx context.Context, durable bool, fn func(*sql.Tx) error) error {
	if s.closed.Load() {
		return ErrClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.MarkDirty()
	if durable {
		return s.SealNow(false)
	}
	return nil
}

// View runs fn in a read-only transaction.
func (s *Store) View(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.closed.Load() {
		return ErrClosed
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

// Close seals a final time, closes the database, removes the live files from tmpfs and wipes the key.
func (s *Store) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.stop != nil {
		s.stop()
	}
	s.wg.Wait()
	err := s.SealNow(false)
	s.sealMu.Lock()
	if s.db != nil {
		if cerr := s.db.Close(); err == nil {
			err = cerr
		}
		s.db = nil
	}
	s.sealMu.Unlock()
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		_ = os.Remove(s.dbPath + suffix)
	}
	sweepSnapshots(s.cfg.TmpDir)
	s.wipeKey()
	s.lock.release()
	return err
}

func (s *Store) wipeKey() {
	for i := range s.key {
		s.key[i] = 0
	}
}

// DataDir returns the data directory (the blob store lives under it).
func (s *Store) DataDir() string { return s.cfg.DataDir }

// KeyForDrill returns a copy of the master key so the in-server restore drill can open a backup
// the way a real restore would (D-28). The caller must Zero it, and must not log or keep it.
func (s *Store) KeyForDrill() (keyfile.Key, error) {
	if s.closed.Load() {
		return keyfile.Key{}, ErrClosed
	}
	return keyfile.FromBytes(s.key[:])
}

// sweepSnapshots removes any plaintext snapshot left in dir by a seal that did not finish. The
// directory is the store's own, so every match is ours.
func sweepSnapshots(dir string) {
	old, _ := filepath.Glob(filepath.Join(dir, "snapshot.*.sqlite"))
	for _, p := range old {
		_ = os.Remove(p)
	}
}
