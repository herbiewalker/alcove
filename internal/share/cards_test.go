package share

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/spaces"
)

func (r *rig) cards(space, viewer string) ([]Card, error) {
	r.t.Helper()
	var out []Card
	err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) {
		out, err = Cards(tx, space, "u"+viewer, spaces.ConsentVersion, t0)
		return
	})
	return out, err
}

func (r *rig) profile(owner, field, value string, shared bool) {
	r.t.Helper()
	s := 0
	if shared {
		s = 1
	}
	r.exec(`INSERT INTO profile(owner_id, field, value, shared, updated_at) VALUES (?,?,?,?,0)`, "u"+owner, field, value, s)
}

func card(cs []Card, owner string) *Card {
	for i := range cs {
		if cs[i].UserID == "u"+owner {
			return &cs[i]
		}
	}
	return nil
}

func fieldsOf(c *Card) string {
	var parts []string
	for _, f := range c.Fields {
		parts = append(parts, f.Label+"="+f.Value)
	}
	return strings.Join(parts, "|")
}

func TestCardsShowOnlySharedFieldsAndAgeForBirth(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.profile("bob", "height", "172 cm", true)
	r.profile("bob", "sex", "private one", false)
	r.profile("bob", "birth", "1990-04-23", true)
	r.profile("bob", "location", "", true) // an empty value is never shown
	cs, err := r.cards(sp, "ann")
	if err != nil || len(cs) != 2 {
		t.Fatalf("cards: %d %v", len(cs), err)
	}
	if cs[0].UserID != "uann" || cs[0].Role != spaces.RoleOwner {
		t.Error("the owner should come first")
	}
	bob := card(cs, "bob")
	got := fieldsOf(bob)
	if got != "Birth date=Age 36|Height=172 cm" {
		t.Errorf("bob shows %q", got)
	}
	for _, leak := range []string{"private one", "1990-04-23", "1990"} {
		if strings.Contains(got, leak) {
			t.Errorf("the card leaks %q", leak)
		}
	}
	if !card(cs, "ann").Mine || card(cs, "bob").Mine {
		t.Error("Mine is wrong")
	}
	if len(card(cs, "ann").Fields) != 0 {
		t.Error("ann shared nothing and shows fields")
	}
}

// Each case must hide bob's shared field while leaving his name on the list (or removing him
// entirely when he is no longer a member).
func TestWhatHidesACard(t *testing.T) {
	cases := []struct {
		name   string
		do     func(r *rig, sp string)
		listed bool
		hidden bool
	}{
		{"paused", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET paused = 1 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		}, true, true},
		{"older wording", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		}, true, true},
		{"left", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'left' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		}, false, false},
		{"removed", func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'removed' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		}, false, false},
		{"disabled", func(r *rig, sp string) { r.exec(`UPDATE user SET status = 'disabled' WHERE id = 'ubob'`) }, false, false},
		{"deleted", func(r *rig, sp string) { r.exec(`UPDATE user SET deleted_at = 5 WHERE id = 'ubob'`) }, false, false},
		{"field unshared", func(r *rig, sp string) { r.exec(`UPDATE profile SET shared = 0`) }, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "ann", "bob")
			sp := r.space("ann", "bob")
			r.profile("bob", "height", "172 cm", true)
			if cs, _ := r.cards(sp, "ann"); card(cs, "bob") == nil || len(card(cs, "bob").Fields) != 1 {
				t.Fatal("setup: bob's field is not shown")
			}
			c.do(r, sp)
			cs, err := r.cards(sp, "ann")
			if err != nil {
				t.Fatal(err)
			}
			b := card(cs, "bob")
			if c.listed != (b != nil) {
				t.Fatalf("listed = %v, want %v", b != nil, c.listed)
			}
			if b != nil {
				if len(b.Fields) != 0 {
					t.Errorf("fields still shown: %s", fieldsOf(b))
				}
				if b.Hidden != c.hidden {
					t.Errorf("Hidden = %v, want %v", b.Hidden, c.hidden)
				}
			}
		})
	}
}

func TestCardsNeedMembershipAndCurrentConsent(t *testing.T) {
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	r.profile("bob", "height", "172 cm", true)
	if _, err := r.cards(sp, "cy"); !errors.Is(err, ErrNotMember) {
		t.Errorf("outsider: %v", err)
	}
	r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'uann'`, sp)
	if _, err := r.cards(sp, "ann"); !errors.Is(err, ErrNeedConsent) {
		t.Errorf("viewer on older wording: %v", err)
	}
	r.exec(`UPDATE space SET deleted_at = 5 WHERE id = ?`, sp)
	if _, err := r.cards(sp, "bob"); !errors.Is(err, ErrNotMember) {
		t.Errorf("deleted space: %v", err)
	}
}

func TestCardsDoNotCrossSpaces(t *testing.T) {
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	other := r.space("cy")
	r.profile("bob", "height", "172 cm", true)
	cs, err := r.cards(other, "cy")
	if err != nil || len(cs) != 1 || card(cs, "bob") != nil {
		t.Fatalf("another space shows %d cards, %v", len(cs), err)
	}
	_ = sp
}
