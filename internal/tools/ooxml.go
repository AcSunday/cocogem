package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// OOXML package media types. One parser handles the container family so adding
// another Office text format does not require another host tool or path.
const (
	DocxMediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	XlsxMediaType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	PptxMediaType = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

	maxOOXMLPartBytes  = 32 << 20
	maxOOXMLTotalBytes = 64 << 20
	maxExtractedText   = 1 << 20
)

type ooxmlPart struct {
	name string
	file *zip.File
}

type ooxmlPackage struct {
	parts map[string]ooxmlPart
}

func openOOXML(content []byte) (ooxmlPackage, error) {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return ooxmlPackage{}, errors.New("invalid or encrypted OOXML document; legacy .doc and password-protected files are unsupported")
	}
	pkg := ooxmlPackage{parts: make(map[string]ooxmlPart, len(reader.File))}
	var total uint64
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if file.Flags&1 != 0 {
			return ooxmlPackage{}, errors.New("encrypted OOXML document parts are unsupported")
		}
		total += file.UncompressedSize64
		if total > maxOOXMLTotalBytes {
			return ooxmlPackage{}, errors.New("OOXML document exceeds the extraction budget")
		}
		pkg.parts[file.Name] = ooxmlPart{name: file.Name, file: file}
	}
	return pkg, nil
}

func (p ooxmlPackage) read(ctx context.Context, name string) ([]byte, bool, error) {
	part, ok := p.parts[name]
	if !ok {
		return nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if part.file.UncompressedSize64 > maxOOXMLPartBytes {
		return nil, true, fmt.Errorf("%s exceeds the OOXML part budget", name)
	}
	file, err := part.file.Open()
	if err != nil {
		return nil, true, errors.New("OOXML document part is unreadable")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxOOXMLPartBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, true, errors.New("OOXML document part is unreadable")
	}
	if closeErr != nil {
		return nil, true, errors.New("OOXML document part could not be closed")
	}
	if len(data) > maxOOXMLPartBytes {
		return nil, true, fmt.Errorf("%s exceeds the OOXML part budget", name)
	}
	return data, true, nil
}

// textCollector bounds the extracted text before it reaches a Source body.
type textCollector struct {
	builder   strings.Builder
	truncated bool
}

func (c *textCollector) write(value string) {
	if c.truncated || value == "" {
		return
	}
	if c.builder.Len()+len(value) <= maxExtractedText {
		c.builder.WriteString(value)
		return
	}
	remain := maxExtractedText - c.builder.Len()
	if remain > 0 {
		end := remain
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		c.builder.WriteString(value[:end])
	}
	c.truncated = true
}

func (c *textCollector) String() string { return c.builder.String() }

func normalizeExtractedText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func ooxmlSource(kind, mediaType, locator, text string, truncated bool, original []byte) w.Source {
	source := w.Source{
		Kind: kind, MediaType: mediaType, Content: text, Truncated: truncated,
		ParseQuality: "ooxml_text", ParserVersion: "ooxml-text-v1", SourceLocator: locator,
		SHA256: sha256Hex(original),
	}
	return originalDocument(source, original)
}

func parseDOCX(ctx context.Context, content []byte) (w.Source, error) {
	pkg, err := openOOXML(content)
	if err != nil {
		return w.Source{}, err
	}
	names := []string{"word/document.xml"}
	for _, name := range pkg.parts {
		if strings.HasPrefix(name.name, "word/header") && strings.HasSuffix(name.name, ".xml") ||
			strings.HasPrefix(name.name, "word/footer") && strings.HasSuffix(name.name, ".xml") ||
			name.name == "word/footnotes.xml" || name.name == "word/endnotes.xml" {
			names = append(names, name.name)
		}
	}
	sort.Strings(names)
	var text textCollector
	paragraphs := 0
	for _, name := range names {
		data, ok, err := pkg.read(ctx, name)
		if err != nil {
			return w.Source{}, err
		}
		if !ok {
			if name == "word/document.xml" {
				return w.Source{}, errors.New("DOCX contains no word/document.xml")
			}
			continue
		}
		count, err := extractWordML(bytes.NewReader(data), &text)
		paragraphs += count
		if err != nil {
			return w.Source{}, fmt.Errorf("parse %s: %w", name, err)
		}
		if text.truncated {
			break
		}
	}
	value := normalizeExtractedText(text.String())
	if value == "" {
		return w.Source{}, errors.New("DOCX contains no readable text")
	}
	if paragraphs == 0 {
		paragraphs = 1
	}
	return ooxmlSource("docx", DocxMediaType, fmt.Sprintf("paragraph:1-%d", paragraphs), value, text.truncated, content), nil
}

func extractWordML(reader io.Reader, text *textCollector) (int, error) {
	decoder := xml.NewDecoder(reader)
	paragraphs := 0
	inText := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return paragraphs, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "t":
				inText = true
			case "tab":
				text.write("\t")
			case "br", "cr":
				text.write("\n")
			}
		case xml.CharData:
			if inText {
				text.write(string(value))
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "p":
				text.write("\n")
				paragraphs++
			}
		}
		if text.truncated {
			break
		}
	}
	return paragraphs, nil
}

