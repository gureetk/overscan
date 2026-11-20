// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
)

// createArchiveLink answers tv_archive/create_link with a link that plays a
// past programme from its channel's archive (D81), or with the reason for
// none: nothing_to_play for a programme that the device cannot play from
// the archive, and limit when the channel's source has no free connection.
// Its cmd names the programme as /media/<id>.mpg, by the ID that the guide
// gives it (protocol notes, section 5).
func (a *API) createArchiveLink(w http.ResponseWriter, r *http.Request, d store.Device) {
	reply := func(cmd, errText string) {
		writeJS(w, struct {
			Cmd   string `json:"cmd"`
			Error string `json:"error"`
		}{cmd, errText})
	}
	entryID, minute, ok := programmeFromCmd(r.Form.Get("cmd"))
	if !ok {
		reply("", "nothing_to_play")
		return
	}
	ctx := r.Context()
	entry, err := a.store.PlayableArchive(ctx, entryID)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrNoArchive) {
		reply("", "nothing_to_play")
		return
	}
	if err != nil {
		a.internalError(w, "look up a channel's archive", err)
		return
	}
	acct, err := a.store.Account(ctx, d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	if entry.LineupID != acct.LineupID || (entry.Locked && acct.PINHash != "") {
		reply("", "nothing_to_play")
		return
	}
	p, err := a.store.ProgrammeStarting(ctx, acct.LineupID, entryID, minute, minute.Add(time.Minute))
	if errors.Is(err, store.ErrNotFound) || err == nil && !store.InArchive(p, entry.Days, a.now()) {
		reply("", "nothing_to_play")
		return
	}
	if err != nil {
		a.internalError(w, "look up a programme", err)
		return
	}
	if a.hub.Room(r.Context(), entry.SourceID, d.ID) != nil {
		reply("", "limit")
		return
	}
	link, _ := a.hub.ArchiveLink(d.ID, entry.PlayableEntry, p.Start, p.Stop)
	reply("ffmpeg http://"+r.Host+link, "")
}

// programmeFromCmd returns the channel and the minute of the start of the
// programme that an archive's cmd names: the last word of the cmd is a path,
// or an address whose path, is /media/<id>.mpg.
func programmeFromCmd(cmd string) (int64, time.Time, bool) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return 0, time.Time{}, false
	}
	u, err := url.Parse(fields[len(fields)-1])
	if err != nil {
		return 0, time.Time{}, false
	}
	rest, ok := strings.CutPrefix(u.Path, "/media/")
	if !ok {
		return 0, time.Time{}, false
	}
	id, ok := strings.CutSuffix(rest, ".mpg")
	if !ok {
		return 0, time.Time{}, false
	}
	return programmeOf(id)
}
