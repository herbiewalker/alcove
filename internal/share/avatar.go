package share

import (
	"database/sql"
	"errors"

	"github.com/herbiewalker/alcove/internal/photo"
)

// Profile pictures and partner pictures (D-52). They are copied out of the owner's upload like
// photos (the derived avatar table) and are open to anyone in a space under the owner's "include
// photos" choice and the pause; outside a space only the owner (here) and administrators (in the
// server, behind its own gate) can reach them. A partner picture is deliberately NOT tied to the
// "show partner names" choice (R35).

// AvatarOf is a picture a viewer may be offered: its media id (the blob's name) and its format.
type AvatarOf struct {
	MediaID string
	Kind    photo.Kind
}

func viewable(k photo.Kind) bool { return k == photo.JPEG || k == photo.PNG }

// OwnAvatar is one of the person's own pictures: "profile" or "partner:<id>". It reads only their row.
func OwnAvatar(tx *sql.Tx, owner, subject string) (AvatarOf, error) {
	var a AvatarOf
	var k int
	err := tx.QueryRow(`SELECT media_id, kind FROM avatar WHERE owner_id = ? AND subject = ?`, owner, subject).Scan(&a.MediaID, &k)
	if errors.Is(err, sql.ErrNoRows) {
		return AvatarOf{}, ErrNoEntry
	}
	a.Kind = photo.Kind(k)
	return a, err
}

// OpenProfilePhoto lets a member of the space fetch another member's profile picture, only while
// that member is active, not paused, on the current wording and includes photos in this space, and
// only if the picture is a format Alcove can clean. Anything else is ErrNoEntry.
func OpenProfilePhoto(tx *sql.Tx, spaceID, viewerID string, consent int, ownerID string) (AvatarOf, error) {
	if err := check(tx, spaceID, viewerID, consent); err != nil {
		return AvatarOf{}, err
	}
	var a AvatarOf
	var k int
	err := tx.QueryRow(`SELECT a.media_id, a.kind FROM avatar a
		JOIN membership m ON m.user_id = a.owner_id AND m.space_id = ?
		JOIN space s ON s.id = m.space_id
		JOIN user u ON u.id = m.user_id
		WHERE a.owner_id = ? AND a.subject = 'profile'
		  AND m.state = 'active' AND m.paused = 0 AND m.consent_version >= ? AND m.include_photos = 1
		  AND s.deleted_at IS NULL AND u.status = 'active' AND u.deleted_at IS NULL`, spaceID, ownerID, consent).Scan(&a.MediaID, &k)
	if errors.Is(err, sql.ErrNoRows) {
		return AvatarOf{}, ErrNoEntry
	}
	if err != nil {
		return AvatarOf{}, err
	}
	a.Kind = photo.Kind(k)
	if !viewable(a.Kind) {
		return AvatarOf{}, ErrNoEntry
	}
	return a, nil
}

// OpenPartnerPhoto lets a member of the space fetch a partner's picture from another member's data,
// only through an entry of that member the space shows (the same scope as Entries), when the owner
// includes photos. The partner's name is not part of the test (R35).
func OpenPartnerPhoto(tx *sql.Tx, spaceID, viewerID string, consent int, ownerID, partnerID string) (AvatarOf, error) {
	if err := check(tx, spaceID, viewerID, consent); err != nil {
		return AvatarOf{}, err
	}
	where, args := scope(spaceID, consent, Query{})
	where += ` AND m.include_photos = 1 AND c.owner_id = ?
		AND EXISTS (SELECT 1 FROM cache_row ep WHERE ep.owner_id = c.owner_id AND ep.tbl = 'encounter_partner'
			AND json_extract(ep.data, '$.encounterId') = c.rid AND json_extract(ep.data, '$.partnerId') = ?)`
	args = append(args, ownerID, partnerID)
	var one int
	if err := tx.QueryRow(`SELECT 1`+where+` LIMIT 1`, args...).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AvatarOf{}, ErrNoEntry
		}
		return AvatarOf{}, err
	}
	a, err := OwnAvatar(tx, ownerID, "partner:"+partnerID)
	if err != nil {
		return AvatarOf{}, err
	}
	if !viewable(a.Kind) {
		return AvatarOf{}, ErrNoEntry
	}
	return a, nil
}

// PartnerPic is a partner's picture offered on an entry's page. Name is empty when the owner does not
// show partner names to this viewer; the picture is shown anyway (R35).
type PartnerPic struct{ ID, Name string }

// hasProfilePhoto reports whether the card of one member should show a picture to the viewer: the picture
// exists in a format that can be shown and the member includes photos in this space (or it is their own card).
func hasProfilePhoto(tx *sql.Tx, spaceID, ownerID string, mine bool) bool {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM avatar a JOIN membership m ON m.user_id = a.owner_id AND m.space_id = ?
		WHERE a.owner_id = ? AND a.subject = 'profile' AND a.kind IN (1, 2) AND (m.include_photos = 1 OR ?)`, spaceID, ownerID, mine).Scan(&one)
	return err == nil
}
