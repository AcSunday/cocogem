package tools

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func documentZIP(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, body := range entries {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func documentRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadFileParsesDOCXWithParagraphsAndTables(t *testing.T) {
	root := documentRoot(t)
	original := documentZIP(t, map[string]string{
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>年度研究报告</w:t></w:r></w:p>
    <w:p><w:r><w:t>收入</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>42</w:t></w:r></w:p>
    <w:tbl>
      <w:tr><w:tc><w:p><w:r><w:t>项目</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>金额</w:t></w:r></w:p></w:tc></w:tr>
    </w:tbl>
  </w:body>
</w:document>`,
	})
	if err := os.WriteFile(filepath.Join(root, "report.docx"), original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadFile(t.Context(), root, "report.docx", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != "docx" || source.MediaType != DocxMediaType || source.ParseQuality != "ooxml_text" {
		t.Fatalf("DOCX source = %+v", source)
	}
	for _, expected := range []string{"年度研究报告", "收入", "\t42", "项目", "金额"} {
		if !strings.Contains(source.Content, expected) {
			t.Fatalf("DOCX content missing %q: %q", expected, source.Content)
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(source.OriginalContent)
	if err != nil || !bytes.Equal(decoded, original) {
		t.Fatalf("DOCX original was not preserved: %v", err)
	}
	if source.OriginalDigest != source.SHA256 {
		t.Fatalf("DOCX digests differ: original=%q file=%q", source.OriginalDigest, source.SHA256)
	}
}

func TestReadFileParsesXLSXSharedStrings(t *testing.T) {
	root := documentRoot(t)
	original := documentZIP(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="数据" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>名称</t></si><si><t>数量</t></si><si><t>苹果</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>3</v></c></row></sheetData></worksheet>`,
	})
	if err := os.WriteFile(filepath.Join(root, "data.xlsx"), original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadFile(t.Context(), root, "data.xlsx", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != "xlsx" || source.MediaType != XlsxMediaType {
		t.Fatalf("XLSX source = %+v", source)
	}
	for _, expected := range []string{"sheet: 数据", "名称\t数量", "苹果\t3"} {
		if !strings.Contains(source.Content, expected) {
			t.Fatalf("XLSX content missing %q: %q", expected, source.Content)
		}
	}
}

func TestReadFileParsesPPTXSlideText(t *testing.T) {
	root := documentRoot(t)
	original := documentZIP(t, map[string]string{
		"ppt/presentation.xml":  `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`,
		"ppt/slides/slide1.xml": `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><a:p><a:r><a:t>缓存方案比较</a:t></a:r></a:p><a:p><a:r><a:t>延迟目标</a:t></a:r></a:p></p:cSld></p:sld>`,
	})
	if err := os.WriteFile(filepath.Join(root, "talk.pptx"), original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadFile(t.Context(), root, "talk.pptx", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != "pptx" || !strings.Contains(source.Content, "缓存方案比较") || !strings.Contains(source.Content, "延迟目标") {
		t.Fatalf("PPTX source = %+v", source)
	}
}

func TestAdaptDocumentRecognizesRenamedOOXMLByContent(t *testing.T) {
	original := documentZIP(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>renamed document</w:t></w:r></w:p></w:body></w:document>`,
	})
	source, supported, err := AdaptDocument(t.Context(), "attachment.bin", original)
	if err != nil || !supported || source.Kind != "docx" || !strings.Contains(source.Content, "renamed document") {
		t.Fatalf("renamed OOXML = %+v, supported=%v, err=%v", source, supported, err)
	}
}

func TestReadFileDecodesUTF16TextAndKeepsOriginalIdentity(t *testing.T) {
	root := documentRoot(t)
	text := "UTF-16 研究资料"
	encoded := []byte{0xff, 0xfe}
	for _, value := range utf16.Encode([]rune(text)) {
		encoded = append(encoded, byte(value), byte(value>>8))
	}
	if err := os.WriteFile(filepath.Join(root, "legacy.txt"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadFile(t.Context(), root, "legacy.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source.Content, text) {
		t.Fatalf("UTF-16 text = %q", source.Content)
	}
	if source.SHA256 != sha256Hex(encoded) {
		t.Fatalf("UTF-16 SHA256 = %q, want original", source.SHA256)
	}
}

func TestAdaptDocumentRejectsUnsupportedArchive(t *testing.T) {
	original := documentZIP(t, map[string]string{"readme.txt": "plain"})
	if _, supported, err := AdaptDocument(t.Context(), "bundle.zip", original); !supported || err == nil || !strings.Contains(err.Error(), "ZIP archive") {
		t.Fatalf("unsupported archive = supported:%v err:%v", supported, err)
	}
}

func TestReadFileConvertsLegacyRTFWhenConverterAvailable(t *testing.T) {
	if _, err := exec.LookPath("textutil"); err != nil {
		if _, err := exec.LookPath("pandoc"); err != nil {
			t.Skip("no legacy document converter is installed")
		}
	}
	root := documentRoot(t)
	original := []byte(`{\rtf1\ansi Hello \b legacy\b0}`)
	if err := os.WriteFile(filepath.Join(root, "legacy.rtf"), original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadFile(t.Context(), root, "legacy.rtf", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != "rtf" || !strings.Contains(source.Content, "Hello legacy") {
		t.Fatalf("legacy RTF source = %+v", source)
	}
}
