// Package skills provides the shared, read-only collaboration instructions.
package skills

import (
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The "all:" prefix is required because the cross-skill reference layer lives
// under "_shared", and a plain embed pattern skips leading-underscore files.
//
//go:embed all:library
var embedded embed.FS

// Info is the discovery metadata; Version hashes the entrypoint, its references
// and the shared reference layer.
type Info struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category,omitempty"`
	Version     string `json:"version"`
	// Project marks a skill discovered read-only from the bound project's
	// .agents/skills directory instead of the built-in library.
	Project bool `json:"project,omitempty"`
}

// ErrNotFound indicates an unknown skill or a resource outside its allowlist.
var ErrNotFound = errors.New("skill or resource not found")

const (
	// librarySize is the fixed number of built-in engineering skills.
	librarySize = 10
	// maxDescription is the bounded description budget per skill. Ten entries at
	// this bound keep the bootstrap index within the documented token budget.
	maxDescription = 240
	// maxBodyLines bounds one SKILL.md body so an entrypoint stays a decision
	// guide rather than a copy of the whole method.
	maxBodyLines = 90
	// maxProjectSkills bounds read-only project discovery.
	maxProjectSkills = 8
	// sharedID is the cross-skill reference layer. It is readable but never an
	// enableable skill.
	sharedID = "_shared"
)

// requiredDescriptionMarkers force every description to state its trigger, its
// non-goal and its output, so the bootstrap index alone is actionable.
var requiredDescriptionMarkers = []string{"Use when", "Not for", "Output"}

type library struct {
	bootstrap string
	infos     []Info
	resources map[string]map[string]string
	// sharedResources is the cross-skill reference layer, keyed by
	// "references/<name>.md".
	sharedResources map[string]string
}

var shared = loadEmbedded()

func loadEmbedded() *library {
	lib, err := load(embedded)
	if err != nil {
		panic("invalid embedded skill library: " + err.Error())
	}
	return lib
}