func parsePPTX(ctx context.Context, content []byte) (w.Source, error) {
	pkg, err := openOOXML(content)
	if err != nil {
		return w.Source{}, err
	}
	names := []string{}
	for name := range pkg.parts {
		if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return slideNumber(names[i]) < slideNumber(names[j])
	})
	if len(names) == 0 {
		return w.Source{}, errors.New("PPTX contains no slides")
	}
	var text textCollector
	paragraphs := 0
	for index, name := range names {
		data, ok, err := pkg.read(ctx, name)
		if err != nil {
			return w.Source{}, err
		}
		if !ok {
			continue
		}
		text.write(fmt.Sprintf("\n--- slide %d ---\n", index+1))
		count, err := extractDrawingML(bytes.NewReader(data), &text)
		paragraphs += count
		if err != nil {
			return w.Source{}, fmt.Errorf("parse %s: %w", name, err)
		}
		if text.truncated {
			break
		}
	}
	value := normalizeExtractedText(text.String())
	if value == "" {
		return w.Source{}, errors.New("PPTX contains no readable text")
	}
	if paragraphs == 0 {
		paragraphs = 1
	}
	return ooxmlSource("pptx", PptxMediaType, fmt.Sprintf("slide:1-%d", len(names)), value, text.truncated, content), nil
}

func extractDrawingML(reader io.Reader, text *textCollector) (int, error) {
	decoder := xml.NewDecoder(reader)
	paragraphs := 0
	inText := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return paragraphs, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "t":
				inText = true
			case "tab":
				text.write("\t")
			case "br":
				text.write("\n")
			}
		case xml.CharData:
			if inText {
				text.write(string(value))
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "p":
				text.write("\n")
				paragraphs++
			}
		}
		if text.truncated {
			break
		}
	}
	return paragraphs, nil
}

func slideNumber(name string) int {
	base := path.Base(name)
	base = strings.TrimSuffix(strings.TrimPrefix(base, "slide"), ".xml")
	value, _ := strconv.Atoi(base)
	return value
}

type xlsxSheet struct {
	name string
	file string
}

func parseXLSX(ctx context.Context, content []byte) (w.Source, error) {
	pkg, err := openOOXML(content)
	if err != nil {
		return w.Source{}, err
	}
	shared, err := xlsxSharedStrings(ctx, pkg)
	if err != nil {
		return w.Source{}, err
	}
	sheets, err := xlsxSheets(ctx, pkg)
	if err != nil {
		return w.Source{}, err
	}
	var text textCollector
	rows := 0
	for _, sheet := range sheets {
		data, ok, err := pkg.read(ctx, sheet.file)
		if err != nil {
			return w.Source{}, err
		}
		if !ok {
			continue
		}
		sheetText, count, err := parseWorksheet(data, shared)
		if err != nil {
			return w.Source{}, fmt.Errorf("parse %s: %w", sheet.file, err)
		}
		if strings.TrimSpace(sheetText) == "" {
			continue
		}
		text.write("\n## sheet: " + sheet.name + "\n")
		text.write(sheetText)
		rows += count
		if text.truncated {
			break
		}
	}
	value := normalizeExtractedText(text.String())
	if value == "" {
		return w.Source{}, errors.New("XLSX contains no readable cells")
	}
	if rows == 0 {
		rows = 1
	}
	return ooxmlSource("xlsx", XlsxMediaType, fmt.Sprintf("sheet:1-%d", len(sheets)), value, text.truncated, content), nil
}

