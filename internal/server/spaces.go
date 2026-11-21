package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// Spaces, invites and joining (D-31). Every space page goes through spaces.Role, the single
// visibility gate; an outsider gets the same 404 as for a space that does not exist.

// consentText is what the join screen says is shared (D-21). Bump spaces.ConsentVersion when it
// changes in substance, so everyone is asked again before anything of theirs is shown.
var consentText = []string{
	"Everyone who is a member of this space can see your entries: dates, times, durations, ratings, moods and the other details you log, including solo entries and entries with people who are not members. Those people's names stay hidden unless you choose to show them, but a picture of them in your backup is shown with your photos (next point).",
	"Your notes and photos are included unless you turn them off for this space. That includes your own profile picture and the pictures your backup holds of the people in your entries, which are shown even where their names are hidden. Photos and pictures are blurred until someone taps them; where a photo was taken stays hidden unless you choose to show it. You can mark any single entry private, pause sharing, or leave at any time. Leaving removes what you contributed from the space, but anything other members have already seen cannot be unseen.",
	"The space owner decides who joins. The server administrator can see everything on this server, and every time they look is written to a log you can ask to see.",
	"Each member's entries appear in the space's shared views as soon as they have uploaded a backup. If this wording ever changes, you are asked to agree again, and until you do nobody sees your entries and you see nobody else's.",
}

type memberRow struct {
	UserID, Name, Username, Role, Since string
	CanRemove, CanHandOver              bool
}

type inviteRow struct {
	ID, Label, Expires, State string
	Live                      bool
}

type spaceRow struct {
	ID, Name, Role, State, Created, Owner, Until string
	Members, Askin                               int
	Deleted                                      bool
}

type spaceView struct {
	ID, Name, Role string
	IsOwner        bool
	Members        []memberRow
	Requests       []memberRow
	Invites        []inviteRow
	HasCode        bool
	CodeSince      string
	Mine           spaces.Settings
	NeedAgree      bool
	Cards          []share.Card
}

func (q *rq) joinBase() string {
	scheme := "http"
	if q.c.Secure {
		scheme = "https"
	}
	return scheme + "://" + q.r.Host
}

// ---- my spaces ----

func (s *Server) spacesPage(q *rq) (*page, error) {
	var mine []spaces.Mine
	var gone []spaces.Space
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if mine, err = spaces.ForUser(tx, q.id.user.ID); err != nil {
			return err
		}
		gone, err = spaces.DeletedFor(tx, q.id.user.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	userDays, _ := s.days()
	p := &page{Title: "Spaces", Active: "spaces", UserDays: userDays, SteppedUp: q.id.sess.SteppedUp(s.now())}
	for _, m := range mine {
		p.Spaces = append(p.Spaces, spaceRow{ID: m.ID, Name: m.Name, Role: m.Role, State: m.State, Created: m.CreatedAt.Format(timeFmt), Members: m.Members})
	}
	for _, d := range gone {
		p.DeletedSpaces = append(p.DeletedSpaces, spaceRow{ID: d.ID, Name: d.Name, Deleted: true,
			Until: d.DeletedAt.Add(time.Duration(userDays) * 24 * time.Hour).Format(timeFmt)})
	}
	return p, nil
}

func (s *Server) getSpaces(q *rq) {
	p, err := s.spacesPage(q)
	if err != nil {
		q.serverError("list spaces", err)
		return
	}
	q.render(http.StatusOK, "spaces", p)
}

func (s *Server) postSpaceCreate(q *rq) {
	var sp spaces.Space
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) (err error) {
		sp, err = spaces.Create(tx, s.now(), q.r.PostForm.Get("name"), q.id.user.ID)
		return
	})
	if errors.Is(err, spaces.ErrBadName) {
		p, perr := s.spacesPage(q)
		if perr != nil {
			q.serverError("list spaces", perr)
			return
		}
		p.Error = "Give the space a name of 1 to 60 characters."
		q.render(http.StatusBadRequest, "spaces", p)
		return
	}
	if err != nil {
		q.serverError("create space", err)
		return
	}
	q.redirect("/spaces/" + sp.ID)
}

// ---- one space ----

