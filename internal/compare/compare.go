// Package compare is the metrics registry for the compare view (D-18, D-35, M4 piece D).
//
// A metric is a small named, versioned definition built only on neutral fields of an entry
// (when, length, rating, and how many distinct members its set columns name), so it survives
// changes in Tryst's schema. It sees only what share.Entries already let through; it never reads
// the database. A metric records which Tryst schema versions it supports and is hidden, not
// wrong, for a member whose backup is from a newer or unknown one. When Tryst adds an Insights
// card, decide here whether it joins the registry.
package compare

import (
	"sort"
	"strconv"
	"time"

	"github.com/herbiewalker/alcove/internal/share"
)

// Period selects which entries the per-period metrics count. To is exclusive; a zero From is "from the start".
type Period struct {
	ID, Label string
	From, To  time.Time
}

// Periods are the choices on the page. The default is the first.
func Periods(now time.Time) []Period {
	y := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	return []Period{
		{ID: "12m", Label: "Last 12 months", From: now.AddDate(-1, 0, 0), To: now.AddDate(0, 0, 1)},
		{ID: "year", Label: "This year", From: y, To: y.AddDate(1, 0, 0)},
		{ID: "all", Label: "All time"},
	}
}

// PeriodByID returns the named period, or the default for anything else.
func PeriodByID(id string, now time.Time) Period {
	ps := Periods(now)
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	return ps[0]
}

func (p Period) has(t time.Time) bool {
	return (p.From.IsZero() || !t.Before(p.From)) && (p.To.IsZero() || t.Before(p.To))
}

// Input is everything a metric may use.
type Input struct {
	Members []share.Member
	Entries []share.Entry // all the entries the gate shows, any period
	Period  Period
	Now     time.Time
}

// Cell is one value in a table. Frac is 0 to 1 for a bar, or negative for none.
type Cell struct {
	Text string
	Frac float64
}

// Section is what one metric produces: a table with one column per member.
type Section struct {
	ID, Title string
	Version   int
	Head      []string
	Rows      []Row
	Skipped   []string // members this metric cannot read (a newer Tryst version)
	Bars      bool
}

// Row is a label and one cell per member.
type Row struct {
	Label string
	Cells []Cell
}

// Metric is one registry entry.
type Metric struct {
	ID, Title string
	Version   int
	// MinSchema and MaxSchema bound the Tryst database versions the metric understands.
	MinSchema, MaxSchema int
	Build                func(in Input, ms []share.Member) Section
}

// Supports reports whether the metric can read a backup of that Tryst version. An unknown version
// (0, no backup) has no entries anyway and is allowed.
func (m Metric) Supports(schema int) bool {
	return schema == 0 || schema >= m.MinSchema && schema <= m.MaxSchema
}

// Registry is the list of metrics, in the order the page shows them.
var Registry = []Metric{
	{ID: "summary", Title: "At a glance", Version: 1, MinSchema: 1, MaxSchema: 19, Build: summary},
	{ID: "rhythm", Title: "Rhythm", Version: 1, MinSchema: 1, MaxSchema: 19, Build: rhythm},
	{ID: "trend", Title: "Trend", Version: 1, MinSchema: 1, MaxSchema: 19, Build: trend},
	{ID: "variety", Title: "Variety", Version: 1, MinSchema: 1, MaxSchema: 19, Build: variety},
}

// Build runs every metric. A member whose backup a metric cannot read is left out of that metric
// and named in its Skipped list.
func Build(in Input) []Section {
	var out []Section
	for _, m := range Registry {
		var use []share.Member
		var skipped []string
		for _, mem := range in.Members {
			if m.Supports(mem.Schema) {
				use = append(use, mem)
			} else {
				skipped = append(skipped, mem.Name)
			}
		}
		s := m.Build(in, use)
		s.ID, s.Title, s.Version, s.Skipped = m.ID, m.Title, m.Version, skipped
		out = append(out, s)
	}
	return out
}

func heads(ms []share.Member) []string {
	h := make([]string, len(ms))
	for i, m := range ms {
		h[i] = m.Name
		if m.Mine {
			h[i] += " (you)"
		}
	}
	return h
}

func byOwner(es []share.Entry, keep func(share.Entry) bool) map[string][]share.Entry {
	out := map[string][]share.Entry{}
	for _, e := range es {
		if keep == nil || keep(e) {
			out[e.OwnerID] = append(out[e.OwnerID], e)
		}
	}
	return out
}

func inPeriod(p Period) func(share.Entry) bool {
	return func(e share.Entry) bool { return p.has(e.Start) }
}

const dash = "–"

func num(n int) Cell { return Cell{Text: strconv.Itoa(n), Frac: -1} }

func text(s string) Cell {
	if s == "" {
		s = dash
	}
	return Cell{Text: s, Frac: -1}
}

func avg(sum float64, n int, digits int) string {
	if n == 0 {
		return dash
	}
	return strconv.FormatFloat(sum/float64(n), 'f', digits, 64)
}

