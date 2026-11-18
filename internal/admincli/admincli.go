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
	"github.com/herbiewalker/alcove/internal/totp"
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

	a, code := openApp("reset-password", cfg, line, stderr)
	if a == nil {
		return code
	}
	defer a.Close()

	var pw string
	var err error
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

// openApp opens the sealed store the way the server would, for a break-glass command. It returns the
// app, or nil and the exit code (after saying why). The caller closes the app.
func openApp(name string, cfg config.Config, line func() string, stderr io.Writer) (*app.App, int) {
	a, err := app.Start(cfg, app.Options{})
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			fmt.Fprintf(stderr, "%s: the server is running on this data folder. Stop it first (for example `docker compose stop`), run this again, then start it. Nothing was changed.\n", name)
			return nil, 1
		}
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return nil, 1
	}
	if a.State() == app.StateLocked {
		pass := line()
		if pass == "" {
			a.Close()
			fmt.Fprintf(stderr, "%s: the key file is passphrase-wrapped; give the passphrase on the first line of standard input\n", name)
			return nil, 2
		}
		if err := a.Unlock(pass); err != nil {
			a.Close()
			if errors.Is(err, store.ErrLocked) {
				fmt.Fprintf(stderr, "%s: the server is running on this data folder. Stop it first. Nothing was changed.\n", name)
				return nil, 1
			}
			fmt.Fprintf(stderr, "%s: the key passphrase did not open the key\n", name)
			return nil, 1
		}
	}
	return a, 0
}

// ResetTwoStep runs `alcove reset-2fa -user NAME`: it turns two-step sign-in off for one person and
// forgets their secret and recovery codes, for the day the phone and the recovery codes are both gone
// (D-49). Same trust and same rules as reset-password: it needs the data folder and the key, and the
// server must be stopped. It does not touch the password, which is a separate command on purpose.
func ResetTwoStep(args []string, cfg config.Config, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reset-2fa", flag.ContinueOnError)
	fs.SetOutput(stderr)
	user := fs.String("user", "", "the account whose two-step sign-in to turn off")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *user == "" {
		fmt.Fprintln(stderr, "reset-2fa: -user is required")
		return 2
	}
	in := bufio.NewReader(stdin)
	line := func() string {
		s, _ := in.ReadString('\n')
		return strings.TrimRight(s, "\r\n")
	}
	a, code := openApp("reset-2fa", cfg, line, stderr)
	if a == nil {
		return code
	}
	defer a.Close()

	var target auth.User
	var was bool
	err := a.Store().Update(nil2ctx(), true, func(tx *sql.Tx) error {
		var e error
		if target, e = auth.ByUsername(tx, *user); e != nil {
			return e
		}
		if target.Deleted() {
			return errDeleted
		}
		if was, e = totp.Enabled(tx, target.ID); e != nil || !was {
			return e
		}
		if e = totp.Disable(tx, target.ID); e != nil {
			return e
		}
		_, e = adminlog.Append(tx, time.Now(), adminlog.System, "user.totp.reset.cli", "user", target.ID, "totp")
		return e
	})
	switch {
	case errors.Is(err, auth.ErrNoUser):
		fmt.Fprintf(stderr, "reset-2fa: there is no account named %q. Nothing was changed.\n", *user)
		return 1
	case errors.Is(err, errDeleted):
		fmt.Fprintln(stderr, "reset-2fa: that account is deleted; restore it from the People page first. Nothing was changed.")
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "reset-2fa:", err)
		return 1
	}
	if !was {
		fmt.Fprintf(stdout, "ok: two-step sign-in was not on for @%s. Nothing was changed.\n", target.Username)
		return 0
	}
	fmt.Fprintf(stdout, "ok: two-step sign-in is off for @%s, and its secret and recovery codes are gone. Their password is unchanged.\n", target.Username)
	fmt.Fprintln(stdout, "Start the server again. The reset is recorded in the admin log. Sign in and set it up again from Account when you are ready.")
	return 0
}