func load(source fs.FS) (*library, error) {
	bootstrap, err := fs.ReadFile(source, "library/BOOTSTRAP.md")
	if err != nil {
		return nil, fmt.Errorf("read bootstrap: %w", err)
	}
	lib := &library{
		bootstrap: string(bootstrap), infos: []Info{},
		resources:       map[string]map[string]string{},
		sharedResources: map[string]string{},
	}
	// The shared layer is cross-skill. Its content is folded into every skill
	// version so any shared change invalidates the run snapshot.
	sharedHash := sha256.New()
	if err := fs.WalkDir(source, "library/"+sharedID, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		resource := strings.TrimPrefix(name, "library/"+sharedID+"/")
		if !strings.HasPrefix(resource, "references/") || !strings.HasSuffix(resource, ".md") {
			return fmt.Errorf("unsupported shared resource %q", resource)
		}
		content, readErr := fs.ReadFile(source, name)
		if readErr != nil {
			return readErr
		}
		lib.sharedResources[resource] = string(content)
		fmt.Fprintf(sharedHash, "%s\x00%s\x00", resource, content)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read shared references: %w", err)
	}
	sharedDigest := fmt.Sprintf("%x", sharedHash.Sum(nil))

	entries, err := fs.ReadDir(source, "library")
	if err != nil {
		return nil, fmt.Errorf("read library: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == sharedID {
			continue
		}
		id := entry.Name()
		if !validID(id) {
			return nil, fmt.Errorf("invalid skill ID %q", id)
		}
		resources := map[string]string{}
		hash := sha256.New()
		fmt.Fprintf(hash, "%s\x00", sharedDigest)
		root := "library/" + id
		err := fs.WalkDir(source, root, func(name string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			resource := strings.TrimPrefix(name, root+"/")
			if resource != "SKILL.md" && !(strings.HasPrefix(resource, "references/") && strings.HasSuffix(resource, ".md")) {
				return fmt.Errorf("unsupported skill resource %q", resource)
			}
			content, readErr := fs.ReadFile(source, name)
			if readErr != nil {
				return readErr
			}
			resources[resource] = string(content)
			fmt.Fprintf(hash, "%s\x00%s\x00", resource, content)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read skill %q: %w", id, err)
		}
		info, err := parseInfo(id, resources["SKILL.md"])
		if err != nil {
			return nil, err
		}
		info.Version = fmt.Sprintf("%x", hash.Sum(nil))
		lib.infos = append(lib.infos, info)
		lib.resources[id] = resources
	}
	if len(lib.infos) != librarySize {
		return nil, fmt.Errorf("the built-in library must contain exactly %d skills, found %d", librarySize, len(lib.infos))
	}
	return lib, nil
}

// parseInfo reads the restricted YAML frontmatter. It supports plain scalars and
// folded/literal multi-line blocks for `description`, and rejects unknown keys so
// a skill cannot smuggle executable configuration through metadata.
func parseInfo(id, content string) (Info, error) {
	return parseInfoWithMarkers(id, content, true)
}

func parseInfoWithMarkers(id, content string, requireMarkers bool) (Info, error) {
	info := Info{ID: id}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return info, fmt.Errorf("skill %q requires frontmatter", id)
	}
	frontmatter, body, ok := strings.Cut(content[4:], "\n---\n")
	if !ok || strings.TrimSpace(body) == "" {
		return info, fmt.Errorf("skill %q requires instructions", id)
	}
	if lines := strings.Count(strings.TrimRight(body, "\n"), "\n") + 1; lines > maxBodyLines {
		return info, fmt.Errorf("skill %q body is %d lines, over the %d-line budget", id, lines, maxBodyLines)
	}
	fields := map[string]string{}
	lines := strings.Split(frontmatter, "\n")
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !found || key == "" {
			return info, fmt.Errorf("skill %q has invalid frontmatter", id)
		}
		if key != "name" && key != "description" && key != "category" {
			return info, fmt.Errorf("skill %q has an unknown frontmatter field %q", id, key)
		}
		if _, exists := fields[key]; exists {
			return info, fmt.Errorf("skill %q repeats the frontmatter field %q", id, key)
		}
		switch value {
		case ">", "|", ">-", "|-":
			// A folded or literal block: every following indented (or blank
			// separator) line belongs to this value.
			block := []string{}
			for index+1 < len(lines) {
				next := lines[index+1]
				if strings.HasPrefix(next, " ") || strings.HasPrefix(next, "\t") {
					index++
					block = append(block, strings.TrimSpace(next))
					continue
				}
				if strings.TrimSpace(next) == "" && index+2 < len(lines) && (strings.HasPrefix(lines[index+2], " ") || strings.HasPrefix(lines[index+2], "\t")) {
					index++
					block = append(block, "")
					continue
				}
				break
			}
			separator := " "
			if strings.HasPrefix(value, "|") {
				separator = "\n"
			}
			value = strings.TrimSpace(strings.Join(block, separator))
		default:
			if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
				value = strings.TrimSpace(value[1 : len(value)-1])
			}
		}
		fields[key] = value
	}
	if fields["name"] != id {
		return info, fmt.Errorf("skill %q must declare name: %s", id, id)
	}
	description := strings.TrimSpace(fields["description"])
	if description == "" {
		return info, fmt.Errorf("skill %q requires a description", id)
	}
	if len([]rune(description)) > maxDescription {
		return info, fmt.Errorf("skill %q description is %d characters, over the %d-character budget", id, len([]rune(description)), maxDescription)
	}
	if requireMarkers {
		for _, marker := range requiredDescriptionMarkers {
			if !strings.Contains(description, marker) {
				return info, fmt.Errorf("skill %q description must contain %q", id, marker)
			}
		}
	}
	info.Name, info.Description, info.Category = fields["name"], description, fields["category"]
	return info, nil
}

