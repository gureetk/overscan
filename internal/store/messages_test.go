// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// pairedBox approves a box by its MAC address and pairs it, as Overscan's
// page is.
func pairedBox(t *testing.T, s *Store, mac, label string, at time.Time) Device {
	t.Helper()
	ctx := context.Background()
	if _, err := s.ApproveDevice(ctx, mac, householdID, label, at); err != nil {
		t.Fatal(err)
	}
	d, err := s.AdoptSecret(ctx, mac, HashSecret("secret of "+mac), at)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	at := time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)

	if _, err := s.AddMessage(ctx, householdID, 0, "Dinner is ready.", at); !errors.Is(err, ErrNoBoxes) {
		t.Errorf("with no box: %v", err)
	}
	one := pairedBox(t, s, "00:1A:79:00:00:01", "Living room", at)
	two := pairedBox(t, s, "00:1A:79:00:00:02", "Kitchen", at)
	// A Stalker client is approved but not paired: it gets no messages.
	client, err := s.ApproveDevice(ctx, "00:1A:79:00:00:03", householdID, "Phone", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, householdID, client.ID, "Hello", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("to a Stalker client: %v", err)
	}

	every, err := s.AddMessage(ctx, householdID, 0, "  Dinner is ready.\r\n\r\n\r\n\r\nCome down.  ", at)
	if err != nil {
		t.Fatal(err)
	}
	if every.Text != "Dinner is ready.\n\nCome down." || !every.EveryBox || every.Boxes != 2 || every.ReadBoxes != 0 ||
		!every.Expires.Equal(at.Add(MessageLife)) {
		t.Errorf("to every box: %+v", every)
	}
	alone, err := s.AddMessage(ctx, householdID, two.ID, "Bedtime in ten minutes.", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if alone.EveryBox || alone.ToLabel != "Kitchen" || alone.ToMAC != two.MAC || alone.Boxes != 1 {
		t.Errorf("to one box: %+v", alone)
	}

	// A box paired later never gets a message sent before.
	three := pairedBox(t, s, "00:1A:79:00:00:04", "Bedroom", at.Add(2*time.Minute))
	if got, _ := s.BoxMessages(ctx, three.ID, at.Add(3*time.Minute)); len(got) != 0 {
		t.Errorf("a later box gets %+v", got)
	}
	got, err := s.BoxMessages(ctx, two.ID, at.Add(3*time.Minute))
	if err != nil || len(got) != 2 || got[0].Text != every.Text || got[1].Text != alone.Text || !got[1].SentAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("the kitchen's messages %+v, %v", got, err)
	}

	// Reading.
	if err := s.ReadMessage(ctx, two.ID, got[0].ID, at.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadMessage(ctx, two.ID, got[0].ID, at.Add(5*time.Minute)); err != nil {
		t.Errorf("reading again: %v", err)
	}
	if err := s.ReadMessage(ctx, one.ID, got[1].ID, at); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading another box's: %v", err)
	}
	if left, _ := s.BoxMessages(ctx, two.ID, at.Add(5*time.Minute)); len(left) != 1 || left[0].ID != got[1].ID {
		t.Errorf("left %+v", left)
	}
	list, err := s.Messages(ctx, householdID, 10)
	if err != nil || len(list) != 2 || list[0].ID != alone.ID || list[1].ID != every.ID || list[1].ReadBoxes != 1 || list[1].Boxes != 2 {
		t.Errorf("list %+v, %v", list, err)
	}

	// Past its expiry, a box never shows it.
	if left, _ := s.BoxMessages(ctx, one.ID, at.Add(MessageLife)); len(left) != 0 {
		t.Errorf("after its expiry: %+v", left)
	}

	// Deleting takes it from the boxes that have not shown it.
	if err := s.DeleteMessage(ctx, householdID, every.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.BoxMessages(ctx, one.ID, at.Add(time.Hour)); len(left) != 0 {
		t.Errorf("after deleting: %+v", left)
	}
	if err := s.DeleteMessage(ctx, householdID, every.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting again: %v", err)
	}
	if _, err := s.Message(ctx, householdID, every.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted message: %v", err)
	}

	// A message's ID is never used again.
	again, err := s.AddMessage(ctx, householdID, one.ID, "Again", at.Add(time.Hour))
	if err != nil || again.ID <= alone.ID {
		t.Errorf("a new message has ID %d (%v), after %d", again.ID, err, alone.ID)
	}
}

func TestTooManyMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	at := time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)
	box := pairedBox(t, s, "00:1A:79:00:00:01", "", at)
	for i := range MaxWaitingMessages {
		if _, err := s.AddMessage(ctx, householdID, box.ID, fmt.Sprint("Message ", i), at); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.AddMessage(ctx, householdID, box.ID, "One more", at); !errors.Is(err, ErrTooManyMessages) {
		t.Errorf("one too many: %v", err)
	}
	// Once the box has read one, there is room again.
	waiting, _ := s.BoxMessages(ctx, box.ID, at)
	if err := s.ReadMessage(ctx, box.ID, waiting[0].ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, householdID, box.ID, "One more", at); err != nil {
		t.Errorf("after one was read: %v", err)
	}
	// Long after their expiry, messages leave the list.
	if _, err := s.AddMessage(ctx, householdID, box.ID, "Much later", at.Add(MessageLife+messagesKept+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Messages(ctx, householdID, 100); len(list) != 1 || list[0].Text != "Much later" {
		t.Errorf("%d messages left", len(list))
	}
}

func TestCleanMessageText(t *testing.T) {
	for in, want := range map[string]string{
		"Hello":                    "Hello",
		"  Hello  ":                "Hello",
		"One\r\nTwo":               "One\nTwo",
		"One  \n\n\n\nTwo":         "One\n\nTwo",
		"Tab\tinside":              "", // a tab is a control character
		strings.Repeat("é", 500):   strings.Repeat("é", 500),
		"<b>markup</b> stays text": "<b>markup</b> stays text",
	} {
		got, err := CleanMessageText(in)
		if want == "" {
			if !errors.Is(err, ErrMessageText) {
				t.Errorf("%q: %q, %v", in, got, err)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", " \n ", "a\x00b", "\xff", strings.Repeat("é", 501)} {
		if _, err := CleanMessageText(in); !errors.Is(err, ErrMessageText) {
			t.Errorf("%q: %v", in, err)
		}
	}
}
