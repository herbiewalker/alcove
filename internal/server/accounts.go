package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/blobstore"
	"github.com/herbiewalker/alcove/internal/retention"
	"github.com/herbiewalker/alcove/internal/store"
)

// Account lifecycle for the administrator (D-30, F2): create, disable, enable, delete, restore and
// reset. Every action asks for step-up and is written to the admin log in the same transaction.

// retFor returns the retention service for the open store.
func (s *Server) retFor(st *store.Store) *retention.Service {
	ing := s.ingestFor(st)
	s.ingMu.Lock()
	defer s.ingMu.Unlock()
	if s.ret == nil || s.retStore != st {
		r := retention.New(st, blobstore.New(st.DataDir()), ing)
		r.Now = s.now
		r.W = retention.Windows{User: time.Duration(s.opt.UserRecoveryDays) * 24 * time.Hour,
			Admin: time.Duration(s.opt.AdminRecoveryDays) * 24 * time.Hour}.Normalize()
		s.ret, s.retStore = r, st
	}
	return s.ret
}

func (s *Server) days() (user, admin int) {
	w := s.retFor(s.be.Store()).W
	return int(w.User / (24 * time.Hour)), int(w.Admin / (24 * time.Hour))
}

type userRow struct {
	ID, Username, Name, Role, State, Created, LastLogin, Until   string
	Uploads                                                      int
	Self, CanDisable, CanEnable, CanDelete, CanRestore, CanReset bool
}

func (s *Server) userRows(q *rq) ([]userRow, []userRow, error) {
	var list []auth.User
	counts := map[string]int{}
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if list, err = auth.List(tx); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT owner_id, COUNT(*) FROM revision WHERE deleted_at IS NULL GROUP BY owner_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o string
			var n int
			if err := rows.Scan(&o, &n); err != nil {
				return err
			}
			counts[o] = n
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	_, adminDays := s.days()
	var live, gone []userRow
	for _, u := range list {
		r := userRow{ID: u.ID, Username: u.Username, Name: u.DisplayName, Role: u.Role, Created: u.CreatedAt.Format(timeFmt),
			LastLogin: "never", Uploads: counts[u.ID], Self: u.ID == q.id.user.ID}
		if !u.LastLoginAt.IsZero() {
			r.LastLogin = u.LastLoginAt.Format(timeFmt)
		}
		switch {
		case u.Deleted():
			r.State, r.CanRestore = "deleted", true
			r.Until = u.DeletedAt.Add(time.Duration(adminDays) * 24 * time.Hour).Format(timeFmt)
			gone = append(gone, r)
			continue
		case u.Status == auth.StatusDisabled:
			r.State, r.CanEnable, r.CanDelete, r.CanReset = "disabled", true, !r.Self, true
		default:
			r.State, r.CanDisable, r.CanReset = "active", !r.Self, !r.Self
		}
		live = append(live, r)
	}
	return live, gone, nil
}

func (s *Server) usersPage(q *rq) (*page, error) {
	live, gone, err := s.userRows(q)
	if err != nil {
		return nil, err
	}
	p := &page{Title: "People", Active: "admin", Users: live, DeletedUsers: gone, SteppedUp: q.id.sess.SteppedUp(s.now())}
	inv, err := s.serverInvites(q)
	if err != nil {
		return nil, err
	}
	p.ServerInvites = inv
	p.UserDays, p.AdminDays = s.days()
	return p, nil
}

func (s *Server) getUsers(q *rq) {
	if err := q.audit("admin.users.view", "console", "users", "", nil); err != nil {
		q.serverError("log users view", err)
		return
	}
	p, err := s.usersPage(q)
	if err != nil {
		q.serverError("list users", err)
		return
	}
	q.render(http.StatusOK, "users", p)
}

func (q *rq) usersError(status int, msg string) {
	p, err := q.s.usersPage(q)
	if err != nil {
		q.serverError("list users", err)
		return
	}
	p.Error = msg
	q.render(status, "users", p)
}

func (s *Server) postUserCreate(q *rq) {
	if q.needStepUp("/admin/users") {
		return
	}
	f := q.r.PostForm
	username, err := auth.NormalizeUsername(f.Get("username"))
	if err != nil {
		q.usersError(http.StatusBadRequest, err.Error())
		return
	}
	role := f.Get("role")
	if role != auth.RoleAdmin && role != auth.RoleMember {
		role = auth.RoleMember
	}
	temp, err := auth.TempPassword()
	if err != nil {
		q.serverError("temporary password", err)
		return
	}
	hash, err := auth.Hash(temp, s.params)
	if err != nil {
		q.serverError("hash password", err)
		return
	}
	var u auth.User
	err = q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		var e error
		if u, e = auth.Create(tx, s.now(), username, f.Get("display"), role, hash); e != nil {
			return e
		}
		if e = auth.SetPasswordForced(tx, u.ID, hash, true); e != nil {
			return e
		}
		_, e = adminlog.Append(tx, s.now(), q.id.user.ID, "user.create", "user", u.ID, "username,role,password")
		return e
	})
	switch {
	case errors.Is(err, auth.ErrUsernameTaken):
		q.usersError(http.StatusConflict, "That username is already taken.")
		return
	case err != nil:
		q.usersError(http.StatusBadRequest, err.Error())
		return
	}
	q.render(http.StatusOK, "usercreated", &page{Title: "Account created", Active: "admin", NewUsername: u.Username, TempPassword: temp, Created: true})
}