// summary: per member and period, entries, active days, average rating and average length.
func summary(in Input, ms []share.Member) Section {
	by := byOwner(in.Entries, inPeriod(in.Period))
	s := Section{Head: heads(ms)}
	rows := []Row{{Label: "Entries"}, {Label: "Active days"}, {Label: "Average rating (of 5)"}, {Label: "Average length (minutes)"}}
	for _, m := range ms {
		es := by[m.UserID]
		days := map[string]bool{}
		var rs, ds float64
		var rn, dn int
		for _, e := range es {
			days[e.Day] = true
			if e.Rating != nil {
				rs += float64(*e.Rating)
				rn++
			}
			if e.Duration != nil {
				ds += float64(*e.Duration)
				dn++
			}
		}
		rows[0].Cells = append(rows[0].Cells, num(len(es)))
		rows[1].Cells = append(rows[1].Cells, num(len(days)))
		rows[2].Cells = append(rows[2].Cells, text(avg(rs, rn, 1)))
		rows[3].Cells = append(rows[3].Cells, text(avg(ds, dn, 0)))
	}
	s.Rows = rows
	return s
}

var weekdays = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}

// rhythm: the longest run of consecutive weeks with an entry, and the busiest weekday and month.
func rhythm(in Input, ms []share.Member) Section {
	by := byOwner(in.Entries, inPeriod(in.Period))
	s := Section{Head: heads(ms)}
	rows := []Row{{Label: "Longest streak (weeks in a row)"}, {Label: "Busiest weekday"}, {Label: "Busiest month"}}
	for _, m := range ms {
		es := by[m.UserID]
		weeks := map[int]bool{}
		wd := map[time.Weekday]int{}
		mo := map[time.Month]int{}
		for _, e := range es {
			weeks[weekIndex(e.Local)] = true
			wd[e.Local.Weekday()]++
			mo[e.Local.Month()]++
		}
		rows[0].Cells = append(rows[0].Cells, num(longestRun(weeks)))
		best, bn := "", 0
		for _, d := range weekdays { // Monday first, so a tie goes to the earlier day
			if wd[d] > bn {
				best, bn = d.String(), wd[d]
			}
		}
		rows[1].Cells = append(rows[1].Cells, text(best))
		bm, bmn := "", 0
		for i := time.January; i <= time.December; i++ {
			if mo[i] > bmn {
				bm, bmn = i.String(), mo[i]
			}
		}
		rows[2].Cells = append(rows[2].Cells, text(bm))
	}
	s.Rows = rows
	return s
}

// weekIndex numbers Monday-to-Sunday weeks from one fixed Monday, so consecutive weeks differ by 1.
func weekIndex(t time.Time) int {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	monday := d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
	ref := time.Date(1970, 1, 5, 0, 0, 0, 0, time.UTC) // a Monday
	return int(monday.Sub(ref).Hours() / (24 * 7))
}

func longestRun(weeks map[int]bool) int {
	if len(weeks) == 0 {
		return 0
	}
	ks := make([]int, 0, len(weeks))
	for k := range weeks {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	best, run := 1, 1
	for i := 1; i < len(ks); i++ {
		if ks[i] == ks[i-1]+1 {
			run++
		} else {
			run = 1
		}
		if run > best {
			best = run
		}
	}
	return best
}

// trend: entries per month for the last twelve months, and per year, side by side. It ignores the
// period choice, because a trend is the whole point of looking across time.
func trend(in Input, ms []share.Member) Section {
	s := Section{Head: heads(ms), Bars: true}
	by := byOwner(in.Entries, nil)
	now := in.Now
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -11, 0)
	type key struct{ owner, label string }
	count := map[key]int{}
	years := map[int]bool{}
	for _, m := range ms {
		for _, e := range by[m.UserID] {
			y := e.Local.Year()
			years[y] = true
			count[key{m.UserID, strconv.Itoa(y)}]++
			if !e.Local.Before(first) {
				count[key{m.UserID, e.Local.Format("2006-01")}]++
			}
		}
	}
	var labels []string
	for i := 0; i < 12; i++ {
		labels = append(labels, first.AddDate(0, i, 0).Format("2006-01"))
	}
	var ys []int
	for y := range years {
		ys = append(ys, y)
	}
	sort.Ints(ys)
	for _, y := range ys {
		labels = append(labels, strconv.Itoa(y))
	}
	maxMonth, maxYear := 0, 0
	for k, n := range count {
		if len(k.label) == 7 && n > maxMonth {
			maxMonth = n
		}
		if len(k.label) == 4 && n > maxYear {
			maxYear = n
		}
	}
	for _, l := range labels {
		row := Row{Label: l}
		mx := maxMonth
		if len(l) == 4 {
			mx = maxYear
		}
		for _, m := range ms {
			n := count[key{m.UserID, l}]
			f := 0.0
			if mx > 0 {
				f = float64(n) / float64(mx)
			}
			row.Cells = append(row.Cells, Cell{Text: strconv.Itoa(n), Frac: f})
		}
		s.Rows = append(s.Rows, row)
	}
	return s
}

var varietyLabels = map[string]string{
	"practicesPerformed": "Different acts done", "practicesReceived": "Different acts received", "positions": "Different positions",
	"kinks": "Different kinks", "contexts": "Different settings", "occasions": "Different occasions", "toys": "Different toys",
}

// variety: how many different things each member used in the period. Counts only: the names are
// each person's own labels, so they cannot be compared across people (D-18).
func variety(in Input, ms []share.Member) Section {
	by := byOwner(in.Entries, inPeriod(in.Period))
	s := Section{Head: heads(ms)}
	for _, col := range share.SetColumns {
		row := Row{Label: varietyLabels[col]}
		for _, m := range ms {
			seen := map[string]bool{}
			for _, e := range by[m.UserID] {
				for _, v := range e.Sets[col] {
					seen[v] = true
				}
			}
			row.Cells = append(row.Cells, num(len(seen)))
		}
		s.Rows = append(s.Rows, row)
	}
	return s
}
