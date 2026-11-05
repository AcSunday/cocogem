package tools

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// AdaptDocument parses only formats with real host support. The original bytes
// remain available alongside the derived, locatable text.
func AdaptDocument(path string, content []byte) (w.Source, bool, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		source, err := parseCSVDocument(content)
		return source, true, err
	case ".pdf":
		source, err := parsePDFDocument(content)
		return source, true, err
	default:
		return w.Source{}, false, nil
	}
}

func originalDocument(source w.Source, content []byte) w.Source {
	source.OriginalContent = base64.StdEncoding.EncodeToString(content)
	source.OriginalDigest = sha256Hex(content)
	source.ParseStatus = "parsed"
	source.Visibility = "task_chain"
	source.CapturedAt = w.Now()
	return source
}

func parseCSVDocument(content []byte) (w.Source, error) {
	if !utf8.Valid(content) {
		return w.Source{}, errors.New("CSV must be UTF-8 text")
	}
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	var out strings.Builder
	rows := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return w.Source{}, fmt.Errorf("parse CSV: %w", err)
		}
		rows++
		if rows > 5000 {
			return w.Source{}, errors.New("CSV exceeds 5000-row parse budget")
		}
		for i, value := range record {
			if len(value) > 32768 {
				return w.Source{}, errors.New("CSV cell exceeds 32 KiB parse budget")
			}
			if i > 0 {
				out.WriteString(" | ")
			}
			out.WriteString(strconv.Quote(value))
		}
		out.WriteByte('\n')
	}
	if rows == 0 {
		return w.Source{}, errors.New("CSV contains no records")
	}
	source := w.Source{
		Kind: "csv", MediaType: "text/csv", Content: out.String(),
		ParseQuality: "rows_and_columns", ParserVersion: "csv-v1", SourceLocator: "row:N; column:M",
		SHA256: sha256Hex(content),
	}
	return originalDocument(source, content), nil
}

var (
	pdfPagePattern   = regexp.MustCompile(`/Type\s*/Page(?:\s|/|>>)`)
	pdfStreamPattern = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
)

func parsePDFDocument(content []byte) (w.Source, error) {
	if !bytes.HasPrefix(content, []byte("%PDF-")) {
		return w.Source{}, errors.New("invalid PDF header")
	}
	if bytes.Contains(content, []byte("/Encrypt")) {
		return w.Source{}, errors.New("encrypted PDF is unsupported")
	}
	if bytes.Contains(content, []byte("/ObjStm")) {
		return w.Source{}, errors.New("PDF object streams are unsupported by the text parser")
	}
	pages := len(pdfPagePattern.FindAll(content, -1))
	if pages != 1 {
		return w.Source{}, fmt.Errorf("text PDF parser supports exactly one page, found %d", pages)
	}
	streams := pdfStreamPattern.FindAllSubmatch(content, -1)
	var out strings.Builder
	for _, match := range streams {
		stream := match[1]
		prefix := content[max(0, bytes.Index(content, match[0])-512):bytes.Index(content, match[0])]
		if bytes.Contains(prefix, []byte("/FlateDecode")) {
			reader := flate.NewReader(bytes.NewReader(stream))
			decoded, err := io.ReadAll(io.LimitReader(reader, 4<<20+1))
			closeErr := reader.Close()
			if err != nil || closeErr != nil || len(decoded) > 4<<20 {
				return w.Source{}, errors.New("invalid or oversized PDF Flate stream")
			}
			stream = decoded
		}
		for _, text := range pdfLiteralStrings(stream) {
			if out.Len() > 32768 {
				break
			}
			out.WriteString(text)
			out.WriteByte('\n')
		}
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return w.Source{}, errors.New("PDF contains no supported text; OCR and complex layouts are unavailable")
	}
	truncated := out.Len() > 32768
	if truncated {
		text = string([]rune(text)[:min(len([]rune(text)), 10000)])
	}
	source := w.Source{
		Kind: "pdf", MediaType: "application/pdf", Content: text, Truncated: truncated,
		ParseQuality: "single_page_text", ParserVersion: "pdf-text-v1", SourceLocator: "page:1",
		StartLine: 1, EndLine: 1, SHA256: sha256Hex(content),
	}
	return originalDocument(source, content), nil
}

func pdfLiteralStrings(stream []byte) []string {
	values := []string{}
	for i := 0; i < len(stream); i++ {
		if stream[i] != '(' {
			continue
		}
		depth := 1
		var literal []byte
		for i++; i < len(stream) && depth > 0; i++ {
			current := stream[i]
			if current == '\\' {
				if i+1 >= len(stream) {
					break
				}
				i++
				next := stream[i]
				switch next {
				case 'n':
					literal = append(literal, '\n')
				case 'r':
					literal = append(literal, '\r')
				case 't':
					literal = append(literal, '\t')
				case '\n':
				case '\r':
					if i+1 < len(stream) && stream[i+1] == '\n' {
						i++
					}
				case '0', '1', '2', '3', '4', '5', '6', '7':
					value := int(next - '0')
					for range 2 {
						if i+1 >= len(stream) || stream[i+1] < '0' || stream[i+1] > '7' {
							break
						}
						i++
						value = value*8 + int(stream[i]-'0')
					}
					literal = append(literal, byte(value))
				default:
					literal = append(literal, next)
				}
				continue
			}
			if current == '(' {
				depth++
				literal = append(literal, current)
				continue
			}
			if current == ')' {
				depth--
				if depth > 0 {
					literal = append(literal, current)
				}
				continue
			}
			literal = append(literal, current)
		}
		if depth == 0 && len(literal) > 0 {
			if decoded, ok := decodePDFText(literal); ok {
				values = append(values, decoded)
			}
		}
	}
	return values
}

func decodePDFText(literal []byte) (string, bool) {
	if bytes.HasPrefix(literal, []byte{0xfe, 0xff}) {
		if len(literal)%2 != 0 {
			return "", false
		}
		values := make([]uint16, 0, (len(literal)-2)/2)
		for i := 2; i < len(literal); i += 2 {
			values = append(values, uint16(literal[i])<<8|uint16(literal[i+1]))
		}
		return string(utf16.Decode(values)), true
	}
	if !utf8.Valid(literal) {
		return "", false
	}
	return string(literal), true
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
