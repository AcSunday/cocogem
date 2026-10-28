package skills

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoad(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		edit func(fstest.MapFS)
	}{
		{name: "missing bootstrap", edit: func(files fstest.MapFS) { delete(files, "library/BOOTSTRAP.md") }},
		{name: "missing entrypoint", edit: func(files fstest.MapFS) { delete(files, "library/task-handoff/SKILL.md") }},
		{name: "executable resource", edit: func(files fstest.MapFS) {
			files["library/task-handoff/scripts/send.sh"] = &fstest.MapFile{Data: []byte("echo send")}
		}},
		{name: "invalid skill ID", edit: func(files fstest.MapFS) {
			files["library/INVALID/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: INVALID\ndescription: Invalid\n---\nBody")}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files := bundledFiles(t)
			tt.edit(files)
			if _, err := load(files); err == nil {
				t.Fatal("invalid library was accepted")
			}
		})
	}
}

func TestParseInfo(t *testing.T) {
	t.Parallel()
	info, err := parseInfo("test-skill", "---\nname: test-skill\ndescription: Useful task: bounded scope\n---\nRead the evidence.\n")
	if err != nil || info.ID != "test-skill" || info.Name != "test-skill" || info.Description != "Useful task: bounded scope" {
		t.Fatalf("parse metadata = %+v, %v", info, err)
	}
	for _, tt := range []struct {
		name    string
		content string
	}{
		{name: "no frontmatter", content: "Read the evidence."},
		{name: "no instructions", content: "---\nname: test-skill\ndescription: Useful task\n---\n"},
		{name: "wrong name", content: "---\nname: other-skill\ndescription: Useful task\n---\nBody"},
		{name: "missing description", content: "---\nname: test-skill\n---\nBody"},
		{name: "duplicate field", content: "---\nname: test-skill\nname: test-skill\ndescription: Useful task\n---\nBody"},
		{name: "unknown field", content: "---\nname: test-skill\ndescription: Useful task\ncommands: true\n---\nBody"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseInfo("test-skill", tt.content); err == nil {
				t.Fatal("invalid skill metadata was accepted")
			}
		})
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	infos := List()
	ids := []string{}
	for _, info := range infos {
		ids = append(ids, info.ID)
		if info.Name != info.ID || info.Description == "" || len(info.Version) != 64 {
			t.Fatalf("incomplete skill metadata: %+v", info)
		}
	}
	want := []string{"independent-review", "synthesize-results", "task-handoff", "task-orchestration"}
	if !slices.Equal(ids, want) {
		t.Fatalf("skill IDs = %v, want %v", ids, want)
	}
	infos[0].Name = "mutated"
	if List()[0].Name == "mutated" {
		t.Fatal("List exposed the shared index")
	}
}

func TestRead(t *testing.T) {
	t.Parallel()
	implicit, err := Read("task-handoff", "")
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Read("task-handoff", "SKILL.md")
	if err != nil || implicit != explicit || !strings.Contains(explicit, "handoff_task") {
		t.Fatalf("entrypoint read = %q, %v", explicit, err)
	}
	ref, err := Read("task-handoff", "references/contract.md")
	if err != nil || !strings.Contains(ref, "target_agent_id") || !strings.Contains(ref, "continuation_id") {
		t.Fatalf("reference read = %q, %v", ref, err)
	}
	for _, tt := range []struct {
		name     string
		id       string
		resource string
	}{
		{name: "unknown skill", id: "unknown", resource: "SKILL.md"},
		{name: "skill escape", id: "../task-handoff", resource: "SKILL.md"},
		{name: "resource escape", id: "task-handoff", resource: "../BOOTSTRAP.md"},
		{name: "cross-skill escape", id: "task-handoff", resource: "../independent-review/SKILL.md"},
		{name: "absolute path", id: "task-handoff", resource: "/etc/passwd"},
		{name: "windows separator", id: "task-handoff", resource: "references\\contract.md"},
		{name: "encoded escape", id: "task-handoff", resource: "%2e%2e/BOOTSTRAP.md"},
		{name: "normalized path", id: "task-handoff", resource: "references/../SKILL.md"},
		{name: "source code", id: "task-handoff", resource: "../../library.go"},
		{name: "unknown reference", id: "task-handoff", resource: "references/missing.md"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if content, err := Read(tt.id, tt.resource); content != "" || !errors.Is(err, ErrNotFound) {
				t.Fatalf("Read(%q, %q) = %q, %v", tt.id, tt.resource, content, err)
			}
		})
	}
}

