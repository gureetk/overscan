// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"overscan.gureet.ca/internal/store"
)

// Messages from the administrator (D96). They come with the lineup and its
// version, which the page asks for when it starts and every few minutes;
// the page shows each once, and says when it was read.

// boxMessage is a message as the box shows it.
type boxMessage struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
	Sent int64  `json:"sent"` // Unix seconds
}

// messagesOf returns the messages that d has still to show, the oldest
// first.
func (a *API) messagesOf(ctx context.Context, d store.Device) ([]boxMessage, error) {
	list, err := a.store.BoxMessages(ctx, d.ID, a.now())
	if err != nil {
		return nil, err
	}
	out := make([]boxMessage, len(list))
	for i, m := range list {
		out[i] = boxMessage{ID: m.ID, Text: m.Text, Sent: m.SentAt.Unix()}
	}
	return out, nil
}

// readMessage answers POST messages/{id}/read, which says that the box
// showed a message and the viewer closed it. Reading one again changes
// nothing.
func (a *API) readMessage(w http.ResponseWriter, r *http.Request, d store.Device) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	err = a.store.ReadMessage(r.Context(), d.ID, id, a.now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "record that a message was read", err)
		return
	}
	a.log.Info("a box read a message", "mac", d.MAC, "label", d.Label, "message", id)
	writeJSON(w, http.StatusOK, struct {
		ID   int64 `json:"id"`
		Read bool  `json:"read"`
	}{id, true})
}
