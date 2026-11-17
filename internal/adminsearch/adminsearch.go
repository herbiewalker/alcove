// Package adminsearch is the administrator's search over everything (F17, D-17, D-46).
//
// It deliberately reads past the sharing gate: the administrator can see every person's current
// notes and partner names whatever they share, private or paused. That is why it is its own
// package and not part of internal/share (D-32: share is the one gate for OTHER MEMBERS). The only
// caller is the admin handler, which writes the audit line in the same transaction.
package adminsearch

import (
	"database/sql"
	"time"

	"github.com/herbiewalker/alcove/internal/share"
)

// Hit is one entry that matched.
type Hit struct {
	OwnerID, OwnerName, EntryID string
	When                        time.Time // in the owner's own time zone
	Segs                        []share.Seg
	Partner                     bool // matched a partner's name or note, not the entry's note
}

// Search finds current entries whose note matches, and entries of a partner whose name or note
// matches, for every person. At most limit entries come back, newest first, each once.
// Entries a person deleted are not in the index (the delete removes them), so they are not found
// here; the person's own "recently deleted" page lists them.
func Search(tx *sql.Tx, raw string, limit int) ([]Hit, error) {
	match, err := share.Match(raw)
	if err != nil {
		return nil, err
	}
	seen := map[[2]string]bool{}
	var out []Hit
	collect := func(q string, partner bool) error {
		rows, err := tx.Query(q, match, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var owner, display, username, id, tz, sn string
			var start float64
			if err := rows.Scan(&owner, &display, &username, &id, &start, &tz, &sn); err != nil {
				return err
			}
			k := [2]string{owner, id}
			if seen[k] {
				continue
			}
			seen[k] = true
			loc := share.LocationByName(tz)
			name := display
			if name == "" {
				name = username
			}
			out = append(out, Hit{OwnerID: owner, OwnerName: name, EntryID: id, When: time.UnixMilli(int64(start)).In(loc), Segs: share.Segments(sn), Partner: partner})
		}
		return rows.Err()
	}
	const cols = `SELECT c.owner_id, u.display_name, u.username, c.rid, json_extract(c.data, '$.startAt'),
		COALESCE((SELECT tz FROM user_tz z WHERE z.user_id = c.owner_id), ''), ` + share.SnippetExpr
	const user = ` JOIN user u ON u.id = c.owner_id`
	const timed = ` AND json_type(c.data, '$.startAt') IN ('integer','real') ORDER BY json_extract(c.data, '$.startAt') DESC LIMIT ?`
	if err := collect(cols+` FROM search JOIN cache_row c ON c.owner_id = search.owner_id AND c.tbl = 'encounters' AND c.rid = search.ref`+user+
		` WHERE search MATCH ? AND search.kind = 'encounter'`+timed, false); err != nil {
		return nil, err
	}
	if err := collect(cols+` FROM search
		JOIN cache_row ep ON ep.owner_id = search.owner_id AND ep.tbl = 'encounter_partner' AND json_extract(ep.data, '$.partnerId') = search.ref
		JOIN cache_row c ON c.owner_id = ep.owner_id AND c.tbl = 'encounters' AND c.rid = json_extract(ep.data, '$.encounterId')`+user+
		` WHERE search MATCH ? AND search.kind = 'partner'`+timed, true); err != nil {
		return nil, err
	}
	sortNewest(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func sortNewest(h []Hit) {
	for i := 1; i < len(h); i++ {
		for j := i; j > 0 && h[j].When.After(h[j-1].When); j-- {
			h[j], h[j-1] = h[j-1], h[j]
		}
	}
}
