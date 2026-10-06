package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hekrun/magnetor-drive/internal/auth"
	"github.com/hekrun/magnetor-drive/internal/drive"
	"github.com/hekrun/magnetor-drive/internal/torrents"
)

type env struct {
	t    *testing.T
	ts   *httptest.Server
	cl   *http.Client
	csrf string
	root string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	store, err := drive.New(filepath.Join(base, "files"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New("owner", "correct horse battery", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tm, err := torrents.New(store, torrents.Options{Dir: filepath.Join(base, "torrents")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tm.Close)
	h := New(Options{Store: store, Auth: a, Torrents: tm, MaxUploadBytes: 1 << 20,
		UI: fstest.MapFS{"index.html": {Data: []byte("<html>ui</html>")}}})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	jar, _ := newJar()
	return &env{t: t, ts: ts, cl: &http.Client{Jar: jar}, root: store.Root()}
}

func (e *env) do(method, path string, body any) (*http.Response, []byte) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, r)
	if e.csrf != "" {
		req.Header.Set("X-CSRF-Token", e.csrf)
	}
	res, err := e.cl.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func (e *env) login() {
	e.t.Helper()
	res, b := e.do("POST", "/api/login", map[string]string{"username": "owner", "password": "correct horse battery"})
	if res.StatusCode != 200 {
		e.t.Fatalf("login: %d %s", res.StatusCode, b)
	}
	var out struct{ CSRF string }
	_ = json.Unmarshal(b, &out)
	e.csrf = out.CSRF
}

func (e *env) upload(dir, name, content string) (*http.Response, []byte) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write([]byte(content))
	mw.Close()
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/upload?path="+dir, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", e.csrf)
	res, err := e.cl.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func TestRequiresAuthentication(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/api/files", "/api/download?path=x", "/api/torrents", "/api/session"} {
		if res, _ := e.do("GET", p, nil); res.StatusCode != 401 {
			t.Errorf("GET %s = %d, want 401", p, res.StatusCode)
		}
	}
	if res, _ := e.do("POST", "/api/login", map[string]string{"username": "owner", "password": "nope"}); res.StatusCode != 401 {
		t.Errorf("bad login = %d", res.StatusCode)
	}
	if res, b := e.do("GET", "/", nil); res.StatusCode != 200 || !strings.Contains(string(b), "ui") {
		t.Errorf("UI should be public: %d", res.StatusCode)
	}
}

func TestCSRFRequired(t *testing.T) {
	e := newEnv(t)
	e.login()
	good := e.csrf
	e.csrf = ""
	if res, _ := e.do("POST", "/api/folders", map[string]string{"path": "", "name": "x"}); res.StatusCode != 403 {
		t.Errorf("missing CSRF = %d", res.StatusCode)
	}
	e.csrf = good
	if res, _ := e.do("POST", "/api/folders", map[string]string{"path": "", "name": "x"}); res.StatusCode != 201 {
		t.Errorf("with CSRF = %d", res.StatusCode)
	}
}

func TestFileWorkflowAndTraversal(t *testing.T) {
	e := newEnv(t)
	e.login()
	if res, b := e.upload("", "hello.txt", "hi there"); res.StatusCode != 201 {
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}
	if res, _ := e.upload("", "hello.txt", "dup"); res.StatusCode != 409 {
		t.Errorf("duplicate upload = %d", res.StatusCode)
	}
	if res, _ := e.upload("", "big.bin", strings.Repeat("x", 2<<20)); res.StatusCode != 413 {
		t.Errorf("oversize upload = %d", res.StatusCode)
	}
	res, b := e.do("GET", "/api/download?path=hello.txt", nil)
	if res.StatusCode != 200 || string(b) != "hi there" || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("download: %d %q %v", res.StatusCode, b, res.Header)
	}
	res, _ = e.do("GET", "/api/preview?path=hello.txt", nil)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("preview headers: %v", res.Header)
	}
	e.upload("", "page.html", "<script>alert(1)</script>")
	if res, _ := e.do("GET", "/api/preview?path=page.html", nil); res.StatusCode != 415 {
		t.Errorf("html must not preview inline: %d", res.StatusCode)
	}
	if res, _ := e.do("GET", "/api/download?path=page.html", nil); res.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("html download type: %s", res.Header.Get("Content-Type"))
	}

	for _, p := range []string{"..", "../..", "a/../../etc", "%2e%2e/%2e%2e", "/etc/passwd", "..%5C..", e.root} {
		res, b := e.do("GET", "/api/files?path="+p, nil)
		if res.StatusCode == 200 && strings.Contains(string(b), "passwd") {
			t.Errorf("traversal %q exposed data", p)
		}
		if strings.Contains(string(b), e.root) {
			t.Errorf("response leaks server path: %s", b)
		}
	}
	if res, _ := e.do("GET", "/api/download?path=../../etc/passwd", nil); res.StatusCode != 400 {
		t.Errorf("traversal download = %d", res.StatusCode)
	}

	e.do("POST", "/api/folders", map[string]string{"path": "", "name": "d"})
	if res, _ := e.do("POST", "/api/copy", map[string]string{"path": "hello.txt", "dest": "d"}); res.StatusCode != 201 {
		t.Errorf("copy = %d", res.StatusCode)
	}
	if res, _ := e.do("POST", "/api/rename", map[string]string{"path": "d/hello.txt", "name": "x.txt"}); res.StatusCode != 200 {
		t.Errorf("rename = %d", res.StatusCode)
	}
	if res, _ := e.do("POST", "/api/move", map[string]string{"path": "d/x.txt", "dest": ""}); res.StatusCode != 200 {
		t.Errorf("move = %d", res.StatusCode)
	}
	if res, _ := e.do("POST", "/api/delete", map[string]string{"path": "x.txt"}); res.StatusCode != 200 {
		t.Errorf("delete = %d", res.StatusCode)
	}
	if res, _ := e.do("POST", "/api/delete", map[string]string{"path": ""}); res.StatusCode != 400 {
		t.Errorf("delete root = %d", res.StatusCode)
	}
}

