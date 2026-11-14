package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/blobstore"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

// ErrDrillFailed wraps the list of problems a drill found.
var ErrDrillFailed = errors.New("backup: the restore drill failed")

// DrillOptions say where a drill may work.
type DrillOptions struct {
	// Scratch is a directory (default: the system temp dir) under which the ciphertext is extracted
	// into a throwaway folder that is removed afterwards.
	Scratch string
	// TmpDir is where the opened database lives while it is checked. In production it must be a
	// tmpfs (decrypted data must never reach disk); default is a folder under Scratch.
	TmpDir string
	// AllowNonTmpfs lets TmpDir be an ordinary folder. Tests and dev boxes only.
	AllowNonTmpfs bool
}

// Report is what a drill found. It holds counts only, never content (constraint 8).
type Report struct {
	Backup      Info
	Users       int
	LogEntries  int64
	UploadBlobs int
	UploadBytes int64
	MediaBlobs  int
	Elapsed     time.Duration
	Problems    []string
}

// Drill proves a backup can really be restored with a given key: it extracts the archive into a
// scratch folder, opens the store with the key, verifies the admin-log chain, counts accounts,
// re-reads every upload and decrypts every photo from the first byte to the last. The scratch
// folder is always removed. A key copy is proved by running this with that copy (D-28).
func Drill(archive string, key keyfile.Key, opt DrillOptions) (Report, error) {
	start := time.Now()
	var rep Report
	scratch, err := os.MkdirTemp(opt.Scratch, ".drill-")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(scratch)

	data := filepath.Join(scratch, "data")
	if rep.Backup, err = Restore(archive, data); err != nil {
		return rep, err
	}
	tmp := opt.TmpDir
	if tmp == "" {
		tmp = filepath.Join(scratch, "tmp")
	}
	st, err := store.Open(store.Config{DataDir: data, TmpDir: tmp, Key: key, AllowNonTmpfsTmp: opt.AllowNonTmpfs, SealEvery: time.Hour})
	if err != nil {
		return rep, fmt.Errorf("backup: this key does not open the backup: %w", err)
	}
	defer st.Close()

	problem := func(format string, a ...any) { rep.Problems = append(rep.Problems, fmt.Sprintf(format, a...)) }
	ctx := context.Background()
	if err := st.View(ctx, func(tx *sql.Tx) error {
		res, verr := adminlog.Verify(tx)
		if verr != nil {
			problem("admin log: %v", verr)
		}
		rep.LogEntries = res.Entries
		n, err := auth.Count(tx)
		rep.Users = n
		return err
	}); err != nil {
		return rep, err
	}

	blobs := blobstore.New(data)
	root := filepath.Join(data, "blobs")
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		parts := strings.Split(filepath.ToSlash(rel), "/") // <owner>/<kind>/<id>
		if len(parts) != 3 {
			return nil
		}
		owner, kind, id := parts[0], parts[1], parts[2]
		switch kind {
		case "u":
			f, err := os.Open(p)
			if err != nil {
				problem("upload %s/%s: %v", owner, id, err)
				return nil
			}
			h := sha256.New()
			n, cerr := io.Copy(h, f)
			_ = f.Close()
			if cerr != nil {
				problem("upload %s/%s: %v", owner, id, cerr)
				return nil
			}
			rep.UploadBlobs++
			rep.UploadBytes += n
		case "m":
			dek, derr := st.DataKey(owner)
			if derr != nil {
				problem("photo %s/%s: no data key for the owner", owner, id)
				return nil
			}
			rc, oerr := blobs.OpenMedia(owner, id, dek.Bytes())
			dek.Zero()
			if oerr != nil {
				problem("photo %s/%s: %v", owner, id, oerr)
				return nil
			}
			_, cerr := io.Copy(io.Discard, rc)
			_ = rc.Close()
			if cerr != nil {
				problem("photo %s/%s: %v", owner, id, cerr)
				return nil
			}
			rep.MediaBlobs++
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	// M3 piece B adds the `upload` table; this is where each stored upload's recorded SHA-256 is
	// compared with the blob it points to.
	rep.Elapsed = time.Since(start)
	if len(rep.Problems) > 0 {
		return rep, fmt.Errorf("%w: %d problem(s), first: %s", ErrDrillFailed, len(rep.Problems), rep.Problems[0])
	}
	return rep, nil
}
