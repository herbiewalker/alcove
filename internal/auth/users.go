package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Roles. A member's reach beyond their own data is decided by space membership (M3, M4); the
// admin role is the operator of the server (constraint 6).
const (
	RoleAdmin  = "admin"
	RoleMember = "member"

	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// User is an account row.
type User struct {
	ID          string
	Username    string
	DisplayName string
	Role        string
	Status      string
	PWHash      string
	CreatedAt   time.Time
	LastLoginAt time.Time // zero if never
	DeletedAt   time.Time // zero unless the account is deleted (pending purge, D-30)
	MustChange  bool      // must set a new password before anything else works
}

var (
	// ErrUsernameTaken is returned for a duplicate username (compared case-insensitively).
	ErrUsernameTaken = errors.New("auth: username is taken")
	// ErrUsername describes a username the form should reject.
	ErrUsername = errors.New("auth: usernames are 3 to 32 characters: letters, digits, dot, dash, underscore")
	// ErrNoUser is returned when no account matches.
	ErrNoUser = errors.New("auth: no such user")
)

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)

// NormalizeUsername lower-cases and validates a username.
func NormalizeUsername(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !usernameRE.MatchString(s) {
		return "", ErrUsername
	}
	return s, nil
}

// NewID makes an opaque account id. It is safe to put in the admin log (plain identifier).
func NewID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "u" + hex.EncodeToString(b[:]), nil
}

const userCols = `id, username, display_name, role, status, pw_hash, created_at, last_login_at, COALESCE(deleted_at, 0), must_change`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created, last, deleted int64
	var must int
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Status, &u.PWHash, &created, &last, &deleted, &must); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNoUser
		}
		return User{}, err
	}
	u.CreatedAt, u.MustChange = time.Unix(created, 0).UTC(), must != 0
	if deleted > 0 {
		u.DeletedAt = time.Unix(deleted, 0).UTC()
	}
	if last > 0 {
		u.LastLoginAt = time.Unix(last, 0).UTC()
	}
	return u, nil
}

// Create inserts an account. pwHash comes from Hash; the caller has already applied CheckPassword.
func Create(tx *sql.Tx, now time.Time, username, display, role, pwHash string) (User, error) {
	un, err := NormalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	display = strings.TrimSpace(display)
	if display == "" {
		display = un
	}
	if len(display) > 80 {
		return User{}, errors.New("auth: display name is too long (80 characters at most)")
	}
	if role != RoleAdmin && role != RoleMember {
		return User{}, fmt.Errorf("auth: unknown role %q", role)
	}
	id, err := NewID()
	if err != nil {
		return User{}, err
	}
	_, err = tx.Exec(`INSERT INTO user(id, username, display_name, role, status, pw_hash, created_at) VALUES (?,?,?,?,?,?,?)`,
		id, un, display, role, StatusActive, pwHash, now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrUsernameTaken
		}
		return User{}, err
	}
	return User{ID: id, Username: un, DisplayName: display, Role: role, Status: StatusActive, PWHash: pwHash, CreatedAt: now.UTC().Truncate(time.Second)}, nil
}

// ByUsername finds an account by (case-insensitive) username.
func ByUsername(tx *sql.Tx, username string) (User, error) {
	return scanUser(tx.QueryRow(`SELECT `+userCols+` FROM user WHERE username = ?`, strings.ToLower(strings.TrimSpace(username))))
}

// ByID finds an account by id.
func ByID(tx *sql.Tx, id string) (User, error) {
	return scanUser(tx.QueryRow(`SELECT `+userCols+` FROM user WHERE id = ?`, id))
}

// Count returns how many accounts exist.
func Count(tx *sql.Tx) (int, error) {
	var n int
	return n, tx.QueryRow(`SELECT COUNT(*) FROM user`).Scan(&n)
}

// SetPassword replaces an account's password hash.
func SetPassword(tx *sql.Tx, id, pwHash string) error {
	return mustChangeRow(tx.Exec(`UPDATE user SET pw_hash = ? WHERE id = ?`, pwHash, id))
}

// SetStatus enables or disables an account. Disabling does not delete anything (constraint 7);
// the caller also revokes the account's sessions.
func SetStatus(tx *sql.Tx, id, status string) error {
	if status != StatusActive && status != StatusDisabled {
		return fmt.Errorf("auth: unknown status %q", status)
	}
	return mustChangeRow(tx.Exec(`UPDATE user SET status = ? WHERE id = ?`, status, id))
}

// TouchLogin records a successful sign-in.
func TouchLogin(tx *sql.Tx, id string, now time.Time) error {
	return mustChangeRow(tx.Exec(`UPDATE user SET last_login_at = ? WHERE id = ?`, now.Unix(), id))
}

func mustChangeRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoUser
	}
	return nil
}

// Deleted reports whether the account is deleted and waiting for its purge.
func (u User) Deleted() bool { return !u.DeletedAt.IsZero() }

// SetPasswordForced replaces the hash and says whether the person must choose a new one at the
// next sign-in (a temporary password from an admin, or a break-glass reset).
func SetPasswordForced(tx *sql.Tx, id, pwHash string, mustChange bool) error {
	n := 0
	if mustChange {
		n = 1
	}
	return mustChangeRow(tx.Exec(`UPDATE user SET pw_hash = ?, must_change = ? WHERE id = ?`, pwHash, n, id))
}

// MarkDeleted starts an account's grace period. The account must already be disabled.
func MarkDeleted(tx *sql.Tx, id string, now time.Time) error {
	return mustChangeRow(tx.Exec(`UPDATE user SET deleted_at = ? WHERE id = ? AND status = 'disabled'`, now.Unix(), id))
}

// Undelete ends the grace period; the account stays disabled until an admin enables it.
func Undelete(tx *sql.Tx, id string) error {
	return mustChangeRow(tx.Exec(`UPDATE user SET deleted_at = NULL WHERE id = ?`, id))
}

// Remove deletes the account row for good (the last step of a purge).
func Remove(tx *sql.Tx, id string) error {
	return mustChangeRow(tx.Exec(`DELETE FROM user WHERE id = ?`, id))
}

// List returns every account, deleted ones last, newest first within each group.
func List(tx *sql.Tx) ([]User, error) {
	rows, err := tx.Query(`SELECT ` + userCols + ` FROM user ORDER BY (deleted_at IS NOT NULL), created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountActiveAdmins counts administrators who can sign in (active and not deleted).
func CountActiveAdmins(tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM user WHERE role = 'admin' AND status = 'active' AND deleted_at IS NULL`).Scan(&n)
	return n, err
}

// ChangePassword is a person changing their own password: the new hash replaces the old and the
// forced-change flag is cleared. (SetPassword leaves the flag alone, so a login-time rehash of a
// temporary password does not clear it.)
func ChangePassword(tx *sql.Tx, id, pwHash string) error {
	return SetPasswordForced(tx, id, pwHash, false)
}

// ErrBadDisplayName means a display name is empty, too long or holds control characters.
var ErrBadDisplayName = errors.New("auth: a name shown to others is 1 to 80 characters with no control characters")

// SetDisplayName changes the name other people see for an account. A person changes only their own;
// the caller decides who may call it.
func SetDisplayName(tx *sql.Tx, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return ErrBadDisplayName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return ErrBadDisplayName
		}
	}
	res, err := tx.Exec(`UPDATE user SET display_name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoUser
	}
	return nil
}
