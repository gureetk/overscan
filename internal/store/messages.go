// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Messages (D96, schema 32): what the administrator sends to an account's
// boxes, those that run Overscan's page. A message to every box gets a row
// for each box paired when it is sent, so later boxes never show it; the
// rows of one message share its batch. A box shows each of its rows once,
// and a row it has not read by its expiry is never shown.

const (
	// MaxMessageText is the most characters a message may have.
	MaxMessageText = 500
	// MessageLife is how long a box has to show a message.
	MessageLife = 7 * 24 * time.Hour
	// MaxWaitingMessages is the most messages an account may have that
	// some box has still to show.
	MaxWaitingMessages = 50
	// messagesKept is how long a message stays in the admin's list after
	// its expiry.
	messagesKept = 30 * 24 * time.Hour
)

var (
	// ErrMessageText is why a message cannot have a text: it is empty,
	// longer than MaxMessageText, or holds a control character other
	// than a line break.
	ErrMessageText = errors.New("store: a message's text is empty, too long, or holds a control character")
	// ErrNoBoxes is why a message to every box cannot be sent: the account
	// has no approved box that runs Overscan's page.
	ErrNoBoxes = errors.New("store: the account has no box to send a message to")
	// ErrTooManyMessages is why an account cannot have another message.
	ErrTooManyMessages = errors.New("store: the account has as many messages waiting as it may")
)

// BoxMessage is a message as a box shows it.
type BoxMessage struct {
	ID     int64
	Text   string
	SentAt time.Time
}

// Message is a message as the administrator sent it.
type Message struct {
	ID       int64 // its batch
	Text     string
	EveryBox bool
	// To is the box it went to, when it went to one alone: its label and
	// MAC address, as the admin names it.
	ToLabel, ToMAC   string
	SentAt, Expires  time.Time
	Boxes, ReadBoxes int // how many boxes it went to, and how many read it
}

// blankLines matches three line breaks or more, with spaces between them.
var blankLines = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)

// CleanMessageText returns text as a message keeps it: without spaces at
// either end or at the end of its lines, with Windows line breaks made
// plain and no more than one empty line in a row; or ErrMessageText.
func CleanMessageText(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", ErrMessageText
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	text = strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	if text == "" || utf8.RuneCountInString(text) > MaxMessageText {
		return "", ErrMessageText
	}
	for _, r := range text {
		if r != '\n' && unicode.IsControl(r) {
			return "", ErrMessageText
		}
	}
	return text, nil
}

// boxesSQL selects the IDs of an account's boxes that run Overscan's page:
// approved and paired.
const boxesSQL = `SELECT id FROM devices WHERE account_id = ? AND state = 'approved' AND secret_hash IS NOT NULL`

