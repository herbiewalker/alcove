// Package backup implements Alcove's instance backup and restore drill (D-28, F23).
//
// A backup is one tar archive of the files in the data directory: manifest.json, the sealed
// database and every blob. All of them are already ciphertext under the master key, so there is no
// second encryption layer; the archive adds a last entry BACKUP.json (every file's size and SHA-256)
// and a sidecar <name>.sha256 for the whole archive. A backup is useless without the master key,
// which is why the key's second copy is proved by restoring a real backup with it (Drill).
//
// Hard constraint 4: nothing here ever writes plaintext. Restore and Drill extract ciphertext only;
// the drill's decrypted working copy lives on the store's tmpfs scratch directory.
package backup

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/store"
)

const (
	// Prefix and Suffix frame every backup file name: alcove-backup-<UTC stamp>-<store id>.tar
	Prefix    = "alcove-backup-"
	Suffix    = ".tar"
	sidecar   = ".sha256"
	indexName = "BACKUP.json"
	formatVer = 1

	manifestName = "manifest.json"
	sealedName   = "alcove.sealed"
	stampLayout  = "20060102T150405Z"

	maxIndexBytes = 64 << 20 // an index of a million files is far below this
)

// Errors.
var (
	ErrNoDir       = errors.New("backup: no backup directory is configured or it is not a directory")
	ErrInsideData  = errors.New("backup: the backup directory must not be inside the data directory")
	ErrCorrupt     = errors.New("backup: the archive is damaged or was changed")
	ErrBadEntry    = errors.New("backup: the archive holds an entry it should not")
	ErrTargetBusy  = errors.New("backup: the restore target must be an empty directory that does not exist yet")
	ErrNoSuchBackp = errors.New("backup: no such backup")
)

type fileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type index struct {
	Format  int         `json:"format"`
	Created string      `json:"created"`
	StoreID string      `json:"store_id"`
	Files   []fileEntry `json:"files"`
}

// Info describes one backup file.
type Info struct {
	Name    string
	Path    string
	Size    int64 // archive size on disk
	Created time.Time
	Files   int   // files inside (0 if not read)
	Bytes   int64 // payload bytes inside (0 if not read)
	StoreID string
	// Sidecar is true when the <name>.sha256 file exists next to the archive.
	Sidecar bool
}

// safeName reports whether an archive entry name is one this format may contain.
func safeName(n string) bool {
	switch n {
	case manifestName, sealedName, indexName:
		return true
	}
	if !strings.HasPrefix(n, "blobs/") || path.Clean(n) != n || strings.ContainsAny(n, "\\\x00") {
		return false
	}
	parts := strings.Split(n, "/")
	if len(parts) != 4 { // blobs/<owner>/<kind>/<id>
		return false
	}
	for _, p := range parts[1:] {
		if p == "" || p == "." || p == ".." || len(p) > 128 {
			return false
		}
	}
	return !strings.HasPrefix(parts[3], ".incoming-")
}

