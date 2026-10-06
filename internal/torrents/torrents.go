// Package torrents creates .torrent files for stored files and seeds them in
// place using the anacrolix/torrent library. No BitTorrent protocol code lives
// here, and no second copy of the data is ever made: the torrent client reads
// directly from the drive's files directory.
package torrents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"github.com/hekrun/magnetor-drive/internal/drive"
)

// States reported to the UI.
const (
	StateVerifying = "verifying"
	StateSeeding   = "seeding"
	StateStopped   = "stopped"
	StateInvalid   = "invalid"
)

var (
	ErrNotFound    = errors.New("torrent not found")
	ErrInvalid     = errors.New("source files changed; regenerate the torrent")
	ErrEmpty       = errors.New("nothing to share: the selection has no data")
	ErrUnsupported = errors.New("selection contains unsupported items (for example symbolic links)")
	ErrDisabled    = errors.New("torrent support is disabled")
)

// Options configures the Manager.
type Options struct {
	Dir      string // private directory for .torrent files and records
	Port     int
	DHT      bool
	UPnP     bool
	Trackers []string
	Disabled bool
}

// Record is the persisted description of a torrent. It contains only virtual
// drive paths, never server filesystem paths.
type Record struct {
	ID          string    `json:"id"` // hex info hash
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	IsDir       bool      `json:"isDir"`
	Size        int64     `json:"size"`
	Magnet      string    `json:"magnet"`
	CreatedAt   time.Time `json:"createdAt"`
	Fingerprint string    `json:"fingerprint"`
	Seed        bool      `json:"seed"` // user wants it seeding
	Invalid     bool      `json:"invalid"`
}

// Status is a Record plus live state.
type Status struct {
	Record
	State    string `json:"state"`
	Peers    int    `json:"peers"`
	Uploaded int64  `json:"uploaded"`
}

type run struct {
	t         *torrent.Torrent
	verifying bool
	cancel    context.CancelFunc
}

// Manager owns the torrent client and the torrent records.
type Manager struct {
	store *drive.Store
	opts  Options

	mu      sync.Mutex
	records map[string]*Record
	running map[string]*run
	client  *torrent.Client
}

// New loads saved records and resumes torrents that were seeding.
func New(store *drive.Store, opts Options) (*Manager, error) {
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{store: store, opts: opts, records: map[string]*Record{}, running: map[string]*run{}}
	b, err := os.ReadFile(filepath.Join(opts.Dir, "records.json"))
	if err == nil {
		var recs []*Record
		if err := json.Unmarshal(b, &recs); err != nil {
			return nil, fmt.Errorf("reading torrent records: %w", err)
		}
		for _, r := range recs {
			m.records[r.ID] = r
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if !opts.Disabled {
		m.mu.Lock()
		for _, r := range m.records {
			if r.Seed && !r.Invalid {
				if err := m.startLocked(r); err != nil {
					log.Printf("torrent %s not resumed: %v", r.ID, err)
				}
			}
		}
		m.mu.Unlock()
	}
	return m, nil
}

// Close stops all seeding.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.running {
		r.cancel()
	}
	if m.client != nil {
		m.client.Close()
		m.client = nil
	}
}

func (m *Manager) torrentPath(id string) string { return filepath.Join(m.opts.Dir, id+".torrent") }

func (m *Manager) saveLocked() error {
	recs := make([]*Record, 0, len(m.records))
	for _, r := range m.records {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].CreatedAt.Before(recs[j].CreatedAt) })
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(m.opts.Dir, "records.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.opts.Dir, "records.json"))
}

func (m *Manager) clientLocked() (*torrent.Client, error) {
	if m.client != nil {
		return m.client, nil
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.ListenPort = m.opts.Port
	cfg.NoDHT = !m.opts.DHT
	cfg.NoDefaultPortForwarding = !m.opts.UPnP
	cfg.Seed = true
	cfg.DataDir = m.opts.Dir
	// Torrents always get explicit per-torrent storage; this default is unused
	// and deliberately keeps no on-disk state.
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   m.opts.Dir,
		UsePartFiles:    generics.Some(false),
		PieceCompletion: storage.NewMapPieceCompletion(),
	})
	c, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("starting torrent client: %w", err)
	}
	m.client = c
	return c, nil
}

