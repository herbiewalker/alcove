package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DataKeySize is the per-user data key length (AES-256).
const DataKeySize = 32

// DataKey is a user's data key. Zero it when finished.
type DataKey struct{ b [DataKeySize]byte }

// Bytes returns a copy of the key.
func (k *DataKey) Bytes() []byte { out := make([]byte, DataKeySize); copy(out, k.b[:]); return out }

// Zero wipes the key.
func (k *DataKey) Zero() {
	for i := range k.b {
		k.b[i] = 0
	}
}

// Errors.
var (
	ErrNoDataKey     = errors.New("store: no data key for this owner")
	ErrDataKeyExists = errors.New("store: this owner already has a data key")
	ErrOwnerID       = errors.New("store: invalid owner id")
)

// ValidOwnerID restricts owner ids to a safe alphabet, because they also name directories.
func ValidOwnerID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func (s *Store) wrapCipher() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func dekAAD(owner string) []byte { return []byte("alcove-data-key|" + owner) }

// CreateDataKey generates a data key for owner, wraps it under the master key and stores it.
// The wrapped key is bound to the owner id, so a row copied to another owner does not unwrap.
// It seals immediately: a data key that exists only in memory would orphan the blobs written with it.
func (s *Store) CreateDataKey(owner string) error {
	if !ValidOwnerID(owner) {
		return ErrOwnerID
	}
	gcm, err := s.wrapCipher()
	if err != nil {
		return err
	}
	var dek [DataKeySize]byte
	if _, err := rand.Read(dek[:]); err != nil {
		return err
	}
	defer func() {
		for i := range dek {
			dek[i] = 0
		}
	}()
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	wrapped := gcm.Seal(nonce, nonce, dek[:], dekAAD(owner))
	res, err := s.db.Exec(`INSERT OR IGNORE INTO data_key(owner_id, wrapped, created_at) VALUES (?, ?, ?)`, owner, wrapped, s.now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDataKeyExists
	}
	s.MarkDirty()
	return s.SealNow(false)
}

// DataKey unwraps owner's data key.
func (s *Store) DataKey(owner string) (DataKey, error) {
	if !ValidOwnerID(owner) {
		return DataKey{}, ErrOwnerID
	}
	var wrapped []byte
	err := s.db.QueryRow(`SELECT wrapped FROM data_key WHERE owner_id = ?`, owner).Scan(&wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return DataKey{}, ErrNoDataKey
	}
	if err != nil {
		return DataKey{}, err
	}
	gcm, err := s.wrapCipher()
	if err != nil {
		return DataKey{}, err
	}
	if len(wrapped) < gcm.NonceSize() {
		return DataKey{}, fmt.Errorf("store: stored data key for %q is malformed", owner)
	}
	pt, err := gcm.Open(nil, wrapped[:gcm.NonceSize()], wrapped[gcm.NonceSize():], dekAAD(owner))
	if err != nil {
		return DataKey{}, fmt.Errorf("store: data key for %q does not unwrap (wrong master key or altered row)", owner)
	}
	var k DataKey
	copy(k.b[:], pt)
	for i := range pt {
		pt[i] = 0
	}
	return k, nil
}

// HasDataKey reports whether owner has a data key.
func (s *Store) HasDataKey(owner string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM data_key WHERE owner_id = ?`, owner).Scan(&n)
	return n > 0, err
}

// DestroyDataKey deletes owner's data key and seals at once (the purge step, "crypto-shred").
// Blobs encrypted under it become unreadable on this instance. Older sealed generations and
// instance backups still hold the key until they age out; that limit is documented (D-16).
func (s *Store) DestroyDataKey(owner string) error {
	if !ValidOwnerID(owner) {
		return ErrOwnerID
	}
	if _, err := s.db.Exec(`DELETE FROM data_key WHERE owner_id = ?`, owner); err != nil {
		return err
	}
	s.MarkDirty()
	return s.SealNow(false)
}

// DestroyDataKeyIfOlderThan removes owner's data key only when it was created before cutoff, and
// reports ErrNoDataKey when nothing was removed. The retention sweep uses it for keys whose
// account no longer exists, with a grace so a key made a moment ago for an account still being
// created is never swept.
func (s *Store) DestroyDataKeyIfOlderThan(owner string, cutoff time.Time) error {
	if !ValidOwnerID(owner) {
		return ErrOwnerID
	}
	res, err := s.db.Exec(`DELETE FROM data_key WHERE owner_id = ? AND created_at <= ?`, owner, cutoff.Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoDataKey
	}
	s.MarkDirty()
	return s.SealNow(false)
}
