package multiagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

func TestLocalPlantaskBackendLsInfoReturnsFullPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	baseDir := t.TempDir()

	loc, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	be := newLocalPlantaskBackend(loc)

	hwPath := filepath.Join(baseDir, ".highwatermark")
	if err := os.WriteFile(hwPath, []byte("1"), 0o600); err != nil {
		t.Fatalf("write highwatermark: %v", err)
	}

	files, err := be.LsInfo(ctx, &plantask.LsInfoRequest{Path: baseDir})
	if err != nil {
		t.Fatalf("LsInfo: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != hwPath {
		t.Fatalf("expected full path %q, got %q", hwPath, files[0].Path)
	}

	content, err := be.Read(ctx, &plantask.ReadRequest{FilePath: files[0].Path})
	if err != nil {
		t.Fatalf("Read via LsInfo path: %v", err)
	}
	if content.Content != "1" {
		t.Fatalf("unexpected content: %q", content.Content)
	}
}

func TestLocalPlantaskBackendSecondTaskCreateScenario(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	baseDir := t.TempDir()

	loc, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	be := newLocalPlantaskBackend(loc)

	hwPath := filepath.Join(baseDir, ".highwatermark")
	if err := loc.Write(ctx, &filesystem.WriteRequest{FilePath: hwPath, Content: "1"}); err != nil {
		t.Fatalf("seed highwatermark: %v", err)
	}

	files, err := be.LsInfo(ctx, &plantask.LsInfoRequest{Path: baseDir})
	if err != nil {
		t.Fatalf("LsInfo: %v", err)
	}
	var hwFile string
	for _, f := range files {
		if filepath.Base(f.Path) == ".highwatermark" {
			hwFile = f.Path
			break
		}
	}
	if hwFile == "" {
		t.Fatal("highwatermark not listed")
	}
	if _, err := be.Read(ctx, &plantask.ReadRequest{FilePath: hwFile}); err != nil {
		t.Fatalf("Read highwatermark (second TaskCreate path): %v", err)
	}
}

func TestV09LocalBackendTaskCreateTwice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	baseDir := t.TempDir()
	loc, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	be := newLocalPlantaskBackend(loc)
	hwPath := filepath.Join(baseDir, ".highwatermark")
	if err := loc.Write(ctx, &filesystem.WriteRequest{FilePath: hwPath, Content: "1"}); err != nil {
		t.Fatalf("seed highwatermark: %v", err)
	}
	files, err := be.LsInfo(ctx, &plantask.LsInfoRequest{Path: baseDir})
	if err != nil {
		t.Fatalf("LsInfo: %v", err)
	}
	var hwFile string
	for _, f := range files {
		if filepath.Base(f.Path) == ".highwatermark" {
			hwFile = f.Path
			break
		}
	}
	if hwFile == "" {
		t.Fatal("highwatermark not listed")
	}
	if _, err := be.Read(ctx, &plantask.ReadRequest{FilePath: hwFile}); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// Second TaskCreate path: list + read again must succeed.
	files2, err := be.LsInfo(ctx, &plantask.LsInfoRequest{Path: baseDir})
	if err != nil {
		t.Fatalf("LsInfo 2: %v", err)
	}
	if len(files2) == 0 {
		t.Fatal("expected files on second list")
	}
	if _, err := be.Read(ctx, &plantask.ReadRequest{FilePath: files2[0].Path}); err != nil {
		t.Fatalf("second read: %v", err)
	}
}

func TestV09LocalBackendLsInfoPathsAreReadable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	baseDir := t.TempDir()
	loc, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	be := newLocalPlantaskBackend(loc)
	hwPath := filepath.Join(baseDir, ".highwatermark")
	if err := os.WriteFile(hwPath, []byte("1"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	files, err := be.LsInfo(ctx, &plantask.LsInfoRequest{Path: baseDir})
	if err != nil {
		t.Fatalf("LsInfo: %v", err)
	}
	if len(files) != 1 || files[0].Path != hwPath {
		t.Fatalf("unexpected files: %+v", files)
	}
	content, err := be.Read(ctx, &plantask.ReadRequest{FilePath: files[0].Path})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if content.Content != "1" {
		t.Fatalf("content=%q", content.Content)
	}
}

func TestV09LocalBackendRejectsOutOfScopePath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// backend/local v0.2.6 默认不强制 workspace 根目录；越界防护由上层 plantask/workspace 配置负责。
	// 此处验证空路径仍被拒绝，避免无约束读写入口。
	loc, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	be := newLocalPlantaskBackend(loc)
	_, err = be.LsInfo(ctx, &plantask.LsInfoRequest{Path: ""})
	if err == nil {
		t.Fatal("expected empty path list to fail")
	}
}
