package tools

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ResolveSourceRoot validates a user-selected directory against any explicit host limits.
// An empty host list lets the authenticated local user bind a directory in settings.
func ResolveSourceRoot(value string, allowed []string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 4096 || strings.ContainsRune(value, 0) {
		return "", errors.New("目录路径无效")
	}
	if value == "~" || strings.HasPrefix(value, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("无法确定本机用户目录，请填写完整路径")
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~"))
	}
	if !filepath.IsAbs(value) {
		return "", errors.New("请填写本机目录的完整绝对路径")
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil {
		return "", errors.New("目录不存在或无法访问")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", errors.New("目录路径无效")
	}
	if DeniedPath(root) {
		return "", errors.New("不能将凭证、应用数据或忽略目录绑定为资料目录")
	}
	if len(allowed) > 0 {
		inScope := false
		for _, limit := range allowed {
			canonical, err := filepath.EvalSymlinks(limit)
			if err != nil {
				continue
			}
			canonical, err = filepath.Abs(canonical)
			if err != nil {
				continue
			}
			relative, err := filepath.Rel(canonical, root)
			if err == nil && (relative == "." || filepath.IsLocal(relative)) {
				inScope = true
				break
			}
		}
		if !inScope {
			return "", errors.New("目录不在服务端允许范围内，请选择允许的目录或其子目录")
		}
	}
	directory, err := os.OpenFile(root, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("目录不可读，请检查本机访问权限")
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return "", errors.New("请选择文件夹，不能选择文件")
	}
	if _, err := directory.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("目录不可读，请检查本机访问权限")
	}
	return root, nil
}

// ValidateSourceRoot prevents a saved binding from silently following a changed symlink.
func ValidateSourceRoot(root string, allowed []string) error {
	resolved, err := ResolveSourceRoot(root, allowed)
	if err != nil {
		return err
	}
	if resolved != root {
		return errors.New("资料目录路径已改变，请在工作空间设置中重新保存")
	}
	return nil
}
