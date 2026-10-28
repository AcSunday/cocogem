package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Message content paging mirrors the streaming flush size: eight chunks by
// default, at most thirty-two per request.
const (
	DefaultContentChunks = 8
	MaxContentChunks     = 32

	// maxContentCursorBytes bounds opaque cursors before decoding. A supported
	// cursor is well under half this size.
	maxContentCursorBytes = 512
)

// ErrInvalidCursor marks a cursor that is malformed or no longer matches the
// message's content snapshot.
var ErrInvalidCursor = errors.New("store: invalid content cursor")

// MessageContentPage is one bounded slice of a message body. ContentFormat is
// inline for legacy complete bodies and chunks_v1 for immutable chunk series.
type MessageContentPage struct {
	MessageID        string `json:"message_id"`
	ContentFormat    string `json:"content_format"`
	Content          string `json:"content"`
	ContentBytes     int    `json:"content_bytes"`
	ContentTruncated bool   `json:"content_truncated"`
	HasMore          bool   `json:"has_more"`
	NextCursor       string `json:"next_cursor,omitempty"`
	// Message is the stored identity and attempt model snapshot for this body.
	// Its content stays empty because the body is paged in this same response.
	Message w.Message `json:"message,omitempty"`
}

// contentCursor binds a page to one immutable snapshot: the chunk highwater
// sequence for chunks_v1, or the body digest and byte length for inline text.
type contentCursor struct {
	MessageID string `json:"message_id"`
	Format    string `json:"format"`
	Snapshot  int    `json:"snapshot"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
	Digest    string `json:"digest,omitempty"`
	After     int    `json:"after"`
}

// AppendMessageChunk persists one immutable body chunk. Chunks are contiguous,
// so a snapshot is exactly the chunks up to its highwater sequence. Repeating
// the same (message, seq, content) is a no-op; a different body at that
// position is a conflict so a retry cannot duplicate or rewrite a chunk.
func (t *Tx) AppendMessageChunk(messageID string, seq int, content string) error {
	if seq <= 0 {
		return errors.New("message chunk seq must be positive")
	}
	if content == "" || len(content) > w.MaxMessageChunkBytes || !utf8.ValidString(content) {
		return fmt.Errorf("message chunk must be 1-%d bytes of valid UTF-8", w.MaxMessageChunkBytes)
	}
	var existing string
	err := t.tx.QueryRowContext(t.ctx, "SELECT content FROM message_chunks WHERE message_id=? AND seq=?", messageID, seq).Scan(&existing)
	if err == nil {
		if existing == content {
			return nil
		}
		return w.ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var next int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM message_chunks WHERE message_id=?", messageID).Scan(&next); err != nil {
		return err
	}
	if seq != next {
		return fmt.Errorf("message chunk seq must be %d", next)
	}
	return t.exec("INSERT INTO message_chunks(message_id,seq,content) VALUES(?,?,?)", messageID, seq, content)
}

// MessageContent pages an immutable snapshot of the complete body without
// loading a chunked body into memory. Appends after the first page do not
// invalidate its cursor; a fresh first page sees the newer snapshot. Legacy
// inline messages are sliced on UTF-8 boundaries as a fallback.
func (t *Tx) MessageContent(messageID, cursor string, limit int) (MessageContentPage, error) {
	if limit < 0 || limit > MaxContentChunks {
		return MessageContentPage{}, fmt.Errorf("content limit must be between 0 and %d", MaxContentChunks)
	}
	if limit == 0 {
		limit = DefaultContentChunks
	}
	message, err := t.Message(messageID)
	if err != nil {
		return MessageContentPage{}, err
	}
	var page MessageContentPage
	switch format := message.Format(); format {
	case w.ContentFormatInline:
		page, err = inlineContentPage(message, cursor, limit)
	case w.ContentFormatChunksV1:
		page, err = t.chunkedContentPage(message, cursor, limit)
	default:
		return MessageContentPage{}, fmt.Errorf("unsupported message content format %q", format)
	}
	if err != nil {
		return MessageContentPage{}, err
	}
	// Reuse the stored Message entity so a consumer outside the timeline page
	// still sees the exact author and model snapshot without a second lookup.
	message.Content = ""
	page.Message = message
	return page, nil
}

func (t *Tx) chunkedContentPage(message w.Message, cursor string, limit int) (MessageContentPage, error) {
	var highwater int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COALESCE(MAX(seq),0) FROM message_chunks WHERE message_id=?", message.ID).Scan(&highwater); err != nil {
		return MessageContentPage{}, err
	}
	snapshot, bytes, truncated := highwater, message.ContentBytes, message.ContentTruncated
	after := 0
	if cursor != "" {
		var c contentCursor
		if err := decodeContentCursor(cursor, &c); err != nil {
			return MessageContentPage{}, err
		}
		if c.MessageID != message.ID || c.Format != w.ContentFormatChunksV1 ||
			c.Snapshot <= 0 || c.Snapshot > highwater || c.Bytes < 0 || c.Bytes > message.ContentBytes ||
			c.After <= 0 || c.After >= c.Snapshot {
			return MessageContentPage{}, ErrInvalidCursor
		}
		if c.After > 0 {
			var seq int
			err := t.tx.QueryRowContext(t.ctx, "SELECT seq FROM message_chunks WHERE message_id=? AND seq=?", message.ID, c.After).Scan(&seq)
			if errors.Is(err, sql.ErrNoRows) {
				return MessageContentPage{}, ErrInvalidCursor
			}
			if err != nil {
				return MessageContentPage{}, err
			}
		}
		snapshot, bytes, truncated, after = c.Snapshot, c.Bytes, c.Truncated, c.After
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT seq,content FROM message_chunks WHERE message_id=? AND seq>? AND seq<=? ORDER BY seq LIMIT ?", message.ID, after, snapshot, limit+1)
	if err != nil {
		return MessageContentPage{}, err
	}
	defer rows.Close()
	type chunk struct {
		seq     int
		content string
	}
	chunks := []chunk{}
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.seq, &c.content); err != nil {
			return MessageContentPage{}, err
		}
		chunks = append(chunks, c)
	}
	if err := rows.Err(); err != nil {
		return MessageContentPage{}, err
	}
	page := MessageContentPage{MessageID: message.ID, ContentFormat: w.ContentFormatChunksV1, ContentBytes: bytes, ContentTruncated: truncated}
	if len(chunks) > limit {
		page.HasMore = true
		chunks = chunks[:limit]
	}
	var body strings.Builder
	for _, c := range chunks {
		body.WriteString(c.content)
		after = c.seq
	}
	page.Content = body.String()
	if page.HasMore {
		next, err := encodeContentCursor(contentCursor{MessageID: message.ID, Format: page.ContentFormat, Snapshot: snapshot, Bytes: bytes, Truncated: truncated, After: after})
		if err != nil {
			return MessageContentPage{}, err
		}
		page.NextCursor = next
	}
	return page, nil
}

func inlineContentPage(message w.Message, cursor string, limit int) (MessageContentPage, error) {
	text := message.Content
	if !utf8.ValidString(text) {
		return MessageContentPage{}, errors.New("legacy message content is not valid UTF-8")
	}
	pageText := contentCursor{MessageID: message.ID, Format: w.ContentFormatInline, Snapshot: len(text), Bytes: len(text), Truncated: message.ContentTruncated, Digest: contentDigest(text)}
	truncated := pageText.Truncated
	after := 0
	if cursor != "" {
		var c contentCursor
		if err := decodeContentCursor(cursor, &c); err != nil {
			return MessageContentPage{}, err
		}
		if c.MessageID != pageText.MessageID || c.Format != pageText.Format || c.Snapshot != pageText.Snapshot ||
			c.Bytes != pageText.Bytes || c.Digest != pageText.Digest || c.After <= 0 || c.After >= c.Snapshot {
			return MessageContentPage{}, ErrInvalidCursor
		}
		truncated, after = c.Truncated, c.After
	}
	if after > len(text) || (after < len(text) && !utf8.RuneStart(text[after])) {
		return MessageContentPage{}, ErrInvalidCursor
	}
	var body strings.Builder
	for count := 0; count < limit && after < len(text); count++ {
		end := after + w.MaxMessageChunkBytes
		if end > len(text) {
			end = len(text)
		}
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		body.WriteString(text[after:end])
		after = end
	}
	page := MessageContentPage{MessageID: message.ID, ContentFormat: w.ContentFormatInline, Content: body.String(), ContentBytes: pageText.Bytes, ContentTruncated: truncated, HasMore: after < len(text)}
	if page.HasMore {
		next, err := encodeContentCursor(contentCursor{MessageID: message.ID, Format: page.ContentFormat, Snapshot: pageText.Snapshot, Bytes: pageText.Bytes, Truncated: truncated, Digest: pageText.Digest, After: after})
		if err != nil {
			return MessageContentPage{}, err
		}
		page.NextCursor = next
	}
	return page, nil
}

func contentDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func encodeContentCursor(v contentCursor) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeContentCursor(token string, v *contentCursor) error {
	if token == "" || len(token) > maxContentCursorBytes {
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
