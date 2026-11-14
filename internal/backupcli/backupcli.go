// Package backupcli implements the command-line side of D-28: `alcove backup-verify`,
// `alcove restore` and `alcove drill`. They work on a backup file with no running server, which
// is the point: a restore must work when the server is gone. In a container, which has no shell,
// run them as `docker run --rm <image> drill ...`.
package backupcli

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/herbiewalker/alcove/internal/backup"
	"github.com/herbiewalker/alcove/internal/keyfile"
)

// loadOpts is how drill parses a wrapped key. Tests allow weak parameters.
var loadOpts = keyfile.ParseOptions{}

// Verify runs `alcove backup-verify -file F`: check every checksum in a backup. No key needed.
func Verify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backup-verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "the backup archive (.tar)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "backup-verify: -file is required")
		return 2
	}
	info, err := backup.Verify(*file)
	if err != nil {
		fmt.Fprintln(stderr, "backup-verify: FAILED:", err)
		return 1
	}
	fmt.Fprintf(stdout, "ok: %s verifies: %d files, every checksum matches\n", info.Name, info.Files)
	return 0
}

// Restore runs `alcove restore -backup F -to DIR`: verify, then extract into an empty directory.
// No key is needed, because the files stay encrypted.
func Restore(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("backup", "", "the backup archive (.tar)")
	to := fs.String("to", "", "an empty or missing directory to extract into (this becomes the data directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" || *to == "" {
		fmt.Fprintln(stderr, "restore: -backup and -to are required")
		return 2
	}
	info, err := backup.Restore(*file, *to)
	if err != nil {
		fmt.Fprintln(stderr, "restore: FAILED:", err)
		return 1
	}
	fmt.Fprintf(stdout, "restored %d files into %s\nStop the server, put this folder where the data directory was, and start it with the same key file.\n", info.Files, *to)
	return 0
}

// Drill runs `alcove drill -backup F -key K`: restore into a throwaway place, open it with the
// key, and read everything back. Running it with a COPY of the key proves the copy.
func Drill(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("drill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("backup", "", "the backup archive (.tar)")
	keyPath := fs.String("key", "", "the key file to test (a plain key, or a wrapped one with the passphrase on stdin)")
	tmp := fs.String("tmp", "", "where the opened database lives (must be a tmpfs; default under the system temp dir)")
	scratch := fs.String("scratch", "", "where the ciphertext is extracted for the test (default: the system temp dir)")
	allow := fs.Bool("allow-nontmpfs", false, "dev boxes only: accept a --tmp that is not a tmpfs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" || *keyPath == "" {
		fmt.Fprintln(stderr, "drill: -backup and -key are required")
		return 2
	}
	k, err := keyfile.Load(*keyPath, "", loadOpts)
	if err == keyfile.ErrNeedsPassphrase {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		pass := strings.TrimRight(line, "\r\n")
		if pass == "" {
			fmt.Fprintln(stderr, "drill: this key file is passphrase-wrapped; give the passphrase on the first line of stdin")
			return 2
		}
		k, err = keyfile.Load(*keyPath, pass, loadOpts)
	}
	if err != nil {
		fmt.Fprintln(stderr, "drill:", err)
		return 1
	}
	defer k.Zero()
	rep, err := backup.Drill(*file, k, backup.DrillOptions{Scratch: *scratch, TmpDir: *tmp, AllowNonTmpfs: *allow})
	if err != nil {
		fmt.Fprintln(stderr, "drill: FAILED:", err)
		for _, p := range rep.Problems {
			fmt.Fprintln(stderr, "  -", p)
		}
		return 1
	}
	fmt.Fprintf(stdout, "ok: key %s opens %s\n  admin log: %d entries, chain verifies\n  accounts: %d\n  uploads read back: %d (%d bytes)\n  photos decrypted: %d\n  took %s\nThe restore worked end to end. This is the proof a key copy needs.\n",
		keyfile.Fingerprint(k), rep.Backup.Name, rep.LogEntries, rep.Users, rep.UploadBlobs, rep.UploadBytes, rep.MediaBlobs, rep.Elapsed.Round(1e6))
	return 0
}
