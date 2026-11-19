// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAdminUser(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

	if _, err := s.AdminUser(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no administrator yet: got %v, want ErrNotFound", err)
	}
	if err := s.SetAdminPassword(ctx, "admin", "$argon2id$first", at, 0); err != nil {
		t.Fatal(err)
	}
	u, err := s.AdminUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "admin" || u.PasswordHash != "$argon2id$first" || !u.PasswordSetAt.Equal(at) {
		t.Errorf("got %+v", u)
	}

	later := at.Add(time.Hour)
	if err := s.SetAdminPassword(ctx, "keeper", "$argon2id$second", later, 0); err != nil {
		t.Fatal(err)
	}
	if u, err = s.AdminUser(ctx); err != nil || u.Username != "keeper" || u.PasswordHash != "$argon2id$second" || !u.PasswordSetAt.Equal(later) {
		t.Errorf("after a new password: %+v, %v", u, err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM admin_users").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d administrators, %v; want one", n, err)
	}

	if err := s.SetAdminPassword(ctx, "", "$argon2id$third", later, 0); err == nil {
		t.Error("an empty username was accepted")
	}
	if err := s.SetAdminPassword(ctx, "admin", "", later, 0); err == nil {
		t.Error("an empty hash was accepted")
	}
}

func TestAdminSessions(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

	if _, err := s.CreateAdminSession(ctx, "hash-0", "csrf-0", "192.0.2.10", "Browser", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a session without an administrator: got %v, want ErrNotFound", err)
	}
	if err := s.SetAdminPassword(ctx, "admin", "$argon2id$x", at, 0); err != nil {
		t.Fatal(err)
	}

	created, err := s.CreateAdminSession(ctx, "hash-1", "csrf-1", "192.0.2.10", "Browser", at)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AdminSession(ctx, "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != created || got.Username != "admin" || got.CSRFToken != "csrf-1" || got.Address != "192.0.2.10" ||
		got.UserAgent != "Browser" || !got.CreatedAt.Equal(at) || !got.LastSeen.Equal(at) {
		t.Errorf("got %+v, created %+v", got, created)
	}
	if _, err := s.AdminSession(ctx, "hash-unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: got %v, want ErrNotFound", err)
	}

	if err := s.TouchAdminSession(ctx, created.ID, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.AdminSession(ctx, "hash-1"); !got.LastSeen.Equal(at.Add(time.Hour)) {
		t.Errorf("last seen %v after a touch", got.LastSeen)
	}

	if err := s.EndAdminSession(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminSession(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ended session: got %v, want ErrNotFound", err)
	}
	if err := s.EndAdminSession(ctx, created.ID); err != nil {
		t.Errorf("ending it again: %v", err)
	}
}

func TestNewAdminPasswordEndsOtherSessions(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if err := s.SetAdminPassword(ctx, "admin", "$argon2id$x", at, 0); err != nil {
		t.Fatal(err)
	}
	keep, err := s.CreateAdminSession(ctx, "hash-keep", "csrf", "", "", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAdminSession(ctx, "hash-other", "csrf", "", "", at); err != nil {
		t.Fatal(err)
	}

	if err := s.SetAdminPassword(ctx, "admin", "$argon2id$y", at, keep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminSession(ctx, "hash-keep"); err != nil {
		t.Errorf("the session that set the password ended: %v", err)
	}
	if _, err := s.AdminSession(ctx, "hash-other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another session survived: %v", err)
	}

	if err := s.SetAdminPassword(ctx, "admin", "$argon2id$z", at, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminSession(ctx, "hash-keep"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a password set from outside a session kept a session: %v", err)
	}
}

func TestExpiredAdminSessionsEnd(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if err := s.SetAdminPassword(ctx, "admin", "$argon2id$x", at, 0); err != nil {
		t.Fatal(err)
	}
	old, _ := s.CreateAdminSession(ctx, "hash-old", "csrf", "", "", at.Add(-40*24*time.Hour))
	if err := s.TouchAdminSession(ctx, old.ID, at); err != nil { // used recently, but too old
		t.Fatal(err)
	}
	if _, err := s.CreateAdminSession(ctx, "hash-idle", "csrf", "", "", at.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAdminSession(ctx, "hash-fresh", "csrf", "", "", at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	n, err := s.EndExpiredAdminSessions(ctx, at.Add(-30*24*time.Hour), at.Add(-7*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("ended %d, %v; want 2", n, err)
	}
	if _, err := s.AdminSession(ctx, "hash-fresh"); err != nil {
		t.Errorf("the fresh session ended: %v", err)
	}
}

func TestAdminSessionsAreCapped(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if err := s.SetAdminPassword(ctx, "admin", "$argon2id$x", at, 0); err != nil {
		t.Fatal(err)
	}
	for i := range maxAdminSessions + 3 {
		if _, err := s.CreateAdminSession(ctx, fmt.Sprintf("hash-%d", i), "csrf", "", "", at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM admin_sessions").Scan(&n); err != nil || n != maxAdminSessions {
		t.Errorf("%d sessions, %v; want %d", n, err, maxAdminSessions)
	}
	for i := range 3 {
		if _, err := s.AdminSession(ctx, fmt.Sprintf("hash-%d", i)); !errors.Is(err, ErrNotFound) {
			t.Errorf("the session seen least recently, %d, survived: %v", i, err)
		}
	}
	if _, err := s.AdminSession(ctx, fmt.Sprintf("hash-%d", maxAdminSessions+2)); err != nil {
		t.Errorf("the newest session ended: %v", err)
	}
}
