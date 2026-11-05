package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

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
