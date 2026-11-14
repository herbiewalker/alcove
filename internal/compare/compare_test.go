package compare

import (
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/share"
)

var now = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func ent(owner string, y int, m time.Month, d int, rating, dur int, sets map[string][]string) share.Entry {
	at := time.Date(y, m, d, 20, 0, 0, 0, time.UTC)
	e := share.Entry{OwnerID: owner, Start: at, Local: at, Day: at.Format("2006-01-02"), Sets: sets}
	if rating > 0 {
		e.Rating = &rating
	}
	if dur > 0 {
		e.Duration = &dur
	}
	return e
}

var ann = share.Member{UserID: "a", Name: "Ann", Schema: 19, Mine: true}
var bob = share.Member{UserID: "b", Name: "Bob", Schema: 19}

func section(t *testing.T, ss []Section, id string) Section {
	t.Helper()
	for _, s := range ss {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no section %q", id)
	return Section{}
}

func row(t *testing.T, s Section, label string) []string {
	t.Helper()
	for _, r := range s.Rows {
		if r.Label == label {
			var out []string
			for _, c := range r.Cells {
				out = append(out, c.Text)
			}
			return out
		}
	}
	t.Fatalf("no row %q in %s", label, s.ID)
	return nil
}

func eq(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestRegistryIsSane(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Registry {
		if m.ID == "" || m.Title == "" || m.Version < 1 || m.Build == nil || m.MinSchema < 1 || m.MaxSchema < m.MinSchema {
			t.Errorf("metric %+v is incomplete", m.ID)
		}
		if seen[m.ID] {
			t.Errorf("duplicate metric id %q", m.ID)
		}
		seen[m.ID] = true
	}
	// A metric must cover every Tryst version Alcove documents (trystbk.LatestKnownSchema is 19).
	for _, m := range Registry {
		if !m.Supports(19) || !m.Supports(1) {
			t.Errorf("%s does not cover schemas 1 to 19", m.ID)
		}
	}
}

func TestSummaryCountsOnlyThePeriod(t *testing.T) {
	es := []share.Entry{
		ent("a", 2026, 11, 1, 4, 30, nil), ent("a", 2026, 11, 1, 2, 60, nil), ent("a", 2026, 3, 5, 5, 0, nil),
		ent("a", 2024, 1, 1, 1, 10, nil), // outside the last 12 months
		ent("b", 2026, 10, 10, 0, 45, nil),
	}
	in := Input{Members: []share.Member{ann, bob}, Entries: es, Now: now, Period: PeriodByID("12m", now)}
	s := section(t, Build(in), "summary")
	eq(t, "entries", row(t, s, "Entries"), "3", "1")
	eq(t, "active days", row(t, s, "Active days"), "2", "1")
	eq(t, "rating", row(t, s, "Average rating (of 5)"), "3.7", dash) // (4+2+5)/3
	eq(t, "length", row(t, s, "Average length (minutes)"), "45", "45")
	in.Period = PeriodByID("all", now)
	eq(t, "all time", row(t, section(t, Build(in), "summary"), "Entries"), "4", "1")
	in.Period = PeriodByID("year", now)
	eq(t, "this year", row(t, section(t, Build(in), "summary"), "Entries"), "3", "1")
	in.Period = PeriodByID("nonsense", now)
	if in.Period.ID != "12m" {
		t.Errorf("an unknown period should fall back to the default, got %q", in.Period.ID)
	}
}

func TestStreakCountsConsecutiveMondayToSundayWeeks(t *testing.T) {
	// 2026-11-08 is a Sunday, 2026-11-09 the next Monday: consecutive weeks. Then a gap, then one week.
	es := []share.Entry{
		ent("a", 2026, 11, 2, 0, 0, nil), ent("a", 2026, 11, 8, 0, 0, nil), ent("a", 2026, 11, 9, 0, 0, nil), // weeks of 2nd, 2nd, 9th
		ent("a", 2026, 9, 1, 0, 0, nil),
		ent("b", 2026, 11, 1, 0, 0, nil), ent("b", 2026, 11, 14, 0, 0, nil), // weeks 26 Oct and 9 Nov: a gap week between
	}
	in := Input{Members: []share.Member{ann, bob}, Entries: es, Now: now, Period: PeriodByID("all", now)}
	s := section(t, Build(in), "rhythm")
	eq(t, "streak", row(t, s, "Longest streak (weeks in a row)"), "2", "1")
	// Weeks that do not touch are not a streak even when entries are close.
	if got := longestRun(map[int]bool{1: true, 3: true, 4: true, 5: true, 9: true}); got != 3 {
		t.Errorf("longestRun = %d, want 3", got)
	}
	if got := longestRun(nil); got != 0 {
		t.Errorf("empty = %d", got)
	}
}

func TestBusiestWeekdayAndMonthWithTies(t *testing.T) {
	// Ann: two Tuesdays and one Friday; two in March, one in June. Bob: a Wednesday and a Monday (tie, Monday wins).
	es := []share.Entry{
		ent("a", 2026, 3, 3, 0, 0, nil),  // Tuesday
		ent("a", 2026, 3, 10, 0, 0, nil), // Tuesday
		ent("a", 2026, 6, 5, 0, 0, nil),  // Friday
		ent("b", 2026, 5, 6, 0, 0, nil),  // Wednesday
		ent("b", 2026, 6, 1, 0, 0, nil),  // Monday
	}
	in := Input{Members: []share.Member{ann, bob}, Entries: es, Now: now, Period: PeriodByID("all", now)}
	s := section(t, Build(in), "rhythm")
	eq(t, "weekday", row(t, s, "Busiest weekday"), "Tuesday", "Monday")
	eq(t, "month", row(t, s, "Busiest month"), "March", "May") // bob: one each in May and June, May is earlier
}

func TestTrendMonthsAndYears(t *testing.T) {
	es := []share.Entry{
		ent("a", 2026, 11, 3, 0, 0, nil), ent("a", 2026, 11, 4, 0, 0, nil), ent("a", 2025, 12, 1, 0, 0, nil),
		ent("a", 2024, 2, 1, 0, 0, nil), ent("b", 2026, 11, 9, 0, 0, nil), ent("b", 2025, 11, 30, 0, 0, nil), // 2025-11 is outside the 12 months ending 2026-11? No: it is the first month shown.
	}
	in := Input{Members: []share.Member{ann, bob}, Entries: es, Now: now, Period: PeriodByID("all", now)}
	s := section(t, Build(in), "trend")
	if !s.Bars || len(s.Rows) < 14 {
		t.Fatalf("trend rows: %d", len(s.Rows))
	}
	if s.Rows[0].Label != "2025-12" || s.Rows[11].Label != "2026-11" {
		t.Errorf("months run %s to %s, want 2025-12 to 2026-11", s.Rows[0].Label, s.Rows[11].Label)
	}
	eq(t, "2026-11", row(t, s, "2026-11"), "2", "1")
	eq(t, "2025-12", row(t, s, "2025-12"), "1", "0")
	eq(t, "2026 total", row(t, s, "2026"), "2", "1")
	eq(t, "2025 total", row(t, s, "2025"), "1", "1")
	eq(t, "2024 total", row(t, s, "2024"), "1", "0")
	for _, r := range s.Rows {
		for _, c := range r.Cells {
			if c.Frac < 0 || c.Frac > 1 {
				t.Errorf("%s: bar fraction %v out of range", r.Label, c.Frac)
			}
		}
	}
	if f := s.Rows[11].Cells[0].Frac; f != 1 {
		t.Errorf("the busiest month should fill its bar, got %v", f)
	}
}

func TestVarietyCountsDistinctMembersNeverNames(t *testing.T) {
	es := []share.Entry{
		ent("a", 2026, 11, 1, 0, 0, map[string][]string{"toys": {"t1", "t2"}, "positions": {"p1"}}),
		ent("a", 2026, 11, 2, 0, 0, map[string][]string{"toys": {"t2", "t3"}}),
		ent("b", 2026, 11, 3, 0, 0, map[string][]string{"kinks": {"k1"}}),
	}
	in := Input{Members: []share.Member{ann, bob}, Entries: es, Now: now, Period: PeriodByID("all", now)}
	s := section(t, Build(in), "variety")
	eq(t, "toys", row(t, s, "Different toys"), "3", "0")
	eq(t, "positions", row(t, s, "Different positions"), "1", "0")
	eq(t, "kinks", row(t, s, "Different kinks"), "0", "1")
	if len(s.Rows) != len(share.SetColumns) {
		t.Errorf("%d rows for %d set columns", len(s.Rows), len(share.SetColumns))
	}
	for _, r := range s.Rows {
		for _, c := range r.Cells {
			if strings.Contains(c.Text, "t1") || strings.Contains(c.Text, "k1") {
				t.Errorf("a name leaked into a cell: %q", c.Text)
			}
		}
	}
}

func TestANewerBackupIsHiddenNotWrong(t *testing.T) {
	newer := share.Member{UserID: "c", Name: "Cy", Schema: 99}
	es := []share.Entry{ent("a", 2026, 11, 1, 3, 20, nil), ent("c", 2026, 11, 1, 5, 20, nil)}
	in := Input{Members: []share.Member{ann, newer}, Entries: es, Now: now, Period: PeriodByID("all", now)}
	for _, s := range Build(in) {
		if len(s.Skipped) != 1 || s.Skipped[0] != "Cy" {
			t.Errorf("%s: skipped = %v", s.ID, s.Skipped)
		}
		if len(s.Head) != 1 {
			t.Errorf("%s: the unreadable member still has a column: %v", s.ID, s.Head)
		}
	}
	// No backup at all (schema 0) is not "unreadable": the member simply has nothing yet.
	none := share.Member{UserID: "d", Name: "Di"}
	s := section(t, Build(Input{Members: []share.Member{ann, none}, Entries: es, Now: now, Period: PeriodByID("all", now)}), "summary")
	if len(s.Skipped) != 0 || len(s.Head) != 2 {
		t.Errorf("a member with no backup: skipped %v, head %v", s.Skipped, s.Head)
	}
	eq(t, "no backup", row(t, s, "Entries"), "1", "0")
}

func TestEmptyInputsDoNotPanic(t *testing.T) {
	for _, in := range []Input{
		{Now: now, Period: PeriodByID("all", now)},
		{Members: []share.Member{ann}, Now: now, Period: PeriodByID("year", now)},
	} {
		if ss := Build(in); len(ss) != len(Registry) {
			t.Errorf("%d sections", len(ss))
		}
	}
	// A "you" marker on the header.
	h := heads([]share.Member{ann, bob})
	if h[0] != "Ann (you)" || h[1] != "Bob" {
		t.Errorf("heads: %v", h)
	}
}

func TestMetricsUseTheOwnersLocalDay(t *testing.T) {
	// 23:30 UTC on the 1st is already the 2nd in Berlin: active days and weekday follow the local day.
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no zone database here")
	}
	at := time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC) // Sunday UTC, Monday in Berlin
	e := share.Entry{OwnerID: "a", Start: at, Local: at.In(loc), Day: at.In(loc).Format("2006-01-02")}
	in := Input{Members: []share.Member{ann}, Entries: []share.Entry{e}, Now: now, Period: PeriodByID("all", now)}
	s := section(t, Build(in), "rhythm")
	eq(t, "weekday", row(t, s, "Busiest weekday"), "Monday")
}