// gate is the visibility check for a space page. A person who is not an active member gets the same
// 404 as for a space that does not exist.
func (s *Server) gate(q *rq) (spaces.Space, string, bool) {
	id := q.r.PathValue("id")
	var sp spaces.Space
	var role string
	ok := false
	_ = q.st.View(q.r.Context(), func(tx *sql.Tx) error {
		var found bool
		if role, found = spaces.Role(tx, id, q.id.user.ID); !found {
			return nil
		}
		var err error
		if sp, err = spaces.Get(tx, id); err == nil {
			ok = true
		}
		return nil
	})
	if !ok {
		q.notFound()
	}
	return sp, role, ok
}

func (s *Server) spacePage(q *rq, sp spaces.Space, role string) (*page, error) {
	v := &spaceView{ID: sp.ID, Name: sp.Name, Role: role, IsOwner: role == spaces.RoleOwner, HasCode: sp.HasCode}
	if sp.HasCode {
		v.CodeSince = sp.CodeAt.Format(timeFmt)
	}
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) error {
		ms, err := spaces.Members(tx, sp.ID, spaces.StateActive, spaces.StateRequested)
		if err != nil {
			return err
		}
		for _, m := range ms {
			row := memberRow{UserID: m.UserID, Name: m.DisplayName, Username: m.Username, Role: m.Role}
			switch {
			case m.State == spaces.StateRequested:
				row.Since = m.RequestedAt.Format(timeFmt)
				if v.IsOwner {
					v.Requests = append(v.Requests, row)
				}
			default:
				row.Since = m.JoinedAt.Format(timeFmt)
				row.CanRemove = v.IsOwner && m.Role != spaces.RoleOwner
				row.CanHandOver = v.IsOwner && m.Role != spaces.RoleOwner
				v.Members = append(v.Members, row)
				if m.UserID == q.id.user.ID {
					v.Mine = spaces.Settings{IncludeNotes: m.IncludeNotes, IncludePhotos: m.IncludePhotos, ShowPartnerNames: m.ShowPartnerNames, Paused: m.Paused, ShowPhotoLocation: m.ShowPhotoLocation}
					v.NeedAgree = m.ConsentVersion < spaces.ConsentVersion
				}
			}
		}
		if v.NeedAgree {
			// Until they agree to the current wording they see nobody else (D-32, audit 2026-11-21 finding 3):
			// no member list and no join requests either, only the agree screen and their own settings.
			v.Members, v.Requests = nil, nil
		} else {
			cs, err := share.Cards(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, s.now())
			if err != nil {
				return err
			}
			v.Cards = cs
		}
		if v.IsOwner {
			invs, err := spaces.Invites(tx, sp.ID)
			if err != nil {
				return err
			}
			for _, i := range invs {
				state := i.State
				if i.State == "open" && !i.Live(s.now()) {
					state = "expired"
				}
				if state == "open" || state == "used" {
					v.Invites = append(v.Invites, inviteRow{ID: i.ID, Label: i.Label, Expires: i.ExpiresAt.Format(timeFmt), State: state, Live: state == "open"})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &page{Title: sp.Name, Active: "spaces", Space: v, Consent: consentText, SteppedUp: q.id.sess.SteppedUp(s.now())}, nil
}

func (s *Server) getSpace(q *rq) {
	sp, role, ok := s.gate(q)
	if !ok {
		return
	}
	p, err := s.spacePage(q, sp, role)
	if err != nil {
		q.serverError("read space", err)
		return
	}
	q.render(http.StatusOK, "space", p)
}

var errNoSpace = errors.New("no such space")
var errBadForm = errors.New("bad form")

func (q *rq) spaceError(status int, sp spaces.Space, role, msg string) {
	p, err := q.s.spacePage(q, sp, role)
	if err != nil {
		q.serverError("read space", err)
		return
	}
	p.Error = msg
	q.render(status, "space", p)
}

// postSpaceAction handles every change on a space page. The gate is checked again inside the
// transaction that makes the change, so a person removed a moment ago cannot slip one through.
func (s *Server) postSpaceAction(q *rq) {
	id, action := q.r.PathValue("id"), q.r.PathValue("action")
	uid, f := q.id.user.ID, q.r.PostForm
	ownerOnly := map[string]bool{"rename": true, "invite": true, "revoke": true, "code": true, "approve": true, "deny": true,
		"remove": true, "transfer": true, "delete": true}
	known := ownerOnly[action] || action == "settings" || action == "leave" || action == "agree"
	if !known {
		q.notFound()
		return
	}
	// Check membership BEFORE anything that could differ between an outsider and a member (such as the
	// step-up redirect), so an outsider only ever sees the same 404 as for a space that does not exist.
	var pre string
	var preOK bool
	_ = q.st.View(q.r.Context(), func(tx *sql.Tx) error { pre, preOK = spaces.Role(tx, id, uid); return nil })
	if !preOK {
		q.notFound()
		return
	}
	if ownerOnly[action] && pre != spaces.RoleOwner {
		q.message(http.StatusForbidden, "Only the owner", "Only the owner of this space can do that.", "/spaces/"+id)
		return
	}
	if action == "transfer" || action == "delete" {
		if q.needStepUp("/spaces") { // a delete and a hand-over are not undone by a click
			return
		}
	}
	var codeShown, kind string
	var sp spaces.Space
	var role string
	var refused string
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		var ok bool
		if role, ok = spaces.Role(tx, id, uid); !ok {
			return errNoSpace
		}
		var e error
		if sp, e = spaces.Get(tx, id); e != nil {
			return e
		}
		if ownerOnly[action] && role != spaces.RoleOwner {
			refused = "Only the owner of this space can do that."
			return errRefused
		}
		now := s.now()
		switch action {
		case "rename":
			e = spaces.Rename(tx, id, f.Get("name"))
		case "settings":
			e = spaces.SetSettings(tx, id, uid, spaces.Settings{IncludeNotes: f.Get("notes") == "1", IncludePhotos: f.Get("photos") == "1",
				ShowPartnerNames: f.Get("partners") == "1", Paused: f.Get("paused") == "1", ShowPhotoLocation: f.Get("photoplace") == "1"})
		case "leave":
			e = spaces.Leave(tx, now, id, uid)
			kind = "left"
		case "agree":
			if f.Get("agree") != "1" {
				refused = "Tick the box to agree."
				return errBadForm
			}
			e = spaces.Agree(tx, now, id, uid)
		case "invite":
			days := map[string]int{"1": 1, "7": 7, "30": 30}[f.Get("days")]
			if days == 0 {
				days = 7
			}
			_, codeShown, e = spaces.CreateInvite(tx, now, spaces.KindSpace, id, uid, f.Get("label"), time.Duration(days)*24*time.Hour)
			kind = "invite"
		case "revoke":
			e = spaces.RevokeInvite(tx, f.Get("invite"), id)
		case "code":
			if f.Get("do") == "disable" {
				e = spaces.DisableCode(tx, id)
			} else {
				codeShown, e = spaces.RotateCode(tx, now, id)
				kind = "code"
			}
		case "approve":
			e = spaces.Approve(tx, now, id, f.Get("user"))
		case "deny":
			e = spaces.Deny(tx, now, id, f.Get("user"))
		case "remove":
			e = spaces.Remove(tx, now, id, f.Get("user"))
		case "transfer":
			e = spaces.Transfer(tx, id, uid, f.Get("user"))
		case "delete":
			e = spaces.Delete(tx, now, id)
			kind = "deleted"
		}
		return e
	})
	switch {
	case errors.Is(err, errNoSpace):
		q.notFound()
		return
	case errors.Is(err, errRefused):
		q.spaceError(http.StatusForbidden, sp, role, refused)
		return
	case errors.Is(err, errBadForm):
		q.spaceError(http.StatusBadRequest, sp, role, refused)
		return
	case errors.Is(err, spaces.ErrLastOwner):
		q.spaceError(http.StatusBadRequest, sp, role, "The owner cannot leave or be removed. Hand the space to another member first, or delete it.")
		return
	case errors.Is(err, spaces.ErrBadName):
		q.spaceError(http.StatusBadRequest, sp, role, "Give the space a name of 1 to 60 characters.")
		return
	case errors.Is(err, spaces.ErrNotMember), errors.Is(err, spaces.ErrNotFound), errors.Is(err, spaces.ErrNotAllowed), errors.Is(err, spaces.ErrNeedConsent):
		q.spaceError(http.StatusBadRequest, sp, role, "That could not be done: the person is not (or no longer) in that state.")
		return
	case err != nil:
		q.serverError("space action", err)
		return
	}
	switch kind {
	case "left", "deleted":
		q.setFlash("space-" + kind)
		q.redirect("/spaces")
		return
	case "invite", "code":
		what := "invite"
		note := "Give it to one person, in person or through your own channel. It works once and expires."
		if kind == "code" {
			what = "space code"
			note = "Anyone who has this code can ask to join; you still approve each person. Making a new code stops the old one."
		}
		q.render(http.StatusOK, "codeshown", &page{Title: "Your " + what, Active: "spaces", Code: codeShown, JoinURL: q.joinBase() + "/join", CodeNote: note, Back: "/spaces/" + id, QR: qrFor(q.joinBase()+"/join", "QR code for the join page")})
		return
	}
	q.setFlash("done")
	q.redirect("/spaces/" + id)
}

// postSpaceRestore brings back a space its owner deleted, inside the user window.
func (s *Server) postSpaceRestore(q *rq) {
	id := q.r.PathValue("id")
	userDays, _ := s.days()
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		gone, err := spaces.DeletedFor(tx, q.id.user.ID)
		if err != nil {
			return err
		}
		for _, d := range gone {
			if d.ID == id {
				return spaces.Restore(tx, s.now(), id, time.Duration(userDays)*24*time.Hour)
			}
		}
		return errNoSpace
	})
	switch {
	case errors.Is(err, errNoSpace):
		q.notFound()
		return
	case errors.Is(err, spaces.ErrExpired):
		p, perr := s.spacesPage(q)
		if perr != nil {
			q.serverError("list spaces", perr)
			return
		}
		p.Error = "The time to restore that space yourself has ended. Ask the administrator."
		q.render(http.StatusBadRequest, "spaces", p)
		return
	case err != nil:
		q.serverError("restore space", err)
		return
	}
	q.setFlash("restored")
	q.redirect("/spaces/" + id)
}

