// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
)

// DeviceState says whether a device may use Overscan.
type DeviceState string

// Device states (D23).
const (
	DevicePending  DeviceState = "pending"  // seen, waiting for approval
	DeviceApproved DeviceState = "approved" // linked to an account
	DeviceBlocked  DeviceState = "blocked"  // refused
)

// Device is a set-top box or player, keyed by its MAC address.
type Device struct {
	ID        int64
	MAC       string // six upper-case hex pairs separated by colons
	State     DeviceState
	AccountID int64  // 0 if the device was never approved
	Label     string // set by the administrator
	Model     string // as the device reports it
	LastSeen  time.Time
	CreatedAt time.Time

	// Pairing (D39): a box that runs the MAG page keeps a secret, and the
	// store keeps its hash.
	SecretHash  string    // the paired box's secret, hashed; "" if unpaired
	PairedAt    time.Time // when the box was paired
	PairingCode string    // a request to pair that waits for approval: its code, or ""
	PairingHash string    // the secret of the box that made the request, hashed
	PairingAt   time.Time // when the request was made

	// ProfileID is the profile it plays as (D94): its own, or its
	// account's first; 0 if it has no account.
	ProfileID int64
}

// Paired reports whether the device is paired with a secret.
func (d Device) Paired() bool { return d.SecretHash != "" }

// HashSecret returns the form in which the store keeps a device secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ErrCodeTaken is returned when another device's request to pair has the
// code.
var ErrCodeTaken = errors.New("store: another request to pair has this code")

// ErrInvalidMAC is returned for a string that is not a MAC address.
var ErrInvalidMAC = errors.New("not a MAC address")

// maxModelLen caps the model a device reports, in bytes.
const maxModelLen = 100

// ParseMAC returns s, a 48-bit MAC address written with colons, hyphens, dots
// or no separators, in the form the store keeps: 00:1A:79:12:34:56.
func ParseMAC(s string) (string, error) {
	t := strings.TrimSpace(s)
	if len(t) == 12 && !strings.ContainsAny(t, ":-.") {
		var b strings.Builder
		for i := 0; i < 12; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(t[i : i+2])
		}
		t = b.String()
	}
	hw, err := net.ParseMAC(t)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("%q is %w", s, ErrInvalidMAC)
	}
	return strings.ToUpper(hw.String()), nil
}

// truncate shortens s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

var deviceColumns = "id, mac, state, account_id, label, model, last_seen, created_at, " +
	"secret_hash, paired_at, pairing_code, pairing_hash, pairing_at, " + deviceProfile("devices")

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDevice(row rowScanner) (Device, error) {
	var d Device
	var state string
	var account sql.NullInt64
	var lastSeen, secretHash, pairedAt, pairingCode, pairingHash, pairingAt sql.NullString
	var created string
	if err := row.Scan(&d.ID, &d.MAC, &state, &account, &d.Label, &d.Model, &lastSeen, &created,
		&secretHash, &pairedAt, &pairingCode, &pairingHash, &pairingAt, &d.ProfileID); err != nil {
		return Device{}, err
	}
	d.State = DeviceState(state)
	d.AccountID = account.Int64
	d.SecretHash, d.PairingCode, d.PairingHash = secretHash.String, pairingCode.String, pairingHash.String
	var err error
	if d.CreatedAt, err = parseTime(created); err != nil {
		return Device{}, err
	}
	for _, t := range []struct {
		s   sql.NullString
		out *time.Time
	}{{lastSeen, &d.LastSeen}, {pairedAt, &d.PairedAt}, {pairingAt, &d.PairingAt}} {
		if t.s.Valid {
			if *t.out, err = parseTime(t.s.String); err != nil {
				return Device{}, err
			}
		}
	}
	return d, nil
}

// queryDevice runs a query that returns at most one device row.
func (s *Store) queryDevice(ctx context.Context, mac, query string, args ...any) (Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("store: device %s: %w", mac, err)
	}
	return d, nil
}