func TestPeriodEndsAreExclusiveAndActiveDaysAreLocal(t *testing.T) {
	// An entry at the stroke of the new year belongs to the new year, not "this year".
	edge := share.Entry{OwnerID: "a", Start: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	edge.Local, edge.Day = edge.Start, "2027-01-01"
	inside := ent("a", 2026, 12, 31, 0, 0, nil)
	// Two entries on one local day that are on different UTC days.
	loc := time.FixedZone("plus10", 10*3600)
	a := share.Entry{OwnerID: "b", Start: time.Date(2026, 5, 1, 23, 0, 0, 0, time.UTC)} // 09:00 on the 2nd locally
	b := share.Entry{OwnerID: "b", Start: time.Date(2026, 5, 2, 5, 0, 0, 0, time.UTC)}  // 15:00 on the 2nd locally
	for _, e := range []*share.Entry{&a, &b} {
		e.Local = e.Start.In(loc)
		e.Day = e.Local.Format("2006-01-02")
	}
	in := Input{Members: []share.Member{ann, bob}, Entries: []share.Entry{edge, inside, a, b}, Now: now, Period: PeriodByID("year", now.AddDate(0, -10, 0))}
	// now - 10 months is January 2026, so the period is calendar 2026.
	s := section(t, Build(in), "summary")
	eq(t, "entries in 2026", row(t, s, "Entries"), "1", "2")
	eq(t, "active days", row(t, s, "Active days"), "1", "1")
}
