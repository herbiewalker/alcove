package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/herbiewalker/alcove/internal/share"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// markClasses is how many distinct colours a member marker cycles through. The marker is always
// the person's initial as well, so colour is never the only signal.
const markClasses = 6

type calMark struct {
	Initial, Class, Name string
	Count                int
}

type calCell struct {
	Num      int
	Date     string
	InMonth  bool
	Selected bool
	Today    bool
	Both     bool // two or more members were active
	Marks    []calMark
	More     int
	Link     string
	Label    string
}

type calLegend struct{ UserID, Initial, Class, Name string }

type calView struct {
	Title      string
	Month      string
	Prev, Next string
	Weeks      [][]calCell
	Legend     []calLegend
	Day        string
	DayRows    []entryRow
	AnyInMonth bool
}

var weekdayNames = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func initialOf(name string) string {
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}
	return "?"
}

// parseMonth reads "2006-01"; anything else, or a month outside 1970 to 2100, is today's month.
func parseMonth(s string, now time.Time) time.Time {
	t, err := time.Parse("2006-01", s)
	if err != nil || t.Year() < 1970 || t.Year() > 2100 {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return t.UTC()
}

// getSpaceCalendar draws one month of the shared calendar: a marker per member per day, with a
// highlight for days when two or more were active, and the day's entries when a day is chosen.
// The only data paths are share.Days and share.Entries, so it shows exactly what the list shows.
func (s *Server) getSpaceCalendar(q *rq) {
	sp, _, ok := s.gate(q)
	if !ok {
		return
	}
	now := s.now().UTC()
	qv := q.r.URL.Query()
	var dayT time.Time
	if d := qv.Get("day"); d != "" {
		if t, err := time.Parse("2006-01-02", d); err == nil && t.Year() >= 1970 && t.Year() <= 2100 {
			dayT = t.UTC()
		}
	}
	month := parseMonth(qv.Get("month"), now)
	if qv.Get("month") == "" && !dayT.IsZero() {
		month = time.Date(dayT.Year(), dayT.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	// The grid starts on the Monday of the first week and ends on the Sunday of the last.
	gridFrom := month.AddDate(0, 0, -((int(month.Weekday()) + 6) % 7))
	next := month.AddDate(0, 1, 0)
	gridTo := next.AddDate(0, 0, (7-((int(next.Weekday())+6)%7))%7)

	var days []share.Day
	var cards []share.Card
	var dayEntries []share.Entry
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		if cards, err = share.Cards(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, now); err != nil {
			return err
		}
		if days, err = share.Days(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, gridFrom, gridTo); err != nil {
			return err
		}
		if !dayT.IsZero() {
			// A day is each person's own calendar day, so ask for a day more on each side and keep the matching ones.
			var all []share.Entry
			all, _, err = share.Entries(tx, sp.ID, q.id.user.ID, spaces.ConsentVersion, share.Query{From: dayT.Add(-26 * time.Hour), To: dayT.AddDate(0, 0, 1).Add(26 * time.Hour)})
			for _, e := range all {
				if e.Day == dayT.Format("2006-01-02") {
					dayEntries = append(dayEntries, e)
				}
			}
		}
		return err
	})
	switch {
	case errors.Is(err, share.ErrNotMember):
		q.notFound()
		return
	case errors.Is(err, share.ErrNeedConsent):
		q.redirect("/spaces/" + sp.ID)
		return
	case err != nil:
		q.serverError("read calendar", err)
		return
	}

	v := &calView{Title: month.Format("January 2006"), Month: month.Format("2006-01"),
		Prev: month.AddDate(0, -1, 0).Format("2006-01"), Next: next.Format("2006-01")}
	for i, c := range cards {
		if c.Hidden {
			continue
		}
		l := calLegend{UserID: c.UserID, Initial: initialOf(c.Name), Class: "m" + string(rune('0'+i%markClasses)), Name: c.Name}
		v.Legend = append(v.Legend, l)
	}
	byDate := map[string]share.Day{}
	for _, d := range days {
		byDate[d.Date] = d
	}
	today := now.Format("2006-01-02")
	for w := gridFrom; w.Before(gridTo); w = w.AddDate(0, 0, 7) {
		var row []calCell
		for i := 0; i < 7; i++ {
			d := w.AddDate(0, 0, i)
			key := d.Format("2006-01-02")
			c := calCell{Num: d.Day(), Date: key, InMonth: d.Month() == month.Month(), Today: key == today, Selected: !dayT.IsZero() && key == dayT.Format("2006-01-02")}
			if dd, ok := byDate[key]; ok {
				// Markers follow the legend order so a person always has the same colour.
				for _, l := range v.Legend {
					if n := dd.ByUser[l.UserID]; n > 0 {
						c.Marks = append(c.Marks, calMark{Initial: l.Initial, Class: l.Class, Name: l.Name, Count: n})
					}
				}
				c.Both = len(c.Marks) >= 2
				if len(c.Marks) > 4 {
					c.More, c.Marks = len(c.Marks)-4, c.Marks[:4]
				}
				if c.InMonth {
					v.AnyInMonth = true
				}
			}
			c.Link = "/spaces/" + sp.ID + "/calendar?month=" + month.Format("2006-01") + "&day=" + key
			c.Label = d.Format("Monday 2 January")
			row = append(row, c)
		}
		v.Weeks = append(v.Weeks, row)
	}
	if !dayT.IsZero() {
		v.Day = dayT.Format("Monday 2 January 2006")
		for _, e := range dayEntries {
			v.DayRows = append(v.DayRows, fmtEntry(e))
		}
	}
	p := &page{Title: sp.Name + " calendar", Active: "spaces", SpaceID: sp.ID, SpaceName: sp.Name, Cal: v}
	q.render(http.StatusOK, "calendar", p)
}