// Device returns the device with the given MAC address, or ErrNotFound.
func (s *Store) Device(ctx context.Context, mac string) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	return s.queryDevice(ctx, m, "SELECT "+deviceColumns+" FROM devices WHERE mac = ?", m)
}

// Devices returns every device, pending ones first, then by MAC address.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+deviceColumns+" FROM devices ORDER BY state <> 'pending', mac")
	if err != nil {
		return nil, fmt.Errorf("store: devices: %w", err)
	}
	defer rows.Close()
	var ds []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("store: devices: %w", err)
		}
		ds = append(ds, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: devices: %w", err)
	}
	return ds, nil
}

// SeeDevice records that the device with the given MAC address contacted
// Overscan at the given time, reporting model (empty if it did not say; cut
// to 100 bytes). A device not seen before is added as pending. It returns the
// device.
func (s *Store) SeeDevice(ctx context.Context, mac, model string, at time.Time) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	model = truncate(model, maxModelLen)
	return s.queryDevice(ctx, m, `
		INSERT INTO devices (mac, model, last_seen) VALUES (?, ?, ?)
		ON CONFLICT (mac) DO UPDATE SET
			last_seen = excluded.last_seen,
			model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE devices.model END
		RETURNING `+deviceColumns,
		m, model, formatTime(at))
}

// ApproveDevice approves the device with the given MAC address for an
// account, adding the device if it is not known yet, so a box can be approved
// before it first connects. A non-empty label replaces the device's label.
// An unpaired device with a request to pair is paired with that request's
// secret at the given time; a paired device keeps its secret, and a request
// to pair it again waits for ApprovePairing.
func (s *Store) ApproveDevice(ctx context.Context, mac string, accountID int64, label string, at time.Time) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	if err := s.checkAccount(ctx, accountID); err != nil {
		return Device{}, err
	}
	// The right-hand sides all see the row as it was.
	return s.queryDevice(ctx, m, `
		INSERT INTO devices (mac, state, account_id, label) VALUES (?1, 'approved', ?2, ?3)
		ON CONFLICT (mac) DO UPDATE SET
			state = 'approved',
			account_id = excluded.account_id,
			label = CASE WHEN excluded.label <> '' THEN excluded.label ELSE devices.label END,
			secret_hash = COALESCE(devices.secret_hash, devices.pairing_hash),
			paired_at = CASE WHEN devices.secret_hash IS NULL AND devices.pairing_hash IS NOT NULL
				THEN ?4 ELSE devices.paired_at END,
			pairing_code = CASE WHEN devices.secret_hash IS NULL THEN NULL ELSE devices.pairing_code END,
			pairing_hash = CASE WHEN devices.secret_hash IS NULL THEN NULL ELSE devices.pairing_hash END,
			pairing_at = CASE WHEN devices.secret_hash IS NULL THEN NULL ELSE devices.pairing_at END
		RETURNING `+deviceColumns,
		m, accountID, label, formatTime(at))
}

// ApprovePairing approves, for an account, the device whose request to pair
// has the given code, and pairs it with that request's secret at the given
// time, replacing any earlier one. It returns ErrNotFound if no request has
// the code. A non-empty label replaces the device's label.
func (s *Store) ApprovePairing(ctx context.Context, code string, accountID int64, label string, at time.Time) (Device, error) {
	if err := s.checkAccount(ctx, accountID); err != nil {
		return Device{}, err
	}
	return s.queryDevice(ctx, "with pairing code "+code, `
		UPDATE devices SET
			state = 'approved',
			account_id = ?2,
			label = CASE WHEN ?3 <> '' THEN ?3 ELSE label END,
			secret_hash = pairing_hash,
			paired_at = ?4,
			pairing_code = NULL, pairing_hash = NULL, pairing_at = NULL
		WHERE pairing_code = ?1
		RETURNING `+deviceColumns,
		code, accountID, label, formatTime(at))
}

