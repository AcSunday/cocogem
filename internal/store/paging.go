package store

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Page sizes are bounded at the store boundary so an HTTP handler cannot ask
// for an unbounded slice of history.
const (
	DefaultMessagePage = 30
	MaxMessagePage     = 100
	DefaultEventLimit  = 100
	MaxEventLimit      = 500

	// maxPageCursorBytes bounds an opaque timeline cursor before decoding.
	maxPageCursorBytes = 512

	// legacyPreviewRunes bounds a projected legacy inline body. SQLite substr
	// counts Unicode code points and 16384 four-byte runes are at most 64 KiB,
	// so the stored row stays untouched while the timeline preview stays bounded.
	legacyPreviewRunes = 16 << 10
)

// pagedMessageBody keeps chunked previews as stored and projects an older
// inline body to a bounded prefix plus its true byte length. json_set avoids
// cutting a UTF-8 sequence the way a CAST to a short blob would.
const pagedMessageBody = `CASE WHEN json_extract(body,'$.content_format')='chunks_v1' THEN body ELSE json_set(body,
	'$.content',COALESCE(substr(json_extract(body,'$.content'),1,?),''),
	'$.content_bytes',COALESCE(length(CAST(json_extract(body,'$.content') AS BLOB)),0),
	'$.content_truncated',CASE WHEN COALESCE(length(CAST(json_extract(body,'$.content') AS BLOB)),0)>COALESCE(length(CAST(substr(json_extract(body,'$.content'),1,?) AS BLOB)),0) THEN json('true') ELSE json('false') END) END`

// MessagePage is one bounded conversation timeline slice. Items stay in
// ascending position order even though the newest page is read first.
type MessagePage struct {
	Items      []w.Message
	NextCursor string
}

// EventPage is one bounded slice of persisted events for non-streaming reads.
type EventPage struct {
	Items      []w.Event `json:"items"`
	NextCursor string    `json:"next_cursor"`
}

type messagePageCursor struct {
	ThreadID string `json:"thread_id"`
	Position int64  `json:"position"`
}

// PagedMessages reads a bounded page ending at the latest message by default,
// or strictly older than a cursor. The cursor binds the thread and a stable
// position, so newly appended messages never shift or duplicate an older page.
func (t *Tx) PagedMessages(threadID, cursor string, limit int) (MessagePage, error) {
	if limit < 0 || limit > MaxMessagePage {
		return MessagePage{}, fmt.Errorf("message page limit must be between 0 and %d", MaxMessagePage)
	}
	if limit == 0 {
		limit = DefaultMessagePage
	}
	before := int64(math.MaxInt64)
	if cursor != "" {
		var c messagePageCursor
		if err := decodePageCursor(cursor, &c); err != nil {
			return MessagePage{}, err
		}
		if c.ThreadID != threadID || c.Position <= 0 {
			return MessagePage{}, ErrInvalidCursor
		}
		// The cursor is exclusive-before, so its position must name a message
		// that actually exists in this thread; a huge fabricated position would
		// otherwise silently behave like an unbounded first page.
		var exists int
		err := t.tx.QueryRowContext(t.ctx, "SELECT 1 FROM messages WHERE thread_id=? AND position=?", threadID, c.Position).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return MessagePage{}, ErrInvalidCursor
		}
		if err != nil {
			return MessagePage{}, err
		}
		before = c.Position
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT position,"+pagedMessageBody+" FROM messages WHERE thread_id=? AND position<? ORDER BY position DESC LIMIT ?", legacyPreviewRunes, legacyPreviewRunes, threadID, before, limit+1)
	if err != nil {
		return MessagePage{}, err
	}
	defer rows.Close()
	type row struct {
		position int64
		body     string
	}
	read := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.position, &r.body); err != nil {
			return MessagePage{}, err
		}
		read = append(read, r)
	}
	if err := rows.Err(); err != nil {
		return MessagePage{}, err
	}
	page := MessagePage{Items: []w.Message{}}
	if len(read) > limit {
		read = read[:limit]
		page.NextCursor, err = encodePageCursor(messagePageCursor{ThreadID: threadID, Position: read[len(read)-1].position})
		if err != nil {
			return MessagePage{}, err
		}
	}
	for i := range read {
		var message w.Message
		if err := json.Unmarshal([]byte(read[len(read)-1-i].body), &message); err != nil {
			return MessagePage{}, err
		}
		page.Items = append(page.Items, message)
	}
	return page, nil
}

// EventPage returns at most limit events after an exclusive sequence cursor.
// NextCursor is the last returned sequence only when another page exists.
func (t *Tx) EventPage(runID string, after int64, limit int) (EventPage, error) {
	if after < 0 {
		return EventPage{}, errors.New("event cursor must not be negative")
	}
	if limit < 0 || limit > MaxEventLimit {
		return EventPage{}, fmt.Errorf("event page limit must be between 0 and %d", MaxEventLimit)
	}
	if limit == 0 {
		limit = DefaultEventLimit
	}
	events, err := list[w.Event](t, "SELECT body FROM events WHERE run_id=? AND seq>? ORDER BY seq LIMIT ?", runID, after, limit+1)
	if err != nil {
		return EventPage{}, err
	}
	page := EventPage{Items: events}
	if len(events) > limit {
		page.Items = events[:limit]
		page.NextCursor = strconv.FormatInt(page.Items[len(page.Items)-1].Seq, 10)
	}
	return page, nil
}

// SourceMetadata lists a conversation's captures without their bodies; the
// detail read still returns the complete captured content.
func (t *Tx) SourceMetadata(threadID string) ([]w.Source, error) {
	return list[w.Source](t, "SELECT json_remove(body,'$.content') FROM sources WHERE thread_id=? ORDER BY rowid", threadID)
}

func encodePageCursor(v messagePageCursor) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodePageCursor(token string, v *messagePageCursor) error {
	if token == "" || len(token) > maxPageCursorBytes {
		return ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return ErrInvalidCursor
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return ErrInvalidCursor
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ErrInvalidCursor
	}
	return nil
}
