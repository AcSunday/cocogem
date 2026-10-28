package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestMemberMigrationPreservesDistinctIdentitiesAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE agent_profiles(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),handle TEXT NOT NULL,revision INTEGER NOT NULL,body TEXT NOT NULL,UNIQUE(workspace_id,handle))"); err != nil {
		t.Fatal(err)
	}
	agents := []w.Agent{
		{ID: "old-a", WorkspaceID: "one", DisplayName: "同名成员", Handle: "shared", Aliases: []string{"旧称"}, Model: "default", Tools: []string{"read_file"}, Revision: 3},
		{ID: "old-b", WorkspaceID: "two", DisplayName: "同名成员", Handle: "shared", Model: "default", Disabled: true, Tools: []string{"web_search"}, Revision: 2},
	}
	for _, agent := range agents {
		ws := w.Workspace{ID: agent.WorkspaceID, Name: agent.WorkspaceID, Revision: 1}
		if _, err := db.Exec("INSERT INTO workspaces(id,revision,body) VALUES(?,?,?)", ws.ID, ws.Revision, body(ws)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO agent_profiles(id,workspace_id,handle,revision,body) VALUES(?,?,?,?,?)", agent.ID, agent.WorkspaceID, agent.Handle, agent.Revision, body(agent)); err != nil {
			t.Fatal(err)
		}
	}
	th := w.Thread{ID: "history-thread", WorkspaceID: "one", Revision: 1, UpdatedAt: w.Now(), AgentIDs: []string{"old-a"}}
	run := w.Run{ID: "history-run", WorkspaceID: "one", ThreadID: th.ID, Agents: agents[:1], Status: "succeeded", CreatedAt: w.Now()}
	if _, err := db.Exec("INSERT INTO threads(id,workspace_id,updated_at,revision,body) VALUES(?,?,?,?,?)", th.ID, th.WorkspaceID, th.UpdatedAt, th.Revision, body(th)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO runs(id,workspace_id,thread_id,status,idempotency_key,request_hash,created_at,body) VALUES(?,?,?,?,?,?,?,?)", run.ID, run.WorkspaceID, run.ThreadID, run.Status, "key", "hash", run.CreatedAt, body(run)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Read(t.Context(), func(tx *Tx) error {
			globals, err := tx.GlobalAgents()
			if err != nil || len(globals) != 2 || globals[0].ID != "old-a" || globals[1].ID != "old-b" {
				t.Fatalf("reopen %d merged legacy identities: %+v, %v", reopen, globals, err)
			}
			for _, old := range agents {
				got, err := tx.WorkspaceAgent(old.WorkspaceID, old.ID)
				if err != nil || got.ID != old.ID || got.Revision != old.Revision || got.Disabled != old.Disabled || !reflect.DeepEqual(got.Tools, old.Tools) || !reflect.DeepEqual(got.Aliases, old.Aliases) {
					t.Fatalf("legacy member changed: %+v, %v", got, err)
				}
			}
			got, err := tx.Run(run.ID)
			if err != nil || !reflect.DeepEqual(got.Agents, run.Agents) {
				t.Fatalf("history rewritten: %+v, %v", got, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGlobalMemberSharingAndWorkspaceRevocation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	agent := w.Agent{ID: "member", DisplayName: "成员", Handle: "member", Model: "default", Tools: []string{"read_file", "web_search"}, Revision: 1}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		for _, id := range []string{"one", "two"} {
			if err := tx.SaveWorkspace(w.Workspace{ID: id, Name: id, Revision: 1}); err != nil {
				return err
			}
		}
		if err := tx.SaveAgent(agent); err != nil {
			return err
		}
		if err := tx.SaveMember(w.WorkspaceMember{WorkspaceID: "one", AgentID: agent.ID, Enabled: true, Tools: []string{"read_file"}, Revision: 1}); err != nil {
			return err
		}
		return tx.SaveMember(w.WorkspaceMember{WorkspaceID: "two", AgentID: agent.ID, Enabled: true, Tools: []string{"web_search", "fetch_url"}, Revision: 1})
	}); err != nil {
		t.Fatal(err)
	}
	assertMember := func(workspace string, tools []string, disabled bool) {
		t.Helper()
		if err := s.Read(t.Context(), func(tx *Tx) error {
			got, err := tx.WorkspaceAgent(workspace, agent.ID)
			if err != nil || got.ID != agent.ID || got.DisplayName != agent.DisplayName || got.Disabled != disabled || !reflect.DeepEqual(got.Tools, tools) {
				t.Fatalf("unexpected effective member: %+v, %v", got, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertMember("one", []string{"read_file"}, false)
	assertMember("two", []string{"web_search"}, false)
	agent.DisplayName, agent.Tools, agent.Revision = "新名称", []string{"read_file"}, 2
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveAgent(agent) }); err != nil {
		t.Fatal(err)
	}
	assertMember("one", []string{"read_file"}, false)
	assertMember("two", []string{}, false)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		member, err := tx.Member("one", agent.ID)
		if err != nil {
			return err
		}
		member.Enabled, member.Revision = false, 2
		return tx.SaveMember(member)
	}); err != nil {
		t.Fatal(err)
	}
	assertMember("one", []string{"read_file"}, true)
	assertMember("two", []string{}, false)
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.DeleteWorkspace("one") }); err != nil {
		t.Fatal(err)
	}
	assertMember("two", []string{}, false)
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.DeleteMember("two", agent.ID) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(context.Background(), func(tx *Tx) error {
		if _, err := tx.WorkspaceAgent("two", agent.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("revoked membership remains available", err)
		}
		_, err := tx.Agent(agent.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMemberNameConflictsAreScopedAndRollbackProfileEdit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := w.Agent{ID: "first", WorkspaceID: "one", Handle: "same", DisplayName: "一", Model: "default", Revision: 1}
	second := first
	second.ID, second.WorkspaceID = "second", "two"
	third := second
	third.ID, third.Handle, third.Aliases = "third", "third", []string{"distinct"}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		for _, id := range []string{"one", "two"} {
			if err := tx.SaveWorkspace(w.Workspace{ID: id, Revision: 1}); err != nil {
				return err
			}
		}
		for _, agent := range []w.Agent{first, second, third} {
			if err := tx.SaveAgent(agent); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		return tx.SaveMember(w.WorkspaceMember{WorkspaceID: "one", AgentID: second.ID, Enabled: true, Revision: 1})
	}); err == nil {
		t.Fatal("same handle admitted to one workspace")
	}
	third.Aliases, third.Revision = []string{"SAME"}, 2
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveAgent(third) }); err == nil {
		t.Fatal("alias conflicts with existing workspace handle")
	}
	if err := s.Read(t.Context(), func(tx *Tx) error {
		got, err := tx.Agent(third.ID)
		if got.Revision != 1 || !reflect.DeepEqual(got.Aliases, []string{"distinct"}) {
			b, _ := json.Marshal(got)
			t.Fatalf("failed edit escaped rollback: %s", b)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
