// Package adminlog is Alcove's append-only, hash-chained admin log (constraint 6, D-6, N5).
//
// Every admin action appends one entry inside the SAME transaction as the action itself, so an
// action and its log line commit together or not at all. Each entry's hash covers the previous
// hash, so editing, removing or reordering any row breaks the chain. A head anchor (the last
// sequence number and hash) is kept separately in meta, so cutting entries off the end is detected
// too. The log records WHICH entry and WHICH fields were touched, never their content: the
// free-text fields are restricted to a safe alphabet so content cannot slip in by accident
// (constraint 8).
package adminlog

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	headKey = "adminlog.head" // "<seq>:<hex hash>" kept in meta, updated in the same transaction
	// System is the actor for actions the server takes on its own (first run, a revert timer).
	System = "system"
)

// Entry is one log line as stored.
type Entry struct {
	Seq        int64
	Time       time.Time
	Actor      string // user id, or System
	Action     string // dotted verb, e.g. "settings.network.change"
	TargetType string // "user", "space", "settings", ...
	TargetID   string // an id, never a name or text
	Fields     string // comma-separated field names touched, never values
	PrevHash   []byte
	Hash       []byte
}

// safe is the only alphabet the free-text fields may use: identifiers, dots, colons, commas, at
// signs and dashes. It is deliberately too narrow to carry a sentence.
var safe = regexp.MustCompile(`^[A-Za-z0-9_.:,@-]{0,200}$`)

// ErrUnsafe means a field would have carried content, not an identifier.
var ErrUnsafe = errors.New("adminlog: field is not a plain identifier")

// Append adds an entry to the chain inside tx. now is passed in so tests are deterministic.
func Append(tx *sql.Tx, now time.Time, actor, action, targetType, targetID, fields string) (Entry, error) {
	for _, v := range []string{actor, action, targetType, targetID, fields} {
		if !safe.MatchString(v) {
			return Entry{}, ErrUnsafe
		}
	}
	if actor == "" || action == "" {
		return Entry{}, errors.New("adminlog: actor and action are required")
	}
	var seq int64
	prev := make([]byte, sha256.Size) // the first entry chains from 32 zero bytes
	var last []byte
	switch err := tx.QueryRow(`SELECT seq, hash FROM admin_log ORDER BY seq DESC LIMIT 1`).Scan(&seq, &last); {
	case errors.Is(err, sql.ErrNoRows):
		seq = 0
	case err != nil:
		return Entry{}, err
	default:
		prev = last
	}
	e := Entry{Seq: seq + 1, Time: now.UTC().Truncate(time.Second), Actor: actor, Action: action,
		TargetType: targetType, TargetID: targetID, Fields: fields, PrevHash: prev}
	e.Hash = hashOf(e)
	if _, err := tx.Exec(`INSERT INTO admin_log(seq, ts, actor, action, target_type, target_id, fields, prev_hash, hash)
		VALUES (?,?,?,?,?,?,?,?,?)`, e.Seq, e.Time.Unix(), e.Actor, e.Action, e.TargetType, e.TargetID, e.Fields, e.PrevHash, e.Hash); err != nil {
		return Entry{}, err
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		headKey, fmt.Sprintf("%d:%s", e.Seq, hex.EncodeToString(e.Hash))); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// hashOf is SHA-256 over the previous hash and each field, each length-prefixed so that moving a
// byte from one field into the next changes the hash.
func hashOf(e Entry) []byte {
	h := sha256.New()
	h.Write(e.PrevHash)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(e.Seq))
	h.Write(n[:])
	binary.BigEndian.PutUint64(n[:], uint64(e.Time.Unix()))
	h.Write(n[:])
	for _, s := range []string{e.Actor, e.Action, e.TargetType, e.TargetID, e.Fields} {
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	return h.Sum(nil)
}

// Result is what Verify found.
type Result struct {
	Entries int64
	Head    string // hex of the last hash; an admin can note it somewhere else as an outside anchor
}

// Verify walks the whole chain. It fails on the first bad entry, a gap in the sequence, or a head
// anchor that does not match (entries cut off the end).
func Verify(tx *sql.Tx) (Result, error) {
	rows, err := tx.Query(`SELECT seq, ts, actor, action, target_type, target_id, fields, prev_hash, hash FROM admin_log ORDER BY seq`)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	prev := make([]byte, sha256.Size)
	var n int64
	var lastHash []byte
	for rows.Next() {
		var e Entry
		var ts int64
		if err := rows.Scan(&e.Seq, &ts, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.Fields, &e.PrevHash, &e.Hash); err != nil {
			return Result{}, err
		}
		e.Time = time.Unix(ts, 0).UTC()
		n++
		if e.Seq != n {
			return Result{}, fmt.Errorf("adminlog: sequence gap at entry %d (found %d)", n, e.Seq)
		}
		if !bytes.Equal(e.PrevHash, prev) {
			return Result{}, fmt.Errorf("adminlog: chain broken at entry %d", e.Seq)
		}
		if !bytes.Equal(e.Hash, hashOf(e)) {
			return Result{}, fmt.Errorf("adminlog: entry %d was altered", e.Seq)
		}
		prev, lastHash = e.Hash, e.Hash
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	var head string
	err = tx.QueryRow(`SELECT value FROM meta WHERE key = ?`, headKey).Scan(&head)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if n != 0 {
			return Result{}, errors.New("adminlog: head anchor is missing")
		}
	case err != nil:
		return Result{}, err
	default:
		want := fmt.Sprintf("%d:%s", n, hex.EncodeToString(lastHash))
		if n == 0 || head != want {
			return Result{}, fmt.Errorf("adminlog: entries were removed from the end (anchor at %s)", strings.SplitN(head, ":", 2)[0])
		}
	}
	return Result{Entries: n, Head: hex.EncodeToString(lastHash)}, nil
}

// List returns entries newest first, for the viewer. offset and limit page through them.
func List(tx *sql.Tx, limit, offset int) ([]Entry, error) {
	rows, err := tx.Query(`SELECT seq, ts, actor, action, target_type, target_id, fields FROM admin_log ORDER BY seq DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts int64
		if err := rows.Scan(&e.Seq, &ts, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.Fields); err != nil {
			return nil, err
		}
		e.Time = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
