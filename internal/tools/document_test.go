package tools

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileParsesCSVWithOriginalAndRowLocators(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("name,amount\nalpha,2\nbeta,\"x,y\"\n")
	if err := os.WriteFile(filepath.Join(root, "data.csv"), original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadFile(t.Context(), root, "data.csv", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != "csv" || source.ParseStatus != "parsed" || source.SourceLocator != "row:N; column:M" {
		t.Fatalf("CSV source = %+v", source)
	}
	if !strings.Contains(source.Content, `3: "beta" | "x,y"`) {
		t.Fatalf("CSV row location = %q", source.Content)
	}
	decoded, err := base64.StdEncoding.DecodeString(source.OriginalContent)
	if err != nil || !bytes.Equal(decoded, original) {
		t.Fatalf("CSV original = %q, %v", decoded, err)
	}
	if source.OriginalDigest != source.SHA256 {
		t.Fatalf("CSV original digest = %q, file digest = %q", source.OriginalDigest, source.SHA256)
	}
}

func TestReadFileParsesSinglePageTextPDF(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("BT /F1 12 Tf (Hello PDF) Tj (Counterevidence) Tj ET")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	original := append([]byte("%PDF-1.4\n1 0 obj << /Type /Page >> endobj\n2 0 obj << /Filter /FlateDecode /Length 99 >> stream\n"), compressed.Bytes()...)
	original = append(original, []byte("\nendstream\nendobj\n")...)
	if err := os.WriteFile(filepath.Join(root, "evidence.pdf"), original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadFile(t.Context(), root, "evidence.pdf", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != "pdf" || source.SourceLocator != "page:1" || !strings.Contains(source.Content, "Hello PDF") || !strings.Contains(source.Content, "Counterevidence") {
		t.Fatalf("PDF source = %+v", source)
	}
	decoded, err := base64.StdEncoding.DecodeString(source.OriginalContent)
	if err != nil || !bytes.Equal(decoded, original) {
		t.Fatalf("PDF original was not preserved: %v", err)
	}
}

func TestPDFParserRefusesUnsupportedComplexity(t *testing.T) {
	multiPage := []byte("%PDF-1.4\n<< /Type /Page >> << /Type /Page >>")
	if _, _, err := AdaptDocument(t.Context(), "multi.pdf", multiPage); err == nil {
		t.Fatal("multi-page PDF was falsely reported as parsed")
	}
	imageOnly := []byte("%PDF-1.4\n<< /Type /Page >>\nstream\n/Image\nendstream")
	if _, _, err := AdaptDocument(t.Context(), "scan.pdf", imageOnly); err == nil {
		t.Fatal("image-only PDF was falsely reported as text")
	}
}