func TestCatalogue(t *testing.T) {
	t.Parallel()
	all, err := Catalogue(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range List() {
		if !strings.Contains(all, "- `"+info.ID+"`") || !strings.Contains(all, info.Version) {
			t.Fatalf("catalogue missing %s or its version", info.ID)
		}
	}
	body, _ := Read("task-handoff", "SKILL.md")
	if strings.Contains(all, body) {
		t.Fatal("catalogue eagerly included the skill body")
	}
	selected, err := Catalogue([]string{"task-handoff"})
	if err != nil || !strings.Contains(selected, "- `task-handoff`") || strings.Contains(selected, "- `independent-review`") {
		t.Fatalf("selected catalogue = %q, %v", selected, err)
	}
	if disabled, err := Catalogue([]string{}); err != nil || disabled != "" {
		t.Fatalf("disabled catalogue = %q, %v", disabled, err)
	}
	if _, err := Catalogue([]string{"unknown"}); err == nil {
		t.Fatal("unknown configured skill was silently ignored")
	}
}

func TestSnapshotHash(t *testing.T) {
	t.Parallel()
	all, err := SnapshotHash(nil)
	if err != nil || len(all) != 64 {
		t.Fatalf("snapshot = %q, %v", all, err)
	}
	a, _ := SnapshotHash([]string{"task-handoff", "independent-review"})
	b, _ := SnapshotHash([]string{"independent-review", "task-handoff", "task-handoff"})
	if a != b {
		t.Fatal("enabled IDs ordering or duplicates changed the snapshot")
	}
	none, _ := SnapshotHash([]string{})
	if none == all {
		t.Fatal("disabling all skills did not change the snapshot")
	}
	if _, err := SnapshotHash([]string{"unknown"}); err == nil {
		t.Fatal("snapshot accepted an unknown skill")
	}
	for _, resource := range []string{"library/task-handoff/SKILL.md", "library/task-handoff/references/contract.md", "library/BOOTSTRAP.md"} {
		t.Run(resource, func(t *testing.T) {
			t.Parallel()
			files := bundledFiles(t)
			files[resource].Data = append(files[resource].Data, []byte("\nAdditional instruction.\n")...)
			changed, err := load(files)
			if err != nil {
				t.Fatal(err)
			}
			got, err := changed.snapshotHash(nil)
			if err != nil || got == all {
				t.Fatalf("content change did not change snapshot: %q, %v", got, err)
			}
			if resource != "library/BOOTSTRAP.md" {
				before, _ := shared.snapshotHash([]string{"independent-review"})
				after, _ := changed.snapshotHash([]string{"independent-review"})
				if before != after {
					t.Fatal("a disabled skill changed the enabled snapshot")
				}
				for i, info := range changed.infos {
					if info.ID == "task-handoff" && info.Version == shared.infos[i].Version {
						t.Fatal("skill version did not include resource content")
					}
				}
			}
		})
	}
}

func TestSharedInstructionsUseRuntimeMembers(t *testing.T) {
	t.Parallel()
	for name, file := range bundledFiles(t) {
		content := strings.ToLower(string(file.Data))
		for _, binding := range []string{"claude", "codex", "gemini", "布偶猫", "缅因猫", "橘猫"} {
			if strings.Contains(content, binding) {
				t.Errorf("%s contains a fixed member or CLI binding %q", name, binding)
			}
		}
	}
}

func bundledFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	if err := fs.WalkDir(embedded, "library", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(embedded, name)
		files[name] = &fstest.MapFile{Data: data}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
