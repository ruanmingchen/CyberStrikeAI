package vision

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveImagePath_underCWD(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveImagePath(img, PathOptions{CWD: dir, ResultStorageDir: "tmp"})
	if err != nil {
		t.Fatal(err)
	}
	want := normalizeAbsPath(img)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveImagePath_rejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveImagePath("../../../etc/passwd", PathOptions{CWD: dir})
	if err == nil {
		t.Fatal("expected error for path outside roots")
	}
}

func TestResolveImagePath_rejectsNonImageExt(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveImagePath(f, PathOptions{CWD: dir})
	if err == nil {
		t.Fatal("expected error for non-image extension")
	}
}
