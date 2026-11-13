// Package keycli implements the `alcove keygen` and `alcove keycheck` commands.
//
// The server never creates a key on its own (a silently created key that was never backed up is
// the one unrecoverable mistake), so the operator runs these deliberately. In a container, which
// has no shell, that is `docker run --rm <image> keygen ...`.
package keycli

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/herbiewalker/alcove/internal/keyfile"
)

// wrapParams is the Argon2id setting used by keygen -wrap. Tests lower it.
var wrapParams = keyfile.DefaultParams()

// loadOpts is how keycheck parses a wrapped key. Tests allow weak parameters.
var loadOpts = keyfile.ParseOptions{}

const reminder = `
Save a second copy of this key somewhere that is not this machine (for example a password
manager), then prove the copy with:  alcove keycheck -file <the copy>
Anyone holding the key can read your data. If every copy is lost, the data cannot be recovered.
Keep the key file OUTSIDE the data directory; the server refuses to start otherwise.
`

// Keygen runs `alcove keygen`. It returns the process exit code.
//
//	alcove keygen                 print a new key line on stdout
//	alcove keygen -out PATH       write a new key file (refuses to overwrite one)
//	alcove keygen -wrap           also wrap it with a passphrase read from stdin (locked mode)
func Keygen(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "write the key to this file instead of printing it (never overwrites)")
	wrap := fs.Bool("wrap", false, "protect the key with a passphrase read from the first line of stdin (locked mode)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	k, line, err := keyfile.Generate()
	if err != nil {
		fmt.Fprintln(stderr, "keygen:", err)
		return 1
	}
	defer k.Zero()
	if *wrap {
		pass, err := readLine(stdin)
		if err != nil || pass == "" {
			fmt.Fprintln(stderr, "keygen: -wrap needs a passphrase on the first line of stdin")
			return 2
		}
		if line, err = keyfile.Wrap(k, pass, wrapParams); err != nil {
			fmt.Fprintln(stderr, "keygen:", err)
			return 1
		}
	}
	fp := keyfile.Fingerprint(k)
	if *out != "" {
		if err := keyfile.WriteNew(*out, line); err != nil {
			fmt.Fprintln(stderr, "keygen:", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s\nfingerprint: %s\n", *out, fp)
	} else {
		fmt.Fprintln(stdout, line)
		fmt.Fprintf(stderr, "fingerprint: %s\n", fp)
	}
	fmt.Fprint(stderr, reminder)
	return 0
}

// Keycheck runs `alcove keycheck -file PATH`: it opens a key file and prints its fingerprint, so
// a second copy can be proved without showing the key. A wrapped file reads its passphrase from
// the first line of stdin.
func Keycheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("keycheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "the key file to open")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "keycheck: -file is required")
		return 2
	}
	k, err := keyfile.Load(*file, "", loadOpts)
	kind := "plain"
	if err == keyfile.ErrNeedsPassphrase {
		kind = "passphrase-wrapped"
		pass, perr := readLine(stdin)
		if perr != nil || pass == "" {
			fmt.Fprintln(stderr, "keycheck: this key file is passphrase-wrapped; give the passphrase on the first line of stdin")
			return 2
		}
		k, err = keyfile.Load(*file, pass, loadOpts)
	}
	if err != nil {
		fmt.Fprintln(stderr, "keycheck:", err)
		return 1
	}
	defer k.Zero()
	fmt.Fprintf(stdout, "ok: %s key file, fingerprint %s\n", kind, keyfile.Fingerprint(k))
	return 0
}

func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
