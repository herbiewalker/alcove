package server

import (
	"database/sql"
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
)

// logIn is what the administrator typed into the log filter (F18). It is text until it is resolved
// against the database, so a page can show it back exactly as typed.
type logIn struct{ Who, Action, Type, From, To string }

// maxLogExport bounds one CSV so it is built in a bounded amount of memory.
const maxLogExport = 200000

func logInOf(v url.Values) logIn {
	t := func(k string) string {
		s := strings.TrimSpace(v.Get(k))
		if len(s) > 64 {
			s = s[:64]
		}
		return s
	}
	return logIn{Who: t("who"), Action: t("action"), Type: t("type"), From: t("from"), To: t("to")}
}

func (in logIn) any() bool { return in != logIn{} }

// query is the filter as link parameters, with a leading "&" so it can follow "?page=n".
func (in logIn) query() string {
	v := url.Values{}
	for k, s := range map[string]string{"who": in.Who, "action": in.Action, "type": in.Type, "from": in.From, "to": in.To} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "&" + v.Encode()
}

// resolveLogFilter turns the typed words into a filter. A word that cannot match anything gives a
// plain explanation and is ignored, so a typo shows the whole log with a note, never a blank page
// that looks like "nothing happened". A user name that does not exist matches nothing.
func resolveLogFilter(tx *sql.Tx, in logIn) (adminlog.Filter, string) {
	var f adminlog.Filter
	var problems []string
	if in.Who != "" {
		switch {
		case strings.EqualFold(in.Who, "system"):
			f.Actor = adminlog.System
		default:
			var id string
			if err := tx.QueryRow(`SELECT id FROM user WHERE username = ?`, in.Who).Scan(&id); err != nil {
				id = "no-such-user" // matches nothing: the log really holds no lines by that name
			}
			f.Actor = id
		}
	}
	for _, w := range []struct {
		name, val string
		dst       *string
	}{{"action", in.Action, &f.Action}, {"target type", in.Type, &f.TargetType}} {
		if w.val == "" {
			continue
		}
		if !adminlog.ValidFilterText(w.val) {
			problems = append(problems, "The "+w.name+" filter can only use letters, digits and . _ : , @ -, so it was ignored.")
			continue
		}
		*w.dst = w.val
	}
	day := func(s string) (time.Time, bool) {
		t, err := time.Parse("2006-01-02", s)
		return t, err == nil
	}
	if in.From != "" {
		if t, ok := day(in.From); ok {
			f.From = t
		} else {
			problems = append(problems, "The from date must look like 2026-11-16, so it was ignored.")
		}
	}
	if in.To != "" {
		if t, ok := day(in.To); ok {
			f.To = t.Add(24*time.Hour - time.Second) // the whole day
		} else {
			problems = append(problems, "The to date must look like 2026-11-16, so it was ignored.")
		}
	}
	return f, strings.Join(problems, " ")
}

// csvCell stops a spreadsheet from running a value as a formula. Log values are plain identifiers,
// but "-", "@" and "=" are allowed in them, so a cell that starts with one is made harmless.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// postLogExport sends the entries that match the filter as a CSV. It is a POST behind the step-up
// and the CSRF check, and the export itself is a line in the log (F18, constraint 6): the count,
// never the content.
func (s *Server) postLogExport(q *rq) {
	if q.needStepUp("/admin/log") {
		return
	}
	in := logInOf(q.r.PostForm)
	var entries []adminlog.Entry
	names := map[string]string{}
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		f, _ := resolveLogFilter(tx, in)
		var err error
		if entries, err = adminlog.Find(tx, f, maxLogExport, 0); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT id, username FROM user`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			names[id] = name
		}
		if err := rows.Err(); err != nil {
			return err
		}
		fields := "rows:" + strconv.Itoa(len(entries))
		if in.any() {
			fields += ",filtered"
		}
		_, err = adminlog.Append(tx, s.now(), q.id.user.ID, "adminlog.export", "admin_log", "all", fields)
		return err
	})
	if err != nil {
		q.serverError("export admin log", err)
		return
	}
	h := q.w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="alcove-admin-log-`+s.now().UTC().Format("2006-01-02")+`.csv"`)
	h.Set("Cache-Control", "no-store")
	q.w.WriteHeader(http.StatusOK)
	w := csv.NewWriter(q.w)
	_ = w.Write([]string{"seq", "time_utc", "who", "action", "target_type", "target_id", "fields"})
	for i := len(entries) - 1; i >= 0; i-- { // oldest first reads best in a spreadsheet
		e := entries[i]
		who := e.Actor
		if n, ok := names[e.Actor]; ok {
			who = n
		}
		_ = w.Write([]string{strconv.FormatInt(e.Seq, 10), e.Time.Format(time.RFC3339), csvCell(who), csvCell(e.Action),
			csvCell(e.TargetType), csvCell(e.TargetID), csvCell(e.Fields)})
	}
	w.Flush()
}
