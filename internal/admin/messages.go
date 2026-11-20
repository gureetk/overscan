// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"overscan.gureet.ca/internal/store"
)

// Messages (D96): the administrator sends a short message to one box, or to
// every box of the household that runs Overscan's page. A box shows it the
// next time it checks, and says when it was read. Stalker clients get none:
// the protocol notes do not say how a portal sends them one.

// messagesListed is how many of the last messages the Devices page lists.
const messagesListed = 10

// messageStoreError turns the store's errors about a message into mistakes
// that the request can put right.
func messageStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrMessageText):
		return badDevice(http.StatusBadRequest, "bad_text",
			"A message has 1 to %d characters, and no control characters but line breaks.", store.MaxMessageText)
	case errors.Is(err, store.ErrNoBoxes):
		return badDevice(http.StatusConflict, "no_boxes", "There is no box to send it to: a message goes to the boxes that run Overscan's page, once they are approved.")
	case errors.Is(err, store.ErrTooManyMessages):
		return badDevice(http.StatusConflict, "too_many_messages",
			"%d messages wait to be read. Delete some, or wait for the boxes to show them.", store.MaxWaitingMessages)
	case errors.Is(err, store.ErrNotFound):
		return badDevice(http.StatusNotFound, "no_such_message", "There is no such message.")
	}
	return err
}

// sendMessage sends a message to the box with the MAC address, or with ""
// to every box of the household.
func (a *Admin) sendMessage(ctx context.Context, text, mac string) (store.Message, error) {
	var deviceID int64
	to := "every box"
	if mac != "" {
		d, err := a.store.Device(ctx, mac)
		if err != nil {
			return store.Message{}, deviceStoreError(err, mac)
		}
		if d.State != store.DeviceApproved || d.AccountID != householdAccount || !d.Paired() {
			return store.Message{}, badDevice(http.StatusConflict, "not_a_box",
				"%s does not run Overscan's page, so it cannot show a message. Stalker clients get none.", deviceName(d))
		}
		deviceID, to = d.ID, d.MAC
	}
	m, err := a.store.AddMessage(ctx, householdAccount, deviceID, text, a.now())
	if err != nil {
		return store.Message{}, messageStoreError(err)
	}
	a.log.Info("a message sent in the admin", "to", to, "boxes", m.Boxes, "characters", utf8.RuneCountInString(m.Text))
	return m, nil
}

// deleteMessage takes a message away, from the boxes that have not shown it
// too.
func (a *Admin) deleteMessage(ctx context.Context, id int64) error {
	if err := a.store.DeleteMessage(ctx, householdAccount, id); err != nil {
		return messageStoreError(err)
	}
	a.log.Info("a message deleted in the admin", "message", id)
	return nil
}

// messageRow is a message as the Devices page lists it.
type messageRow struct {
	store.Message
	Now time.Time
}

// To names where the message went.
func (m messageRow) To() string {
	switch {
	case m.EveryBox:
		return "To every box"
	case m.ToLabel != "":
		return "To " + m.ToLabel
	}
	return "To " + m.ToMAC
}

// Status says whether the boxes read it.
func (m messageRow) Status() string {
	expired := !m.Now.Before(m.Expires)
	switch {
	case m.ReadBoxes == m.Boxes && m.Boxes == 1:
		return "Read"
	case m.ReadBoxes == m.Boxes:
		return fmt.Sprintf("Read on all %d boxes", m.Boxes)
	case m.Boxes == 1 && expired:
		return "Not shown in time"
	case m.Boxes == 1:
		return "Waiting"
	case expired:
		return fmt.Sprintf("Read on %d of %d boxes; the others did not show it in time", m.ReadBoxes, m.Boxes)
	}
	return fmt.Sprintf("Read on %d of %d boxes", m.ReadBoxes, m.Boxes)
}

// messageRows returns the household's last messages, as the page lists them.
func (a *Admin) messageRows(ctx context.Context) ([]messageRow, error) {
	list, err := a.store.Messages(ctx, householdAccount, messagesListed)
	if err != nil {
		return nil, err
	}
	now := a.now()
	out := make([]messageRow, len(list))
	for i, m := range list {
		out[i] = messageRow{Message: m, Now: now}
	}
	return out, nil
}

// messageAction serves the message forms of the devices page: sending one,
// and deleting one.
func (a *Admin) messageAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	text, to := r.PostFormValue("text"), strings.TrimSpace(r.PostFormValue("to"))
	var notice string
	var err error
	switch r.PathValue("action") {
	case "":
		var m store.Message
		if m, err = a.sendMessage(ctx, text, to); err == nil {
			notice = "Sent the message. Boxes show it within five minutes."
			if m.Boxes > 1 {
				notice = fmt.Sprintf("Sent the message to %d boxes. They show it within five minutes.", m.Boxes)
			}
		}
	case "delete":
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			a.notFound(w, r)
			return
		}
		if err = a.deleteMessage(ctx, id); err == nil {
			notice = "Deleted the message. Boxes that have not shown it never will."
		}
	default:
		a.notFound(w, r)
		return
	}
	a.devicesDone(w, r, s, err, notice, devicesData{MessageText: truncate(text, 4*store.MaxMessageText), MessageTo: truncate(to, 40)})
}

// The API.

// apiMessage is a message as the API describes it.
type apiMessage struct {
	ID       int64     `json:"id"`
	Text     string    `json:"text"`
	EveryBox bool      `json:"every_box"`
	MAC      *string   `json:"mac"`
	SentAt   time.Time `json:"sent_at"`
	Expires  time.Time `json:"expires_at"`
	Boxes    int       `json:"boxes"`
	Read     int       `json:"read"`
}

func messageBody(m store.Message) apiMessage {
	out := apiMessage{ID: m.ID, Text: m.Text, EveryBox: m.EveryBox, SentAt: m.SentAt, Expires: m.Expires, Boxes: m.Boxes, Read: m.ReadBoxes}
	if !m.EveryBox {
		mac := m.ToMAC
		out.MAC = &mac
	}
	return out
}

func (a *Admin) apiMessages(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	list, err := a.store.Messages(r.Context(), householdAccount, 100)
	if err != nil {
		a.apiInternal(w, "cannot list the messages", err)
		return
	}
	out := make([]apiMessage, len(list))
	for i, m := range list {
		out[i] = messageBody(m)
	}
	writeJSON(w, http.StatusOK, struct {
		Messages []apiMessage `json:"messages"`
	}{out})
}

func (a *Admin) apiSendMessage(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body struct {
		Text string `json:"text"`
		MAC  string `json:"mac"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	m, err := a.sendMessage(r.Context(), body.Text, strings.TrimSpace(body.MAC))
	if a.apiFailed(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, messageBody(m))
}

func (a *Admin) apiDeleteMessage(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "no_such_message", "There is no such message.")
		return
	}
	if a.apiFailed(w, a.deleteMessage(r.Context(), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
