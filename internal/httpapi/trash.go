package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// errRunActive keeps deletion from racing a queued or running collaboration.
var errRunActive = &RequestError{409, "run_active", "有协作正在进行，请先停止再删除"}

// errOwnedProcess keeps a recorded child's ownership evidence until it is settled.
var errOwnedProcess = &RequestError{409, "owned_process_alive", "本平台记录的 CLI 进程可能仍在运行，请先继续或核实该任务再删除"}

func (s *Server) ownedProcessAlive(r *http.Request, threadID string) (bool, error) {
	if s.engine == nil {
		return false, nil
	}
	return s.engine.OwnedProcessAlive(r.Context(), threadID)
}

func (s *Server) deleteThread(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if live, liveErr := s.ownedProcessAlive(r, id); liveErr != nil {
		respond(rw, r, nil, liveErr)
		return
	} else if live {
		respond(rw, r, nil, errOwnedProcess)
		return
	}
	var purgeAt string
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		thread, err := tx.ThreadAny(id)
		if err != nil {
			return err
		}
		if thread.DeletedAt != "" {
			return store.ErrNotFound
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		if slices.ContainsFunc(active, func(run w.Run) bool { return run.ThreadID == id }) {
			return errRunActive
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		thread.DeletedAt = now.Format(time.RFC3339Nano)
		thread.PurgeAt = now.AddDate(0, 0, w.EffectiveRetentionDays(settings.RetentionDays)).Format(time.RFC3339Nano)
		thread.Revision++
		// Moving a conversation to the trash ends its execution authorization but
		// keeps unapplied candidate worktrees and the IO area frozen on disk, so a
		// restore preserves the user's unfinished code without re-granting
		// authority: conversation grants are revoked and pending requests are
		// canceled, and only a permanent delete/purge reclaims the directories.
		if err := tx.RevokeConversationGrantsForThread(id, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if err := tx.CancelThreadPendingRequests(id, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		purgeAt = thread.PurgeAt
		return tx.SaveThread(thread)
	})
	if err == nil {
	}
	respond(rw, r, DeleteReply{ID: id, PurgeAt: purgeAt}, err)
}

func (s *Server) trash(rw http.ResponseWriter, r *http.Request) {
	var items []TrashItem
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		threads, err := tx.DeletedThreads()
		if err != nil {
			return err
		}
		workspaces, err := tx.Workspaces()
		if err != nil {
			return err
		}
		names := make(map[string]string, len(workspaces))
		for _, workspace := range workspaces {
			names[workspace.ID] = workspace.Name
		}
		items = make([]TrashItem, 0, len(threads))
		for _, thread := range threads {
			items = append(items, TrashItem{Thread: thread, WorkspaceName: names[thread.WorkspaceID]})
		}
		return nil
	})
	respond(rw, r, TrashList{Items: items}, err)
}

func (s *Server) restoreThread(rw http.ResponseWriter, r *http.Request) {
	var restored w.Thread
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		thread, err := tx.ThreadAny(r.PathValue("id"))
		if err != nil {
			return err
		}
		if thread.DeletedAt == "" {
			return store.ErrNotFound
		}
		if err := revision(r, thread.Revision); err != nil {
			return err
		}
		if _, err := tx.Workspace(thread.WorkspaceID); err != nil {
			return err
		}
		thread.DeletedAt, thread.PurgeAt = "", ""
		thread.Revision++
		thread.UpdatedAt = w.Now()
		restored = thread
		return tx.SaveThread(thread)
	})
	respond(rw, r, restored, err)
}

func (s *Server) permanentDeleteThread(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var (
		candidates []w.WorkspaceExecution
		ioScopes   []string
		roots      []string
	)
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		thread, err := tx.ThreadAny(id)
		if err != nil {
			return err
		}
		if thread.DeletedAt == "" {
			return store.ErrNotFound
		}
		if err := revision(r, thread.Revision); err != nil {
			return err
		}
		// Capture candidates, IO scopes and repo roots before deleting rows;
		// after tx.DeleteThread commits, the database no longer names them.
		candidates, ioScopes, roots, err = captureThreadCleanup(tx, thread)
		if err != nil {
			return err
		}
		return tx.DeleteThread(id)
	})
	if err == nil && s.engine != nil {
		// Permanent deletion reclaims the candidate worktrees and the IO area;
		// a soft delete keeps them so a restore preserves unapplied work.
		if releaseErr := s.engine.ReleaseDeletedTaskExecutions(context.WithoutCancel(r.Context()), candidates, ioScopes, roots); releaseErr != nil {
			slog.Error("release thread executions failed", "thread", id, "error", releaseErr)
		}
	}
	respond(rw, r, DeleteReply{ID: id}, err)
}

