package workspace

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func collectText(t *testing.T, ref TextRef, text string, maxBytes int) string {
	t.Helper()
	var collected strings.Builder
	cursor := ""
	for pages := 0; pages < 100; pages++ {
		page, err := PageText(ref, text, cursor, maxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(page.Content) || len(page.Content) > MaxTextPageBytes {
			t.Fatalf("invalid page: bytes=%d", len(page.Content))
		}
		encoded, err := json.Marshal(page)
		if err != nil || len(encoded) > MaxTextPageWireBytes {
			t.Fatalf("wire page exceeds budget: %d: %v", len(encoded), err)
		}
		collected.WriteString(page.Content)
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatal("final page has a cursor")
			}
			return collected.String()
		}
		if page.NextCursor == "" {
			t.Fatal("more pages promised without a cursor")
		}
		cursor = page.NextCursor
	}
	t.Fatal("pagination did not terminate")
	return ""
}

func TestPageTextRoundTripsUTF8(t *testing.T) {
	ref := TextRef{Kind: "artifact", ID: "artifact-1", Version: 2}
	text := strings.Repeat("研究证据🔍", 3000)
	if got := collectText(t, ref, text, 0); got != text {
		t.Fatalf("pages did not reconstruct the exact text: got %d bytes, want %d", len(got), len(text))
	}
	page, err := PageText(ref, "猫猫", "", 1)
	if err == nil || page.Content != "" {
		t.Fatalf("a page split a multi-byte character: %+v %v", page, err)
	}
	if _, err := PageText(ref, text, "", -1); err == nil {
		t.Fatal("negative max_bytes accepted")
	}
	if _, err := PageText(ref, text, "", MaxTextPageBytes+1); err == nil {
		t.Fatal("oversized raw page accepted")
	}
	if _, err := PageText(TextRef{Kind: "artifact", ID: "artifact-1"}, text, "", 0); err == nil {
		t.Fatal("artifact without an exact version accepted")
	}
	if _, err := PageText(ref, string([]byte{0xff}), "", 0); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestPageTextCursorIsBoundToReferenceVersion(t *testing.T) {
	text := strings.Repeat("证据", 4000)
	ref := TextRef{Kind: "source", ID: "source-1"}
	first, err := PageText(ref, text, "", 1024)
	if err != nil || !first.HasMore {
		t.Fatal(err, first)
	}
	if _, err := PageText(TextRef{Kind: "source", ID: "source-2"}, text, first.NextCursor, 1024); err == nil {
		t.Fatal("cursor crossed source identity")
	}
	artifact := TextRef{Kind: "artifact", ID: "artifact-1", Version: 1}
	first, err = PageText(artifact, text, "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PageText(TextRef{Kind: "artifact", ID: "artifact-1", Version: 2}, text, first.NextCursor, 1024); err == nil {
		t.Fatal("cursor crossed artifact version")
	}
	if _, err := PageText(artifact, text+"changed", first.NextCursor, 1024); err == nil {
		t.Fatal("cursor crossed content digest")
	}
	if _, err := PageText(ref, text, "not-a-cursor", 1024); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}

func TestTextPageWireBudgetWithEscapedControlCharacters(t *testing.T) {
	page, err := PageText(TextRef{Kind: "source", ID: "source-1"}, strings.Repeat("\x00", MaxTextPageBytes), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > MaxTextPageWireBytes {
		t.Fatalf("wire budget exceeded: %d: %v", len(encoded), err)
	}
}

func TestPageTextFullRawBudgetWithEscapesAndMultibyte(t *testing.T) {
	ref := TextRef{Kind: "source", ID: "source-1"}
	text := strings.Repeat("\x00", 4096) + strings.Repeat("猫", 1365) + "a"
	if len(text) != MaxTextPageBytes {
		t.Fatalf("fixture is %d bytes, want %d", len(text), MaxTextPageBytes)
	}
	page, err := PageText(ref, text, "", MaxTextPageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if page.Content != text || page.HasMore || len(page.Content) != MaxTextPageBytes {
		t.Fatalf("full-budget page = %d bytes, more=%v", len(page.Content), page.HasMore)
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > MaxTextPageWireBytes {
		t.Fatalf("wire budget exceeded: %d: %v", len(encoded), err)
	}
	if got := collectText(t, ref, text, 4096); got != text {
		t.Fatalf("escaped content did not round-trip: got %d bytes", len(got))
	}
	page, err = PageText(ref, strings.Repeat("猫", 3000), "", 4000)
	if err != nil || len(page.Content) > 4000 || len(page.Content)%3 != 0 || !utf8.ValidString(page.Content) {
		t.Fatalf("odd raw budget split a multi-byte character: %d bytes, %v", len(page.Content), err)
	}
}

func TestPageTextRejectsMalformedCursors(t *testing.T) {
	ref := TextRef{Kind: "source", ID: "source-1"}
	text := strings.Repeat("证", 3000)
	first, err := PageText(ref, text, "", 1024)
	if err != nil || !first.HasMore || first.NextCursor == "" {
		t.Fatal(err, first)
	}
	raw, err := base64.RawURLEncoding.DecodeString(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	var valid textCursor
	if err := json.Unmarshal(raw, &valid); err != nil {
		t.Fatal(err)
	}
	forged := func(mutate func(*textCursor)) string {
		c := valid
		mutate(&c)
		token, err := encodeCursor(c)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	for _, tc := range []struct {
		name   string
		cursor string
	}{
		{"oversized", strings.Repeat("A", maxTextCursorBytes+1)},
		{"not base64", "%%%"},
		{"unknown field", base64.RawURLEncoding.EncodeToString([]byte(`{"kind":"source","id":"source-1","version":0,"digest":"` + valid.Digest + `","offset":1024,"extra":1}`))},
		{"trailing value", base64.RawURLEncoding.EncodeToString(append(append([]byte{}, raw...), []byte(`{}`)...))},
		{"negative offset", forged(func(c *textCursor) { c.Offset = -1 })},
		{"offset beyond text", forged(func(c *textCursor) { c.Offset = len(text) + 1 })},
		{"mid rune", forged(func(c *textCursor) { c.Offset = 1 })},
	} {
		if _, err := PageText(ref, text, tc.cursor, 1024); err == nil {
			t.Fatalf("%s cursor was accepted", tc.name)
		}
	}
}
