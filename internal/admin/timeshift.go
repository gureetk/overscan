// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"overscan.gureet.ca/internal/store"
)

// timeshiftChoices are the minutes of a box's live channel that the
// Settings page offers to keep (D92); 0 is off.
var timeshiftChoices = []int{0, 15, 30, 60, 120}

// setTimeshift sets how many minutes of a box's live channel the server
// keeps, so that the box can pause live TV and go back in it; 0 turns
// timeshift off.
func (a *Admin) setTimeshift(ctx context.Context, minutes int) error {
	if minutes < 0 || minutes > store.MaxTimeshift {
		return badEdit(http.StatusBadRequest, "bad_timeshift", "Timeshift keeps a whole number of minutes, from 0 to %d.", store.MaxTimeshift)
	}
	if err := a.store.SetTimeshift(ctx, minutes); err != nil {
		return err
	}
	a.log.Info("timeshift set in the admin", "minutes", minutes)
	return nil
}

// timeshiftForm sets timeshift from the Settings page.
func (a *Admin) timeshiftForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	minutes, err := strconv.Atoi(r.PostFormValue("minutes"))
	if err != nil {
		a.settingsDone(w, r, s, badEdit(http.StatusBadRequest, "bad_form", "Choose how much to keep, or Off."), "")
		return
	}
	done := "Boxes play live TV as it comes, from the next channel they play."
	if minutes > 0 {
		done = fmt.Sprintf("Boxes can pause live TV and go back up to %s in it, from the next channel they play.", minutesText(minutes))
	}
	a.settingsDone(w, r, s, a.setTimeshift(r.Context(), minutes), done)
}

// TimeshiftChoices returns the minutes that the Settings page offers to
// keep, with the ones set if it is another.
func (d settingsData) TimeshiftChoices() []int {
	choices := timeshiftChoices
	if !slices.Contains(choices, d.Timeshift) {
		choices = append(slices.Clone(choices), d.Timeshift)
		slices.Sort(choices)
	}
	return choices
}

// MinutesText says how long minutes are, such as 30 minutes or 2 hours.
func (settingsData) MinutesText(minutes int) string { return minutesText(minutes) }

func minutesText(minutes int) string {
	switch {
	case minutes == 60:
		return "an hour"
	case minutes%60 == 0:
		return strconv.Itoa(minutes/60) + " hours"
	case minutes == 1:
		return "a minute"
	}
	return strconv.Itoa(minutes) + " minutes"
}