// checkAccount returns an error wrapping ErrNotFound if there is no account
// with the given ID.
func (s *Store) checkAccount(ctx context.Context, accountID int64) error {
	if _, err := s.Account(ctx, accountID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("store: no account %d: %w", accountID, ErrNotFound)
		}
		return err
	}
	return nil
}

// RequestPairing records a request to pair the device with the given MAC
// address, which reports model ("" if it reports none), at the given time:
// the box shows code and keeps the secret whose hash is secretHash. A device
// not seen before is added as pending. The request replaces any earlier one
// for the device. It returns ErrCodeTaken if another device's request has the
// code.
func (s *Store) RequestPairing(ctx context.Context, mac, model, code, secretHash string, at time.Time) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	d, err := s.queryDevice(ctx, m, `
		INSERT INTO devices (mac, model, last_seen, pairing_code, pairing_hash, pairing_at)
		VALUES (?1, ?2, ?3, ?4, ?5, ?3)
		ON CONFLICT (mac) DO UPDATE SET
			last_seen = excluded.last_seen,
			model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE devices.model END,
			pairing_code = excluded.pairing_code,
			pairing_hash = excluded.pairing_hash,
			pairing_at = excluded.pairing_at
		RETURNING `+deviceColumns,
		m, truncate(model, maxModelLen), formatTime(at), code, secretHash)
	if errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		return Device{}, ErrCodeTaken
	}
	return d, err
}

// AdoptSecret pairs an approved device that has no secret yet, such as a box
// approved by its MAC address before it first ran the MAG page, with the
// secret whose hash is secretHash, at the given time. It returns ErrNotFound
// unless the device is approved and unpaired.
func (s *Store) AdoptSecret(ctx context.Context, mac, secretHash string, at time.Time) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	return s.queryDevice(ctx, m, `
		UPDATE devices SET
			secret_hash = ?2, paired_at = ?3,
			pairing_code = NULL, pairing_hash = NULL, pairing_at = NULL
		WHERE mac = ?1 AND state = 'approved' AND secret_hash IS NULL
		RETURNING `+deviceColumns,
		m, secretHash, formatTime(at))
}

// BlockDevice blocks the device with the given MAC address, adding the
// device if it is not known yet. A request to pair it is dropped; a paired
// device keeps its secret, for if it is approved again.
func (s *Store) BlockDevice(ctx context.Context, mac string) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	return s.queryDevice(ctx, m, `
		INSERT INTO devices (mac, state) VALUES (?, 'blocked')
		ON CONFLICT (mac) DO UPDATE SET
			state = 'blocked',
			pairing_code = NULL, pairing_hash = NULL, pairing_at = NULL
		RETURNING `+deviceColumns,
		m)
}

// SetDeviceLabel sets the label of the device with the given MAC address;
// an empty label clears it. It returns ErrNotFound for an unknown device.
func (s *Store) SetDeviceLabel(ctx context.Context, mac, label string) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	return s.queryDevice(ctx, m, `
		UPDATE devices SET label = ?2 WHERE mac = ?1
		RETURNING `+deviceColumns,
		m, label)
}

// PendingDevices returns how many devices wait for approval.
func (s *Store) PendingDevices(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM devices WHERE state = 'pending'").Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count pending devices: %w", err)
	}
	return n, nil
}

// RemoveDevice forgets the device with the given MAC address, or returns
// ErrNotFound. If the device contacts Overscan again, it is pending again.
func (s *Store) RemoveDevice(ctx context.Context, mac string) error {
	m, err := ParseMAC(mac)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM devices WHERE mac = ?", m)
	if err != nil {
		return fmt.Errorf("store: device %s: %w", m, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: device %s: %w", m, err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// formatTime formats t the way the store keeps times: UTC, RFC 3339, seconds.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
