// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"overscan.gureet.ca/internal/store"
)

// Logins checks the logins and playlist tokens of accounts (D102), for the
// outputs that players other than the boxes use. Wrong ones are slowed down
// as the administrator's sign-in is (D44), with a count of their own, so
// that a player that keeps a wrong password cannot lock the administrator
// out.
//
// Players send their login with every request, a stream's included, and an
// argon2id check takes a few hundred milliseconds, one at a time. So a
// login that was right is remembered for a while, by a keyed hash of its
// username and password that lives only in memory, together with the hash
// it was checked against: a new password, or a removed login, makes the
// remembered one useless at once.
type Logins struct {
	store *store.Store
	limit *limiter
	now   func() time.Time
	key   []byte // keys the remembered logins

	mu    sync.Mutex
	known map[[sha256.Size]byte]knownLogin
}

// knownLogin is a login that was checked lately and was right.
type knownLogin struct {
	account int64
	hash    string // the password hash that it was checked against
	at      time.Time
}

// How long a right login is remembered, and how many are at most.
const (
	loginKnownFor  = 15 * time.Minute
	maxKnownLogins = 256
)

// ErrLogin is why a login or a token is refused: no account has it.
var ErrLogin = errors.New("admin: no account has this login")

// LoginWait is why a login is refused before it is checked: its address
// has failed too often, and must wait For.
type LoginWait struct{ For time.Duration }

func (e *LoginWait) Error() string {
	return fmt.Sprintf("admin: too many wrong logins from this address; wait %s", e.For.Round(time.Second))
}

// dummy is the hash checked against when no account has the username, so
// that a wrong username takes as long as a wrong password; made when it is
// first needed.
var (
	dummyOnce sync.Once
	dummy     string
)

func makeDummy() {
	h, err := HashPassword(context.Background(), "not the password of any account", DefaultParams)
	if err == nil {
		dummy = h
	}
}

// NewLogins returns a checker of the accounts' logins in st.
func NewLogins(st *store.Store) *Logins {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &Logins{store: st, limit: newLimiter(), now: time.Now, key: key, known: map[[sha256.Size]byte]knownLogin{}}
}

// Check returns the account whose login is username and password, asked
// for from addr. It fails with *LoginWait while addr must wait, ErrLogin
// for a login that no account has, or the store's error.
func (l *Logins) Check(ctx context.Context, username, password, addr string) (store.Account, error) {
	now := l.now()
	if wait := l.limit.wait(addr, now); wait > 0 {
		return store.Account{}, &LoginWait{For: wait}
	}
	acct, err := l.store.AccountByUsername(ctx, username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		acct = store.Account{}
	case err != nil:
		return store.Account{}, err
	}
	if acct.ID != 0 && acct.PasswordHash != "" {
		k := l.keyOf(username, password)
		l.mu.Lock()
		kl, ok := l.known[k]
		l.mu.Unlock()
		if ok && kl.account == acct.ID && kl.hash == acct.PasswordHash && now.Sub(kl.at) < loginKnownFor {
			return acct, nil
		}
		right, err := CheckPassword(ctx, acct.PasswordHash, password)
		if err != nil && !errors.Is(err, ErrBadHash) {
			return store.Account{}, err
		}
		if right {
			l.remember(k, knownLogin{account: acct.ID, hash: acct.PasswordHash, at: now})
			l.limit.succeed(addr)
			return acct, nil
		}
	} else {
		dummyOnce.Do(makeDummy)
		if _, err := CheckPassword(ctx, dummy, password); err != nil && !errors.Is(err, ErrBadHash) {
			return store.Account{}, err
		}
	}
	l.limit.fail(addr, now)
	return store.Account{}, ErrLogin
}

// Token returns the account whose playlist token is token. It fails with
// ErrLogin for a token that no account has. Wrong tokens are not slowed
// down: a token holds 256 random bits, which no one guesses, and an
// address that keeps an old token after a new one was made should not
// keep the same device's right logins waiting.
func (l *Logins) Token(ctx context.Context, token string) (store.Account, error) {
	acct, err := l.store.AccountByToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return store.Account{}, ErrLogin
	}
	return acct, err
}

// keyOf returns the key by which a login is remembered: an HMAC of its
// username, whatever its case, and password, under a key that lives only
// in memory.
func (l *Logins) keyOf(username, password string) [sha256.Size]byte {
	m := hmac.New(sha256.New, l.key)
	m.Write([]byte(strings.ToLower(username)))
	m.Write([]byte{0})
	m.Write([]byte(password))
	var k [sha256.Size]byte
	copy(k[:], m.Sum(nil))
	return k
}

// remember keeps a right login, forgetting the oldest when there are too
// many.
func (l *Logins) remember(k [sha256.Size]byte, kl knownLogin) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.known[k]; !ok && len(l.known) >= maxKnownLogins {
		var oldest [sha256.Size]byte
		var at time.Time
		first := true
		for key, v := range l.known {
			if first || v.at.Before(at) {
				oldest, at, first = key, v.at, false
			}
		}
		delete(l.known, oldest)
	}
	l.known[k] = kl
}
