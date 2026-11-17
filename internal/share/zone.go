package share

import (
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata" // zone names must work in the distroless image, which has no zoneinfo
)

var defaultZone atomic.Pointer[time.Location]

func init() { defaultZone.Store(time.UTC) }

// ErrBadZone means a time zone name is not a real IANA zone.
var ErrBadZone = errors.New("share: that is not a time zone name like Europe/Berlin")

// SetDefaultZone sets the zone used for people who have not chosen one (the server's own, D-35).
// An empty name means UTC. Call it once at start-up.
func SetDefaultZone(name string) error {
	loc, err := load(name)
	if err != nil {
		return err
	}
	defaultZone.Store(loc)
	return nil
}

// ValidZone reports whether name is empty (UTC) or a real IANA zone, without changing anything.
func ValidZone(name string) error {
	_, err := load(name)
	return err
}

// DefaultZoneName is the name of the zone used when a person has not chosen one.
func DefaultZoneName() string { return defaultZone.Load().String() }

func load(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC, nil
	}
	if len(name) > 64 || strings.ContainsAny(name, "\\:") || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return nil, ErrBadZone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrBadZone
	}
	return loc, nil
}

// ZoneName is the zone a person chose, or "" for the default.
func ZoneName(tx *sql.Tx, userID string) string {
	var tz string
	if err := tx.QueryRow(`SELECT tz FROM user_tz WHERE user_id = ?`, userID).Scan(&tz); err != nil {
		return ""
	}
	return tz
}

// SetZone saves a person's own time zone; an empty name removes the choice.
func SetZone(tx *sql.Tx, userID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		_, err := tx.Exec(`DELETE FROM user_tz WHERE user_id = ?`, userID)
		return err
	}
	if _, err := load(name); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO user_tz(user_id, tz) VALUES (?,?) ON CONFLICT(user_id) DO UPDATE SET tz = excluded.tz`, userID, name)
	return err
}

// zones caches locations for one query.
type zones map[string]*time.Location

func (z zones) get(name string) *time.Location {
	if name == "" {
		return defaultZone.Load()
	}
	if l, ok := z[name]; ok {
		return l
	}
	l, err := load(name)
	if err != nil {
		l = defaultZone.Load()
	}
	z[name] = l
	return l
}

// LocationOf is the zone a person's times are shown in: their own choice, or the server's default.
func LocationOf(tx *sql.Tx, userID string) *time.Location {
	return zones{}.get(ZoneName(tx, userID))
}

// LocationByName is the zone a stored name means, or the default zone for an empty or unusable one.
func LocationByName(name string) *time.Location { return zones{}.get(name) }
