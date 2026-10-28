package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// Text paging bounds shared by HTTP and workflow retrieval. MaxTextPageBytes
// bounds the raw UTF-8 slice; MaxTextPageWireBytes bounds its JSON envelope.
const (
	MaxTextPageBytes     = 8 << 10
	MaxTextPageWireBytes = 64 << 10

	// maxTextCursorBytes bounds opaque cursors before decoding; a supported
	// cursor is well under half this size.
	maxTextCursorBytes = 512
)

// TextRef binds a cursor to one immutable source or exact artifact version.
type TextRef struct {
	Kind    string
	ID      string
	Version int
}

// TextPage is a bounded slice of one immutable text value.
type TextPage struct {
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	TotalBytes int    `json:"total_bytes"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type textCursor struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version int    `json:"version"`
	Digest  string `json:"digest"`
	Offset  int    `json:"offset"`
}

// PageText returns at most maxBytes of a UTF-8 value, never splitting a rune.
// A cursor is valid only for the same reference and content digest.
func PageText(ref TextRef, text, cursor string, maxBytes int) (TextPage, error) {
	if ref.ID == "" {
		return TextPage{}, errors.New("text reference is required")
	}
	if ref.Kind != "source" && ref.Kind != "artifact" {
		return TextPage{}, errors.New("text reference kind must be source or artifact")
	}
	if ref.Kind == "artifact" && ref.Version <= 0 {
		return TextPage{}, errors.New("artifact version is required")
	}
	if !utf8.ValidString(text) {
		return TextPage{}, errors.New("text must be valid UTF-8")
	}
	if maxBytes == 0 {
		maxBytes = MaxTextPageBytes
	}
	if maxBytes < 0 || maxBytes > MaxTextPageBytes {
		return TextPage{}, fmt.Errorf("max_bytes must be between 1 and %d", MaxTextPageBytes)
	}
	sum := sha256.Sum256([]byte(text))
	digest := hex.EncodeToString(sum[:])
	offset := 0
	if cursor != "" {
		var c textCursor
		if err := decodeCursor(cursor, &c); err != nil {
			return TextPage{}, err
		}
		if c.Kind != ref.Kind || c.ID != ref.ID || c.Version != ref.Version || c.Digest != digest {
			return TextPage{}, errors.New("cursor belongs to another text version")
		}
		offset = c.Offset
	}
	if offset < 0 || offset > len(text) || (offset < len(text) && !utf8.RuneStart(text[offset])) {
		return TextPage{}, errors.New("cursor offset is outside the text")
	}
	end := offset
	for end < len(text) {
		_, size := utf8.DecodeRuneInString(text[end:])
		if end+size-offset > maxBytes {
			break
		}
		end += size
	}
	if end == offset && offset < len(text) {
		return TextPage{}, errors.New("max_bytes is too small for the next UTF-8 character")
	}
	page := TextPage{Content: text[offset:end], Offset: offset, TotalBytes: len(text), HasMore: end < len(text)}
	if page.HasMore {
		next, err := encodeCursor(textCursor{Kind: ref.Kind, ID: ref.ID, Version: ref.Version, Digest: digest, Offset: end})
		if err != nil {
			return TextPage{}, err
		}
		page.NextCursor = next
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > MaxTextPageWireBytes {
		return TextPage{}, errors.New("text page exceeds the wire budget")
	}
	return page, nil
}

func encodeCursor(v textCursor) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(token string, v *textCursor) error {
	if token == "" || len(token) > maxTextCursorBytes {
		return errors.New("cursor is not valid")
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return errors.New("cursor is not valid")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("cursor is not valid")
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("cursor is not valid")
	}
	return nil
}
