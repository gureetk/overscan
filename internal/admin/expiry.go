// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
)

// The household's end date (D95). The administrator sets the last day the
// household's account plays, in the server's time zone; past it, its boxes
// say that it has ended and play nothing, its Stalker clients are refused
// as blocked ones are, and its recordings stop. Without one, it never ends.

// dayForm is how a date input sends a day.
const dayForm = "2006-01-02"

// endOfDay returns when an account that plays through day ends: the start
// of the next day, in loc.
func endOfDay(day time.Time, loc *time.Location) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

// lastDay returns the day an account that ends at t plays through, in loc.
func lastDay(t time.Time, loc *time.Location) time.Time {
	return t.Add(-time.Second).In(loc)
}

// setExpiry sets when the household's account ends; the zero time means
// never.
func (a *Admin) setExpiry(ctx context.Context, t time.Time) error {
	if err := a.store.SetAccountExpiry(ctx, householdAccount, t); err != nil {
		return err
	}
	if t.IsZero() {
		a.log.Info("the household's end date removed in the admin")
	} else {
		a.log.Info("the household's end date set in the admin", "ends", t.UTC().Format(time.RFC3339), "ended", !a.now().Before(t))
	}
	return nil
}

// expiryForm sets or removes the household's end date from the Settings
// page.
func (a *Admin) expiryForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	day := strings.TrimSpace(r.PostFormValue("date"))
	if r.PostFormValue("remove") != "" || day == "" {
		a.settingsDone(w, r, s, a.setExpiry(r.Context(), time.Time{}), "Removed the end date: the household's account never ends.")
		return
	}
	d, err := time.ParseInLocation(dayForm, day, time.Local)
	if err != nil || d.Year() < 2000 || d.Year() > 9998 {
		a.settingsDone(w, r, s, badEdit(http.StatusBadRequest, "bad_date", "Give the end date as a day, such as 2027-12-31."), "")
		return
	}
	ends := endOfDay(d, time.Local)
	done := "The household's account plays until the end of " + d.Format("2 January 2006") + "."
	if !a.now().Before(ends) {
		done = "The household's account has ended: its boxes play nothing, and its Stalker clients are refused."
	}
	a.settingsDone(w, r, s, a.setExpiry(r.Context(), ends), done)
}

// apiExpiry is the household's end date as the API describes it.
type apiExpiry struct {
	ExpiresAt *time.Time `json:"expires_at"`
	Ended     bool       `json:"ended"`
}

// expiryBody returns the household's end date as the API describes it.
func (a *Admin) expiryBody(ctx context.Context) (apiExpiry, error) {
	acct, err := a.store.Account(ctx, householdAccount)
	if err != nil {
		return apiExpiry{}, err
	}
	out := apiExpiry{Ended: acct.Ended(a.now())}
	if !acct.ExpiresAt.IsZero() {
		t := acct.ExpiresAt.UTC()
		out.ExpiresAt = &t
	}
	return out, nil
}

func (a *Admin) apiExpiry(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	out, err := a.expiryBody(r.Context())
	if err != nil {
		a.apiInternal(w, "cannot read the household's end date", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Admin) apiSetExpiry(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	var body struct {
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	var t time.Time
	if len(body.ExpiresAt) == 0 || string(body.ExpiresAt) == "null" || json.Unmarshal(body.ExpiresAt, &t) != nil || t.IsZero() {
		apiError(w, http.StatusBadRequest, "bad_date", "Give expires_at as a time, such as 2028-01-01T00:00:00Z; to remove the end date, use DELETE.")
		return
	}
	if err := a.setExpiry(r.Context(), t); err != nil {
		a.apiInternal(w, "cannot set the household's end date", err)
		return
	}
	a.apiExpiry(w, r, s)
}

func (a *Admin) apiRemoveExpiry(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	if err := a.setExpiry(r.Context(), time.Time{}); err != nil {
		a.apiInternal(w, "cannot remove the household's end date", err)
		return
	}
	a.apiExpiry(w, r, s)
}

// EndDay returns the last day the household's account plays, as a date
// input takes it, or "" if it never ends.
func (d settingsData) EndDay() string {
	if d.EndsAt.IsZero() {
		return ""
	}
	return lastDay(d.EndsAt, time.Local).Format(dayForm)
}

// EndText says the last day the household's account plays, such as
// 31 December 2027.
func (d settingsData) EndText() string {
	if d.EndsAt.IsZero() {
		return ""
	}
	return lastDay(d.EndsAt, time.Local).Format("2 January 2006")
}

// EndedText says the last day an account that has ended played, for the
// overview.
func (d overviewData) EndedText() string {
	if d.Ended.IsZero() {
		return ""
	}
	return lastDay(d.Ended, time.Local).Format("2 January 2006")
}