func captureThreadCleanup(tx *store.Tx, thread w.Thread) ([]w.WorkspaceExecution, []string, []string, error) {
	items, err := tx.ThreadExecutions(thread.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	seenExec := make(map[string]bool, len(items))
	for _, item := range items {
		seenExec[item.ID] = true
	}
	ioScopes := []string{thread.ID}
	if thread.TaskID != "" {
		ioScopes = append(ioScopes, thread.TaskID)
		taskItems, err := tx.ScopeExecutions(thread.TaskID)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, item := range taskItems {
			if !seenExec[item.ID] {
				seenExec[item.ID] = true
				items = append(items, item)
			}
		}
	}
	seenRoot := map[string]bool{}
	roots := []string{}
	addRoot := func(root string) {
		if root != "" && !seenRoot[root] {
			seenRoot[root] = true
			roots = append(roots, root)
		}
	}
	for _, item := range items {
		addRoot(item.CanonicalRoot)
	}
	if thread.WorkspaceID != "" {
		if ws, err := tx.Workspace(thread.WorkspaceID); err == nil {
			addRoot(ws.Root)
		}
	}
	return items, ioScopes, roots, nil
}

// PurgeExpiredThreads performs the retention cleanup in one bounded write. It
// runs at startup and from the maintenance ticker, not as a side effect of a
// list request.
func (s *Server) PurgeExpiredThreads(ctx context.Context) (int, error) {
	// Existing trash entries follow the current retention even if the setting
	// changed in an earlier release; this is idempotent after the first run.
	if err := s.store.Write(ctx, func(tx *store.Tx) error {
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		return project.RescheduleTrashInTx(tx, settings.RetentionDays)
	}); err != nil {
		return 0, err
	}
	// Candidate expiry is read before the owned-process check; the final write
	// must not trust it because a restore or a second delete may have committed
	// a fresh retention window in between.
	cutoff := time.Now().UTC()
	// Owned child processes are checked outside the serialized store
	// transactions; the final transaction rechecks active runs and visibility.
	var expired []w.Thread
	if err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		expired, err = tx.ExpiredThreads(w.Now())
		return err
	}); err != nil {
		return 0, err
	}
	candidates := make([]w.Thread, 0, len(expired))
	for _, thread := range expired {
		if s.engine != nil {
			live, err := s.engine.OwnedProcessAlive(ctx, thread.ID)
			if err != nil {
				return 0, err
			}
			if live {
				continue
			}
		}
		candidates = append(candidates, thread)
	}
	type purgedCleanup struct {
		threadID   string
		candidates []w.WorkspaceExecution
		ioScopes   []string
		roots      []string
	}
	purged := []purgedCleanup{}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		active, err := tx.Active()
		if err != nil {
			return err
		}
		activeThreads := map[string]bool{}
		for _, run := range active {
			activeThreads[run.ThreadID] = true
		}
		for _, candidate := range candidates {
			if activeThreads[candidate.ID] {
				continue
			}
			thread, err := tx.ThreadAny(candidate.ID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return err
			}
			if thread.DeletedAt == "" || thread.PurgeAt == "" {
				continue
			}
			purgeAt, err := time.Parse(time.RFC3339Nano, thread.PurgeAt)
			if err != nil || purgeAt.After(cutoff) {
				// The scanned candidate was restored and deleted again, so the
				// current trash entry still has time left; never purge it early.
				continue
			}
			execs, ioScopes, roots, err := captureThreadCleanup(tx, thread)
			if err != nil {
				return err
			}
			if err := tx.DeleteThread(thread.ID); err != nil {
				return err
			}
			purged = append(purged, purgedCleanup{
				threadID:   thread.ID,
				candidates: execs,
				ioScopes:   ioScopes,
				roots:      roots,
			})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(purged) > 0 && s.engine != nil {
		// Retention purge is a permanent removal: reclaim candidate worktrees and
		// IO areas captured before deletion so nothing survives on disk or in git.
		for _, item := range purged {
			if err := s.engine.ReleaseDeletedTaskExecutions(ctx, item.candidates, item.ioScopes, item.roots); err != nil {
				slog.Error("release purged thread executions failed", "thread", item.threadID, "error", err)
			}
		}
	}
	return len(purged), nil
}

// StartMaintenance runs startup cleanup and a low-frequency bounded ticker.
// The returned wait function blocks until the maintenance goroutine has
// exited, so the shared database is not closed underneath a cleanup write.
func (s *Server) StartMaintenance(ctx context.Context) (wait func()) {
	purge := s.PurgeExpiredThreads
	if s.purgeExpired != nil {
		purge = s.purgeExpired
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := purge(ctx); err != nil && ctx.Err() == nil {
			slog.Error("trash cleanup failed", "error", err)
		}
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := purge(ctx); err != nil && ctx.Err() == nil {
					slog.Error("trash cleanup failed", "error", err)
				}
			}
		}
	}()
	return func() { <-done }
}