func within(parent, child string) bool {
	p, err1 := filepath.Abs(parent)
	c, err2 := filepath.Abs(child)
	if err1 != nil || err2 != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if r, err := filepath.EvalSymlinks(c); err == nil {
		c = r
	}
	rel, err := filepath.Rel(p, c)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Create writes a new backup of the open store into dir and returns its description. The caller
// holds whatever lock keeps two backups from running at once.
func Create(st *store.Store, dir string, now time.Time) (Info, error) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return Info{}, ErrNoDir
	}
	if within(st.DataDir(), dir) {
		return Info{}, ErrInsideData
	}
	created := now.UTC().Truncate(time.Second)
	var idx index
	idx.Format, idx.Created = formatVer, created.Format(time.RFC3339)

	// Name and open the destination first so a bad directory fails before the store is touched.
	manifestRaw, err := os.ReadFile(filepath.Join(st.DataDir(), manifestName))
	if err != nil {
		return Info{}, fmt.Errorf("backup: read manifest: %w", err)
	}
	var man struct {
		StoreID string `json:"store_id"`
	}
	if err := json.Unmarshal(manifestRaw, &man); err != nil || !safeID(man.StoreID) {
		return Info{}, errors.New("backup: the manifest is unreadable")
	}
	idx.StoreID = man.StoreID
	short := man.StoreID
	if len(short) > 8 {
		short = short[:8]
	}
	final := filepath.Join(dir, Prefix+created.Format(stampLayout)+"-"+short+Suffix)
	if _, err := os.Stat(final); err == nil {
		return Info{}, fmt.Errorf("backup: %s already exists (backups are never overwritten)", filepath.Base(final))
	}
	part := final + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Info{}, fmt.Errorf("backup: create archive: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(part)
		}
	}()
	whole := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(f, whole))

	add := func(name string, src io.Reader, size int64) error {
		h := sha256.New()
		hdr := &tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: created, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		n, err := io.Copy(io.MultiWriter(tw, h), src)
		if err != nil {
			return err
		}
		if n != size {
			return fmt.Errorf("backup: %s changed while it was being copied", name)
		}
		idx.Files = append(idx.Files, fileEntry{Name: name, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))})
		return nil
	}
	addFile := func(name, p string) error {
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		info, err := src.Stat()
		if err != nil {
			return err
		}
		return add(name, src, info.Size())
	}

	// The database first, under the seal lock, so the manifest and the sealed file belong together.
	err = st.SealedSnapshot(func(data string) error {
		if err := addFile(manifestName, filepath.Join(data, manifestName)); err != nil {
			return err
		}
		return addFile(sealedName, filepath.Join(data, sealedName))
	})
	if err != nil {
		return Info{}, fmt.Errorf("backup: snapshot: %w", err)
	}
	// Blobs second (D-28: a blob is written before the row that mentions it), in a stable order.
	var blobs []string
	root := filepath.Join(st.DataDir(), "blobs")
	werr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(st.DataDir(), p)
		name := filepath.ToSlash(rel)
		if safeName(name) {
			blobs = append(blobs, name)
		}
		return nil
	})
	if werr != nil {
		return Info{}, fmt.Errorf("backup: list blobs: %w", werr)
	}
	sort.Strings(blobs)
	for _, name := range blobs {
		if err := addFile(name, filepath.Join(st.DataDir(), filepath.FromSlash(name))); err != nil {
			return Info{}, fmt.Errorf("backup: copy %s: %w", name, err)
		}
	}
	raw, _ := json.Marshal(idx)
	if err := add(indexName, strings.NewReader(string(raw)), int64(len(raw))); err != nil {
		return Info{}, err
	}
	idx.Files = idx.Files[:len(idx.Files)-1] // the index does not list itself
	if err := tw.Close(); err != nil {
		return Info{}, err
	}
	if err := f.Sync(); err != nil {
		return Info{}, err
	}
	if err := f.Close(); err != nil {
		return Info{}, err
	}
	if err := os.Rename(part, final); err != nil {
		return Info{}, err
	}
	ok = true
	side := hex.EncodeToString(whole.Sum(nil)) + "  " + filepath.Base(final) + "\n"
	if err := os.WriteFile(final+sidecar, []byte(side), 0o600); err != nil {
		return Info{}, fmt.Errorf("backup: write checksum file: %w", err)
	}
	return describe(final, idx), nil
}

func safeID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func describe(p string, idx index) Info {
	info := Info{Name: filepath.Base(p), Path: p, StoreID: idx.StoreID, Files: len(idx.Files)}
	if t, err := time.Parse(time.RFC3339, idx.Created); err == nil {
		info.Created = t
	}
	for _, f := range idx.Files {
		info.Bytes += f.Size
	}
	if st, err := os.Stat(p); err == nil {
		info.Size = st.Size()
	}
	_, err := os.Stat(p + sidecar)
	info.Sidecar = err == nil
	return info
}