// userAction runs one lifecycle action on the account named by the form's id.
func (s *Server) userAction(kind string) func(*rq) {
	return func(q *rq) {
		if q.needStepUp("/admin/users") {
			return
		}
		id := q.r.PostForm.Get("id")
		var temp string
		var target auth.User
		fail := ""
		err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
			var e error
			if target, e = auth.ByID(tx, id); e != nil {
				fail = "That person was not found."
				return e
			}
			self := target.ID == q.id.user.ID
			refuse := func(msg string) error { fail = msg; return errRefused }
			act := "user." + kind
			fields := "status"
			switch kind {
			case "disable":
				if target.Deleted() || target.Status != auth.StatusActive {
					return refuse("That account is already disabled.")
				}
				if self {
					return refuse("You cannot disable your own account.")
				}
				if target.Role == auth.RoleAdmin {
					if n, e := auth.CountActiveAdmins(tx); e != nil {
						return e
					} else if n <= 1 {
						return refuse("That is the only active administrator, so it cannot be disabled.")
					}
				}
				e = auth.SetStatus(tx, id, auth.StatusDisabled)
			case "enable":
				if target.Deleted() || target.Status != auth.StatusDisabled {
					return refuse("That account is not disabled.")
				}
				e = auth.SetStatus(tx, id, auth.StatusActive)
			case "delete":
				if self {
					return refuse("You cannot delete your own account.")
				}
				if target.Deleted() {
					return refuse("That account is already deleted.")
				}
				if target.Status != auth.StatusDisabled {
					return refuse("Disable the account first. Deleting starts the grace period before everything is removed for good.")
				}
				e, fields = auth.MarkDeleted(tx, id, s.now()), "deleted_at"
			case "restore":
				if !target.Deleted() {
					return refuse("That account is not deleted.")
				}
				e, fields = auth.Undelete(tx, id), "deleted_at"
			case "reset":
				act, fields = "user.password.reset", "password"
				if target.Deleted() {
					return refuse("Restore the account before resetting its password.")
				}
				if self {
					return refuse("Change your own password on the Account page.")
				}
				if temp, e = auth.TempPassword(); e != nil {
					return e
				}
				var h string
				if h, e = auth.Hash(temp, s.params); e != nil {
					return e
				}
				e = auth.SetPasswordForced(tx, id, h, true)
			default:
				return refuse("Unknown action.")
			}
			if e != nil {
				return e
			}
			_, e = adminlog.Append(tx, s.now(), q.id.user.ID, act, "user", id, fields)
			return e
		})
		if errors.Is(err, errRefused) || fail != "" && err != nil {
			q.usersError(http.StatusBadRequest, fail)
			return
		}
		if err != nil {
			q.serverError("account action", err)
			return
		}
		if kind == "disable" || kind == "delete" || kind == "reset" {
			s.sessions.RevokeUser(id) // their devices are signed out at once
		}
		if kind == "reset" {
			q.render(http.StatusOK, "usercreated", &page{Title: "Password reset", Active: "admin", NewUsername: target.Username, TempPassword: temp})
			return
		}
		q.setFlash("done")
		q.redirect("/admin/users")
	}
}

var errRefused = errors.New("refused")

// ---- the admin's "recently deleted" ----

type delRow struct {
	Owner, OwnerName, RevisionID, Filename, Size, DeletedAt, Until string
	Entries                                                        int
}

func (s *Server) adminDeletedPage(q *rq) (*page, error) {
	r := s.retFor(q.st)
	list, err := r.ListDeleted(q.r.Context(), "", true)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	var users []auth.User
	if err := q.st.View(q.r.Context(), func(tx *sql.Tx) (e error) { users, e = auth.List(tx); return }); err != nil {
		return nil, err
	}
	for _, u := range users {
		names[u.ID] = u.Username
	}
	p := &page{Title: "Recently deleted", Active: "admin", SteppedUp: q.id.sess.SteppedUp(s.now())}
	for _, d := range list {
		p.DeletedUploads = append(p.DeletedUploads, delRow{Owner: d.Owner, OwnerName: names[d.Owner], RevisionID: d.RevisionID, Filename: d.Filename,
			Size: humanBytes(d.Size), DeletedAt: d.DeletedAt.Format(timeFmt), Until: d.Until.Format(timeFmt), Entries: d.Entries})
	}
	_, gone, err := s.userRows(q)
	if err != nil {
		return nil, err
	}
	p.DeletedUsers = gone
	p.UserDays, p.AdminDays = s.days()
	return p, nil
}