func validID(id string) bool {
	if id == "" || len(id) > 64 || id[0] == '-' || id[len(id)-1] == '-' || strings.Contains(id, "--") {
		return false
	}
	for _, c := range id {
		if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// List returns a stable, independent copy of the built-in skill index.
func List() []Info { return slices.Clone(shared.infos) }

// SharedSkillID is the read-only pseudoskill exposing the cross-skill reference
// layer. It is never an enableable skill.
func SharedSkillID() string { return sharedID }

// SharedReferences lists the cross-skill reference resources, sorted.
func SharedReferences() []string {
	out := make([]string, 0, len(shared.sharedResources))
	for name := range shared.sharedResources {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Read returns only a bundled Markdown resource. Empty resource selects SKILL.md.
// The pseudoskill "_shared" exposes the cross-skill reference layer.
func Read(id, resource string) (string, error) {
	if resource == "" {
		resource = "SKILL.md"
	}
	if !fs.ValidPath(resource) || strings.Contains(resource, "\\") {
		return "", ErrNotFound
	}
	if id == sharedID {
		content, ok := shared.sharedResources[resource]
		if !ok {
			return "", ErrNotFound
		}
		return content, nil
	}
	if !validID(id) {
		return "", ErrNotFound
	}
	content, ok := shared.resources[id][resource]
	if !ok {
		return "", ErrNotFound
	}
	return content, nil
}

func (lib *library) selectInfos(enabledIDs []string) ([]Info, error) {
	if enabledIDs == nil {
		return lib.infos, nil
	}
	for _, id := range enabledIDs {
		if _, ok := lib.resources[id]; !ok {
			return nil, fmt.Errorf("unknown skill %q", id)
		}
	}
	selected := []Info{}
	for _, info := range lib.infos {
		if slices.Contains(enabledIDs, info.ID) {
			selected = append(selected, info)
		}
	}
	return selected, nil
}

// Catalogue returns the shared bootstrap and metadata, without full skill bodies.
// Nil enables all bundled skills; a non-nil empty slice disables all skills.
func Catalogue(enabledIDs []string) (string, error) {
	return shared.catalogue(enabledIDs, nil)
}

func (lib *library) catalogue(enabledIDs []string, project []Info) (string, error) {
	infos, err := lib.selectInfos(enabledIDs)
	if err != nil || len(infos) == 0 && len(project) == 0 {
		return "", err
	}
	var out strings.Builder
	out.WriteString(lib.bootstrap)
	out.WriteString("\n## 本轮启用的技能\n\n")
	for _, info := range infos {
		fmt.Fprintf(&out, "- `%s` — %s（版本 `%s`）\n", info.ID, info.Description, info.Version)
	}
	for _, info := range project {
		fmt.Fprintf(&out, "- `%s` — %s（项目技能，版本 `%s`）\n", info.ID, info.Description, info.Version)
	}
	if len(lib.sharedResources) > 0 {
		out.WriteString("\n## 共享参考\n\n")
		out.WriteString("跨技能方法通过 `read_skill` 读取 `skill_id=\"" + sharedID + "\"`，例如 `references/claim-grounding.md`。\n")
	}
	return out.String(), nil
}

// SnapshotHash identifies the bootstrap and exact enabled skill versions.
func SnapshotHash(enabledIDs []string) (string, error) { return shared.snapshotHash(enabledIDs, nil) }

func (lib *library) snapshotHash(enabledIDs []string, project []Info) (string, error) {
	infos, err := lib.selectInfos(enabledIDs)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	if len(infos) > 0 || len(project) > 0 {
		fmt.Fprintf(hash, "%s\x00", lib.bootstrap)
	}
	for _, info := range infos {
		fmt.Fprintf(hash, "%s\x00%s\x00", info.ID, info.Version)
	}
	for _, info := range project {
		fmt.Fprintf(hash, "project:%s\x00%s\x00", info.ID, info.Version)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// projectSkills discovers read-only project skills from
// <root>/.agents/skills/<id>/SKILL.md. It accepts at most maxProjectSkills
// entries, never follows a symlink, never reads a sensitive path and never
// shadows a built-in skill. A missing directory is not an error.
func projectSkills(root string) ([]Info, map[string]string, error) {
	infos := []Info{}
	resources := map[string]string{}
	if root == "" {
		return infos, resources, nil
	}
	// Check BOTH .agents and .agents/skills with Lstat so a symlinked .agents
	// parent directory cannot escape the bound project root.
	agentsDir := filepath.Join(root, ".agents")
	agentsInfo, err := os.Lstat(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return infos, resources, nil
		}
		return infos, resources, nil
	}
	if agentsInfo.Mode()&os.ModeSymlink != 0 || !agentsInfo.IsDir() {
		return infos, resources, fmt.Errorf("project .agents path is not a real directory")
	}
	dir := filepath.Join(agentsDir, "skills")
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return infos, resources, nil
		}
		return infos, resources, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return infos, resources, fmt.Errorf("project skills path is not a real directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return infos, resources, nil
	}
	ids := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		if _, builtin := shared.resources[entry.Name()]; builtin {
			// A project skill may never override or shadow a built-in skill.
			continue
		}
		ids = append(ids, entry.Name())
	}
	slices.Sort(ids)
	for _, id := range ids {
		if len(infos) >= maxProjectSkills {
			break
		}
		skillDir := filepath.Join(dir, id)
		if entryInfo, err := os.Lstat(skillDir); err != nil || entryInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(skillDir, "SKILL.md")
		fileInfo, err := os.Lstat(path)
		if err != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
			continue
		}
		if fileInfo.Size() > 128<<10 {
			continue
		}
		// A sensitive name anywhere in the relative path is refused, so a
		// project skill cannot read a credential file through discovery.
		relative, err := filepath.Rel(root, path)
		if err != nil || sensitivePath(relative) {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		parsed, err := parseProjectInfo(id, string(content))
		if err != nil {
			continue
		}
		parsed.Version = contentDigest(string(content))
		parsed.Project = true
		infos = append(infos, parsed)
		resources[id] = string(content)
	}
	return infos, resources, nil
}

// parseProjectInfo applies the same bounded metadata rules and unknown-key
// checks to a project skill without requiring the built-in wording markers.
func parseProjectInfo(id, content string) (Info, error) {
	return parseInfoWithMarkers(id, content, false)
}

// sensitivePath mirrors the host file-tool deny policy for discovery: a project
// skill path must not name a credential or private directory component.
func sensitivePath(relative string) bool {
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == ".cocogem" || lower == ".ssh" || lower == "secrets" {
			return true
		}
		for _, prefix := range []string{".env", "id_rsa", ".git-credentials"} {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
		for _, suffix := range []string{".pem", ".key", ".sqlite"} {
			if strings.HasSuffix(lower, suffix) {
				return true
			}
		}
	}
	return false
}

func contentDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

// ListFor returns the built-in index plus the bound project's read-only skills.
func ListFor(root string) []Info {
	project, _, err := projectSkills(root)
	if err != nil {
		return slices.Clone(shared.infos)
	}
	combined := slices.Clone(shared.infos)
	return append(combined, project...)
}

// ReadFor reads one skill or resource as visible to the bound project.
func ReadFor(root, id, resource string) (string, error) {
	if id == sharedID {
		return Read(id, resource)
	}
	if _, builtin := shared.resources[id]; !builtin {
		_, resources, err := projectSkills(root)
		if err != nil {
			return "", ErrNotFound
		}
		content, ok := resources[id]
		if !ok || (resource != "" && resource != "SKILL.md") {
			return "", ErrNotFound
		}
		return content, nil
	}
	return Read(id, resource)
}

// CatalogueFor includes the bound project's discovered skills in the catalogue.
func CatalogueFor(root string, enabledIDs []string) (string, error) {
	project, _, err := projectSkills(root)
	if err != nil {
		project = nil
	}
	// Enabled IDs that name a project skill are validated against discovery,
	// never against the built-in index.
	builtinEnabled := []string{}
	for _, id := range enabledIDs {
		if _, builtin := shared.resources[id]; builtin {
			builtinEnabled = append(builtinEnabled, id)
		} else if !slices.ContainsFunc(project, func(candidate Info) bool { return candidate.ID == id }) {
			return "", fmt.Errorf("unknown skill %q", id)
		}
	}
	if enabledIDs == nil {
		builtinEnabled = nil
	}
	selectedProject := project
	if enabledIDs != nil {
		selectedProject = []Info{}
		for _, info := range project {
			if slices.Contains(enabledIDs, info.ID) {
				selectedProject = append(selectedProject, info)
			}
		}
	}
	return shared.catalogue(builtinEnabled, selectedProject)
}

// SnapshotHashFor folds project skills into the run snapshot.
func SnapshotHashFor(root string, enabledIDs []string) (string, error) {
	project, _, err := projectSkills(root)
	if err != nil {
		// A rejected project skills path (for example a symlinked .agents)
		// means "no project skills", exactly as ListFor and CatalogueFor treat
		// it. Failing here would block every run in that workspace. An enabled
		// project skill that can no longer be found still fails below.
		project = nil
	}
	if enabledIDs == nil {
		return shared.snapshotHash(nil, project)
	}
	selectedProject := []Info{}
	for _, id := range enabledIDs {
		if _, builtin := shared.resources[id]; builtin {
			continue
		}
		found := slices.ContainsFunc(project, func(candidate Info) bool { return candidate.ID == id })
		if !found {
			return "", fmt.Errorf("unknown skill %q", id)
		}
		for _, info := range project {
			if info.ID == id {
				selectedProject = append(selectedProject, info)
			}
		}
	}
	builtinEnabled := []string{}
	for _, id := range enabledIDs {
		if _, builtin := shared.resources[id]; builtin {
			builtinEnabled = append(builtinEnabled, id)
		}
	}
	return shared.snapshotHash(builtinEnabled, selectedProject)
}
