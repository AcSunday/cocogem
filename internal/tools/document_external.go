package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Legacy document conversion is intentionally a host fallback, not another
// hand-written parser per vendor. It keeps the same bounded invocation and
// explicit failure model as the native document path.
func convertLegacyDocument(ctx context.Context, path string, content []byte) (w.Source, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if !slicesContains([]string{".doc", ".xls", ".ppt", ".rtf", ".odt", ".wordml", ".webarchive"}, ext) {
		return w.Source{}, fmt.Errorf("legacy document type %s is unsupported", ext)
	}
	var text string
	var truncated bool
	var parser string
	var lastErr error
	if runtime.GOOS == "darwin" {
		converted, cut, err := textutilText(ctx, ext, content)
		if err == nil {
			text, truncated, parser = converted, cut, "textutil-txt-v1"
		} else {
			lastErr = err
		}
	}
	if text == "" {
		if format := pandocFormat(ext); format != "" {
			converted, cut, err := pandocText(ctx, format, content)
			if err == nil {
				text, truncated, parser = converted, cut, "pandoc-plain-v1"
			} else {
				lastErr = err
			}
		}
	}
	if text == "" {
		if lastErr != nil {
			return w.Source{}, fmt.Errorf("legacy %s document could not be converted: %w", ext, lastErr)
		}
		return w.Source{}, fmt.Errorf("legacy %s document is unsupported on this host; install textutil or pandoc, or save it as .docx/PDF", ext)
	}
	text = normalizeExtractedText(text)
	if text == "" {
		return w.Source{}, fmt.Errorf("legacy %s document contains no readable text", ext)
	}
	source := w.Source{
		Kind: strings.TrimPrefix(ext, "."), MediaType: legacyMediaType(ext), Content: text,
		Truncated: truncated, ParseQuality: "host_converter_text", ParserVersion: parser,
		SourceLocator: "text", SHA256: sha256Hex(content),
	}
	return originalDocument(source, content), nil
}

func textutilText(ctx context.Context, ext string, content []byte) (string, bool, error) {
	bin, err := exec.LookPath("textutil")
	if err != nil {
		return "", false, errors.New("textutil is unavailable")
	}
	dir, err := os.MkdirTemp("", "cocogem-textutil-*")
	if err != nil {
		return "", false, errors.New("could not create a converter directory")
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input"+ext)
	if err := os.WriteFile(input, content, 0600); err != nil {
		return "", false, errors.New("could not prepare the legacy document")
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(runCtx, bin, "-convert", "txt", "-stdout", "-noload", "-nostore", "-timeout", "10", "--", input)
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C.UTF-8", "TMPDIR=" + dir, "HOME=" + dir}
	output := &boundedBuffer{limit: maxExtractedText}
	diagnostics := &boundedBuffer{limit: 8192}
	command.Stdout, command.Stderr = output, diagnostics
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(diagnostics.String())
		if detail == "" {
			detail = "converter exited unsuccessfully"
		}
		return "", false, errors.New(shortField(detail, 512))
	}
	if runCtx.Err() != nil {
		return "", false, errors.New("legacy document conversion timed out")
	}
	text := output.String()
	return text, output.truncated || len(text) > maxExtractedText, nil
}

func pandocText(ctx context.Context, format string, content []byte) (string, bool, error) {
	bin, err := exec.LookPath("pandoc")
	if err != nil {
		return "", false, errors.New("pandoc is unavailable")
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(runCtx, bin, "--from", format, "--to", "plain", "--wrap=none", "--quiet")
	command.Stdin = bytes.NewReader(content)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "LANG=C.UTF-8"}
	output := &boundedBuffer{limit: maxExtractedText}
	diagnostics := &boundedBuffer{limit: 8192}
	command.Stdout, command.Stderr = output, diagnostics
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(diagnostics.String())
		if detail == "" {
			detail = "converter exited unsuccessfully"
		}
		return "", false, errors.New(shortField(detail, 512))
	}
	if runCtx.Err() != nil {
		return "", false, errors.New("legacy document conversion timed out")
	}
	text := output.String()
	return text, output.truncated || len(text) > maxExtractedText, nil
}

func pandocFormat(ext string) string {
	switch ext {
	case ".odt":
		return "odt"
	case ".rtf":
		return "rtf"
	default:
		return ""
	}
}

func legacyMediaType(ext string) string {
	switch ext {
	case ".doc":
		return "application/msword"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".rtf":
		return "application/rtf"
	case ".odt":
		return "application/vnd.oasis.opendocument.text"
	case ".wordml":
		return "application/xml"
	case ".webarchive":
		return "application/webarchive"
	default:
		return "application/octet-stream"
	}
}

func shortField(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