func (s *Server) getAdminDeleted(q *rq) {
	if err := q.audit("retention.view", "console", "deleted", "", nil); err != nil {
		q.serverError("log deleted view", err)
		return
	}
	p, err := s.adminDeletedPage(q)
	if err != nil {
		q.serverError("list deleted", err)
		return
	}
	q.render(http.StatusOK, "admindeleted", p)
}

func (q *rq) deletedError(status int, msg string) {
	p, err := q.s.adminDeletedPage(q)
	if err != nil {
		q.serverError("list deleted", err)
		return
	}
	p.Error = msg
	q.render(status, "admindeleted", p)
}

func (s *Server) postAdminRestoreUpload(q *rq) {
	if q.needStepUp("/admin/deleted") {
		return
	}
	rev := q.r.PostForm.Get("id")
	out, err := s.retFor(q.st).RestoreUpload(q.r.Context(), "", rev, true)
	switch {
	case errors.Is(err, retention.ErrExpired):
		q.deletedError(http.StatusBadRequest, "The recovery window for that upload has ended.")
		return
	case errors.Is(err, retention.ErrNotFound):
		q.deletedError(http.StatusBadRequest, "That upload was not found.")
		return
	case err != nil:
		q.serverError("admin restore upload", err)
		return
	}
	_ = out
	if err := q.audit("upload.restore", "revision", rev, "deleted_at", nil); err != nil {
		q.serverError("log restore", err)
		return
	}
	q.setFlash("done")
	q.redirect("/admin/deleted")
}

func (s *Server) postAdminPurge(q *rq) {
	if q.needStepUp("/admin/deleted") {
		return
	}
	res, err := s.retFor(q.st).Purge(q.r.Context())
	if err != nil {
		q.serverError("purge", err)
		return
	}
	if err := q.audit("retention.purge.run", "retention", "all", res.Fields(), nil); err != nil {
		q.serverError("log purge", err)
		return
	}
	q.setFlash("purged")
	q.redirect("/admin/deleted")
}

// ---- forced and voluntary password change ----

func (s *Server) getPassword(q *rq) {
	q.render(http.StatusOK, "forcepw", &page{Title: "Choose a new password", Active: "account", Forced: q.id.user.MustChange})
}

func (s *Server) postPassword(q *rq) {
	f := q.r.PostForm
	again := func(status int, msg string) {
		q.render(status, "forcepw", &page{Title: "Choose a new password", Active: "account", Forced: q.id.user.MustChange, Error: msg})
	}
	if !s.stepLim.Allow(q.ip() + "|pw") {
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a little and try again.", "/account/password")
		return
	}
	if ok, _ := auth.Verify(f.Get("current"), q.id.user.PWHash, s.params); !ok {
		again(http.StatusUnauthorized, "The current password is not right.")
		return
	}
	pw := f.Get("password")
	if pw != f.Get("confirm") {
		again(http.StatusBadRequest, "The two new passwords are not the same.")
		return
	}
	if pw == f.Get("current") {
		again(http.StatusBadRequest, "Choose a password that is different from the current one.")
		return
	}
	if err := auth.CheckPassword(pw, q.id.user.Username); err != nil {
		again(http.StatusBadRequest, strings.TrimPrefix(err.Error(), "password policy: "))
		return
	}
	hash, err := auth.Hash(pw, s.params)
	if err != nil {
		q.serverError("hash password", err)
		return
	}
	u := q.id.user
	err = q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		if err := auth.ChangePassword(tx, u.ID, hash); err != nil {
			return err
		}
		if u.Role == auth.RoleAdmin {
			_, err := adminlog.Append(tx, s.now(), u.ID, "account.password.change", "user", u.ID, "password")
			return err
		}
		return nil
	})
	if err != nil {
		q.serverError("change password", err)
		return
	}
	s.sessions.RevokeUserExcept(u.ID, q.id.sess.ID)
	q.setFlash("pw")
	q.redirect("/")
}

// ---- the background purge ----

// StartBackground runs the purge shortly after start and then every hour until ctx ends. A purge
// is idempotent and transactional, so a run cut short by a shutdown is finished by the next one.
func (s *Server) StartBackground(ctx context.Context) {
	go func() {
		t := time.NewTimer(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.purgeOnce(ctx)
				t.Reset(time.Hour)
			}
		}
	}()
}

func (s *Server) purgeOnce(ctx context.Context) {
	st := s.be.Store()
	if st == nil {
		return
	}
	res, err := s.retFor(st).Purge(ctx)
	if err != nil {
		s.log.Error("scheduled purge failed", "err", err.Error())
		return
	}
	if res.Any() {
		s.log.Info("purge removed expired data", "uploads", res.Uploads, "accounts", res.Accounts, "orphans", res.Orphans)
	}
}
