package vision

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const chatUploadsDirName = "chat_uploads"

var allowedImageExt = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".webp": {}, ".gif": {},
	".bmp": {}, ".tif": {}, ".tiff": {},
}

// PathOptions 图片路径白名单根目录。
type PathOptions struct {
	CWD              string
	ResultStorageDir string   // 相对 CWD，如 tmp
	ExtraRoots       []string // vision.allowed_roots 绝对路径
}

// ResolveImagePath 解析并校验可读图片路径（防穿越、symlink 逃逸）。
func ResolveImagePath(path string, opt PathOptions) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", fmt.Errorf("path is empty")
	}
	cwd := strings.TrimSpace(opt.CWD)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
	}
	cwdAbs, err := filepath.Abs(filepath.Clean(cwd))
	if err != nil {
		return "", err
	}

	var candidate string
	if filepath.IsAbs(p) {
		candidate = filepath.Clean(p)
	} else {
		candidate = filepath.Clean(filepath.Join(cwdAbs, p))
	}
	candidate = normalizeAbsPath(candidate)
	if candidate == "" {
		return "", fmt.Errorf("invalid path")
	}

	ext := strings.ToLower(filepath.Ext(candidate))
	if _, ok := allowedImageExt[ext]; !ok {
		return "", fmt.Errorf("unsupported image extension %q", ext)
	}

	roots := buildAllowedRoots(cwdAbs, opt)
	resolved, err := evalUnderAllowedRoots(candidate, roots)
	if err != nil {
		return "", err
	}

	st, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("not a regular file")
	}
	if st.Size() > 0 && st.Size() > 1<<30 {
		return "", fmt.Errorf("file too large on disk")
	}
	return resolved, nil
}

func normalizeAbsPath(p string) string {
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return ""
	}
	if link, err := filepath.EvalSymlinks(abs); err == nil {
		return link
	}
	return abs
}

func buildAllowedRoots(cwdAbs string, opt PathOptions) []string {
	seen := make(map[string]struct{})
	var roots []string
	add := func(r string) {
		r = strings.TrimSpace(r)
		if r == "" {
			return
		}
		abs := normalizeAbsPath(r)
		if abs == "" {
			return
		}
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		roots = append(roots, abs)
	}
	add(cwdAbs)
	add(filepath.Join(cwdAbs, chatUploadsDirName))
	rs := strings.TrimSpace(opt.ResultStorageDir)
	if rs == "" {
		rs = "tmp"
	}
	if filepath.IsAbs(rs) {
		add(rs)
	} else {
		add(filepath.Join(cwdAbs, rs))
	}
	for _, r := range opt.ExtraRoots {
		add(r)
	}
	return roots
}

func evalUnderAllowedRoots(candidate string, roots []string) (string, error) {
	check := normalizeAbsPath(candidate)
	for _, root := range roots {
		if isUnderRoot(check, root) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("path %q is outside allowed directories", candidate)
}

func isUnderRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, root+sep)
}