// ---- joining ----

func (s *Server) getJoin(q *rq) {
	if q.tlsGate() {
		return
	}
	q.render(http.StatusOK, "join", &page{Title: "Join with a code", Active: "spaces", Code: q.r.URL.Query().Get("code")})
}

func (q *rq) joinError(status int, msg, code string) {
	q.render(status, "join", &page{Title: "Join with a code", Active: "spaces", Error: msg, Code: code})
}

// consentPage renders the screen that says exactly what joining shares.
func (q *rq) consentPage(status int, f spaces.Found, code string, errMsg string) {
	p := &page{Title: "Before you join", Active: "spaces", Code: code, Consent: consentText, Error: errMsg,
		JoinName: f.Name, JoinIsServer: f.SpaceID == "", JoinIsCode: f.Kind == "code", SignedIn: q.id != nil}
	q.render(status, "joinconsent", p)
}

const badCodeMsg = "That code did not work. It may be mistyped, used, expired or turned off."

func (s *Server) postJoinCode(q *rq) {
	if q.tlsGate() {
		return
	}
	if !s.joinLim.Allow(q.ip()) {
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a little and try again.", "/join")
		return
	}
	code := q.r.PostForm.Get("code")
	var f spaces.Found
	var err error
	if verr := q.st.View(q.r.Context(), func(tx *sql.Tx) error { f, err = spaces.Lookup(tx, s.now(), code); return nil }); verr != nil {
		q.serverError("look up a code", verr)
		return
	}
	if err != nil {
		s.log.Warn("join code refused", "ip", q.ip())
		q.joinError(http.StatusBadRequest, badCodeMsg, "")
		return
	}
	if q.id != nil && f.SpaceID == "" {
		q.joinError(http.StatusBadRequest, "That invite is for creating a new account, and you already have one.", "")
		return
	}
	if q.id == nil && f.Kind == "code" {
		q.joinError(http.StatusBadRequest, "A space code is for people who already have an account here. Sign in first, then enter it again.", "")
		return
	}
	q.consentPage(http.StatusOK, f, code, "")
}