// fingerprint hashes the relative names, sizes and mtimes of everything under
// root, and rejects anything that is not a regular file or folder.
func fingerprint(root string) (string, int64, error) {
	h := sha256.New()
	var total int64
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := de.Info()
		if err != nil {
			return err
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			return ErrUnsupported
		}
		if strings.HasPrefix(de.Name(), ".magnetor-") {
			return ErrUnsupported
		}
		rel, _ := filepath.Rel(root, p)
		if fi.IsDir() {
			fmt.Fprintf(h, "d|%s\n", filepath.ToSlash(rel))
			return nil
		}
		total += fi.Size()
		fmt.Fprintf(h, "f|%s|%d|%d\n", filepath.ToSlash(rel), fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), total, err
}

// Create builds a .torrent for the file or folder at virt and starts seeding
// it from the existing stored files. An existing torrent for the same path is
// replaced (this is how a torrent is regenerated after the source changed).
func (m *Manager) Create(virt string) (Status, error) {
	if m.opts.Disabled {
		return Status{}, ErrDisabled
	}
	parent, name, entry, err := m.store.SourceDir(virt)
	if err != nil {
		return Status{}, err
	}
	root := filepath.Join(parent, name)
	fp, total, err := fingerprint(root)
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			return Status{}, err
		}
		return Status{}, fmt.Errorf("reading source: %s", "io error")
	}
	if total == 0 {
		return Status{}, ErrEmpty
	}
	info := metainfo.Info{}
	if err := info.BuildFromFilePath(root); err != nil {
		return Status{}, fmt.Errorf("hashing source: %s", "io error")
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		return Status{}, err
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes, CreatedBy: "Magnetor Drive", CreationDate: time.Now().Unix()}
	if len(m.opts.Trackers) > 0 {
		mi.Announce = m.opts.Trackers[0]
		mi.AnnounceList = [][]string{append([]string(nil), m.opts.Trackers...)}
	}
	id := mi.HashInfoBytes().HexString()
	rec := &Record{
		ID: id, Name: entry.Name, Path: entry.Path, IsDir: entry.IsDir, Size: total,
		Magnet: mi.Magnet(nil, &info).String(), CreatedAt: time.Now().UTC(),
		Fingerprint: fp, Seed: true,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for oid, old := range m.records {
		if old.Path == rec.Path && oid != id {
			m.removeLocked(oid)
		}
	}
	if old, ok := m.records[id]; ok {
		m.stopLocked(id)
		rec.CreatedAt = old.CreatedAt
	}
	f, err := os.OpenFile(m.torrentPath(id), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Status{}, errors.New("cannot save torrent file")
	}
	if err := mi.Write(f); err != nil {
		f.Close()
		return Status{}, errors.New("cannot save torrent file")
	}
	if err := f.Close(); err != nil {
		return Status{}, errors.New("cannot save torrent file")
	}
	m.records[id] = rec
	if err := m.startLocked(rec); err != nil {
		rec.Seed = false
		_ = m.saveLocked()
		return m.statusLocked(rec), err
	}
	if err := m.saveLocked(); err != nil {
		return Status{}, errors.New("cannot save torrent records")
	}
	return m.statusLocked(rec), nil
}

