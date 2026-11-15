package share

import (
	"database/sql"
	"strings"
	"time"
)

// GalleryItem is one picture in a gallery, with the entry it belongs to (D-40).
type GalleryItem struct {
	OwnerID, OwnerName string
	EntryID, MediaID   string
	Caption            string
	When               time.Time // the entry's time, in its owner's time zone
	Mine               bool
}

// OwnGallery lists the person's own pictures that a browser can show, newest entry first. It reads
// only their rows.
func OwnGallery(tx *sql.Tx, owner string, limit, offset int) ([]GalleryItem, int, error) {
	const from = ` FROM media_blob b JOIN cache_row c ON c.owner_id = b.owner_id AND c.tbl = 'encounters' AND c.rid = b.entry_id
		WHERE b.owner_id = ? AND b.kind != 0 AND json_type(c.data, '$.startAt') IN ('integer','real')`
	var total int
	if err := tx.QueryRow(`SELECT COUNT(*)`+from, owner).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(`SELECT b.media_id, b.entry_id, json_extract(c.data, '$.startAt'), COALESCE(json_extract(m.data, '$.caption'), '')`+
		strings.Replace(from, "WHERE", "LEFT JOIN cache_row m ON m.owner_id = b.owner_id AND m.tbl = 'media' AND m.rid = b.media_id WHERE", 1)+
		` ORDER BY json_extract(c.data, '$.startAt') DESC, b.entry_id, COALESCE(m.pos, 0), b.media_id LIMIT ? OFFSET ?`, owner, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	loc := zones{}.get(ZoneName(tx, owner))
	var out []GalleryItem
	for rows.Next() {
		var it GalleryItem
		var ms float64
		if err := rows.Scan(&it.MediaID, &it.EntryID, &ms, &it.Caption); err != nil {
			return nil, 0, err
		}
		it.OwnerID, it.Mine = owner, true
		it.Caption = strings.TrimSpace(it.Caption)
		it.When = time.UnixMilli(int64(ms)).In(loc)
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// Gallery lists the pictures a space shows the viewer: the same member conditions and private flag
// as Entries, only from members who include photos, and only formats Alcove can clean of metadata
// (the same rule OpenPhoto applies). A caption is shown only if its owner includes notes. A place
// is never listed here.
func Gallery(tx *sql.Tx, spaceID, viewerID string, consent int, limit, offset int) ([]GalleryItem, int, error) {
	if err := check(tx, spaceID, viewerID, consent); err != nil {
		return nil, 0, err
	}
	where, args := scope(spaceID, consent, Query{})
	where += ` AND m.include_photos = 1 AND EXISTS (SELECT 1 FROM media_blob x WHERE x.owner_id = c.owner_id AND x.entry_id = c.rid AND x.kind IN (1, 2))`
	// One row per picture: the entry rows joined to their pictures.
	from := strings.Replace(where, "WHERE c.tbl", `JOIN media_blob b ON b.owner_id = c.owner_id AND b.entry_id = c.rid AND b.kind IN (1, 2)
		LEFT JOIN cache_row md ON md.owner_id = b.owner_id AND md.tbl = 'media' AND md.rid = b.media_id
		WHERE c.tbl`, 1)
	var total int
	if err := tx.QueryRow(`SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sel := `SELECT c.owner_id, u.display_name, u.username, c.rid, b.media_id, json_extract(c.data, '$.startAt'), m.include_notes,
		COALESCE(json_extract(md.data, '$.caption'), ''), COALESCE((SELECT tz FROM user_tz z WHERE z.user_id = c.owner_id), '')` + from +
		` ORDER BY json_extract(c.data, '$.startAt') DESC, c.owner_id, c.rid, COALESCE(md.pos, 0), b.media_id LIMIT ? OFFSET ?`
	rows, err := tx.Query(sel, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	zs := zones{}
	var out []GalleryItem
	for rows.Next() {
		var it GalleryItem
		var display, username, caption, tz string
		var ms float64
		var notes int
		if err := rows.Scan(&it.OwnerID, &display, &username, &it.EntryID, &it.MediaID, &ms, &notes, &caption, &tz); err != nil {
			return nil, 0, err
		}
		it.OwnerName = display
		if it.OwnerName == "" {
			it.OwnerName = username
		}
		if notes == 1 {
			it.Caption = strings.TrimSpace(caption)
		}
		it.When = time.UnixMilli(int64(ms)).In(zs.get(tz))
		it.Mine = it.OwnerID == viewerID
		out = append(out, it)
	}
	return out, total, rows.Err()
}