// postJoinAccept finishes a join. When the person is not signed in this is the ONLY route that makes
// an account without an administrator or the first-run code (constraint 11), so everything about
// it is checked inside one transaction that also spends the invite.
func (s *Server) postJoinAccept(q *rq) {
	if q.tlsGate() {
		return
	}
	if !s.joinLim.Allow(q.ip()) {
		q.message(http.StatusTooManyRequests, "Too many attempts", "Wait a little and try again.", "/join")
		return
	}
	f := q.r.PostForm
	code := f.Get("code")
	consent := f.Get("consent") == "1"
	var found spaces.Found
	var lerr error
	if verr := q.st.View(q.r.Context(), func(tx *sql.Tx) error { found, lerr = spaces.Lookup(tx, s.now(), code); return nil }); verr != nil {
		q.serverError("look up a code", verr)
		return
	}
	if lerr != nil {
		q.joinError(http.StatusBadRequest, badCodeMsg, "")
		return
	}
	again := func(status int, msg string) { q.consentPage(status, found, code, msg) }
	signedIn := q.id != nil
	switch {
	case signedIn && found.SpaceID == "":
		q.joinError(http.StatusBadRequest, "That invite is for creating a new account, and you already have one.", "")
		return
	case !signedIn && found.Kind == "code":
		q.joinError(http.StatusBadRequest, "A space code is for people who already have an account here. Sign in first.", "")
		return
	case found.SpaceID != "" && !consent:
		again(http.StatusBadRequest, "Tick the box to agree before joining.")
		return
	}

	var username, hash string
	if !signedIn {
		var err error
		if username, err = auth.NormalizeUsername(f.Get("username")); err != nil {
			again(http.StatusBadRequest, err.Error())
			return
		}
		pw := f.Get("password")
		if pw != f.Get("confirm") {
			again(http.StatusBadRequest, "The two passwords are not the same.")
			return
		}
		if err := auth.CheckPassword(pw, username); err != nil {
			again(http.StatusBadRequest, strings.TrimPrefix(err.Error(), "password policy: "))
			return
		}
		if hash, err = auth.Hash(pw, s.params); err != nil {
			q.serverError("hash password", err)
			return
		}
	}

	var newUser auth.User
	var spaceID string
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		now := s.now()
		fnd, err := spaces.Lookup(tx, now, code) // again, inside the transaction that spends it
		if err != nil {
			return err
		}
		uid := ""
		if q.id != nil {
			uid = q.id.user.ID
		}
		if !signedIn {
			if fnd.Kind != "invite" {
				return spaces.ErrBadCode
			}
			if newUser, err = auth.Create(tx, now, username, f.Get("display"), auth.RoleMember, hash); err != nil {
				return err
			}
			uid = newUser.ID
		}
		spaceID = fnd.SpaceID
		if fnd.Kind == "invite" {
			if err := spaces.Spend(tx, now, fnd.Invite.ID, uid); err != nil {
				return err
			}
			if fnd.SpaceID == "" {
				return nil // a server invite: the account is all it makes
			}
			return spaces.JoinViaInvite(tx, now, fnd.SpaceID, uid, consent)
		}
		return spaces.RequestJoin(tx, now, fnd.SpaceID, uid, consent)
	})
	switch {
	case errors.Is(err, spaces.ErrBadCode):
		q.joinError(http.StatusBadRequest, badCodeMsg, "")
		return
	case errors.Is(err, auth.ErrBadDisplayName):
		again(http.StatusBadRequest, "A name shown to others is 1 to 80 characters, with no control or direction-changing characters.")
		return
	case errors.Is(err, auth.ErrUsernameTaken):
		again(http.StatusConflict, "That username is already taken.")
		return
	case errors.Is(err, spaces.ErrAlreadyIn):
		q.joinError(http.StatusConflict, "You are already in that space.", "")
		return
	case errors.Is(err, spaces.ErrNeedConsent):
		again(http.StatusBadRequest, "Tick the box to agree before joining.")
		return
	case err != nil:
		q.serverError("join", err)
		return
	}
	switch {
	case !signedIn:
		s.hasUsers.Store(true)
		s.log.Info("account created from an invite", "user", newUser.ID)
		sess, serr := s.sessions.Issue(newUser.ID, q.ip())
		if serr != nil {
			q.serverError("issue session", serr)
			return
		}
		q.setSession(sess)
		if spaceID != "" {
			q.redirect("/spaces/" + spaceID)
			return
		}
		q.redirect("/")
	case found.Kind == "code":
		q.setFlash("asked")
		q.redirect("/spaces")
	default:
		q.redirect("/spaces/" + spaceID)
	}
}

