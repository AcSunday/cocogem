package tools

import (
	"bytes"
	"compress/flate"
	"context"
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

// AdaptDocument extracts readable text from the document families the host can
// understand. It is deliberately a dispatch over formats, not one special case
// per extension: OOXML covers DOCX/XLSX/PPTX through one bounded ZIP/XML path,
// PDF/CSV/HTML have native parsers, and legacy formats use a host converter
// when one is available. Unknown UTF-8 files remain the caller's text fallback.
func AdaptDocument(ctx context.Context, path string, content []byte) (w.Source, bool, error) {
	switch documentFormat(path, content) {
	case "csv":
		source, err := parseDelimitedDocument(content, ',')
		return source, true, err
	case "tsv":
		source, err := parseDelimitedDocument(content, '\t')
		return source, true, err
	case "pdf":
		source, err := parsePDFDocument(content)
		return source, true, err
	case "html":
		source, err := parseHTMLDocument(content)
		return source, true, err
	case "docx":
		source, err := parseDOCX(ctx, content)
		return source, true, err
	case "xlsx":
		source, err := parseXLSX(ctx, content)
		return source, true, err
	case "pptx":
		source, err := parsePPTX(ctx, content)
		return source, true, err
	case "legacy":
		source, err := convertLegacyDocument(ctx, path, content)
		return source, true, err
	case "archive":
		return w.Source{}, true, errors.New("ZIP archive is not a supported document; extract the needed file or convert it to text, PDF, or OOXML")
	default:
		return w.Source{}, false, nil
	}
}

const maxDocumentFileBytes = 8 << 20

// documentReadLimit gives document containers enough room for embedded styles
// and images while keeping ordinary text files on the tighter read budget.
func documentReadLimit(path string) int64 {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".tsv", ".pdf", ".html", ".htm", ".xhtml",
		".docx", ".docm", ".xlsx", ".xlsm", ".pptx", ".pptm",
		".doc", ".xls", ".ppt", ".rtf", ".odt", ".wordml", ".webarchive":
		return maxDocumentFileBytes
	default:
		return 1 << 20
	}
}

// documentFormat prefers a trustworthy extension, then content signatures so a
// renamed Office document does not silently become unreadable binary.
func documentFormat(path string, content []byte) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return "csv"
	case ".tsv":
		return "tsv"
	case ".pdf":
		return "pdf"
	case ".html", ".htm", ".xhtml":
		return "html"
	case ".docx", ".docm":
		return "docx"
	case ".xlsx", ".xlsm":
		return "xlsx"
	case ".pptx", ".pptm":
		return "pptx"
	case ".doc", ".xls", ".ppt", ".rtf", ".odt", ".wordml", ".webarchive":
		return "legacy"
	}
	trimmed := bytes.TrimSpace(content)
	lower := bytes.ToLower(trimmed)
	switch {
	case bytes.HasPrefix(content, []byte("%PDF-")):
		return "pdf"
	case bytes.HasPrefix(trimmed, []byte("{\\rtf")):
		return "legacy"
	case bytes.HasPrefix(lower, []byte("<!doctype html")), bytes.HasPrefix(lower, []byte("<html")):
		return "html"
	case bytes.HasPrefix(content, []byte("PK\x03\x04")):
		if kind, err := sniffOOXML(content); err == nil {
			return kind
		}
		return "archive"
	}
	return ""
}

func sniffOOXML(content []byte) (string, error) {
	pkg, err := openOOXML(content)
	if err != nil {
		return "", err
	}
	switch {
	case pkg.parts["word/document.xml"].file != nil:
		return "docx", nil
	case pkg.parts["xl/workbook.xml"].file != nil:
		return "xlsx", nil
	case pkg.parts["ppt/presentation.xml"].file != nil:
		return "pptx", nil
	default:
		return "", errors.New("not an OOXML document")
	}
}

func parseHTMLDocument(content []byte) (w.Source, error) {
	original := content
	decoded := decodeTextContent(content)
	if decoded == nil {
		return w.Source{}, errors.New("HTML document must be UTF-8 or UTF-16 text")
	}
	_, text, err := extractHTML(string(decoded))
	if err != nil {
		return w.Source{}, errors.New("HTML document could not be parsed")
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return w.Source{}, errors.New("HTML document contains no readable text")
	}
	source := w.Source{
		Kind: "html", MediaType: "text/html", Content: text, ParseQuality: "text_only",
		ParserVersion: "html-text-v1", SourceLocator: "text", SHA256: sha256Hex(original),
	}
	return originalDocument(source, original), nil
}

// decodeTextContent accepts the text encodings common in imported office and
// legacy files without guessing an arbitrary binary as text.
func decodeTextContent(content []byte) []byte {
	switch {
	case bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}):
		content = content[3:]
	case bytes.HasPrefix(content, []byte{0xff, 0xfe}):
		content = decodeUTF16(content[2:], true)
	case bytes.HasPrefix(content, []byte{0xfe, 0xff}):
		content = decodeUTF16(content[2:], false)
	}
	if !utf8.Valid(content) || bytes.ContainsRune(content, 0) {
		return nil
	}
	return content
}

func decodeUTF16(content []byte, littleEndian bool) []byte {
	if len(content)%2 != 0 {
		return nil
	}
	values := make([]uint16, 0, len(content)/2)
	for i := 0; i < len(content); i += 2 {
		if littleEndian {
			values = append(values, uint16(content[i])|uint16(content[i+1])<<8)
		} else {
			values = append(values, uint16(content[i])<<8|uint16(content[i+1]))
		}
	}
	return []byte(string(utf16.Decode(values)))
}

func originalDocument(source w.Source, content []byte) w.Source {
	source.OriginalContent = base64.StdEncoding.EncodeToString(content)
	source.OriginalDigest = sha256Hex(content)
	source.ParseStatus = "parsed"
	source.Visibility = "task_chain"
	source.CapturedAt = w.Now()
	return source
}

func parseDelimitedDocument(content []byte, comma rune) (w.Source, error) {
	kind, mediaType, parser := "csv", "text/csv", "csv-v1"
	label := "CSV"
	if comma == '\t' {
		kind, mediaType, parser, label = "tsv", "text/tab-separated-values", "tsv-v1", "TSV"
	}
	original := content
	if decoded := decodeTextContent(content); decoded == nil {
		return w.Source{}, fmt.Errorf("%s must be UTF-8 or UTF-16 text", label)
	} else {
		content = decoded
	}
	reader := csv.NewReader(bytes.NewReader(content))
	reader.Comma = comma
	reader.FieldsPerRecord = -1
	var out strings.Builder
	rows := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return w.Source{}, fmt.Errorf("parse %s: %w", label, err)
		}
		rows++
		if rows > 5000 {
			return w.Source{}, fmt.Errorf("%s exceeds 5000-row parse budget", label)
		}
		for i, value := range record {
			if len(value) > 32768 {
				return w.Source{}, fmt.Errorf("%s cell exceeds 32 KiB parse budget", label)
			}
			if i > 0 {
				out.WriteString(" | ")
			}
			out.WriteString(strconv.Quote(value))
		}
		out.WriteByte('\n')
	}
	if rows == 0 {
		return w.Source{}, fmt.Errorf("%s contains no records", label)
	}
	source := w.Source{
		Kind: kind, MediaType: mediaType, Content: out.String(),
		ParseQuality: "rows_and_columns", ParserVersion: parser, SourceLocator: "row:N; column:M",
		SHA256: sha256Hex(original),
	}
	return originalDocument(source, original), nil
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
