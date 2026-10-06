// Package drive implements the private file workspace. All paths handled by
// this package are "virtual": slash-separated, relative to the workspace root.
// Server filesystem paths never leave this package.
package drive

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Errors returned by Store. They intentionally carry no server paths.
var (
	ErrInvalidPath = errors.New("invalid path")
	ErrInvalidName = errors.New("invalid name")
	ErrNotFound    = errors.New("not found")
	ErrExists      = errors.New("already exists")
	ErrNotDir      = errors.New("not a directory")
	ErrIsDir       = errors.New("is a directory")
	ErrTooLarge    = errors.New("file too large")
	ErrConflict    = errors.New("invalid operation")
	ErrUnsupported = errors.New("unsupported file type")
)

// Entry describes one file or folder.
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// Store is a workspace rooted at a directory on the server.
type Store struct {
	root string
}

// New creates the root directory if needed and returns a Store.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &Store{root: real}, nil
}

// Root returns the real on-disk root. It is for the torrent engine only and
// must never be sent to clients.
func (s *Store) Root() string { return s.root }

// Clean normalises a virtual path. "" and "/" are the root (returned as "").
func Clean(p string) (string, error) {
	if strings.ContainsAny(p, "\x00\\") {
		return "", ErrInvalidPath
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", ErrInvalidPath
		}
	}
	c := path.Clean("/" + p)
	if c == "/" {
		return "", nil
	}
	return c[1:], nil
}

// ValidName reports whether name is a legal single path component.
func ValidName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 255 ||
		strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, ".magnetor-") {
		return ErrInvalidName
	}
	return nil
}

// resolve maps a virtual path to a real path, rejecting any symlink on the way.
func (s *Store) resolve(virt string) (string, string, error) {
	clean, err := Clean(virt)
	if err != nil {
		return "", "", err
	}
	real := s.root
	if clean == "" {
		return real, clean, nil
	}
	for _, seg := range strings.Split(clean, "/") {
		if err := ValidName(seg); err != nil {
			return "", "", ErrInvalidPath
		}
		real = filepath.Join(real, seg)
		fi, err := os.Lstat(real)
		if errors.Is(err, fs.ErrNotExist) {
			continue // remaining segments cannot exist either
		}
		if err != nil {
			return "", "", mapErr(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", "", ErrInvalidPath
		}
	}
	return real, clean, nil
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, fs.ErrExist):
		return ErrExists
	}
	return fmt.Errorf("storage error: %s", errKind(err))
}

// errKind strips the path out of an os error.
func errKind(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Op + ": " + le.Err.Error()
	}
	return "io error"
}

func (s *Store) existing(virt string) (real, clean string, fi os.FileInfo, err error) {
	real, clean, err = s.resolve(virt)
	if err != nil {
		return
	}
	fi, err = os.Lstat(real)
	err = mapErr(err)
	if err == nil && !fi.IsDir() && !fi.Mode().IsRegular() {
		err = ErrUnsupported
	}
	return
}

func entryFor(clean string, fi os.FileInfo) Entry {
	return Entry{Name: fi.Name(), Path: clean, IsDir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()}
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// Stat returns information about a path.
func (s *Store) Stat(virt string) (Entry, error) {
	_, clean, fi, err := s.existing(virt)
	if err != nil {
		return Entry{}, err
	}
	e := entryFor(clean, fi)
	if clean == "" {
		e.Name = ""
	}
	return e, nil
}

// List returns the entries of a folder, folders first.
func (s *Store) List(virt string) ([]Entry, error) {
	real, clean, fi, err := s.existing(virt)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, ErrNotDir
	}
	des, err := os.ReadDir(real)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".magnetor-") {
			continue
		}
		info, err := de.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue // skip symlinks and special files
		}
		out = append(out, entryFor(join(clean, de.Name()), info))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Mkdir creates a folder named name inside parent.
func (s *Store) Mkdir(parent, name string) (Entry, error) {
	if err := ValidName(name); err != nil {
		return Entry{}, err
	}
	real, clean, fi, err := s.existing(parent)
	if err != nil {
		return Entry{}, err
	}
	if !fi.IsDir() {
		return Entry{}, ErrNotDir
	}
	target := filepath.Join(real, name)
	if err := os.Mkdir(target, 0o750); err != nil {
		return Entry{}, mapErr(err)
	}
	return s.Stat(join(clean, name))
}

// Save streams r into a new file inside dir. maxBytes<=0 means unlimited.
// An existing file is never overwritten.
func (s *Store) Save(dir, name string, r io.Reader, maxBytes int64) (Entry, error) {
	if err := ValidName(name); err != nil {
		return Entry{}, err
	}
	real, clean, fi, err := s.existing(dir)
	if err != nil {
		return Entry{}, err
	}
	if !fi.IsDir() {
		return Entry{}, ErrNotDir
	}
	target := filepath.Join(real, name)
	if _, err := os.Lstat(target); err == nil {
		return Entry{}, ErrExists
	}
	tmp, err := os.CreateTemp(real, ".magnetor-upload-*")
	if err != nil {
		return Entry{}, mapErr(err)
	}
	defer os.Remove(tmp.Name())
	src := r
	if maxBytes > 0 {
		src = io.LimitReader(r, maxBytes+1)
	}
	n, err := io.Copy(tmp, src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Entry{}, mapErr(err)
	}
	if maxBytes > 0 && n > maxBytes {
		return Entry{}, ErrTooLarge
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return Entry{}, mapErr(err)
	}
	// Link+remove (via deferred Remove) fails if target appeared meanwhile.
	if err := os.Link(tmp.Name(), target); err != nil {
		return Entry{}, mapErr(err)
	}
	return s.Stat(join(clean, name))
}

