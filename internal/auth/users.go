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

const userCols = `id, username, display_name, role, status, pw_hash, created_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created, last int64
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Status, &u.PWHash, &created, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNoUser
		}
		return User{}, err
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
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
	return mustChange(tx.Exec(`UPDATE user SET pw_hash = ? WHERE id = ?`, pwHash, id))
}

// SetStatus enables or disables an account. Disabling does not delete anything (constraint 7);
// the caller also revokes the account's sessions.
func SetStatus(tx *sql.Tx, id, status string) error {
	if status != StatusActive && status != StatusDisabled {
		return fmt.Errorf("auth: unknown status %q", status)
	}
	return mustChange(tx.Exec(`UPDATE user SET status = ? WHERE id = ?`, status, id))
}

// TouchLogin records a successful sign-in.
func TouchLogin(tx *sql.Tx, id string, now time.Time) error {
	return mustChange(tx.Exec(`UPDATE user SET last_login_at = ? WHERE id = ?`, now.Unix(), id))
}

func mustChange(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoUser
	}
	return nil
}
