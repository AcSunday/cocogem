package httpapi

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/tools"
)

// Directory is a selectable local folder, without file contents or metadata.
type Directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// DirectoryListing contains immediate child folders and an allowed parent.
// An empty path is the virtual list of explicitly allowed roots.
type DirectoryListing struct {
	Path      string      `json:"path"`
	Parent    string      `json:"parent"`
	Items     []Directory `json:"items"`
	Truncated bool        `json:"truncated"`
}

func (s *Server) directories(rw http.ResponseWriter, r *http.Request) {
	listing, err := browseDirectories(r.Context(), r.URL.Query().Get("path"), s.config.Roots)
	respond(rw, r, listing, err)
}

func browseDirectories(ctx context.Context, path string, allowed []string) (DirectoryListing, error) {
	listing := DirectoryListing{Items: []Directory{}}
	if strings.TrimSpace(path) == "" {
		if len(allowed) > 0 {
			for _, limit := range allowed {
				root, err := tools.ResolveSourceRoot(limit, allowed)
				if err == nil && root != "" {
					listing.Items = append(listing.Items, Directory{Name: root, Path: root})
				}
			}
			if len(listing.Items) == 0 {
				return listing, errors.New("没有可访问的允许目录")
			}
			return listing, nil
		}
		var err error
		path, err = os.UserHomeDir()
		if err != nil {
			return listing, errors.New("无法确定本机用户目录，请手动输入目录路径")
		}
	}
	root, err := tools.ResolveSourceRoot(path, allowed)
	if err != nil {
		return listing, err
	}
	listing.Path = root
	parent := filepath.Dir(root)
	if parent != root {
		if resolved, err := tools.ResolveSourceRoot(parent, allowed); err == nil {
			listing.Parent = resolved
		}
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		return listing, errors.New("目录不可读，请检查本机访问权限")
	}
	defer directory.Close()
	file, err := directory.Open(".")
	if err != nil {
		return listing, errors.New("目录不可读，请检查本机访问权限")
	}
	defer file.Close()
	// shortcut: scan at most 1000 entries; add paging if large folders need full browsing.
	entries, err := file.ReadDir(1001)
	if err != nil && !errors.Is(err, io.EOF) {
		return listing, errors.New("读取目录失败，请检查本机访问权限")
	}
	if len(entries) > 1000 {
		listing.Truncated = true
		entries = entries[:1000]
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return listing, err
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || tools.DeniedPath(entry.Name()) {
			continue
		}
		child := filepath.Join(root, entry.Name())
		if resolved, err := tools.ResolveSourceRoot(child, allowed); err == nil && resolved == child {
			listing.Items = append(listing.Items, Directory{Name: entry.Name(), Path: child})
		}
	}
	slices.SortFunc(listing.Items, func(a, b Directory) int { return cmp.Compare(a.Name, b.Name) })
	return listing, nil
}
