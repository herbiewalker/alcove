package totp

import (
	"database/sql"
	"errors"
	"time"
)

// ErrReplay means a code's step is not newer than the last one accepted.
var ErrReplay = errors.New("totp: that code was already used")

// Enabled reports whether a person has two-step sign-in on.
func Enabled(tx *sql.Tx, userID string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM user_totp WHERE user_id = ?`, userID).Scan(&n)
	return n > 0, err
}

// Secret returns a person's secret and the last step accepted. ok is false when it is not turned on.
func Secret(tx *sql.Tx, userID string) (secret []byte, lastStep int64, ok bool, err error) {
	err = tx.QueryRow(`SELECT secret, last_step FROM user_totp WHERE user_id = ?`, userID).Scan(&secret, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	return secret, lastStep, err == nil, err
}

// Enable turns it on with a new secret and a fresh set of recovery code hashes, replacing any earlier
// ones. lastStep is the step of the code that proved the person's app works, so that code cannot be
// reused to sign in a moment later.
func Enable(tx *sql.Tx, now time.Time, userID string, secret []byte, lastStep int64, hashes []string) error {
	if _, err := tx.Exec(`INSERT INTO user_totp(user_id, secret, enabled_at, last_step) VALUES (?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET secret = excluded.secret, enabled_at = excluded.enabled_at, last_step = excluded.last_step`,
		userID, secret, now.Unix(), lastStep); err != nil {
		return err
	}
	return SetRecovery(tx, userID, hashes)
}

// SetRecovery replaces a person's recovery codes (used and unused alike).
func SetRecovery(tx *sql.Tx, userID string, hashes []string) error {
	if _, err := tx.Exec(`DELETE FROM user_recovery WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(`INSERT INTO user_recovery(user_id, hash) VALUES (?,?)`, userID, h); err != nil {
			return err
		}
	}
	return nil
}

// Disable turns it off and forgets the secret and every recovery code.
func Disable(tx *sql.Tx, userID string) error {
	if _, err := tx.Exec(`DELETE FROM user_recovery WHERE user_id = ?`, userID); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM user_totp WHERE user_id = ?`, userID)
	return err
}

// AcceptStep records that a code of this step was used. It is one statement, so two sign-ins racing
// with the same code cannot both win.
func AcceptStep(tx *sql.Tx, userID string, step int64) error {
	res, err := tx.Exec(`UPDATE user_totp SET last_step = ? WHERE user_id = ? AND last_step < ?`, step, userID, step)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrReplay
	}
	return nil
}

// UseRecovery spends one recovery code. ok is false for a wrong or already used code. remaining is
// how many unused codes are left after this one.
func UseRecovery(tx *sql.Tx, now time.Time, userID, code string) (ok bool, remaining int, err error) {
	res, err := tx.Exec(`UPDATE user_recovery SET used_at = ? WHERE user_id = ? AND hash = ? AND used_at = 0`,
		now.Unix(), userID, HashRecovery(code))
	if err != nil {
		return false, 0, err
	}
	n, _ := res.RowsAffected()
	remaining, err = RecoveryLeft(tx, userID)
	return n == 1, remaining, err
}

// RecoveryLeft counts a person's unused recovery codes.
func RecoveryLeft(tx *sql.Tx, userID string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM user_recovery WHERE user_id = ? AND used_at = 0`, userID).Scan(&n)
	return n, err
}
