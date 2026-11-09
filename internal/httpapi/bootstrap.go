package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// BootstrapInput creates only the project and conversation needed by the first
// accepted input. A blank WorkspaceID starts an unnamed, directory-free project.
type BootstrapInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Title       string `json:"title"`
	Workflow    string `json:"workflow"`
}

// BootstrapReply is reconstructed from durable identities on replay.
type BootstrapReply struct {
	Workspace w.Workspace `json:"workspace"`
	Thread    w.Thread    `json:"thread"`
	Agents    []w.Agent   `json:"agents"`
}

func (s *Server) bootstrap(rw http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 16 || len(key) > 100 {
		respond(rw, r, nil, &RequestError{Status: http.StatusBadRequest, Code: "invalid_request", Message: "首次启动需要 16–100 字符的幂等键"})
		return
	}
	var in BootstrapInput
	if !decode(rw, r, &in) {
		return
	}
	if err := validateThreadTitle(in.Title); err != nil {
		respond(rw, r, nil, err)
		return
	}
	if in.Workflow == "" {
		in.Workflow = "free"
	}
	digest := project.Digest(in)
	var out BootstrapReply
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		previous, previousDigest, err := tx.BootstrapReceipt(key)
		if err == nil {
			if previousDigest != digest {
				return &RequestError{Status: http.StatusConflict, Code: "conflict", Message: "相同首次启动键不能用于不同请求"}
			}
			out.Workspace, err = tx.Workspace(previous.WorkspaceID)
			if err != nil {
				return err
			}
			out.Thread, err = tx.Thread(previous.ThreadID)
			if err != nil {
				return err
			}
			out.Agents, err = tx.Agents(out.Workspace.ID)
			return err
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		workspaceID := strings.TrimSpace(in.WorkspaceID)
		if workspaceID == "" {
			out.Workspace = w.Workspace{ID: w.NewID(), Name: "未命名项目", Root: "", Revision: 1, CreatedAt: w.Now(), ExecutionMode: w.ExecutionControlled}
			if err := tx.SaveWorkspace(out.Workspace); err != nil {
				return err
			}
		} else {
			out.Workspace, err = tx.Workspace(workspaceID)
			if err != nil {
				return err
			}
		}
		if err := s.initializeMembers(tx, out.Workspace.ID); err != nil {
			return err
		}
		out.Agents, err = tx.Agents(out.Workspace.ID)
		if err != nil {
			return err
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		out.Thread = w.Thread{ID: w.NewID(), WorkspaceID: out.Workspace.ID, Title: strings.TrimSpace(in.Title), Workflow: in.Workflow, Revision: 1, UpdatedAt: w.Now()}
		applyThreadDefaults(&out.Thread, out.Agents, settings)
		if err := validateThread(out.Thread, out.Agents); err != nil {
			return err
		}
		if err := tx.SaveThread(out.Thread); err != nil {
			return err
		}
		return tx.SaveBootstrapReceipt(key, digest, out.Workspace.ID, out.Thread.ID)
	})
	respond(rw, r, out, err)
}