// ---- the administrator's view ----

func (s *Server) adminSpacesPage(q *rq) (*page, error) {
	var all []spaces.Overview
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (e error) { all, e = spaces.AllSpaces(tx); return })
	if err != nil {
		return nil, err
	}
	_, adminDays := s.days()
	p := &page{Title: "Spaces", Active: "admin", SteppedUp: q.id.sess.SteppedUp(s.now())}
	for _, o := range all {
		r := spaceRow{ID: o.ID, Name: o.Name, Owner: o.Owner, Members: o.Active, Askin: o.Askin, Created: o.CreatedAt.Format(timeFmt), Deleted: o.Deleted()}
		if o.Deleted() {
			r.Until = o.DeletedAt.Add(time.Duration(adminDays) * 24 * time.Hour).Format(timeFmt)
		}
		p.Spaces = append(p.Spaces, r)
	}
	return p, nil
}

func (s *Server) getAdminSpaces(q *rq) {
	if err := q.audit("admin.spaces.view", "console", "spaces", "", nil); err != nil {
		q.serverError("log spaces view", err)
		return
	}
	p, err := s.adminSpacesPage(q)
	if err != nil {
		q.serverError("list spaces", err)
		return
	}
	q.render(http.StatusOK, "adminspaces", p)
}

func (s *Server) postAdminSpaceAction(q *rq) {
	if q.needStepUp("/admin/spaces") {
		return
	}
	kind, id := q.r.PostForm.Get("do"), q.r.PostForm.Get("id")
	if kind != "delete" && kind != "restore" {
		q.notFound()
		return
	}
	_, adminDays := s.days()
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		var e error
		if kind == "delete" {
			e = spaces.Delete(tx, s.now(), id)
		} else {
			e = spaces.Restore(tx, s.now(), id, time.Duration(adminDays)*24*time.Hour)
		}
		if e != nil {
			return e
		}
		_, e = adminlog.Append(tx, s.now(), q.id.user.ID, "space."+kind, "space", id, "deleted_at")
		return e
	})
	if err != nil {
		p, perr := s.adminSpacesPage(q)
		if perr != nil {
			q.serverError("list spaces", perr)
			return
		}
		p.Error = "That could not be done: the space was not found, or its recovery window has ended."
		q.render(http.StatusBadRequest, "adminspaces", p)
		return
	}
	q.setFlash("done")
	q.redirect("/admin/spaces")
}

