package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Workflow reads never load a complete stored body into memory just to show a
// bounded preview. SQLite extracts and truncates the JSON field in place; Go
// only trims the character-sliced value to an exact UTF-8-safe byte budget.

const previewField = `COALESCE(substr(json_extract(body,'$.content'),1,?),'')`
const previewLength = `COALESCE(length(CAST(json_extract(body,'$.content') AS BLOB)),0)`

// MessagePreviews returns stable timeline order with each body bounded to
// maxBytes. Legacy inline bodies keep their true length for truncation flags.
func (t *Tx) MessagePreviews(threadID string, maxBytes int) ([]w.Message, error) {
	if maxBytes <= 0 {
		return nil, errors.New("message preview byte budget must be positive")
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT json_set(body,'$.content',"+previewField+"),"+previewLength+" FROM messages WHERE thread_id=? ORDER BY position", maxBytes, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []w.Message{}
	for rows.Next() {
		var body string
		var total int
		if err := rows.Scan(&body, &total); err != nil {
			return nil, err
		}
		message, err := messageFromPreview(body, total, maxBytes)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

// MessagePreview returns one bounded timeline item, never its whole body.
func (t *Tx) MessagePreview(id string, maxBytes int) (w.Message, error) {
	if maxBytes <= 0 {
		return w.Message{}, errors.New("message preview byte budget must be positive")
	}
	var body string
	var total int
	err := t.tx.QueryRowContext(t.ctx, "SELECT json_set(body,'$.content',"+previewField+"),"+previewLength+" FROM messages WHERE id=?", maxBytes, id).Scan(&body, &total)
	if err != nil {
		return w.Message{}, err
	}
	return messageFromPreview(body, total, maxBytes)
}

// MessagesMetadata lists a conversation without transferring any body text.
func (t *Tx) MessagesMetadata(threadID string) ([]w.Message, error) {
	return list[w.Message](t, "SELECT json_remove(body,'$.content') FROM messages WHERE thread_id=? ORDER BY position", threadID)
}

// SetMessageStatus changes only the lifecycle field, preserving the bounded
// preview and every immutable chunk already committed for the message.
func (t *Tx) SetMessageStatus(id, status string) error {
	return t.exec("UPDATE messages SET body=json_set(body,'$.status',?) WHERE id=?", status, id)
}

// ArtifactsMetadata lists exact versions for identity/authorization scans
// without transferring bodies or structured payloads. Callers that may show an
// artifact fetch that one exact version through Artifact/ArtifactPreview.
func (t *Tx) ArtifactsMetadata(threadID string) ([]w.Artifact, error) {
	return list[w.Artifact](t, "SELECT json_remove(body,'$.body','$.findings','$.limitations','$.source_ids') FROM artifacts WHERE thread_id=? ORDER BY rowid", threadID)
}

// ArtifactPreview returns one exact version with a bounded body preview.
func (t *Tx) ArtifactPreview(id string, version, maxBytes int) (w.Artifact, error) {
	if maxBytes <= 0 {
		return w.Artifact{}, errors.New("artifact preview byte budget must be positive")
	}
	var body string
	err := t.tx.QueryRowContext(t.ctx, "SELECT json_set(body,'$.body',COALESCE(substr(json_extract(body,'$.body'),1,?),'')) FROM artifacts WHERE id=? AND version=?", maxBytes, id, version).Scan(&body)
	if err != nil {
		return w.Artifact{}, err
	}
	if !utf8.ValidString(body) {
		return w.Artifact{}, errors.New("stored artifact preview is not valid UTF-8")
	}
	var artifact w.Artifact
	if err := json.Unmarshal([]byte(body), &artifact); err != nil {
		return w.Artifact{}, err
	}
	if len(artifact.Body) > maxBytes {
		artifact.Body = trimUTF8(artifact.Body, maxBytes)
	}
	return artifact, nil
}

func messageFromPreview(body string, total, maxBytes int) (w.Message, error) {
	if !utf8.ValidString(body) {
		return w.Message{}, errors.New("stored message preview is not valid UTF-8")
	}
	var message w.Message
	if err := json.Unmarshal([]byte(body), &message); err != nil {
		return w.Message{}, err
	}
	if len(message.Content) > maxBytes {
		message.Content = trimUTF8(message.Content, maxBytes)
	}
	if message.ContentBytes < total {
		message.ContentBytes = total
	}
	if message.ContentBytes > len(message.Content) {
		message.ContentTruncated = true
	}
	return message, nil
}

func trimUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

// MessageMeta reads one timeline item without transferring its body. For
// chunked messages ContentBytes is the committed body size; for legacy inline
// messages it is the stored byte length.
func (t *Tx) MessageMeta(id string) (w.Message, error) {
	var encoded string
	var inlineBytes, chunkedBytes int
	err := t.tx.QueryRowContext(t.ctx, "SELECT json_remove(body,'$.content'),COALESCE(length(CAST(json_extract(body,'$.content') AS BLOB)),0),COALESCE(json_extract(body,'$.content_bytes'),0) FROM messages WHERE id=?", id).Scan(&encoded, &inlineBytes, &chunkedBytes)
	if err != nil {
		return w.Message{}, err
	}
	var message w.Message
	if err := json.Unmarshal([]byte(encoded), &message); err != nil {
		return w.Message{}, err
	}
	message.Content = ""
	if message.Format() == w.ContentFormatInline {
		message.ContentBytes = inlineBytes
	} else if message.ContentBytes == 0 {
		message.ContentBytes = chunkedBytes
	}
	return message, nil
}

// MessageDigest hashes one complete body by streaming SQLite rows, so legacy
// inline bodies are never loaded into Go memory as a single string.
func (t *Tx) MessageDigest(id string) (string, int, string, error) {
	meta, err := t.MessageMeta(id)
	if err != nil {
		return "", 0, "", err
	}
	format := meta.Format()
	hash := sha256.New()
	if format == w.ContentFormatInline {
		total := meta.ContentBytes
		for offset := 0; offset < total; {
			chunk, err := t.inlineBlob(id, offset, min(w.MaxMessageChunkBytes, total-offset))
			if err != nil {
				return "", 0, "", err
			}
			if len(chunk) == 0 {
				return "", 0, "", errors.New("legacy message body ended before its declared length")
			}
			hash.Write(chunk)
			offset += len(chunk)
		}
		return hex.EncodeToString(hash.Sum(nil)), total, format, nil
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT content FROM message_chunks WHERE message_id=? ORDER BY seq", id)
	if err != nil {
		return "", 0, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return "", 0, "", err
		}
		hash.Write([]byte(content))
	}
	if err := rows.Err(); err != nil {
		return "", 0, "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), meta.ContentBytes, format, nil
}

func (t *Tx) inlineBlob(id string, offset, length int) ([]byte, error) {
	var chunk []byte
	err := t.tx.QueryRowContext(t.ctx, "SELECT substr(CAST(json_extract(body,'$.content') AS BLOB),?,?) FROM messages WHERE id=?", offset+1, length, id).Scan(&chunk)
	return chunk, err
}

// MessageWindow returns at most maxBytes of one message body starting at an
// immutable chunk position, without loading the whole body. Legacy inline
// bodies are sliced by SQLite as a read-only fallback.
func (t *Tx) MessageWindow(id string, afterSeq, afterOffset, maxBytes int) (string, int, int, int, bool, error) {
	if maxBytes < 1 || maxBytes > w.MaxMessageChunkBytes {
		return "", 0, 0, 0, false, errors.New("message window must be between 1 and 8192 bytes")
	}
	if afterSeq < 1 {
		afterSeq = 1
	}
	if afterOffset < 0 {
		return "", 0, 0, 0, false, errors.New("message window offset is invalid")
	}
	meta, err := t.MessageMeta(id)
	if err != nil {
		return "", 0, 0, 0, false, err
	}
	if meta.Format() == w.ContentFormatInline {
		return t.inlineWindow(meta, afterSeq, afterOffset, maxBytes)
	}
	var base int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COALESCE(SUM(length(CAST(content AS BLOB))),0) FROM message_chunks WHERE message_id=? AND seq<?", id, afterSeq).Scan(&base); err != nil {
		return "", 0, 0, 0, false, err
	}
	absolute := base + afterOffset
	if absolute > meta.ContentBytes {
		return "", 0, 0, 0, false, errors.New("message window is outside the chunked body")
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT seq,content FROM message_chunks WHERE message_id=? AND seq>=? ORDER BY seq", id, afterSeq)
	if err != nil {
		return "", 0, 0, 0, false, err
	}
	defer rows.Close()
	var body strings.Builder
	nextSeq, nextOffset := afterSeq, afterOffset
	seenSeq := false
	hasMore := false
	for rows.Next() {
		var seq int
		var content string
		if err := rows.Scan(&seq, &content); err != nil {
			return "", 0, 0, 0, false, err
		}
		if seq < afterSeq {
			continue
		}
		start := 0
		if seq == afterSeq {
			seenSeq = true
			if afterOffset > len(content) || (afterOffset < len(content) && !utf8.RuneStart(content[afterOffset])) {
				return "", 0, 0, 0, false, errors.New("message window offset is outside the chunk")
			}
			start = afterOffset
			if start == len(content) {
				continue
			}
		}
		for start < len(content) {
			_, size := utf8.DecodeRuneInString(content[start:])
			if body.Len()+size > maxBytes {
				if body.Len() == 0 {
					return "", 0, 0, 0, false, errors.New("max_bytes is too small for the next character")
				}
				nextSeq, nextOffset = seq, start
				hasMore = true
				return body.String(), nextSeq, nextOffset, absolute, true, rows.Err()
			}
			body.WriteString(content[start : start+size])
			start += size
		}
		nextSeq, nextOffset = seq+1, 0
	}
	if err := rows.Err(); err != nil {
		return "", 0, 0, 0, false, err
	}
	if !seenSeq && afterSeq > 1 {
		return "", 0, 0, 0, false, errors.New("message window sequence is outside the chunked body")
	}
	if body.Len() == 0 {
		if absolute == meta.ContentBytes {
			return "", afterSeq, afterOffset, absolute, false, nil
		}
		return "", 0, 0, 0, false, errors.New("message window is outside the chunked body")
	}
	return body.String(), nextSeq, nextOffset, absolute, hasMore, nil
}

func (t *Tx) inlineWindow(meta w.Message, afterSeq, afterOffset, maxBytes int) (string, int, int, int, bool, error) {
	if afterSeq > 1 {
		return "", 0, 0, 0, false, errors.New("message window sequence is outside the inline body")
	}
	total := meta.ContentBytes
	if afterOffset > total {
		return "", 0, 0, 0, false, errors.New("message window is outside the inline body")
	}
	if afterOffset == total {
		return "", 1, total, afterOffset, false, nil
	}
	// One extra UTF-8 rune of lookahead distinguishes a rune cut by the window
	// from the window boundary.
	window, err := t.inlineBlob(meta.ID, afterOffset, min(maxBytes+utf8.UTFMax, total-afterOffset))
	if err != nil {
		return "", 0, 0, 0, false, err
	}
	end := 0
	for end < len(window) {
		r, size := utf8.DecodeRune(window[end:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if end+size > maxBytes {
			break
		}
		end += size
	}
	if end == 0 && afterOffset < total {
		return "", 0, 0, 0, false, errors.New("max_bytes is too small for the next character")
	}
	return string(window[:end]), 1, afterOffset + end, afterOffset, afterOffset+end < total, nil
}

// UpdateRunBody records policy/checkpoint metadata without changing the
// status column, which keeps interrupted runs resumable.
func (t *Tx) UpdateRunBody(v w.Run) error {
	return t.exec("UPDATE runs SET body=? WHERE id=? AND status=?", body(v), v.ID, v.Status)
}

// TransitionRun is the narrow CAS for terminalizing an interrupted
// checkpoint (for example an expired resume). It deliberately does not widen
// SaveRun's guard for every terminal state.
func (t *Tx) TransitionRun(from string, v w.Run) error {
	result, err := t.tx.ExecContext(t.ctx, "UPDATE runs SET status=?,body=? WHERE id=? AND status=?", v.Status, body(v), v.ID, from)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return w.ErrConflict
	}
	return nil
}
