package server

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/profile"
)

type profileField struct {
	Key, Label, Value string
	Shared            bool
	Long              bool
	Birth             bool
}

type offerRow struct {
	Key, Label, Value string
	Checked           bool
}

func (s *Server) profileFields(q *rq, values map[string]string, shared map[string]bool) []profileField {
	var out []profileField
	for _, f := range profile.Fields {
		out = append(out, profileField{Key: f.Key, Label: f.Label, Value: values[f.Key], Shared: shared[f.Key], Long: f.Key == "about", Birth: f.Key == profile.Birth})
	}
	return out
}

func (s *Server) profilePage(q *rq) (*page, error) {
	var stored map[string]profile.Value
	var name string
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		stored, err = profile.Get(tx, q.id.user.ID)
		if err != nil {
			return err
		}
		u, err := auth.ByID(tx, q.id.user.ID)
		name = u.DisplayName
		return err
	})
	if err != nil {
		return nil, err
	}
	values, shared := map[string]string{}, map[string]bool{}
	for k, v := range stored {
		values[k], shared[k] = v.Value, v.Shared
	}
	return &page{Title: "Your profile", Active: "account", ProfileName: name, ProfileFields: s.profileFields(q, values, shared)}, nil
}

func (s *Server) getProfile(q *rq) {
	p, err := s.profilePage(q)
	if err != nil {
		q.serverError("read profile", err)
		return
	}
	q.render(http.StatusOK, "profile", p)
}

func profileMessage(err error) string {
	switch {
	case errors.Is(err, profile.ErrBadBirth):
		return "The birth date must be a real date written like 1990-04-23."
	case errors.Is(err, profile.ErrUnderage):
		return "Alcove is for adults, so the birth date must be at least 18 years ago."
	case errors.Is(err, profile.ErrTooLong):
		return "One of the fields is too long or holds characters that are not allowed."
	case errors.Is(err, auth.ErrBadDisplayName):
		return "The name shown to others must be 1 to 80 characters."
	}
	return ""
}

// postProfile saves the person's own profile and name. Everything is validated first; one bad
// field changes nothing. Only the signed-in person's own rows are ever written.
func (s *Server) postProfile(q *rq) {
	f := q.r.PostForm
	values, shared := map[string]string{}, map[string]bool{}
	for _, fd := range profile.Fields {
		values[fd.Key] = f.Get("v_" + fd.Key)
		shared[fd.Key] = f.Get("s_"+fd.Key) == "1"
	}
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		if err := profile.Save(tx, s.now(), q.id.user.ID, values, shared); err != nil {
			return err
		}
		return auth.SetDisplayName(tx, q.id.user.ID, f.Get("name"))
	})
	if msg := profileMessage(err); msg != "" {
		p := &page{Title: "Your profile", Active: "account", Error: msg, ProfileName: f.Get("name"), ProfileFields: s.profileFields(q, values, shared)}
		q.render(http.StatusBadRequest, "profile", p)
		return
	}
	if err != nil {
		q.serverError("save profile", err)
		return
	}
	q.setFlash("profile")
	q.redirect("/profile")
}

func (s *Server) offersPage(q *rq) (*page, error) {
	var offers []profile.Offer
	err := q.st.View(q.r.Context(), func(tx *sql.Tx) (err error) {
		offers, err = profile.Offers(tx, q.id.user.ID, s.now())
		return
	})
	if err != nil {
		return nil, err
	}
	p := &page{Title: "Fill from my backup", Active: "account"}
	for _, o := range offers {
		p.Offers = append(p.Offers, offerRow{Key: o.Key, Label: o.Label, Value: o.Value, Checked: o.Name})
	}
	return p, nil
}

func (s *Server) getPrefill(q *rq) {
	p, err := s.offersPage(q)
	if errors.Is(err, profile.ErrNoProfile) {
		q.message(http.StatusOK, "Nothing to fill from", "Your latest backup has no profile details, or you have not uploaded one yet. You can type them in yourself.", "/profile")
		return
	}
	if err != nil {
		q.serverError("read offers", err)
		return
	}
	q.render(http.StatusOK, "prefill", p)
}

// postPrefill imports exactly the fields the person ticked. It reads the offers again from the
// stored backup rather than trusting any value in the form, and never turns sharing on.
func (s *Server) postPrefill(q *rq) {
	f := q.r.PostForm
	err := q.st.Update(q.r.Context(), true, func(tx *sql.Tx) error {
		offers, err := profile.Offers(tx, q.id.user.ID, s.now())
		if err != nil {
			return err
		}
		stored, err := profile.Get(tx, q.id.user.ID)
		if err != nil {
			return err
		}
		values, shared := map[string]string{}, map[string]bool{}
		for k, v := range stored {
			values[k], shared[k] = v.Value, v.Shared
		}
		for _, o := range offers {
			if f.Get("take_"+o.Key) != "1" {
				continue
			}
			if o.Name {
				if err := auth.SetDisplayName(tx, q.id.user.ID, o.Value); err != nil {
					return err
				}
				continue
			}
			values[o.Key] = o.Value // an imported field keeps its current share choice, private if new
		}
		return profile.Save(tx, s.now(), q.id.user.ID, values, shared)
	})
	if errors.Is(err, profile.ErrNoProfile) {
		q.redirect("/profile")
		return
	}
	if err != nil {
		q.serverError("import profile", err)
		return
	}
	q.setFlash("profile-filled")
	q.redirect("/profile")
}