// Open opens a regular file for reading.
func (s *Store) Open(virt string) (*os.File, Entry, error) {
	real, clean, fi, err := s.existing(virt)
	if err != nil {
		return nil, Entry{}, err
	}
	if fi.IsDir() {
		return nil, Entry{}, ErrIsDir
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, Entry{}, mapErr(err)
	}
	return f, entryFor(clean, fi), nil
}

// Rename changes the name of an item within its folder.
func (s *Store) Rename(virt, newName string) (Entry, error) {
	if err := ValidName(newName); err != nil {
		return Entry{}, err
	}
	real, clean, _, err := s.existing(virt)
	if err != nil {
		return Entry{}, err
	}
	if clean == "" {
		return Entry{}, ErrConflict
	}
	dest := filepath.Join(filepath.Dir(real), newName)
	if err := noOverwrite(dest); err != nil {
		return Entry{}, err
	}
	if err := os.Rename(real, dest); err != nil {
		return Entry{}, mapErr(err)
	}
	parent := path.Dir(clean)
	if parent == "." {
		parent = ""
	}
	return s.Stat(join(parent, newName))
}

func noOverwrite(real string) error {
	if _, err := os.Lstat(real); err == nil {
		return ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapErr(err)
	}
	return nil
}

func isWithin(p, dir string) bool {
	return dir == "" || p == dir || strings.HasPrefix(p, dir+"/")
}

// Move relocates an item into destDir, keeping its name.
func (s *Store) Move(virt, destDir string) (Entry, error) {
	real, clean, _, err := s.existing(virt)
	if err != nil {
		return Entry{}, err
	}
	dreal, dclean, dfi, err := s.existing(destDir)
	if err != nil {
		return Entry{}, err
	}
	if clean == "" || !dfi.IsDir() {
		return Entry{}, ErrConflict
	}
	if isWithin(dclean, clean) {
		return Entry{}, ErrConflict // into itself
	}
	name := path.Base(clean)
	target := filepath.Join(dreal, name)
	if err := noOverwrite(target); err != nil {
		return Entry{}, err
	}
	if err := os.Rename(real, target); err != nil {
		return Entry{}, mapErr(err)
	}
	return s.Stat(join(dclean, name))
}

// Copy duplicates an item into destDir. If the name is taken, a " (copy)"
// suffix is added. Symlinks and special files inside folders are skipped.
func (s *Store) Copy(virt, destDir string) (Entry, error) {
	real, clean, fi, err := s.existing(virt)
	if err != nil {
		return Entry{}, err
	}
	dreal, dclean, dfi, err := s.existing(destDir)
	if err != nil {
		return Entry{}, err
	}
	if clean == "" || !dfi.IsDir() {
		return Entry{}, ErrConflict
	}
	if fi.IsDir() && isWithin(dclean, clean) {
		return Entry{}, ErrConflict
	}
	name, err := uniqueName(dreal, path.Base(clean))
	if err != nil {
		return Entry{}, err
	}
	if err := copyTree(real, filepath.Join(dreal, name), fi); err != nil {
		_ = os.RemoveAll(filepath.Join(dreal, name))
		return Entry{}, mapErr(err)
	}
	return s.Stat(join(dclean, name))
}

func uniqueName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	cand := name
	for i := 1; i < 10000; i++ {
		if _, err := os.Lstat(filepath.Join(dir, cand)); errors.Is(err, fs.ErrNotExist) {
			return cand, nil
		} else if err != nil {
			return "", mapErr(err)
		}
		if i == 1 {
			cand = fmt.Sprintf("%s (copy)%s", base, ext)
		} else {
			cand = fmt.Sprintf("%s (copy %d)%s", base, i, ext)
		}
	}
	return "", ErrExists
}

func copyTree(src, dst string, fi os.FileInfo) error {
	if fi.IsDir() {
		if err := os.Mkdir(dst, 0o750); err != nil {
			return err
		}
		des, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, de := range des {
			info, err := de.Info()
			if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
				continue
			}
			if err := copyTree(filepath.Join(src, de.Name()), filepath.Join(dst, de.Name()), info); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Delete removes a file or folder (recursively). The root cannot be deleted.
func (s *Store) Delete(virt string) error {
	real, clean, _, err := s.existing(virt)
	if err != nil {
		return err
	}
	if clean == "" {
		return ErrConflict
	}
	return mapErr(os.RemoveAll(real))
}

// SourceDir returns the real parent directory and entry name for a virtual
// path, for the torrent engine (which seeds in place).
func (s *Store) SourceDir(virt string) (parentReal, name string, e Entry, err error) {
	real, clean, fi, err := s.existing(virt)
	if err != nil {
		return "", "", Entry{}, err
	}
	if clean == "" {
		return "", "", Entry{}, ErrConflict
	}
	return filepath.Dir(real), filepath.Base(real), entryFor(clean, fi), nil
}
