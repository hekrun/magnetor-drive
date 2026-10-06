package torrents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hekrun/magnetor-drive/internal/drive"
)

func setup(t *testing.T) (*drive.Store, *Manager, string) {
	t.Helper()
	base := t.TempDir()
	store, err := drive.New(filepath.Join(base, "files"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "torrents")
	m, err := New(store, Options{Dir: dir, Port: 0, DHT: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return store, m, dir
}

func waitState(t *testing.T, m *Manager, id, want string) Status {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.List() {
			if s.ID == id && s.State == want {
				return s
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("torrent never reached state %q: %+v", want, m.List())
	return Status{}
}

func TestCreateSeedStopInvalidate(t *testing.T) {
	store, m, dir := setup(t)
	if _, err := store.Mkdir("", "album"); err != nil {
		t.Fatal(err)
	}
	data := strings.Repeat("magnetor", 50_000)
	if _, err := store.Save("album", "a.bin", strings.NewReader(data), 0); err != nil {
		t.Fatal(err)
	}
	st, err := m.Create("album")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.Magnet, "magnet:?xt=urn:btih:"+st.ID) || st.Path != "album" || !st.IsDir {
		t.Errorf("unexpected status %+v", st)
	}
	if strings.Contains(st.Magnet, store.Root()) {
		t.Error("magnet leaks server path")
	}
	waitState(t, m, st.ID, StateSeeding)

	// No second copy: the private dir holds only the .torrent and records.
	var total int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		fi, _ := e.Info()
		total += fi.Size()
	}
	if total > int64(len(data))/2 {
		t.Errorf("torrent dir unexpectedly large (%d bytes); data may have been copied", total)
	}
	if p, _, err := m.File(st.ID); err != nil || filepath.Ext(p) != ".torrent" {
		t.Errorf("File: %v %v", p, err)
	}

	if s, err := m.Stop(st.ID); err != nil || s.State != StateStopped {
		t.Fatalf("stop: %+v %v", s, err)
	}
	if s, err := m.Start(st.ID); err != nil || (s.State != StateVerifying && s.State != StateSeeding) {
		t.Fatalf("start: %+v %v", s, err)
	}
	waitState(t, m, st.ID, StateSeeding)

	// Moving/editing the source invalidates the torrent.
	if _, err := store.Rename("album", "renamed"); err != nil {
		t.Fatal(err)
	}
	if n := m.Invalidate("album"); n != 1 {
		t.Fatalf("Invalidate = %d", n)
	}
	waitState(t, m, st.ID, StateInvalid)
	if _, err := m.Start(st.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("start invalid: %v", err)
	}

	// Regenerating from the new location works and replaces the old entry.
	st2, err := m.Create("renamed")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, st2.ID, StateSeeding)
	if err := m.Delete(st.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 1 {
		t.Errorf("expected only the regenerated torrent: %+v", m.List())
	}
	if err := m.Delete(st2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat("renamed/a.bin"); err != nil {
		t.Error("deleting a torrent must not delete source files")
	}
}

func TestChangedSourceDetectedOnStart(t *testing.T) {
	store, m, _ := setup(t)
	if _, err := store.Save("", "f.bin", strings.NewReader(strings.Repeat("z", 100_000)), 0); err != nil {
		t.Fatal(err)
	}
	st, err := m.Create("f.bin")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, st.ID, StateSeeding)
	if _, err := m.Stop(st.ID); err != nil {
		t.Fatal(err)
	}
	// Edit the file behind the drive's back, keeping its size.
	f, _, _ := store.Open("f.bin")
	name := f.Name()
	f.Close()
	if err := os.WriteFile(name, []byte(strings.Repeat("y", 100_000)), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(st.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestCreateRejectsEmptyAndMissing(t *testing.T) {
	store, m, _ := setup(t)
	if _, err := store.Mkdir("", "empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("empty"); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty: %v", err)
	}
	if _, err := m.Create("nope"); !errors.Is(err, drive.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := m.Create(""); err == nil {
		t.Error("root must not be shareable")
	}
	if _, err := m.Create("../x"); !errors.Is(err, drive.ErrInvalidPath) {
		t.Errorf("traversal: %v", err)
	}
}

func TestRecordsPersist(t *testing.T) {
	store, m, dir := setup(t)
	if _, err := store.Save("", "p.bin", strings.NewReader(strings.Repeat("q", 50_000)), 0); err != nil {
		t.Fatal(err)
	}
	st, err := m.Create("p.bin")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, st.ID, StateSeeding)
	m.Close()
	m2, err := New(store, Options{Dir: dir, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	waitState(t, m2, st.ID, StateSeeding)
}