// ---- server invites (the administrator's) ----

func (s *Server) postServerInvite(q *rq) {
	if q.needStepUp("/admin/users") {
		return
	}
	days := map[string]int{"1": 1, "7": 7, "30": 30}[q.r.PostForm.Get("days")]
	if days == 0 {
		days = 7
	}
	var code string
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		inv, c, err := spaces.CreateInvite(tx, s.now(), spaces.KindServer, "", q.id.user.ID, q.r.PostForm.Get("label"), time.Duration(days)*24*time.Hour)
		if err != nil {
			return err
		}
		code = c
		_, err = adminlog.Append(tx, s.now(), q.id.user.ID, "invite.create", "invite", inv.ID, "kind,expires")
		return err
	})
	if err != nil {
		q.serverError("create invite", err)
		return
	}
	q.render(http.StatusOK, "codeshown", &page{Title: "Your invite", Active: "admin", Code: code, JoinURL: q.joinBase() + "/join",
		QR:       qrFor(q.joinBase()+"/join", "QR code for the join page"),
		CodeNote: "Give it to one person, in person or through your own channel. It creates one account, works once, and expires.", Back: "/admin/users"})
}

func (s *Server) postServerInviteRevoke(q *rq) {
	if q.needStepUp("/admin/users") {
		return
	}
	id := q.r.PostForm.Get("id")
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		if err := spaces.RevokeInvite(tx, id, ""); err != nil {
			return err
		}
		_, err := adminlog.Append(tx, s.now(), q.id.user.ID, "invite.revoke", "invite", id, "state")
		return err
	})
	if err != nil {
		q.usersError(http.StatusBadRequest, "That invite was not found or is no longer open.")
		return
	}
	q.setFlash("done")
	q.redirect("/admin/users")
}

func (s *Server) serverInvites(q *rq) ([]inviteRow, error) {
	var out []inviteRow
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) error {
		invs, err := spaces.Invites(tx, "")
		if err != nil {
			return err
		}
		for _, i := range invs {
			state := i.State
			if i.State == "open" && !i.Live(s.now()) {
				state = "expired"
			}
			if state == "open" || state == "used" {
				out = append(out, inviteRow{ID: i.ID, Label: i.Label, Expires: i.ExpiresAt.Format(timeFmt), State: state, Live: state == "open"})
			}
		}
		return nil
	})
	return out, err
}
