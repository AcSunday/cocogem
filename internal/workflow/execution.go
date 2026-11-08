package workflow

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// prepareExecution performs filesystem work after the SQLite capacity admission.
// Legacy/unit engines without a Manager retain their direct-root behavior and
// are not evidence that production isolation was verified.
func (e *Engine) prepareExecution(ctx context.Context, run w.Run) (w.Run, error) {
	if e.Executions == nil || run.TaskID == "" {
		return run, nil
	}
	dir, err := e.Executions.TaskDirectory(run.TaskID)
	if err != nil {
		return run, err
	}
	prepared := run.TaskDirectory != ""
	run.TaskDirectory = dir
	baseline := run.OutputBaseline
	if !prepared {
		baseline = map[string]string{}
		entries, err := os.ReadDir(filepath.Join(dir, "outputs"))
		if err != nil {
			return run, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			digest, err := tools.OutputFingerprint(ctx, dir, filepath.Join("outputs", entry.Name()))
			if err != nil {
				return run, err
			}
			baseline[entry.Name()] = digest
		}
	}
	var sources []w.Source
	if err := e.Store.Read(ctx, func(tx *store.Tx) error { var err error; sources, err = tx.Sources(run.ThreadID); return err }); err != nil {
		return run, err
	}
	for _, source := range sources {
		if source.OriginalContent == "" || !slices.Contains(run.Input.SourceIDs, source.ID) {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(source.OriginalContent)
		if err != nil {
			return run, fmt.Errorf("decode original input: %w", err)
		}
		name := source.ID + "-" + filepath.Base(source.Title)
		if !filepath.IsLocal(name) {
			return run, fmt.Errorf("invalid input filename")
		}
		inputDir := filepath.Join(dir, "inputs")
		if err := os.MkdirAll(inputDir, 0700); err != nil {
			return run, err
		}
		if info, err := os.Lstat(inputDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return run, errors.New("task input directory is not a regular managed directory")
		}
		if _, err := os.Lstat(filepath.Join(inputDir, name)); err == nil {
			existing, err := tools.ReadBinary(ctx, inputDir, name, tools.MaxTaskFileBytes)
			if err != nil {
				return run, err
			}
			if contentDigest(string(existing)) != contentDigest(string(data)) {
				return run, errors.New("task input copy changed; original must be restored before continuation")
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return run, err
		}
		root, err := os.OpenRoot(inputDir)
		if err != nil {
			return run, err
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0400)
		if err == nil {
			_, err = file.Write(data)
			err = errors.Join(err, file.Close())
		}
		root.Close()
		if err != nil {
			return run, err
		}
	}
	if err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.TaskDirectory = dir
		current.OutputBaseline = baseline
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		return run, err
	}
	if !run.WriteBranch {
		return run, nil
	}
	candidate, err := e.Executions.Prepare(ctx, run)
	if err != nil {
		return run, err
	}
	run.ExecutionRef = candidate.ID
	run.ExecutionRoot = candidate.Path
	if err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return w.ErrConflict
		}
		current.ExecutionRef = candidate.ID
		current.ExecutionRoot = candidate.Path
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		if finishErr := e.Executions.Finish(context.WithoutCancel(ctx), candidate.ID); finishErr != nil {
			e.report(finishErr)
		}
		return run, err
	}
	return run, nil
}
