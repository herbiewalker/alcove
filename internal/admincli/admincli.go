// Package admincli implements `alcove reset-password`, the break-glass way back in when the only
// administrator has lost their password (D-30, closing the M2b gap).
//
// It opens the store directly with the key file, so it is only possible for someone who can run a
// container with the data folder and the key, which is exactly who is trusted to run the server.
// Because two processes must never write the same sealed database, the data directory is locked:
// run it with the server stopped. If the server is running the command is refused and changes
// nothing.
package admincli

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/app"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/config"
	"github.com/herbiewalker/alcove/internal/store"
)

// Params is the Argon2id setting used for the new hash. Tests lower it.
var Params = auth.DefaultParams

// ResetPassword runs `alcove reset-password -user NAME [-generate]`. Standard input carries, in
// order: the key passphrase (only if the key file is wrapped) and the new password (unless
// -generate). The password is never an argument, so it is not in the process list or a history.
func ResetPassword(args []string, cfg config.Config, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	user := fs.String("user", "", "the account to reset")
	gen := fs.Bool("generate", false, "make a random temporary password and print it, instead of reading one")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *user == "" {
		fmt.Fprintln(stderr, "reset-password: -user is required")
		return 2
	}
	in := bufio.NewReader(stdin)
	line := func() string {
		s, _ := in.ReadString('\n')
		return strings.TrimRight(s, "\r\n")
	}

	a, err := app.Start(cfg, app.Options{})
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			fmt.Fprintln(stderr, "reset-password: the server is running on this data folder. Stop it first (for example `docker compose stop`), run this again, then start it. Nothing was changed.")
			return 1
		}
		fmt.Fprintln(stderr, "reset-password:", err)
		return 1
	}
	defer a.Close()
	if a.State() == app.StateLocked {
		pass := line()
		if pass == "" {
			fmt.Fprintln(stderr, "reset-password: the key file is passphrase-wrapped; give the passphrase on the first line of standard input")
			return 2
		}
		if err := a.Unlock(pass); err != nil {
			if errors.Is(err, store.ErrLocked) {
				fmt.Fprintln(stderr, "reset-password: the server is running on this data folder. Stop it first. Nothing was changed.")
				return 1
			}
			fmt.Fprintln(stderr, "reset-password: the key passphrase did not open the key")
			return 1
		}
	}

	var pw string
	if *gen {
		if pw, err = auth.TempPassword(); err != nil {
			fmt.Fprintln(stderr, "reset-password:", err)
			return 1
		}
	} else {
		if pw = line(); pw == "" {
			fmt.Fprintln(stderr, "reset-password: give the new password on a line of standard input, or use -generate")
			return 2
		}
		if err := auth.CheckPassword(pw, *user); err != nil {
			fmt.Fprintln(stderr, "reset-password:", strings.TrimPrefix(err.Error(), "password policy: "))
			return 2
		}
	}
	hash, err := auth.Hash(pw, Params)
	if err != nil {
		fmt.Fprintln(stderr, "reset-password:", err)
		return 1
	}

	var target auth.User
	err = a.Store().Update(nil2ctx(), true, func(tx *sql.Tx) error {
		var e error
		if target, e = auth.ByUsername(tx, *user); e != nil {
			return e
		}
		if target.Deleted() {
			return errDeleted
		}
		if e = auth.SetPasswordForced(tx, target.ID, hash, true); e != nil {
			return e
		}
		_, e = adminlog.Append(tx, time.Now(), adminlog.System, "user.password.reset.cli", "user", target.ID, "password")
		return e
	})
	switch {
	case errors.Is(err, auth.ErrNoUser):
		fmt.Fprintf(stderr, "reset-password: there is no account named %q. Nothing was changed.\n", *user)
		return 1
	case errors.Is(err, errDeleted):
		fmt.Fprintln(stderr, "reset-password: that account is deleted; restore it from the People page first. Nothing was changed.")
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "reset-password:", err)
		return 1
	}
	fmt.Fprintf(stdout, "ok: the password for @%s was reset and must be changed at the next sign-in.\n", target.Username)
	if *gen {
		fmt.Fprintf(stdout, "temporary password (shown only now): %s\n", pw)
	}
	if target.Status == auth.StatusDisabled {
		fmt.Fprintln(stdout, "note: the account is disabled; enable it from the People page.")
	}
	fmt.Fprintln(stdout, "Start the server again. The reset is recorded in the admin log.")
	return 0
}

var errDeleted = errors.New("deleted")

func nil2ctx() context.Context { return context.Background() }