func xlsxSharedStrings(ctx context.Context, pkg ooxmlPackage) ([]string, error) {
	data, ok, err := pkg.read(ctx, "xl/sharedStrings.xml")
	if err != nil || !ok {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	values := []string{}
	inString, inText := false, false
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid XLSX shared strings")
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "si":
				inString = true
				text.Reset()
			case "t":
				if inString {
					inText = true
				}
			}
		case xml.CharData:
			if inText {
				text.Write(value)
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "si":
				values = append(values, text.String())
				inString = false
			}
		}
	}
	return values, nil
}

func xlsxSheets(ctx context.Context, pkg ooxmlPackage) ([]xlsxSheet, error) {
	data, ok, err := pkg.read(ctx, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	sheets := []xlsxSheet{}
	if ok {
		decoder := xml.NewDecoder(bytes.NewReader(data))
		for {
			token, err := decoder.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, errors.New("invalid XLSX workbook")
			}
			start, isStart := token.(xml.StartElement)
			if !isStart || start.Name.Local != "sheet" {
				continue
			}
			name, relID := "", ""
			for _, attr := range start.Attr {
				switch attr.Name.Local {
				case "name":
					name = attr.Value
				case "id":
					relID = attr.Value
				}
			}
			if name != "" && relID != "" {
				sheets = append(sheets, xlsxSheet{name: name, file: relID})
			}
		}
	}
	rels := map[string]string{}
	if data, ok, err := pkg.read(ctx, "xl/_rels/workbook.xml.rels"); err != nil {
		return nil, err
	} else if ok {
		decoder := xml.NewDecoder(bytes.NewReader(data))
		for {
			token, err := decoder.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, errors.New("invalid XLSX workbook relationships")
			}
			start, isStart := token.(xml.StartElement)
			if !isStart || start.Name.Local != "Relationship" {
				continue
			}
			id, target := "", ""
			for _, attr := range start.Attr {
				switch attr.Name.Local {
				case "Id":
					id = attr.Value
				case "Target":
					target = attr.Value
				}
			}
			if id != "" && target != "" && !strings.HasPrefix(target, "../") {
				rels[id] = path.Clean(path.Join("xl", strings.TrimPrefix(target, "/")))
			}
		}
	}
	out := make([]xlsxSheet, 0, len(sheets))
	for _, sheet := range sheets {
		file := rels[sheet.file]
		if file == "" {
			continue
		}
		if _, ok := pkg.parts[file]; ok {
			out = append(out, xlsxSheet{name: sheet.name, file: file})
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	// A few generators omit or use non-standard relationships. Falling back to
	// sorted worksheet parts still exposes the cell text rather than failing.
	names := []string{}
	for name := range pkg.parts {
		if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for index, name := range names {
		out = append(out, xlsxSheet{name: fmt.Sprintf("sheet%d", index+1), file: name})
	}
	if len(out) == 0 {
		return nil, errors.New("XLSX contains no worksheets")
	}
	return out, nil
}

func parseWorksheet(data []byte, shared []string) (string, int, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out strings.Builder
	cells := []string{}
	cellText := strings.Builder{}
	rows := 0
	inValue, inInline, inText := false, false, false
	cellType := ""
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", rows, errors.New("invalid XLSX worksheet")
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "row":
				cells = cells[:0]
			case "c":
				cellType = ""
				cellText.Reset()
				for _, attr := range value.Attr {
					if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
			case "v":
				inValue = true
			case "is":
				inInline = true
			case "t":
				if inInline {
					inText = true
				}
			}
		case xml.CharData:
			if inValue || inText {
				cellText.Write(value)
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "v":
				inValue = false
			case "t":
				inText = false
			case "is":
				inInline = false
			case "c":
				text := cellText.String()
				if cellType == "s" {
					if index, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && index >= 0 && index < len(shared) {
						text = shared[index]
					}
				}
				cells = append(cells, strings.TrimSpace(text))
			case "row":
				if len(cells) > 0 {
					out.WriteString(strings.Join(cells, "\t"))
					out.WriteByte('\n')
					rows++
				}
			}
		}
	}
	return out.String(), rows, nil
}