func TestTorrentAPI(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.upload("", "movie.bin", strings.Repeat("m", 200_000))
	res, b := e.do("POST", "/api/torrents", map[string]string{"path": "movie.bin"})
	if res.StatusCode != 201 {
		t.Fatalf("create: %d %s", res.StatusCode, b)
	}
	var st torrents.Status
	_ = json.Unmarshal(b, &st)
	if !strings.HasPrefix(st.Magnet, "magnet:?") || strings.Contains(string(b), e.root) {
		t.Errorf("bad create response: %s", b)
	}
	if res, b := e.do("GET", "/api/torrents/"+st.ID+"/file", nil); res.StatusCode != 200 || !bytes.HasPrefix(b, []byte("d")) {
		t.Errorf("torrent file: %d", res.StatusCode)
	}
	if res, _ := e.do("POST", "/api/torrents/"+st.ID+"/stop", map[string]string{}); res.StatusCode != 200 {
		t.Errorf("stop = %d", res.StatusCode)
	}
	// Renaming the source invalidates the torrent.
	e.do("POST", "/api/rename", map[string]string{"path": "movie.bin", "name": "film.bin"})
	if res, _ := e.do("POST", "/api/torrents/"+st.ID+"/start", map[string]string{}); res.StatusCode != 409 {
		t.Errorf("start invalid = %d", res.StatusCode)
	}
	_, b = e.do("GET", "/api/torrents", nil)
	if !strings.Contains(string(b), `"state":"invalid"`) {
		t.Errorf("expected invalid state: %s", b)
	}
	if res, _ := e.do("DELETE", "/api/torrents/"+st.ID, nil); res.StatusCode != 200 {
		t.Errorf("delete = %d", res.StatusCode)
	}
	if res, _ := e.do("POST", "/api/torrents/zzz/stop", map[string]string{}); res.StatusCode != 404 {
		t.Errorf("unknown = %d", res.StatusCode)
	}
}
