package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

const maxSourceUploadBytes = 8 << 20

func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// SourceImportList is the bounded, body-free attachment manager projection.
type SourceImportList struct {
	Items []w.SourceImport `json:"items"`
}

func sourceUploadName(name string) (string, error) {
	name = strings.TrimSpace(path.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || !utf8.ValidString(name) || len([]rune(name)) > 240 {
		return "", errors.New("文件名无效或过长")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("文件名不能包含控制字符")
		}
	}
	return name, nil
}

func publicSourceImport(v w.SourceImport) w.SourceImport {
	v.Content = ""
	v.OriginalContent = ""
	return v
}

func (s *Server) importSource(rw http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(rw, r.Body, maxSourceUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(rw, r, 413, "source_too_large", "资料原件不能超过 8 MiB")
		} else {
			writeError(rw, r, 400, "invalid_multipart", "请选择单个 CSV 或 PDF 文件")
		}
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.Value) != 0 || len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		writeError(rw, r, 400, "invalid_multipart", "上传请求必须且只能包含一个 file 文件")
		return
	}
	header := r.MultipartForm.File["file"][0]
	name, err := sourceUploadName(header.Filename)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	file, err := header.Open()
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxSourceUploadBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		respond(rw, r, nil, readErr)
		return
	}
	if closeErr != nil {
		respond(rw, r, nil, closeErr)
		return
	}
	if len(content) > maxSourceUploadBytes {
		writeError(rw, r, 413, "source_too_large", "资料原件不能超过 8 MiB")
		return
	}
	parsed, supported, parseErr := tools.AdaptDocument(r.Context(), name, content)
	if !supported && utf8.Valid(content) && !bytes.ContainsRune(content, 0) {
		parsed = w.Source{Kind: "file", MediaType: "text/plain; charset=utf-8", Content: string(content), ParseQuality: "text", ParserVersion: "utf8-v1"}
		supported = true
	}
	if !supported {
		media := tools.FileMediaType(name, content)
		kind := "attachment"
		if strings.HasPrefix(media, "image/") {
			kind = "image"
		}
		parsed = w.Source{Kind: kind, MediaType: media, Content: "Attached original file. Use view_image for image pixels, or inspect/convert the original in the task directory with run_command. This metadata is not the file contents.", ParseStatus: "available"}
	}
	if err := r.Context().Err(); err != nil {
		respond(rw, r, nil, err)
		return
	}
	imported := w.SourceImport{
		ID: w.NewID(), ThreadID: r.PathValue("id"), Kind: parsed.Kind, Title: name, FileName: name,
		MediaType: parsed.MediaType, Content: parsed.Content, ContentDigest: digest(parsed.Content),
		OriginalContent: base64.StdEncoding.EncodeToString(content), OriginalDigest: digestBytes(content),
		SHA256: digestBytes(content), CapturedAt: w.Now(), Truncated: parsed.Truncated,
		StartLine: parsed.StartLine, EndLine: parsed.EndLine, ParseStatus: "parsed",
		ParseQuality: parsed.ParseQuality, ParserVersion: parsed.ParserVersion,
		SourceLocator: parsed.SourceLocator, Visibility: "task_chain",
	}
	if !supported {
		imported.ParseStatus = "available"
	}
	if parseErr != nil {
		imported.Content = ""
		imported.ContentDigest = ""
		imported.ParseStatus = "failed"
		imported.ParseError = parseErr.Error()
		imported.ParseQuality = ""
		imported.ParserVersion = ""
		imported.SourceLocator = ""
		imported.StartLine = 0
		imported.EndLine = 0
		imported.Truncated = false
	}
	if err := r.Context().Err(); err != nil {
		respond(rw, r, nil, err)
		return
	}
	err = s.store.Write(r.Context(), func(tx *store.Tx) error {
		thread, err := tx.Thread(imported.ThreadID)
		if err != nil {
			return err
		}
		imported.WorkspaceID = thread.WorkspaceID
		if err := r.Context().Err(); err != nil {
			return err
		}
		return tx.SaveSourceImport(imported)
	})
	if errors.Is(err, w.ErrConflict) {
		writeError(rw, r, 409, "source_limit", "每个会话最多保留 100 份上传资料，请删除未使用的导入")
		return
	}
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	writeJSON(rw, 201, publicSourceImport(imported))
}

func (s *Server) sourceImports(rw http.ResponseWriter, r *http.Request) {
	var result SourceImportList
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		thread, err := tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		result.Items, err = tx.SourceImportMetadata(thread.ID)
		return err
	})
	respond(rw, r, result, err)
}

func (s *Server) sourceOriginal(rw http.ResponseWriter, r *http.Request) {
	var name, mediaType, encoded string
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		imported, err := tx.SourceImport(r.PathValue("id"))
		if err == nil {
			if _, err := tx.Thread(imported.ThreadID); err != nil {
				return err
			}
			name, mediaType, encoded = imported.FileName, imported.MediaType, imported.OriginalContent
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		source, err := tx.Source(r.PathValue("id"))
		if err != nil {
			return err
		}
		if _, err := tx.Thread(source.ThreadID); err != nil {
			return err
		}
		if source.OriginalContent == "" {
			return &RequestError{404, "original_unavailable", "该资料没有上传原件"}
		}
		name, mediaType, encoded = source.Title, source.MediaType, source.OriginalContent
		return nil
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(content) == 0 {
		respond(rw, r, nil, &RequestError{404, "original_unavailable", "资料原件不可用"})
		return
	}
	if name == "" {
		name = "source"
	}
	switch mediaType {
	case "text/csv", "text/tab-separated-values", "application/pdf",
		tools.DocxMediaType, tools.XlsxMediaType, tools.PptxMediaType,
		"application/msword", "application/vnd.ms-excel", "application/vnd.ms-powerpoint",
		"application/rtf", "application/vnd.oasis.opendocument.text", "application/xml", "application/webarchive":
	default:
		mediaType = "application/octet-stream"
	}
	rw.Header().Set("Content-Type", mediaType)
	rw.Header().Set("Content-Length", strconv.Itoa(len(content)))
	rw.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Cache-Control", "private, no-store")
	rw.WriteHeader(200)
	_, _ = rw.Write(content)
}

func (s *Server) deleteSourceImport(rw http.ResponseWriter, r *http.Request) {
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		imported, err := tx.SourceImport(r.PathValue("id"))
		if err != nil {
			return err
		}
		if _, err := tx.Thread(imported.ThreadID); err != nil {
			return err
		}
		if _, err := tx.Source(imported.ID); err == nil {
			return &RequestError{409, "source_in_use", "该资料已进入任务证据链，不能删除；历史原件和解析记录会继续保留"}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return tx.DeleteSourceImport(imported.ID)
	})
	respond(rw, r, DeleteReply{ID: r.PathValue("id")}, err)
}

func validateSourceSelection(ids []string) error {
	if len(ids) > 5 {
		return fmt.Errorf("每次任务最多选择 5 份上传资料")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return fmt.Errorf("上传资料引用不能为空或重复")
		}
		seen[id] = true
	}
	return nil
}
