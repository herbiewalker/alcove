package profile

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

var now = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	k, _, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st, err := store.Open(store.Config{DataDir: filepath.Join(root, "d"), TmpDir: filepath.Join(root, "t"), Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func upd(t *testing.T, st *store.Store, fn func(tx *sql.Tx) error) error {
	t.Helper()
	return st.Update(context.Background(), false, fn)
}

func TestAge(t *testing.T) {
	for _, c := range []struct {
		birth string
		want  int
	}{{"1990-11-14", 36}, {"1990-11-15", 35}, {"1990-12-01", 35}, {"2008-11-14", 18}, {"2008-11-15", 17}} {
		b, _ := time.Parse("2006-01-02", c.birth)
		if got := Age(b, now); got != c.want {
			t.Errorf("Age(%s) = %d, want %d", c.birth, got, c.want)
		}
	}
}

func TestCleanBirthDate(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  error
	}{
		{"1990-04-23", "1990-04-23", nil},
		{" 1990-04-23 ", "1990-04-23", nil},
		{"", "", nil},
		{"2008-11-14", "2008-11-14", nil},     // exactly 18 today
		{"2008-11-15", "", ErrUnderage},       // one day short
		{"2015-01-01", "", ErrUnderage},       // a child
		{"2030-01-01", "", ErrBadBirth},       // the future
		{"1850-01-01", "", ErrBadBirth},       // too old to be real
		{"23/04/1990", "", ErrBadBirth},       // wrong shape
		{"1990-02-30", "", ErrBadBirth},       // not a date
		{"1990-04-23T00:00", "", ErrBadBirth}, // extra
		{"not a date", "", ErrBadBirth},
	}
	for _, c := range cases {
		got, err := Clean(Birth, c.in, now)
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("Clean(birth, %q) = %q, %v; want %q, %v", c.in, got, err, c.want, c.err)
		}
	}
}

func TestCleanTextAndUnknown(t *testing.T) {
	if _, err := Clean("shoe_size", "9", now); !errors.Is(err, ErrUnknownField) {
		t.Errorf("unknown field: %v", err)
	}
	long := make([]byte, 121)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Clean("sex", string(long), now); !errors.Is(err, ErrTooLong) {
		t.Errorf("121 characters: %v", err)
	}
	if _, err := Clean("about", string(long), now); err != nil {
		t.Errorf("about allows 300: %v", err)
	}
	if _, err := Clean("sex", "a\x00b", now); err == nil {
		t.Error("a control character was accepted")
	}
	if v, err := Clean("height", "  172 cm  ", now); v != "172 cm" || err != nil {
		t.Errorf("trim: %q %v", v, err)
	}
}

func TestDisplayShowsAgeNeverTheDate(t *testing.T) {
	if got := Display(Birth, "1990-04-23", now); got != "Age 36" {
		t.Errorf("birth shown as %q", got)
	}
	if got := Display("height", "172 cm", now); got != "172 cm" {
		t.Errorf("text shown as %q", got)
	}
	if got := Display(Birth, "garbage", now); got != "" {
		t.Errorf("a bad stored date is shown as %q", got)
	}
}

