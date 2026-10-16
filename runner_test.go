package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
)

func TestScanEvents(t *testing.T) {
	t.Parallel()
	longLine := strings.Repeat("a", 70*1024)
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "blank lines CRLF and no final newline", input: "\r\n  first\r\nsecond", want: "first\nsecond\n"},
		{name: "long line", input: longLine, want: longLine + "\n"},
		{name: "line exceeds limit", input: strings.Repeat("a", 4*1024*1024), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			_, err := scanEvents("test", strings.NewReader(tt.input), &output, func(line []byte, w io.Writer) (string, error) {
				_, err := fmt.Fprintln(w, string(line))
				return "", err
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
			if output.String() != tt.want {
				t.Fatalf("output does not match: length = %d, want %d", output.Len(), len(tt.want))
			}
		})
	}
}

func TestScanEventsErrors(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	wantErr := errors.New("bad event")
	_, err := scanEvents("test", strings.NewReader("bad\n"), &output, func([]byte, io.Writer) (string, error) {
		return "", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	_, err = scanEvents("test", failingReader{wantErr}, &output, func([]byte, io.Writer) (string, error) { return "", nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("read error = %v, want %v", err, wantErr)
	}
}

func TestRunCLIErrors(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("sh", "-c", "exit 7")
	_, err := runCLI("test", cmd, func([]byte, io.Writer) (string, error) { return "", nil }, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("error = %v, want exit status 7", err)
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }
