// Package spaces implements spaces, memberships, invites and join requests (D-31, F4 to F6, F27).
//
// The one rule everything else depends on: Role is the ONLY place that decides whether a person
// may see a space. Every space page asks it, and from M4 every shared view will too. A person who
// is not an active member, whose account is disabled or deleted, or whose space is deleted gets
// the same answer as someone who was never there.
package spaces

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
)

// ConsentVersion identifies the wording of the join screen. From M4 a member's entries are shown
// only when they agreed to the CURRENT version, so changing what a space shares means bumping it.
const ConsentVersion = 1

// Roles and states.
const (
	RoleOwner  = "owner"
	RoleMember = "member"

	StateRequested = "requested"
	StateActive    = "active"
	StateLeft      = "left"
	StateRemoved   = "removed"

	KindSpace  = "space"
	KindServer = "server"

	// InviteTTL is how long an invite lives unless the creator picks another.
	InviteTTL = 7 * 24 * time.Hour
)

// Errors. Messages are written for a person.
var (
	ErrNotFound    = errors.New("spaces: not found")
	ErrNotAllowed  = errors.New("spaces: not allowed")
	ErrLastOwner   = errors.New("spaces: this person is the owner; hand the space to another member or delete it first")
	ErrBadName     = errors.New("spaces: give the space a name of 1 to 60 characters")
	ErrBadCode     = errors.New("spaces: that code is not valid")
	ErrNotMember   = errors.New("spaces: that person is not a member")
	ErrAlreadyIn   = errors.New("spaces: already a member")
	ErrNeedConsent = errors.New("spaces: the join screen must be agreed to")
	ErrExpired     = errors.New("spaces: the recovery window for this space has ended")
)

// Space is a space as its members see it.
type Space struct {
	ID, Name, CreatedBy string
	CreatedAt           time.Time
	HasCode             bool
	CodeAt              time.Time
	DeletedAt           time.Time
}

// Deleted reports whether the space is soft-deleted.
func (s Space) Deleted() bool { return !s.DeletedAt.IsZero() }

// Member is one membership row with the person's name.
type Member struct {
	UserID, Username, DisplayName, Role, State string
	JoinedAt, RequestedAt                      time.Time
	ConsentVersion                             int
	IncludeNotes, IncludePhotos                bool
	ShowPartnerNames, Paused                   bool
}

// Settings are a member's sharing choices for one space (D-21).
type Settings struct {
	IncludeNotes, IncludePhotos, ShowPartnerNames, Paused bool
}

// NewID makes an opaque id with a prefix, safe to put in the admin log.
func NewID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// ---- codes ----

const codeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no i, l, o, 0, 1

// NewCode returns a random code (16 characters, about 80 bits, shown as xxxx-xxxx-xxxx-xxxx) and
// the hash that is stored in its place.
func NewCode() (code, hash string, err error) {
	var b [16]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", "", err
	}
	out := make([]byte, 0, 19)
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			out = append(out, '-')
		}
		out = append(out, codeAlphabet[int(c)%len(codeAlphabet)])
	}
	code = string(out)
	return code, HashCode(code), nil
}

// NormalizeCode lower-cases a typed code and drops spaces and dashes.
func NormalizeCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// HashCode is what is stored for a code: SHA-256 of its normalized form.
func HashCode(code string) string {
	h := sha256.Sum256([]byte(NormalizeCode(code)))
	return hex.EncodeToString(h[:])
}

func cleanName(s string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	n := strings.TrimSpace(b.String())
	if n == "" || len([]rune(n)) > 60 {
		return "", ErrBadName
	}
	return n, nil
}

func ts(t time.Time) int64 { return t.Unix() }

func fromTS(n sql.NullInt64) time.Time {
	if !n.Valid || n.Int64 == 0 {
		return time.Time{}
	}
	return time.Unix(n.Int64, 0).UTC()
}

// ---- the gate ----