// scan reads the archive once, calls visit for every entry with its content, and returns the
// index. visit may be nil.
func scan(archive string, visit func(name string, size int64, r io.Reader) error) (index, error) {
	f, err := os.Open(archive)
	if err != nil {
		return index{}, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	seen := map[string]fileEntry{}
	var idx index
	haveIndex := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return index{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		if hdr.Typeflag != tar.TypeReg || !safeName(hdr.Name) {
			return index{}, fmt.Errorf("%w: %q", ErrBadEntry, hdr.Name)
		}
		if _, dup := seen[hdr.Name]; dup || haveIndex {
			return index{}, fmt.Errorf("%w: %q appears twice or after the index", ErrBadEntry, hdr.Name)
		}
		if hdr.Name == indexName {
			if hdr.Size > maxIndexBytes {
				return index{}, fmt.Errorf("%w: index too large", ErrCorrupt)
			}
			raw, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
			if err != nil || int64(len(raw)) != hdr.Size {
				return index{}, fmt.Errorf("%w: short index", ErrCorrupt)
			}
			if err := json.Unmarshal(raw, &idx); err != nil || idx.Format != formatVer {
				return index{}, fmt.Errorf("%w: unreadable index", ErrCorrupt)
			}
			haveIndex = true
			continue
		}
		h := sha256.New()
		var src io.Reader = io.TeeReader(io.LimitReader(tr, hdr.Size), h)
		if visit != nil {
			if err := visit(hdr.Name, hdr.Size, src); err != nil {
				return index{}, err
			}
		}
		_, err = io.Copy(io.Discard, src) // drain whatever visit did not read
		if err != nil {
			return index{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		seen[hdr.Name] = fileEntry{Name: hdr.Name, Size: hdr.Size, SHA256: hex.EncodeToString(h.Sum(nil))}
	}
	if !haveIndex {
		return index{}, fmt.Errorf("%w: no index (truncated?)", ErrCorrupt)
	}
	if len(idx.Files) != len(seen) {
		return index{}, fmt.Errorf("%w: the index lists %d files, the archive holds %d", ErrCorrupt, len(idx.Files), len(seen))
	}
	for _, want := range idx.Files {
		got, ok := seen[want.Name]
		if !ok || got.Size != want.Size || got.SHA256 != want.SHA256 {
			return index{}, fmt.Errorf("%w: %s does not match its recorded hash", ErrCorrupt, want.Name)
		}
	}
	for _, need := range []string{manifestName, sealedName} {
		if _, ok := seen[need]; !ok {
			return index{}, fmt.Errorf("%w: %s is missing", ErrCorrupt, need)
		}
	}
	return idx, nil
}

// Verify re-reads an archive and checks every file against the index, and the whole archive
// against its sidecar when present. It needs no key.
func Verify(archive string) (Info, error) {
	idx, err := scan(archive, nil)
	if err != nil {
		return Info{}, err
	}
	// The whole-archive hash covers the tar padding too, so compute it from the file itself.
	if raw, err := os.ReadFile(archive + sidecar); err == nil {
		want := strings.Fields(string(raw))
		if len(want) == 0 {
			return Info{}, fmt.Errorf("%w: empty checksum file", ErrCorrupt)
		}
		whole, err := fileSHA256(archive)
		if err != nil {
			return Info{}, err
		}
		if want[0] != whole {
			return Info{}, fmt.Errorf("%w: the archive does not match its checksum file", ErrCorrupt)
		}
	}
	return describe(archive, idx), nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Restore verifies an archive and extracts its files into to, which must not exist or be empty.
// It needs no key: the files stay ciphertext. BACKUP.json is not extracted.
func Restore(archive, to string) (Info, error) {
	info, err := Verify(archive)
	if err != nil {
		return Info{}, err
	}
	if st, err := os.Stat(to); err == nil {
		if !st.IsDir() {
			return Info{}, ErrTargetBusy
		}
		ents, err := os.ReadDir(to)
		if err != nil || len(ents) > 0 {
			return Info{}, ErrTargetBusy
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Info{}, err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return Info{}, err
	}
	_, err = scan(archive, func(name string, size int64, r io.Reader) error {
		dst := filepath.Join(to, filepath.FromSlash(name))
		if !within(to, filepath.Dir(dst)) && filepath.Dir(dst) != filepath.Clean(to) {
			return fmt.Errorf("%w: %q", ErrBadEntry, name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, r); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
	if err != nil {
		_ = os.RemoveAll(to)
		return Info{}, err
	}
	return info, nil
}

// List returns the backups in dir, newest first. It reads names and sizes only; Verify checks
// content.
func List(dir string) ([]Info, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, ErrNoDir
	}
	var out []Info
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, Prefix) || !strings.HasSuffix(n, Suffix) {
			continue
		}
		stamp := strings.TrimPrefix(n, Prefix)
		if len(stamp) < len(stampLayout) {
			continue
		}
		t, err := time.Parse(stampLayout, stamp[:len(stampLayout)])
		if err != nil {
			continue
		}
		info := Info{Name: n, Path: filepath.Join(dir, n), Created: t}
		if st, err := e.Info(); err == nil {
			info.Size = st.Size()
		}
		if _, err := os.Stat(info.Path + sidecar); err == nil {
			info.Sidecar = true
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// Find returns the backup called name in dir; name must be a bare file name from List.
func Find(dir, name string) (Info, error) {
	if name != filepath.Base(name) || !strings.HasPrefix(name, Prefix) || !strings.HasSuffix(name, Suffix) {
		return Info{}, ErrNoSuchBackp
	}
	list, err := List(dir)
	if err != nil {
		return Info{}, err
	}
	for _, i := range list {
		if i.Name == name {
			return i, nil
		}
	}
	return Info{}, ErrNoSuchBackp
}

// Prune deletes all but the newest keep backups (never fewer than one) and returns their names.
// Call it only after a new backup has verified.
func Prune(dir string, keep int) ([]string, error) {
	if keep < 1 {
		keep = 1
	}
	list, err := List(dir)
	if err != nil {
		return nil, err
	}
	var gone []string
	for i, b := range list {
		if i < keep {
			continue
		}
		if err := os.Remove(b.Path); err != nil {
			return gone, err
		}
		_ = os.Remove(b.Path + sidecar)
		gone = append(gone, b.Name)
	}
	return gone, nil
}

// CleanStale removes leftovers of an interrupted backup or drill: *.part files and .drill-*
// folders older than age. It never touches a finished backup.
func CleanStale(dir string, age time.Duration) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, ErrNoDir
	}
	n := 0
	for _, e := range ents {
		name := e.Name()
		isPart := !e.IsDir() && strings.HasPrefix(name, Prefix) && strings.HasSuffix(name, ".part")
		isDrill := e.IsDir() && strings.HasPrefix(name, ".drill-")
		if !isPart && !isDrill {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < age {
			continue
		}
		if os.RemoveAll(filepath.Join(dir, name)) == nil {
			n++
		}
	}
	return n, nil
}