// AddMessage sends a message to one box of an account, or with deviceID 0
// to every box of it, at the given time. It fails with ErrMessageText,
// ErrNoBoxes, ErrTooManyMessages, or ErrNotFound when the device is not one
// of the account's boxes.
func (s *Store) AddMessage(ctx context.Context, accountID, deviceID int64, text string, at time.Time) (Message, error) {
	text, err := CleanMessageText(text)
	if err != nil {
		return Message{}, err
	}
	var batch int64
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		// Messages long past their expiry leave the admin's list.
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE account_id = ? AND expires_at < ?`,
			accountID, at.Add(-messagesKept).Unix()); err != nil {
			return err
		}
		var waiting int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(DISTINCT batch) FROM messages
			WHERE account_id = ? AND read_at IS NULL AND expires_at > ?`, accountID, at.Unix()).Scan(&waiting); err != nil {
			return err
		}
		if waiting >= MaxWaitingMessages {
			return ErrTooManyMessages
		}
		var boxes []int64
		query, args := boxesSQL, []any{accountID}
		if deviceID != 0 {
			query, args = boxesSQL+` AND id = ?`, []any{accountID, deviceID}
		}
		rows, err := tx.QueryContext(ctx, query+` ORDER BY id`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			boxes = append(boxes, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		switch {
		case len(boxes) == 0 && deviceID != 0:
			return ErrNotFound
		case len(boxes) == 0:
			return ErrNoBoxes
		}
		every := deviceID == 0
		for _, box := range boxes {
			var id int64
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO messages (batch, account_id, device_id, every_box, text, created_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
				batch, accountID, box, every, text, at.Unix(), at.Add(MessageLife).Unix()).Scan(&id); err != nil {
				return err
			}
			if batch == 0 {
				batch = id
				if _, err := tx.ExecContext(ctx, `UPDATE messages SET batch = ? WHERE id = ?`, id, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, ErrTooManyMessages), errors.Is(err, ErrNoBoxes), errors.Is(err, ErrNotFound):
		return Message{}, err
	case err != nil:
		return Message{}, fmt.Errorf("store: send a message to account %d: %w", accountID, err)
	}
	return s.Message(ctx, accountID, batch)
}

const messageColumns = `m.batch, min(m.text), max(m.every_box), COALESCE(min(CASE WHEN m.every_box = 0 THEN d.label END), ''),
	COALESCE(min(CASE WHEN m.every_box = 0 THEN d.mac END), ''), min(m.created_at), min(m.expires_at),
	count(*), count(m.read_at)`

func scanMessage(row rowScanner) (Message, error) {
	var m Message
	var sent, expires int64
	if err := row.Scan(&m.ID, &m.Text, &m.EveryBox, &m.ToLabel, &m.ToMAC, &sent, &expires, &m.Boxes, &m.ReadBoxes); err != nil {
		return Message{}, err
	}
	m.SentAt, m.Expires = time.Unix(sent, 0).UTC(), time.Unix(expires, 0).UTC()
	return m, nil
}

// Message returns one of an account's messages by its batch, or
// ErrNotFound.
func (s *Store) Message(ctx context.Context, accountID, id int64) (Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, `
		SELECT `+messageColumns+` FROM messages m JOIN devices d ON d.id = m.device_id
		WHERE m.account_id = ? AND m.batch = ? GROUP BY m.batch`, accountID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("store: message %d: %w", id, err)
	}
	return m, nil
}

// Messages returns an account's last messages, the newest first, at most
// limit of them.
func (s *Store) Messages(ctx context.Context, accountID int64, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+messageColumns+` FROM messages m JOIN devices d ON d.id = m.device_id
		WHERE m.account_id = ? GROUP BY m.batch ORDER BY m.batch DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: messages of account %d: %w", accountID, err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: messages of account %d: %w", accountID, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: messages of account %d: %w", accountID, err)
	}
	return out, nil
}

// DeleteMessage takes one of an account's messages away, from the boxes
// that have not shown it too. It fails with ErrNotFound.
func (s *Store) DeleteMessage(ctx context.Context, accountID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE account_id = ? AND batch = ?`, accountID, id)
	return oneRow(res, err, "delete message", id)
}

// BoxMessages returns the messages that a box has still to show, the
// oldest first.
func (s *Store) BoxMessages(ctx context.Context, deviceID int64, now time.Time) ([]BoxMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, text, created_at FROM messages
		WHERE device_id = ? AND read_at IS NULL AND expires_at > ? ORDER BY id`, deviceID, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: messages for device %d: %w", deviceID, err)
	}
	defer rows.Close()
	var out []BoxMessage
	for rows.Next() {
		var m BoxMessage
		var sent int64
		if err := rows.Scan(&m.ID, &m.Text, &sent); err != nil {
			return nil, fmt.Errorf("store: messages for device %d: %w", deviceID, err)
		}
		m.SentAt = time.Unix(sent, 0).UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: messages for device %d: %w", deviceID, err)
	}
	return out, nil
}

// ReadMessage records that a box read one of its messages. Reading it again
// changes nothing. It fails with ErrNotFound if the message is not the
// box's.
func (s *Store) ReadMessage(ctx context.Context, deviceID, id int64, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE messages SET read_at = COALESCE(read_at, ?) WHERE id = ? AND device_id = ?`, at.Unix(), id, deviceID)
	return oneRow(res, err, "read message", id)
}
