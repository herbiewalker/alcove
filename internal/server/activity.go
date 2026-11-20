package server

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/herbiewalker/alcove/internal/adminlog"
)

// The member activity page (D-51, F25): a person sees when an administrator, or the server's command
// line, opened or changed their data. It is a read of the admin log, filtered to lines that name
// that person, so it holds no content: only a time, a kind and field names.

// activityKey is the administrator's switch. Off (absent) by default.
const activityKey = "setting.member_activity"

const activityPageSize = entriesPerPage // setPager pages by this

// activityShown maps each admin log action that is about a person to the sentence a person reads.
// Every action in the log vocabulary must be here or in activityHidden; TestEveryAdminActionIsShownOrHidden
// reads the source to enforce it, so a new admin action cannot silently skip this decision.
var activityShown = map[string]string{
	"data.view":               "An administrator looked at your data",
	"data.edit":               "An administrator changed your data",
	"data.revert":             "An administrator reverted a change in your data",
	"data.delete":             "An administrator deleted one of your entries",
	"data.restore":            "An administrator restored one of your entries",
	"user.create":             "An administrator created your account",
	"user.disable":            "An administrator turned your account off",
	"user.enable":             "An administrator turned your account back on",
	"user.delete":             "An administrator scheduled your account for removal",
	"user.restore":            "An administrator restored your account",
	"user.password.reset":     "An administrator reset your password",
	"user.password.reset.cli": "Your password was reset from the server's command line",
	"user.totp.reset":         "An administrator cleared your two-step sign-in",
	"user.totp.reset.cli":     "Your two-step sign-in was cleared from the server's command line",
}

// activityHidden lists every other action in the log, each with why it is not on a person's page.
var activityHidden = map[string]string{
	"admin.first_run": "server setup", "admin.search": "logged by shape, not per person (D-46)",
	"admin.console.view": "the console", "admin.users.view": "the console", "admin.spaces.view": "the console",
	"admin.health.view": "the console", "adminlog.view": "the log itself", "adminlog.verify": "the log itself",
	"adminlog.export": "the log itself", "backup.view": "the whole instance", "backup.create": "the whole instance",
	"backup.verify": "the whole instance", "backup.drill": "the whole instance", "backup.rebuild": "the whole instance",
	"retention.view": "the whole instance", "retention.purge": "the whole instance", "retention.purge.run": "the whole instance",
	"settings.view": "server settings", "settings.tls.change": "server settings", "settings.proxy.change": "server settings",
	"settings.timezone.change": "server settings", "settings.networks.propose": "server settings",
	"settings.networks.confirm": "server settings", "settings.networks.cancel": "server settings",
	"settings.networks.revert": "server settings", "settings.activity": "server settings",
	"upload.restore": "targets an upload, not a person", "space.delete": "targets a space, not a person", "space.restore": "targets a space, not a person", "invite.create": "an invite, not a person",
	"invite.revoke": "an invite, not a person", "session.login": "the person's own", "session.stepup": "the person's own",
	"session.2fa.locked": "the person's own", "account.password.change": "the person's own",
	"totp.enable": "the person's own", "totp.disable": "the person's own", "totp.recovery.new": "the person's own",
	"export.create": "the person's own", "entry.touch": "test fixture only",
}

func shownActions() []string {
	out := make([]string, 0, len(activityShown))
	for a := range activityShown {
		out = append(out, a)
	}
	return out
}

func (s *Server) activityOn(q *rq) bool {
	v, ok, err := q.st.GetMeta(activityKey)
	return err == nil && ok && v == "on"
}

type activityRow struct{ Time, Text, Fields string }

// activityText is the sentence for one log line: the fixed text for its action, with what it touched
// (an entry, a partner, a page) and the field names, never a value.
var viewWhat = map[string]string{
	"entries":  "An administrator opened your list of entries",
	"entry":    "An administrator opened one of your entries",
	"edit":     "An administrator opened the edit page of something of yours",
	"history":  "An administrator opened the history of something of yours",
	"delete":   "An administrator opened the delete page of one of your entries",
	"partners": "An administrator opened your partners",
	"deleted":  "An administrator opened your recently deleted entries",
}

// activityText is the sentence for one log line: fixed text for its action, with what it touched
// (an entry or a partner) and the field names, never a value.
func activityText(e adminlog.Entry) (text, fields string) {
	text = activityShown[e.Action]
	switch e.Action {
	case "data.view":
		if t, ok := viewWhat[e.Fields]; ok {
			text = t
		}
		return text, ""
	case "data.edit", "data.revert", "data.delete", "data.restore":
		noun := "one of your entries"
		if e.TargetType == "partners" {
			noun = "one of your partners"
		}
		text = strings.Replace(text, "your data", noun, 1)
		text = strings.Replace(text, "one of your entries", noun, 1)
	}
	return text, e.Fields
}

func (s *Server) getActivity(q *rq) {
	if !s.activityOn(q) {
		q.notFound()
		return
	}
	n := pageNumber(q)
	var rows []adminlog.Entry
	var total int
	if err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		rows, total, err = adminlog.About(tx, q.id.user.ID, shownActions(), activityPageSize, (n-1)*activityPageSize)
		return
	}); err != nil {
		q.serverError("read activity", err)
		return
	}
	p := &page{Title: "Activity on my data", Active: "account"}
	for _, e := range rows {
		text, fields := activityText(e)
		p.Activity = append(p.Activity, activityRow{Time: e.Time.Format(timeFmt) + " UTC", Text: text, Fields: fields})
	}
	setPager(p, n, total)
	q.render(http.StatusOK, "activity", p)
}

// postActivity turns the page on or off for everyone. It is an administrator action behind step-up
// and is logged.
func (s *Server) postActivity(q *rq) {
	if q.needStepUp("/admin/settings") {
		return
	}
	on := q.r.PostForm.Get("activity") == "1"
	val := map[bool]string{true: "on", false: "off"}[on]
	if err := q.audit("settings.activity", "settings", "activity", val, func(tx *sqlTx) error {
		if !on {
			_, err := tx.Exec(`DELETE FROM meta WHERE key = ?`, activityKey)
			return err
		}
		return putMeta(tx, map[string]string{activityKey: "on"}, activityKey)
	}); err != nil {
		q.serverError("save activity switch", err)
		return
	}
	q.setFlash("activity")
	q.redirect("/admin/settings")
}
