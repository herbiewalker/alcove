package ingest

import (
	"bufio"
	"context"
	"database/sql"
	"strings"

	"github.com/herbiewalker/alcove/internal/photo"
	"github.com/herbiewalker/alcove/internal/trystbk"
)

// maxPhotoBytes bounds one stored photo. It matches the Tryst reader's own per-entry bound.
const maxPhotoBytes = 256 << 20

// PhotoResult says what became of the photos of an upload.
type PhotoResult struct {
	Stored, Skipped int
}

// photoRef is one encounter photo found in a backup: a media row that names an encounter, whose
// bytes are present in the container.
type photoRef struct{ id, entry, mime string }

// stored is a photo that was written, with its size and the picture format found in its bytes.
type stored struct {
	ref  photoRef
	size int64
	kind photo.Kind
}

func photoRefs(bk *trystbk.Backup) (refs []photoRef, skipped int) {
	t := bk.Table("media")
	if t == nil {
		return nil, 0
	}
	present := map[string]bool{}
	for _, m := range bk.Media() {
		present[strings.TrimPrefix(m.Name, trystbk.EntryMediaPrefix)] = true
	}
	seen := map[string]bool{}
	for _, r := range t.Rows {
		id, _ := r.String("id")
		enc, _ := r.String("encounterId")
		mime, _ := r.String("mimeType")
		if id == "" || enc == "" || seen[id] {
			continue // not an encounter photo (or a repeat): nothing to store
		}
		seen[id] = true
		if !present[id] {
			skipped++
			continue
		}
		refs = append(refs, photoRef{id: id, entry: enc, mime: mime})
	}
	return refs, skipped
}

// storePhotos replaces an owner's stored photos with the encounter photos of bk (their current
// upload). The files are encrypted under the owner's data key on the way in; the list of them is
// the derived media_blob table. It is best effort by design: a photo that cannot be stored is
// counted, not fatal, because the upload itself is already safe and a rebuild can try again.
func (s *Service) storePhotos(ctx context.Context, owner string, bk *trystbk.Backup) (PhotoResult, error) {
	var res PhotoResult
	refs, skipped := photoRefs(bk)
	res.Skipped = skipped
	if err := s.Blobs.DeleteMedia(owner); err != nil {
		return res, err
	}
	var done []stored
	if len(refs) > 0 {
		dek, err := s.St.DataKey(owner) // before any transaction: it uses the store's one connection
		if err != nil {
			return res, err
		}
		defer dek.Zero()
		key := dek.Bytes()
		defer func() {
			for i := range key {
				key[i] = 0
			}
		}()
		for _, ref := range refs {
			if ctx.Err() != nil {
				break
			}
			rc, err := bk.OpenMedia(trystbk.EntryMediaPrefix + ref.id)
			if err != nil {
				res.Skipped++
				continue
			}
			br := bufio.NewReaderSize(rc, 4096)
			head, _ := br.Peek(8)
			kind := photo.Sniff(head) // decided from the bytes, never from the label
			n, err := s.Blobs.PutMedia(owner, ref.id, key, br, maxPhotoBytes)
			_ = rc.Close()
			if err != nil {
				res.Skipped++
				continue
			}
			done = append(done, stored{ref, n, kind})
		}
	}
	err := s.St.Update(ctx, true, func(tx *sql.Tx) error {
		if _, e := tx.Exec(`DELETE FROM media_blob WHERE owner_id = ?`, owner); e != nil {
			return e
		}
		for _, d := range done {
			if _, e := tx.Exec(`INSERT INTO media_blob(owner_id, media_id, entry_id, mime, size, kind) VALUES (?,?,?,?,?,?)`,
				owner, d.ref.id, d.ref.entry, cleanMIME(d.ref.mime), d.size, int(d.kind)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	res.Stored = len(done)
	return res, nil
}

// cleanMIME keeps only a plausible content type word for display; what is served is decided from the bytes.
func cleanMIME(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if len(m) > 40 || !strings.HasPrefix(m, "image/") {
		return "image/unknown"
	}
	return m
}
