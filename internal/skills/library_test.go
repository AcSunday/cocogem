package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	const valid = "Use when a bounded scope is needed. Not for unbounded work. Output: the evidence."
	info, err := parseInfo("test-skill", "---\nname: test-skill\ndescription: "+valid+"\ncategory: engineering\n---\nRead the evidence.\n")
	if err != nil || info.ID != "test-skill" || info.Name != "test-skill" || info.Description != valid || info.Category != "engineering" {
		t.Fatalf("parse metadata = %+v, %v", info, err)
	}
	// A folded multi-line description is supported and normalised to one line.
	folded, err := parseInfo("test-skill", "---\nname: test-skill\ndescription: >\n  Use when a bounded scope is needed.\n  Not for unbounded work.\n  Output: the evidence.\n---\nRead the evidence.\n")
	if err != nil || folded.Description != valid {
		t.Fatalf("folded description = %q, %v", folded.Description, err)
	}
	for _, tt := range []struct {
		name    string
		content string
	}{
		{name: "no frontmatter", content: "Read the evidence."},
		{name: "no instructions", content: "---\nname: test-skill\ndescription: " + valid + "\n---\n"},
		{name: "wrong name", content: "---\nname: other-skill\ndescription: " + valid + "\n---\nBody"},
		{name: "missing description", content: "---\nname: test-skill\n---\nBody"},
		{name: "duplicate field", content: "---\nname: test-skill\nname: test-skill\ndescription: " + valid + "\n---\nBody"},
		{name: "unknown field", content: "---\nname: test-skill\ndescription: " + valid + "\ncommands: true\n---\nBody"},
		{name: "missing use-when", content: "---\nname: test-skill\ndescription: Not for work. Output: evidence.\n---\nBody"},
		{name: "description too long", content: "---\nname: test-skill\ndescription: Use when " + strings.Repeat("x", 240) + " Not for y Output: z\n---\nBody"},
		{name: "body too long", content: "---\nname: test-skill\ndescription: " + valid + "\n---\n" + strings.Repeat("line\n", 95)},
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
	want := []string{
		"debugging", "file-delivery", "independent-review", "quality-gate",
		"receive-review", "synthesize-results", "task-handoff", "task-orchestration",
		"tdd", "writing-plans",
	}
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
			got, err := changed.snapshotHash(nil, nil)
			if err != nil || got == all {
				t.Fatalf("content change did not change snapshot: %q, %v", got, err)
			}
			if resource != "library/BOOTSTRAP.md" {
				before, _ := shared.snapshotHash([]string{"independent-review"}, nil)
				after, _ := changed.snapshotHash([]string{"independent-review"}, nil)
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

// TestBuiltInLibraryHonoursTheFixedBudget is the A18 static gate: exactly ten
// built-in skills, each description carrying the trigger/non-goal/output markers
// within 240 characters, and each body within 90 lines.
func TestBuiltInLibraryHonoursTheFixedBudget(t *testing.T) {
	t.Parallel()
	want := []string{
		"debugging", "file-delivery", "independent-review", "quality-gate",
		"receive-review", "synthesize-results", "task-handoff", "task-orchestration",
		"tdd", "writing-plans",
	}
	infos := List()
	if len(infos) != librarySize {
		t.Fatalf("built-in skills = %d, want %d", len(infos), librarySize)
	}
	ids := []string{}
	for _, info := range infos {
		ids = append(ids, info.ID)
		if len([]rune(info.Description)) > maxDescription {
			t.Fatalf("%s description is over the budget", info.ID)
		}
		for _, marker := range requiredDescriptionMarkers {
			if !strings.Contains(info.Description, marker) {
				t.Fatalf("%s description lacks %q", info.ID, marker)
			}
		}
		body, err := Read(info.ID, "SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		_, instructions, ok := strings.Cut(body, "\n---\n")
		if !ok || instructions == "" {
			t.Fatalf("%s has no body", info.ID)
		}
		if lines := strings.Count(strings.TrimRight(instructions, "\n"), "\n") + 1; lines > maxBodyLines {
			t.Fatalf("%s body is %d lines, over %d", info.ID, lines, maxBodyLines)
		}
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("skill IDs = %v, want %v", ids, want)
	}
	// The bootstrap index must stay within the documented token budget.
	catalogue, err := Catalogue(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(catalogue)) > 1200*4 {
		t.Fatalf("catalogue is %d characters, over the bounded index", len([]rune(catalogue)))
	}
}

// TestSharedReferencesAreCrossSkillAndHashed is the cross-skill layer gate: a
// shared reference is readable under the _shared pseudoskill, is never an
// enableable skill, and any shared change invalidates the snapshot.
func TestSharedReferencesAreCrossSkillAndHashed(t *testing.T) {
	t.Parallel()
	refs := SharedReferences()
	if len(refs) == 0 {
		t.Fatal("the shared reference layer is empty")
	}
	for _, ref := range refs {
		content, err := Read(SharedSkillID(), ref)
		if err != nil || content == "" {
			t.Fatalf("shared reference %q = %q, %v", ref, content, err)
		}
	}
	// _shared is not an enableable skill.
	if _, err := Catalogue([]string{SharedSkillID()}); err == nil {
		t.Fatal("_shared was accepted as an enableable skill")
	}
	before, err := SnapshotHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	files := bundledFiles(t)
	for name := range files {
		if strings.HasPrefix(name, "library/_shared/") {
			files[name].Data = append(files[name].Data, []byte("\nshared change\n")...)
		}
	}
	changed, err := load(files)
	if err != nil {
		t.Fatal(err)
	}
	after, err := changed.snapshotHash(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("a shared reference change did not change the snapshot")
	}
}

// TestProjectSkillsAreReadOnlyBoundedAndNeverShadowBuiltIns is the A19 gate.
func TestProjectSkillsAreReadOnlyBoundedAndNeverShadowBuiltIns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".agents", "skills")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(id, description string) string {
		t.Helper()
		folder := filepath.Join(dir, id)
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + id + "\ndescription: " + description + "\n---\nFollow the project convention.\n"
		path := filepath.Join(folder, "SKILL.md")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("house-style", "项目约定的命名与提交规范")
	// A built-in name is never shadowed.
	shadow := write("tdd", "shadow the built-in")
	// A symlink out of the project is refused.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "hidden.md"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(dir, "escaped")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}

	infos, resources, err := projectSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	project := []Info{}
	for _, info := range infos {
		if info.Project {
			project = append(project, info)
		}
	}
	if len(project) != 1 || project[0].ID != "house-style" {
		t.Fatalf("project skills = %+v", project)
	}
	if _, ok := resources["tdd"]; ok {
		t.Fatalf("a built-in was shadowed: %v", resources)
	}
	if _, ok := resources["escaped"]; ok {
		t.Fatal("a symlinked project skill was discovered")
	}
	if _, err := ReadFor(root, "house-style", "SKILL.md"); err != nil {
		t.Fatalf("project skill read failed: %v", err)
	}
	if _, err := ReadFor(root, "house-style", "references/other.md"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a project skill exposed an unbounded resource")
	}
	// Project discovery is part of the snapshot and of the catalogue.
	withProject, err := SnapshotHashFor(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	without, err := SnapshotHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	if withProject == without {
		t.Fatal("a project skill did not change the snapshot")
	}
	catalogue, err := CatalogueFor(root, nil)
	if err != nil || !strings.Contains(catalogue, "house-style") {
		t.Fatalf("project catalogue = %q, %v", catalogue, err)
	}
	// A project skill only participates when the project enables it.
	if _, err := SnapshotHashFor(root, []string{"task-handoff"}); err != nil {
		t.Fatal(err)
	}
	// Editing the project skill changes the snapshot again.
	if err := os.WriteFile(write("house-style", "项目约定已更新"), []byte("---\nname: house-style\ndescription: 项目约定已更新\n---\n新的约定。\n"), 0600); err != nil {
		t.Fatal(err)
	}
	edited, err := SnapshotHashFor(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if edited == withProject {
		t.Fatal("editing a project skill did not change the snapshot")
	}
	_ = shadow
}

// TestProjectSkillsAreCappedAndIgnoreTraversal keeps discovery bounded and path-
// safe even for a hostile project directory.
func TestProjectSkillsAreCappedAndIgnoreTraversal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".agents", "skills")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for index := range maxProjectSkills + 3 {
		id := "project-skill-" + string(rune('a'+index))
		folder := filepath.Join(dir, id)
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("---\nname: "+id+"\ndescription: 项目技能\n---\nBody\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	infos, _, err := projectSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != maxProjectSkills {
		t.Fatalf("project skills = %d, want the %d cap", len(infos), maxProjectSkills)
	}
	// A .env file named as a skill id is refused by the sensitive-path rule.
	sensitive := filepath.Join(dir, ".env")
	if err := os.MkdirAll(sensitive, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sensitive, "SKILL.md"), []byte("---\nname: .env\ndescription: credential\n---\nBody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	infos, _, err = projectSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == ".env" {
			t.Fatal("a credential-named directory was discovered as a skill")
		}
	}
}

func TestBuiltInSkillMarkdownLinksResolveAndAgentsDirSymlinkRejected(t *testing.T) {
	t.Parallel()
	files := bundledFiles(t)
	for path, file := range files {
		if !strings.HasSuffix(path, ".md") {
			continue
		}
		text := string(file.Data)
		for idx := 0; idx < len(text); {
			open := strings.Index(text[idx:], "](")
			if open < 0 {
				break
			}
			start := idx + open + 2
			end := strings.Index(text[start:], ")")
			if end < 0 {
				break
			}
			link := strings.TrimSpace(text[start : start+end])
			idx = start + end + 1
			if link == "" || strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "#") {
				continue
			}
			if hash := strings.Index(link, "#"); hash >= 0 {
				link = link[:hash]
			}
			target := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(path), link)))
			if _, ok := files[target]; !ok {
				t.Errorf("%s has dead markdown link %q (resolved to %q)", path, link, target)
			}
		}
	}

	// Verify symlinked .agents parent directory is rejected.
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "skills", "escape"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "skills", "escape", "SKILL.md"), []byte("---\nname: escape\ndescription: escaped\n---\nBody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".agents")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	infos, _, err := projectSkills(root)
	if err == nil {
		t.Fatalf("symlinked .agents directory should return an error, got %+v", infos)
	}
}