func TestSaveIsAllOrNothingAndPrivateByDefault(t *testing.T) {
	st := newStore(t)
	err := upd(t, st, func(tx *sql.Tx) error {
		return Save(tx, now, "ua", map[string]string{"sex": "x", "height": "170", "birth": "1990-04-23"}, map[string]bool{"height": true})
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]Value
	_ = st.View(context.Background(), func(tx *sql.Tx) (err error) { got, err = Get(tx, "ua"); return })
	if len(got) != 3 || got["height"].Shared != true || got["sex"].Shared || got["birth"].Shared {
		t.Fatalf("stored: %+v", got)
	}
	// One bad field changes nothing.
	err = upd(t, st, func(tx *sql.Tx) error {
		return Save(tx, now, "ua", map[string]string{"sex": "changed", "birth": "2015-01-01"}, map[string]bool{"sex": true})
	})
	if !errors.Is(err, ErrUnderage) {
		t.Fatalf("underage: %v", err)
	}
	_ = st.View(context.Background(), func(tx *sql.Tx) (err error) { got, err = Get(tx, "ua"); return })
	if got["sex"].Value != "x" || len(got) != 3 {
		t.Errorf("a refused save changed the profile: %+v", got)
	}
	// An empty value removes the field, so it cannot stay shared.
	_ = upd(t, st, func(tx *sql.Tx) error {
		return Save(tx, now, "ua", map[string]string{"height": ""}, map[string]bool{"height": true})
	})
	_ = st.View(context.Background(), func(tx *sql.Tx) (err error) { got, err = Get(tx, "ua"); return })
	if len(got) != 0 {
		t.Errorf("empty values should leave no rows: %+v", got)
	}
	// Another person is untouched.
	_ = upd(t, st, func(tx *sql.Tx) error { return Save(tx, now, "ub", map[string]string{"sex": "y"}, nil) })
	_ = st.View(context.Background(), func(tx *sql.Tx) (err error) { got, err = Get(tx, "ua"); return })
	if len(got) != 0 {
		t.Error("saving for b changed a")
	}
}

func TestOffersComeFromTheBackupAndImportNothing(t *testing.T) {
	st := newStore(t)
	row := `{"id":"self","displayName":"Synthetic Self","sex":"F","height":172,"birthDate":"1990-04-23","bodyType":null,"note":"hi","orientation":""}`
	if err := upd(t, st, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES ('ua','profile','self',0,?)`, row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var offers []Offer
	err := st.View(context.Background(), func(tx *sql.Tx) (err error) { offers, err = Offers(tx, "ua", now); return })
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, o := range offers {
		keys[o.Key] = o.Value
	}
	want := map[string]string{NameKey: "Synthetic Self", "sex": "F", "height": "172", "birth": "1990-04-23", "about": "hi"}
	if len(keys) != len(want) {
		t.Fatalf("offers: %v", keys)
	}
	for k, v := range want {
		if keys[k] != v {
			t.Errorf("offer %s = %q, want %q", k, keys[k], v)
		}
	}
	if !offers[0].Name {
		t.Error("the name should be first")
	}
	var n int
	_ = st.View(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM profile`).Scan(&n) })
	if n != 0 {
		t.Error("looking at the offers imported something")
	}
	// Nobody else's profile, and a backup with nothing in it.
	err = st.View(context.Background(), func(tx *sql.Tx) (err error) { _, err = Offers(tx, "ub", now); return })
	if !errors.Is(err, ErrNoProfile) {
		t.Errorf("another person: %v", err)
	}
	// An underage birth date in a backup is not offered at all.
	_ = upd(t, st, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE cache_row SET data = ? WHERE owner_id = 'ua'`, `{"id":"self","birthDate":"2015-01-01","sex":"F"}`)
		return err
	})
	_ = st.View(context.Background(), func(tx *sql.Tx) (err error) { offers, err = Offers(tx, "ua", now); return })
	for _, o := range offers {
		if o.Key == Birth {
			t.Error("an underage birth date was offered")
		}
	}
}

func TestOffersAcceptsAnEpochBirthDate(t *testing.T) {
	st := newStore(t)
	ms := time.Date(1990, 4, 23, 0, 0, 0, 0, time.UTC).UnixMilli()
	_ = upd(t, st, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES ('ua','profile','self',0,?)`, `{"id":"self","birthDate":`+itoa(ms)+`}`)
		return err
	})
	var offers []Offer
	_ = st.View(context.Background(), func(tx *sql.Tx) (err error) { offers, err = Offers(tx, "ua", now); return })
	if len(offers) != 1 || offers[0].Value != "1990-04-23" {
		t.Fatalf("offers: %+v", offers)
	}
}

func itoa(n int64) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
