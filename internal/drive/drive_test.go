package drive

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "files"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCleanRejectsTraversal(t *testing.T) {
	for _, p := range []string{"..", "a/../b", "../etc", "a\\b", "a\x00b", "/../x"} {
		if _, err := Clean(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Clean(%q) = %v, want ErrInvalidPath", p, err)
		}
	}
	for in, want := range map[string]string{"": "", "/": "", "/a//b/": "a/b", "a/./b": "a/b"} {
		if got, err := Clean(in); err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	s := newStore(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Root(), "link")); err != nil {
		t.Skip("symlinks unsupported")
	}
	if _, err := s.List("link"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("List through symlink: %v", err)
	}
	if _, _, err := s.Open("link/secret"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("Open through symlink: %v", err)
	}
	entries, _ := s.List("")
	if len(entries) != 0 {
		t.Errorf("symlink should be hidden, got %v", entries)
	}
}

func TestFileLifecycle(t *testing.T) {
	s := newStore(t)
	if _, err := s.Mkdir("", "docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mkdir("", "docs"); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate mkdir: %v", err)
	}
	if _, err := s.Save("docs", "a.txt", strings.NewReader("hello"), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("docs", "a.txt", strings.NewReader("again"), 100); !errors.Is(err, ErrExists) {
		t.Errorf("overwrite must fail: %v", err)
	}
	if _, err := s.Save("docs", "big.bin", strings.NewReader(strings.Repeat("x", 101)), 100); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if _, err := s.Stat("docs/big.bin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("oversized upload must not remain: %v", err)
	}
	if _, err := s.Save("docs", "../evil", strings.NewReader("x"), 0); !errors.Is(err, ErrInvalidName) {
		t.Errorf("bad name: %v", err)
	}

	e, err := s.Rename("docs/a.txt", "b.txt")
	if err != nil || e.Path != "docs/b.txt" {
		t.Fatalf("rename: %v %v", e, err)
	}
	if _, err := s.Mkdir("", "other"); err != nil {
		t.Fatal(err)
	}
	if e, err = s.Move("docs/b.txt", "other"); err != nil || e.Path != "other/b.txt" {
		t.Fatalf("move: %v %v", e, err)
	}
	if e, err = s.Copy("other/b.txt", "other"); err != nil || e.Name != "b (copy).txt" {
		t.Fatalf("copy: %v %v", e, err)
	}
	if _, err := s.Move("docs", "docs"); !errors.Is(err, ErrConflict) {
		t.Errorf("move into itself: %v", err)
	}
	if _, err := s.Copy("other", "other"); !errors.Is(err, ErrConflict) {
		t.Errorf("copy folder into itself: %v", err)
	}
	f, _, err := s.Open("other/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	list, _ := s.List("other")
	if len(list) != 2 {
		t.Errorf("list: %v", list)
	}
	if err := s.Delete(""); !errors.Is(err, ErrConflict) {
		t.Errorf("deleting root: %v", err)
	}
	if err := s.Delete("other"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat("other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func TestErrorsDoNotLeakServerPaths(t *testing.T) {
	s := newStore(t)
	_, err := s.Stat("missing/x")
	if err == nil || strings.Contains(err.Error(), s.Root()) {
		t.Errorf("error leaks path: %v", err)
	}
}
