package httpapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (s *Server) artifactFile(rw http.ResponseWriter, r *http.Request) {
	version, err := artifactVersion(r)
	var file w.ArtifactFile
	var source w.Source
	if err == nil {
		err = s.store.Read(r.Context(), func(tx *store.Tx) error {
			artifact, err := tx.Artifact(r.PathValue("id"), version)
			if err != nil {
				return err
			}
			if _, err := tx.Thread(artifact.ThreadID); err != nil {
				return err
			}
			run, err := tx.Run(artifact.RunID)
			if err != nil {
				return err
			}
			if !w.IsDeliveredArtifact(run, artifact) {
				return store.ErrNotFound
			}
			index := slices.IndexFunc(artifact.Files, func(f w.ArtifactFile) bool { return f.SourceID == r.PathValue("sourceID") })
			if index < 0 {
				return store.ErrNotFound
			}
			file = artifact.Files[index]
			source, err = tx.Source(file.SourceID)
			if err != nil {
				return err
			}
			if source.ThreadID != artifact.ThreadID || source.Kind != "file_output" || source.OriginalDigest != file.SHA256 {
				return store.ErrNotFound
			}
			return nil
		})
	}
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	data, err := base64.StdEncoding.DecodeString(source.OriginalContent)
	if err != nil || len(data) != file.Size || digestBytes(data) != file.SHA256 {
		respond(rw, r, nil, store.ErrNotFound)
		return
	}
	rw.Header().Set("Content-Type", file.MediaType)
	rw.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	rw.Header().Set("Content-Length", strconv.Itoa(len(data)))
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Cache-Control", "private, no-store")
	_, _ = rw.Write(data)
}

func (s *Server) source(rw http.ResponseWriter, r *http.Request) {
	var source w.Source
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		source, err = tx.Source(r.PathValue("id"))
		if err != nil {
			return err
		}
		_, err = tx.Thread(source.ThreadID)
		return err
	})
	respond(rw, r, source, err)
}

func (s *Server) artifacts(rw http.ResponseWriter, r *http.Request) {
	var artifacts []w.Artifact
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Thread(r.PathValue("id")); err != nil {
			return err
		}
		var err error
		artifacts, err = tx.Artifacts(r.PathValue("id"))
		if err != nil {
			return err
		}
		runs, err := tx.Runs(r.PathValue("id"))
		if err != nil {
			return err
		}
		byRun := make(map[string]w.Run, len(runs))
		for _, run := range runs {
			byRun[run.ID] = run
		}
		// Only the run's authoritative delivery is a formal artifact. Drafts
		// stay reachable through the exact id/version endpoint and run events.
		artifacts = slices.DeleteFunc(artifacts, func(artifact w.Artifact) bool {
			run, ok := byRun[artifact.RunID]
			return !ok || !w.IsDeliveredArtifact(run, artifact)
		})
		return nil
	})
	respond(rw, r, map[string]any{"items": artifacts}, err)
}

func artifactVersion(r *http.Request) (int, error) {
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || v < 1 {
		return 0, errors.New("invalid artifact version")
	}
	return v, nil
}

func (s *Server) artifact(rw http.ResponseWriter, r *http.Request) {
	v, err := artifactVersion(r)
	var a w.Artifact
	if err == nil {
		err = s.store.Read(r.Context(), func(tx *store.Tx) error {
			var err error
			a, err = tx.Artifact(r.PathValue("id"), v)
			if err != nil {
				return err
			}
			_, err = tx.Thread(a.ThreadID)
			return err
		})
	}
	respond(rw, r, a, err)
}

func (s *Server) feedback(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Feedback string `json:"feedback"`
	}
	if !decode(rw, r, &in) {
		return
	}
	v, err := artifactVersion(r)
	var a w.Artifact
	if err == nil {
		err = s.store.Write(r.Context(), func(tx *store.Tx) error {
			var err error
			a, err = tx.Artifact(r.PathValue("id"), v)
			if err != nil {
				return err
			}
			if _, err := tx.Thread(a.ThreadID); err != nil {
				return err
			}
			if err := revision(r, a.Revision); err != nil {
				return err
			}
			if !slices.Contains([]string{"unreviewed", "accepted", "dismissed"}, in.Feedback) {
				return errors.New("无效的人工标记")
			}
			a.Feedback = in.Feedback
			a.FeedbackAt = w.Now()
			a.Revision++
			return tx.SaveArtifact(a)
		})
	}
	respond(rw, r, a, err)
}

func (s *Server) export(rw http.ResponseWriter, r *http.Request) {
	v, err := artifactVersion(r)
	var a w.Artifact
	var sources []w.Source
	var run w.Run
	if err == nil {
		err = s.store.Read(r.Context(), func(tx *store.Tx) error {
			var err error
			a, err = tx.Artifact(r.PathValue("id"), v)
			if err != nil {
				return err
			}
			run, err = tx.Run(a.RunID)
			if err != nil {
				return err
			}
			if _, err := tx.Thread(a.ThreadID); err != nil {
				return err
			}
			sources, err = tx.SourceMetadata(a.ThreadID)
			return err
		})
	}
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	rw.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	rw.Header().Set("Content-Disposition", `attachment; filename="cocogem-report.md"`)
	fmt.Fprintf(rw, "# %s\n\nVersion: %d · %s · Feedback: %s\n\n", a.Title, a.Version, a.CreatedAt, a.Feedback)
	fmt.Fprintf(rw, "## 原始问题\n%s\n\n%s\n\n## 成员\n", run.Input.Message, a.Body)
	if len(a.Findings) > 0 {
		fmt.Fprint(rw, "\n## 结构化发现\n")
		for _, finding := range a.Findings {
			fmt.Fprintf(rw, "\n### %s\n状态：%s · 严重度：%s\n\n%s\n\n来源：%s\n", finding.Title, finding.Status, finding.Severity, finding.Detail, strings.Join(finding.SourceIDs, ", "))
		}
	}
	for _, agent := range run.Agents {
		fmt.Fprintf(rw, "- %s (@%s), model: %s\n", agent.DisplayName, agent.Handle, agent.Model)
	}
	fmt.Fprint(rw, "\n## 来源\n")
	for _, source := range sources {
		if slices.Contains(a.SourceIDs, source.ID) {
			fmt.Fprintf(rw, "- [%s] %s %s %s L%d-%d (%s)\n", source.ID, source.Title, source.URL, source.Path, source.StartLine, source.EndLine, source.CapturedAt)
		}
	}
	fmt.Fprint(rw, "\n## 限制\n")
	for _, limit := range a.Limitations {
		fmt.Fprintf(rw, "- %s\n", limit)
	}
}
