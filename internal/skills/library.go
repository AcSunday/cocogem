// Package skills provides the shared, read-only collaboration instructions.
package skills

import (
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

//go:embed library
var embedded embed.FS

// Info is the discovery metadata; Version hashes the entrypoint and references.
type Info struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

// ErrNotFound indicates an unknown skill or a resource outside its allowlist.
var ErrNotFound = errors.New("skill or resource not found")

type library struct {
	bootstrap string
	infos     []Info
	resources map[string]map[string]string
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
	lib := &library{bootstrap: string(bootstrap), infos: []Info{}, resources: map[string]map[string]string{}}
	entries, err := fs.ReadDir(source, "library")
	if err != nil {
		return nil, fmt.Errorf("read library: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if !validID(id) {
			return nil, fmt.Errorf("invalid skill ID %q", id)
		}
		resources := map[string]string{}
		hash := sha256.New()
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
	return lib, nil
}

func parseInfo(id, content string) (Info, error) {
	info := Info{ID: id}
	if !strings.HasPrefix(content, "---\n") {
		return info, fmt.Errorf("skill %q requires frontmatter", id)
	}
	frontmatter, body, ok := strings.Cut(content[4:], "\n---\n")
	if !ok || strings.TrimSpace(body) == "" {
		return info, fmt.Errorf("skill %q requires instructions", id)
	}
	fields := map[string]string{}
	// shortcut: bundled frontmatter uses plain single-line scalars; add a YAML parser if external skill import is introduced.
	for line := range strings.SplitSeq(frontmatter, "\n") {
		key, value, found := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		if !found || value == "" || fields[key] != "" || key != "name" && key != "description" {
			return info, fmt.Errorf("skill %q has invalid frontmatter", id)
		}
		fields[key] = value
	}
	if fields["name"] != id || len(fields["description"]) == 0 || len(fields["description"]) > 1024 {
		return info, fmt.Errorf("skill %q has invalid metadata", id)
	}
	info.Name, info.Description = fields["name"], fields["description"]
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

// List returns a stable, independent copy of the shared skill index.
func List() []Info { return slices.Clone(shared.infos) }

// Read returns only a bundled Markdown resource. Empty resource selects SKILL.md.
// The caller must separately check that the skill is enabled for the current run.
func Read(id, resource string) (string, error) {
	if resource == "" {
		resource = "SKILL.md"
	}
	if !validID(id) || !fs.ValidPath(resource) || strings.Contains(resource, "\\") {
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
	infos, err := shared.selectInfos(enabledIDs)
	if err != nil || len(infos) == 0 {
		return "", err
	}
	var out strings.Builder
	out.WriteString(shared.bootstrap)
	out.WriteString("\n## 本轮启用的技能\n\n")
	for _, info := range infos {
		fmt.Fprintf(&out, "- `%s` — %s（版本 `%s`）\n", info.ID, info.Description, info.Version)
	}
	return out.String(), nil
}

// SnapshotHash identifies the bootstrap and exact enabled skill versions.
func SnapshotHash(enabledIDs []string) (string, error) { return shared.snapshotHash(enabledIDs) }

func (lib *library) snapshotHash(enabledIDs []string) (string, error) {
	infos, err := lib.selectInfos(enabledIDs)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	if len(infos) > 0 {
		fmt.Fprintf(hash, "%s\x00", lib.bootstrap)
	}
	for _, info := range infos {
		fmt.Fprintf(hash, "%s\x00%s\x00", info.ID, info.Version)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