// Role is THE visibility check. It returns the person's role in the space and true only if they are
// an ACTIVE member, the space is not deleted, and their account is active and not deleted.
func Role(tx *sql.Tx, spaceID, userID string) (string, bool) {
	var role string
	err := tx.QueryRow(`SELECT m.role FROM membership m
		JOIN space s ON s.id = m.space_id
		JOIN user u ON u.id = m.user_id
		WHERE m.space_id = ? AND m.user_id = ? AND m.state = 'active'
		  AND s.deleted_at IS NULL AND u.status = 'active' AND u.deleted_at IS NULL`, spaceID, userID).Scan(&role)
	if err != nil {
		return "", false
	}
	return role, true
}

// ---- spaces ----

// Create makes a space; the creator becomes its owner, having agreed to the current join text.
func Create(tx *sql.Tx, now time.Time, name, creator string) (Space, error) {
	n, err := cleanName(name)
	if err != nil {
		return Space{}, err
	}
	id, err := NewID("sp")
	if err != nil {
		return Space{}, err
	}
	if _, err := tx.Exec(`INSERT INTO space(id, name, created_by, created_at) VALUES (?,?,?,?)`, id, n, creator, ts(now)); err != nil {
		return Space{}, err
	}
	if _, err := tx.Exec(`INSERT INTO membership(space_id, user_id, role, state, created_at, joined_at, consent_version, consent_at)
		VALUES (?,?,?,?,?,?,?,?)`, id, creator, RoleOwner, StateActive, ts(now), ts(now), ConsentVersion, ts(now)); err != nil {
		return Space{}, err
	}
	return Space{ID: id, Name: n, CreatedBy: creator, CreatedAt: now.UTC().Truncate(time.Second)}, nil
}

// Get returns a space by id, including a deleted one. Callers decide who may see it (see Role).
func Get(tx *sql.Tx, id string) (Space, error) {
	var s Space
	var created int64
	var code sql.NullString
	var codeAt, del sql.NullInt64
	err := tx.QueryRow(`SELECT id, name, created_by, created_at, join_code_hash, join_code_at, deleted_at FROM space WHERE id = ?`, id).
		Scan(&s.ID, &s.Name, &s.CreatedBy, &created, &code, &codeAt, &del)
	if errors.Is(err, sql.ErrNoRows) {
		return Space{}, ErrNotFound
	}
	if err != nil {
		return Space{}, err
	}
	s.CreatedAt, s.HasCode, s.CodeAt, s.DeletedAt = time.Unix(created, 0).UTC(), code.Valid && code.String != "", fromTS(codeAt), fromTS(del)
	return s, nil
}

// Mine is one of a person's spaces with their standing in it.
type Mine struct {
	Space
	Role, State string
	Members     int
}