// startLocked adds the torrent to the client, reading directly from the
// stored files, and verifies the data in the background.
func (m *Manager) startLocked(rec *Record) error {
	if m.opts.Disabled {
		return ErrDisabled
	}
	if _, ok := m.running[rec.ID]; ok {
		return nil
	}
	parent, name, _, err := m.store.SourceDir(rec.Path)
	if err != nil {
		m.invalidateLocked(rec)
		return ErrInvalid
	}
	fp, _, err := fingerprint(filepath.Join(parent, name))
	if err != nil || fp != rec.Fingerprint {
		m.invalidateLocked(rec)
		return ErrInvalid
	}
	mi, err := metainfo.LoadFromFile(m.torrentPath(rec.ID))
	if err != nil {
		return errors.New("torrent file missing")
	}
	spec, err := torrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		return errors.New("torrent file unreadable")
	}
	spec.Storage = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   parent,
		UsePartFiles:    generics.Some(false),
		PieceCompletion: storage.NewMapPieceCompletion(),
	})
	// Seed only: never let remote peers write into the user's files.
	spec.DisallowDataDownload = true
	cl, err := m.clientLocked()
	if err != nil {
		return err
	}
	t, _, err := cl.AddTorrentSpec(spec)
	if err != nil {
		return errors.New("cannot add torrent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{t: t, verifying: true, cancel: cancel}
	m.running[rec.ID] = r
	rec.Seed = true
	go m.verify(ctx, rec.ID, r)
	return nil
}

func (m *Manager) verify(ctx context.Context, id string, r *run) {
	<-r.t.GotInfo()
	err := r.t.VerifyDataContext(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running[id] != r {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil || r.t.BytesMissing() > 0 {
		if rec := m.records[id]; rec != nil {
			m.invalidateLocked(rec)
		}
		return
	}
	r.verifying = false
	r.t.DownloadAll() // no-op for complete data; keeps client state consistent
}

func (m *Manager) stopLocked(id string) {
	if r, ok := m.running[id]; ok {
		r.cancel()
		r.t.Drop()
		delete(m.running, id)
	}
}

func (m *Manager) invalidateLocked(rec *Record) {
	m.stopLocked(rec.ID)
	rec.Invalid = true
	rec.Seed = false
	if err := m.saveLocked(); err != nil {
		log.Printf("saving torrent records: %v", err)
	}
}

func (m *Manager) removeLocked(id string) {
	m.stopLocked(id)
	delete(m.records, id)
	_ = os.Remove(m.torrentPath(id))
}

func (m *Manager) statusLocked(rec *Record) Status {
	s := Status{Record: *rec}
	switch r, running := m.running[rec.ID]; {
	case rec.Invalid:
		s.State = StateInvalid
	case !running:
		s.State = StateStopped
	case r.verifying:
		s.State = StateVerifying
	default:
		s.State = StateSeeding
		st := r.t.Stats()
		s.Peers = st.ActivePeers
		s.Uploaded = st.BytesWrittenData.Int64()
	}
	s.Seed = s.State == StateSeeding || s.State == StateVerifying
	return s
}

// List returns all torrents, newest first.
func (m *Manager) List() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.records))
	for _, r := range m.records {
		out = append(out, m.statusLocked(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Start resumes seeding a stopped torrent.
func (m *Manager) Start(id string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[id]
	if !ok {
		return Status{}, ErrNotFound
	}
	if rec.Invalid {
		return Status{}, ErrInvalid
	}
	if err := m.startLocked(rec); err != nil {
		return m.statusLocked(rec), err
	}
	_ = m.saveLocked()
	return m.statusLocked(rec), nil
}

// Stop stops seeding but keeps the torrent so it can be started again.
func (m *Manager) Stop(id string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[id]
	if !ok {
		return Status{}, ErrNotFound
	}
	m.stopLocked(id)
	rec.Seed = false
	if err := m.saveLocked(); err != nil {
		return Status{}, errors.New("cannot save torrent records")
	}
	return m.statusLocked(rec), nil
}

// Delete stops seeding and forgets the torrent. Source files are untouched.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[id]; !ok {
		return ErrNotFound
	}
	m.removeLocked(id)
	return m.saveLocked()
}

// File returns the path of the stored .torrent file (for download).
func (m *Manager) File(id string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[id]
	if !ok {
		return "", "", ErrNotFound
	}
	return m.torrentPath(id), rec.Name + ".torrent", nil
}

func related(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") || a == "" || b == ""
}

// Invalidate marks every torrent whose source overlaps virt as invalid and
// stops seeding it. Call it after any change to the drive at virt. It returns
// the number of torrents affected.
func (m *Manager) Invalidate(virt string) int {
	virt, err := drive.Clean(virt)
	if err != nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, rec := range m.records {
		if !rec.Invalid && related(rec.Path, virt) {
			m.invalidateLocked(rec)
			n++
		}
	}
	return n
}