// ForUser lists the spaces a person is active in, or has asked to join, newest first. Deleted
// spaces are listed separately by DeletedFor.
func ForUser(tx *sql.Tx, userID string) ([]Mine, error) {
	rows, err := tx.Query(`SELECT s.id, s.name, s.created_by, s.created_at, m.role, m.state,
			(SELECT COUNT(*) FROM membership x JOIN user ux ON ux.id = x.user_id
			 WHERE x.space_id = s.id AND x.state = 'active' AND ux.status = 'active' AND ux.deleted_at IS NULL)
		FROM membership m JOIN space s ON s.id = m.space_id
		WHERE m.user_id = ? AND m.state IN ('active','requested') AND s.deleted_at IS NULL
		ORDER BY m.state, s.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mine
	for rows.Next() {
		var m Mine
		var created int64
		if err := rows.Scan(&m.ID, &m.Name, &m.CreatedBy, &created, &m.Role, &m.State, &m.Members); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeletedFor lists spaces a person owned that are soft-deleted (for the "recently deleted" list).
func DeletedFor(tx *sql.Tx, userID string) ([]Space, error) {
	rows, err := tx.Query(`SELECT s.id, s.name, s.created_by, s.created_at, s.deleted_at FROM space s
		JOIN membership m ON m.space_id = s.id AND m.user_id = ? AND m.role = 'owner' AND m.state = 'active'
		WHERE s.deleted_at IS NOT NULL ORDER BY s.deleted_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Space
	for rows.Next() {
		var s Space
		var created, del int64
		if err := rows.Scan(&s.ID, &s.Name, &s.CreatedBy, &created, &del); err != nil {
			return nil, err
		}
		s.CreatedAt, s.DeletedAt = time.Unix(created, 0).UTC(), time.Unix(del, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// AllSpaces lists every space for the administrator, with owner and member counts.
type Overview struct {
	Space
	Owner         string // owner's username, "" if none
	Active, Askin int
}

func AllSpaces(tx *sql.Tx) ([]Overview, error) {
	rows, err := tx.Query(`SELECT s.id, s.name, s.created_by, s.created_at, s.deleted_at,
			COALESCE((SELECT u.username FROM membership m JOIN user u ON u.id = m.user_id WHERE m.space_id = s.id AND m.role = 'owner' AND m.state = 'active' LIMIT 1), ''),
			(SELECT COUNT(*) FROM membership m WHERE m.space_id = s.id AND m.state = 'active'),
			(SELECT COUNT(*) FROM membership m WHERE m.space_id = s.id AND m.state = 'requested')
		FROM space s ORDER BY (s.deleted_at IS NOT NULL), s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Overview
	for rows.Next() {
		var o Overview
		var created int64
		var del sql.NullInt64
		if err := rows.Scan(&o.ID, &o.Name, &o.CreatedBy, &created, &del, &o.Owner, &o.Active, &o.Askin); err != nil {
			return nil, err
		}
		o.CreatedAt, o.DeletedAt = time.Unix(created, 0).UTC(), fromTS(del)
		out = append(out, o)
	}
	return out, rows.Err()
}

// Members lists a space's memberships in the given states (all states if none are given).
func Members(tx *sql.Tx, spaceID string, states ...string) ([]Member, error) {
	q := `SELECT m.user_id, u.username, u.display_name, m.role, m.state, m.joined_at, m.created_at, m.consent_version,
			m.include_notes, m.include_photos, m.show_partner_names, m.paused
		FROM membership m JOIN user u ON u.id = m.user_id WHERE m.space_id = ?`
	args := []any{spaceID}
	if len(states) > 0 {
		q += ` AND m.state IN (` + strings.TrimSuffix(strings.Repeat("?,", len(states)), ",") + `)`
		for _, s := range states {
			args = append(args, s)
		}
	}
	q += ` ORDER BY (m.role = 'owner') DESC, m.joined_at, m.created_at`
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var joined sql.NullInt64
		var created int64
		var a, b, c, d int
		if err := rows.Scan(&m.UserID, &m.Username, &m.DisplayName, &m.Role, &m.State, &joined, &created, &m.ConsentVersion, &a, &b, &c, &d); err != nil {
			return nil, err
		}
		m.JoinedAt, m.RequestedAt = fromTS(joined), time.Unix(created, 0).UTC()
		m.IncludeNotes, m.IncludePhotos, m.ShowPartnerNames, m.Paused = a != 0, b != 0, c != 0, d != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// Rename changes a space's name.
func Rename(tx *sql.Tx, spaceID, name string) error {
	n, err := cleanName(name)
	if err != nil {
		return err
	}
	return mustChange(tx.Exec(`UPDATE space SET name = ? WHERE id = ?`, n, spaceID))
}

func mustChange(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSettings saves a member's own sharing choices.
func SetSettings(tx *sql.Tx, spaceID, userID string, s Settings) error {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	return mustChange(tx.Exec(`UPDATE membership SET include_notes = ?, include_photos = ?, show_partner_names = ?, paused = ?
		WHERE space_id = ? AND user_id = ? AND state = 'active'`, b(s.IncludeNotes), b(s.IncludePhotos), b(s.ShowPartnerNames), b(s.Paused), spaceID, userID))
}

// Leave ends a member's membership. The only owner cannot leave (ErrLastOwner).
func Leave(tx *sql.Tx, now time.Time, spaceID, userID string) error {
	role, ok := Role(tx, spaceID, userID)
	if !ok {
		return ErrNotMember
	}
	if role == RoleOwner {
		return ErrLastOwner
	}
	return mustChange(tx.Exec(`UPDATE membership SET state = 'left', ended_at = ? WHERE space_id = ? AND user_id = ? AND state = 'active'`, ts(now), spaceID, userID))
}

// Remove lets an owner end another member's membership. An owner cannot be removed this way.
func Remove(tx *sql.Tx, now time.Time, spaceID, userID string) error {
	var role, state string
	err := tx.QueryRow(`SELECT role, state FROM membership WHERE space_id = ? AND user_id = ?`, spaceID, userID).Scan(&role, &state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && state != StateActive) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	if role == RoleOwner {
		return ErrLastOwner
	}
	return mustChange(tx.Exec(`UPDATE membership SET state = 'removed', ended_at = ? WHERE space_id = ? AND user_id = ?`, ts(now), spaceID, userID))
}

// Transfer hands ownership from one active owner to another active member; the old owner becomes a
// member. A space always keeps exactly one owner.
func Transfer(tx *sql.Tx, spaceID, from, to string) error {
	if from == to {
		return ErrNotAllowed
	}
	if role, ok := Role(tx, spaceID, from); !ok || role != RoleOwner {
		return ErrNotAllowed
	}
	if _, ok := Role(tx, spaceID, to); !ok {
		return ErrNotMember
	}
	if _, err := tx.Exec(`UPDATE membership SET role = 'member' WHERE space_id = ? AND user_id = ?`, spaceID, from); err != nil {
		return err
	}
	return mustChange(tx.Exec(`UPDATE membership SET role = 'owner' WHERE space_id = ? AND user_id = ? AND state = 'active'`, spaceID, to))
}

// Delete soft-deletes a space (the owner's action, or the admin's).
func Delete(tx *sql.Tx, now time.Time, spaceID string) error {
	return mustChange(tx.Exec(`UPDATE space SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, ts(now), spaceID))
}

// Restore undoes a delete if it is still inside window.
func Restore(tx *sql.Tx, now time.Time, spaceID string, window time.Duration) error {
	s, err := Get(tx, spaceID)
	if err != nil {
		return err
	}
	if !s.Deleted() {
		return nil
	}
	if now.Sub(s.DeletedAt) > window {
		return ErrExpired
	}
	return mustChange(tx.Exec(`UPDATE space SET deleted_at = NULL WHERE id = ?`, spaceID))
}

// ---- joining ----

// join makes (or revives) a membership in the given state. Consent must be recorded for an
// active or requested membership: it is the act of joining (D-21).
func join(tx *sql.Tx, now time.Time, spaceID, userID, state string, consent bool) error {
	if !consent {
		return ErrNeedConsent
	}
	var cur string
	err := tx.QueryRow(`SELECT state FROM membership WHERE space_id = ? AND user_id = ?`, spaceID, userID).Scan(&cur)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		joined := sql.NullInt64{}
		if state == StateActive {
			joined = sql.NullInt64{Int64: ts(now), Valid: true}
		}
		_, err = tx.Exec(`INSERT INTO membership(space_id, user_id, role, state, created_at, joined_at, consent_version, consent_at)
			VALUES (?,?,?,?,?,?,?,?)`, spaceID, userID, RoleMember, state, ts(now), joined, ConsentVersion, ts(now))
		return err
	case err != nil:
		return err
	case cur == StateActive:
		return ErrAlreadyIn
	case cur == StateRequested && state == StateRequested:
		return nil // asking twice is harmless
	}
	// A person who left, was removed, or is promoted from requested comes back as an ordinary member.
	joined := sql.NullInt64{}
	if state == StateActive {
		joined = sql.NullInt64{Int64: ts(now), Valid: true}
	}
	_, err = tx.Exec(`UPDATE membership SET role = 'member', state = ?, created_at = ?, joined_at = ?, ended_at = NULL,
		consent_version = ?, consent_at = ? WHERE space_id = ? AND user_id = ?`, state, ts(now), joined, ConsentVersion, ts(now), spaceID, userID)
	return err
}

// Approve lets an owner admit a person who asked to join. The person's consent was given when they
// asked, so it is already on the row; Approve checks it is the current version.
func Approve(tx *sql.Tx, now time.Time, spaceID, userID string) error {
	var v int
	err := tx.QueryRow(`SELECT consent_version FROM membership WHERE space_id = ? AND user_id = ? AND state = 'requested'`, spaceID, userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	if v != ConsentVersion {
		return ErrNeedConsent
	}
	return mustChange(tx.Exec(`UPDATE membership SET state = 'active', joined_at = ? WHERE space_id = ? AND user_id = ? AND state = 'requested'`, ts(now), spaceID, userID))
}

// Deny turns down a request. The person may ask again.
func Deny(tx *sql.Tx, now time.Time, spaceID, userID string) error {
	return mustChange(tx.Exec(`UPDATE membership SET state = 'removed', ended_at = ? WHERE space_id = ? AND user_id = ? AND state = 'requested'`, ts(now), spaceID, userID))
}

// ---- space code ----

// RotateCode makes a new space code (the old one stops working) and returns it once. Only its hash
// is stored.
func RotateCode(tx *sql.Tx, now time.Time, spaceID string) (string, error) {
	code, hash, err := NewCode()
	if err != nil {
		return "", err
	}
	if err := mustChange(tx.Exec(`UPDATE space SET join_code_hash = ?, join_code_at = ? WHERE id = ? AND deleted_at IS NULL`, hash, ts(now), spaceID)); err != nil {
		return "", err
	}
	return code, nil
}

// DisableCode turns the space code off.
func DisableCode(tx *sql.Tx, spaceID string) error {
	return mustChange(tx.Exec(`UPDATE space SET join_code_hash = NULL, join_code_at = NULL WHERE id = ?`, spaceID))
}

// ---- invites ----

// Invite is an invite as its creator sees it. The code itself is never stored.
type Invite struct {
	ID, Kind, SpaceID, Label, State, CreatedBy string
	CreatedAt, ExpiresAt, UsedAt               time.Time
	UsedBy                                     string
}

// Live reports whether the invite can still be redeemed at now.
func (i Invite) Live(now time.Time) bool { return i.State == "open" && now.Before(i.ExpiresAt) }

// CreateInvite makes an invite and returns its code once. spaceID is empty for a server invite.
func CreateInvite(tx *sql.Tx, now time.Time, kind, spaceID, creator, label string, ttl time.Duration) (Invite, string, error) {
	if kind != KindSpace && kind != KindServer {
		return Invite{}, "", ErrNotAllowed
	}
	if (kind == KindSpace) != (spaceID != "") {
		return Invite{}, "", ErrNotAllowed
	}
	if ttl <= 0 {
		ttl = InviteTTL
	}
	label = strings.TrimSpace(label)
	if r := []rune(label); len(r) > 40 {
		label = string(r[:40])
	}
	code, hash, err := NewCode()
	if err != nil {
		return Invite{}, "", err
	}
	id, err := NewID("iv")
	if err != nil {
		return Invite{}, "", err
	}
	var sp any
	if spaceID != "" {
		sp = spaceID
	}
	if _, err := tx.Exec(`INSERT INTO invite(id, code_hash, kind, space_id, created_by, created_at, expires_at, state, label)
		VALUES (?,?,?,?,?,?,?,?,?)`, id, hash, kind, sp, creator, ts(now), ts(now.Add(ttl)), "open", label); err != nil {
		return Invite{}, "", err
	}
	return Invite{ID: id, Kind: kind, SpaceID: spaceID, Label: label, State: "open", CreatedBy: creator,
		CreatedAt: now.UTC().Truncate(time.Second), ExpiresAt: now.Add(ttl).UTC().Truncate(time.Second)}, code, nil
}

// Invites lists invites for one space, or the server invites when spaceID is empty, newest first.
func Invites(tx *sql.Tx, spaceID string) ([]Invite, error) {
	q := `SELECT id, kind, COALESCE(space_id, ''), label, state, created_by, created_at, expires_at, COALESCE(used_by, ''), used_at FROM invite WHERE `
	var args []any
	if spaceID == "" {
		q += `kind = 'server'`
	} else {
		q += `space_id = ?`
		args = append(args, spaceID)
	}
	rows, err := tx.Query(q+` ORDER BY created_at DESC LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var i Invite
		var created, expires int64
		var used sql.NullInt64
		if err := rows.Scan(&i.ID, &i.Kind, &i.SpaceID, &i.Label, &i.State, &i.CreatedBy, &created, &expires, &i.UsedBy, &used); err != nil {
			return nil, err
		}
		i.CreatedAt, i.ExpiresAt, i.UsedAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC(), fromTS(used)
		out = append(out, i)
	}
	return out, rows.Err()
}

// RevokeInvite cancels an open invite. spaceID scopes it: a space owner can only revoke their own
// space's invites (an empty spaceID is the administrator's server invites).
func RevokeInvite(tx *sql.Tx, inviteID, spaceID string) error {
	if spaceID == "" {
		return mustChange(tx.Exec(`UPDATE invite SET state = 'revoked' WHERE id = ? AND kind = 'server' AND state = 'open'`, inviteID))
	}
	return mustChange(tx.Exec(`UPDATE invite SET state = 'revoked' WHERE id = ? AND space_id = ? AND state = 'open'`, inviteID, spaceID))
}

// Found says what a typed code points at.
type Found struct {
	Invite   *Invite // a live invite
	SpaceID  string  // for an invite to a space, or a space code
	Kind     string  // "invite" or "code"
	Name     string  // the space's name, for the join screen
	ViaInvit bool
}

// Lookup resolves a typed code. A wrong, expired, used or revoked invite, an off code and a
// deleted space all return ErrBadCode, so the answer never says which.
func Lookup(tx *sql.Tx, now time.Time, code string) (Found, error) {
	h := HashCode(code)
	if NormalizeCode(code) == "" {
		return Found{}, ErrBadCode
	}
	var i Invite
	var created, expires int64
	var used sql.NullInt64
	err := tx.QueryRow(`SELECT id, kind, COALESCE(space_id, ''), label, state, created_by, created_at, expires_at, COALESCE(used_by, ''), used_at
		FROM invite WHERE code_hash = ?`, h).Scan(&i.ID, &i.Kind, &i.SpaceID, &i.Label, &i.State, &i.CreatedBy, &created, &expires, &i.UsedBy, &used)
	if err == nil {
		i.CreatedAt, i.ExpiresAt, i.UsedAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC(), fromTS(used)
		if !i.Live(now) {
			return Found{}, ErrBadCode
		}
		f := Found{Invite: &i, Kind: "invite", SpaceID: i.SpaceID, ViaInvit: true}
		if i.SpaceID != "" {
			s, err := Get(tx, i.SpaceID)
			if err != nil || s.Deleted() {
				return Found{}, ErrBadCode
			}
			f.Name = s.Name
		}
		return f, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Found{}, err
	}
	var s Space
	var created2 int64
	err = tx.QueryRow(`SELECT id, name, created_by, created_at FROM space WHERE join_code_hash = ? AND deleted_at IS NULL`, h).Scan(&s.ID, &s.Name, &s.CreatedBy, &created2)
	if errors.Is(err, sql.ErrNoRows) {
		return Found{}, ErrBadCode
	}
	if err != nil {
		return Found{}, err
	}
	return Found{SpaceID: s.ID, Kind: "code", Name: s.Name}, nil
}

// Spend marks a live invite used by userID, inside the caller's transaction, and fails if it is no
// longer live, so two people cannot spend one invite.
func Spend(tx *sql.Tx, now time.Time, inviteID, userID string) error {
	res, err := tx.Exec(`UPDATE invite SET state = 'used', used_by = ?, used_at = ? WHERE id = ? AND state = 'open' AND expires_at > ?`,
		userID, ts(now), inviteID, ts(now))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBadCode
	}
	return nil
}

// JoinViaInvite makes userID an active member after an invite to the space was spent.
func JoinViaInvite(tx *sql.Tx, now time.Time, spaceID, userID string, consent bool) error {
	return join(tx, now, spaceID, userID, StateActive, consent)
}

// RequestJoin records that userID asks to join (via the space code); the owner decides.
func RequestJoin(tx *sql.Tx, now time.Time, spaceID, userID string, consent bool) error {
	return join(tx, now, spaceID, userID, StateRequested, consent)
}
