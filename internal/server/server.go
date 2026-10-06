// Package server exposes the drive over an authenticated JSON/HTTP API and
// serves the browser UI.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"

	"github.com/hekrun/magnetor-drive/internal/auth"
	"github.com/hekrun/magnetor-drive/internal/drive"
	"github.com/hekrun/magnetor-drive/internal/torrents"
)

const (
	cookieName  = "magnetor_session"
	maxJSONBody = 1 << 20
)

// Options configures the server.
type Options struct {
	Store          *drive.Store
	Auth           *auth.Authenticator
	Torrents       *torrents.Manager
	UI             fs.FS
	CookieSecure   bool
	MaxUploadBytes int64
}

// Server is the HTTP handler.
type Server struct {
	opts Options
	mux  *http.ServeMux
}

// New builds the handler.
func New(opts Options) *Server {
	s := &Server{opts: opts, mux: http.NewServeMux()}
	m := s.mux
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.protect(s.logout))
	m.HandleFunc("GET /api/session", s.protect(s.session))
	m.HandleFunc("GET /api/files", s.protect(s.list))
	m.HandleFunc("POST /api/folders", s.protect(s.mkdir))
	m.HandleFunc("POST /api/upload", s.protect(s.upload))
	m.HandleFunc("GET /api/download", s.protect(s.download))
	m.HandleFunc("GET /api/preview", s.protect(s.preview))
	m.HandleFunc("POST /api/rename", s.protect(s.rename))
	m.HandleFunc("POST /api/move", s.protect(s.move))
	m.HandleFunc("POST /api/copy", s.protect(s.copy))
	m.HandleFunc("POST /api/delete", s.protect(s.remove))
	m.HandleFunc("GET /api/torrents", s.protect(s.listTorrents))
	m.HandleFunc("POST /api/torrents", s.protect(s.createTorrent))
	m.HandleFunc("POST /api/torrents/{id}/start", s.protect(s.startTorrent))
	m.HandleFunc("POST /api/torrents/{id}/stop", s.protect(s.stopTorrent))
	m.HandleFunc("DELETE /api/torrents/{id}", s.protect(s.deleteTorrent))
	m.HandleFunc("GET /api/torrents/{id}/file", s.protect(s.torrentFile))
	m.Handle("/", http.FileServerFS(opts.UI))
	return s
}

// ServeHTTP adds security headers and dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; media-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
	}
	s.mux.ServeHTTP(w, r)
}

type handler func(w http.ResponseWriter, r *http.Request, sess *auth.Session)

// protect requires a valid session and, for state-changing methods, a CSRF token.
func (s *Server) protect(next handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		var sess *auth.Session
		ok := false
		if err == nil {
			sess, ok = s.opts.Auth.Lookup(c.Value)
		}
		if !ok {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !auth.EqualToken(r.Header.Get("X-CSRF-Token"), sess.CSRF) {
				writeErr(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
		}
		next(w, r, sess)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// fail maps domain errors to HTTP responses without leaking server details.
func fail(w http.ResponseWriter, err error) {
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, drive.ErrNotFound), errors.Is(err, torrents.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, drive.ErrInvalidPath), errors.Is(err, drive.ErrInvalidName):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, drive.ErrExists):
		writeErr(w, http.StatusConflict, "an item with that name already exists")
	case errors.Is(err, drive.ErrNotDir), errors.Is(err, drive.ErrIsDir), errors.Is(err, drive.ErrConflict),
		errors.Is(err, drive.ErrUnsupported), errors.Is(err, torrents.ErrEmpty), errors.Is(err, torrents.ErrUnsupported):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, drive.ErrTooLarge), errors.As(err, &mbe):
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
	case errors.Is(err, torrents.ErrInvalid):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, torrents.ErrDisabled):
		writeErr(w, http.StatusNotImplemented, err.Error())
	default:
		log.Printf("internal error: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if !readJSON(w, r, &req) {
		return
	}
	ip := clientIP(r)
	if s.opts.Auth.Throttled(ip) {
		w.Header().Set("Retry-After", "900")
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts; try again later")
		return
	}
	tok, sess, ok := s.opts.Auth.Login(ip, req.Username, req.Password)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: s.opts.CookieSecure,
		SameSite: http.SameSiteStrictMode, Expires: sess.Expires,
	})
	writeJSON(w, http.StatusOK, map[string]string{"user": sess.User, "csrf": sess.CSRF})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.opts.Auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.opts.CookieSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) session(w http.ResponseWriter, _ *http.Request, sess *auth.Session) {
	writeJSON(w, http.StatusOK, map[string]string{"user": sess.User, "csrf": sess.CSRF})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	p, err := drive.Clean(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	entries, err := s.opts.Store.List(p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": entries})
}

type pathReq struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Dest string `json:"dest"`
}

func (s *Server) mkdir(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	var req pathReq
	if !readJSON(w, r, &req) {
		return
	}
	e, err := s.opts.Store.Mkdir(req.Path, req.Name)
	if err != nil {
		fail(w, err)
		return
	}
	s.changed(e.Path)
	writeJSON(w, http.StatusCreated, e)
}

// changed invalidates torrents affected by a change at the given paths.
func (s *Server) changed(paths ...string) {
	for _, p := range paths {
		s.opts.Torrents.Invalidate(p)
	}
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	dir, err := drive.Clean(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	if s.opts.MaxUploadBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes+(1<<20))
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart upload")
		return
	}
	var saved []drive.Entry
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, err)
			return
		}
		if part.FileName() == "" {
			continue
		}
		name := path.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
		e, err := s.opts.Store.Save(dir, name, part, s.opts.MaxUploadBytes)
		if err != nil {
			fail(w, err)
			return
		}
		s.changed(e.Path)
		saved = append(saved, e)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"saved": saved})
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	s.serve(w, r, false)
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	s.serve(w, r, true)
}

// previewTypes are the only types ever rendered inline; everything else is
// forced to download so that user-supplied HTML/SVG can never run as script.
var previewTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".pdf": "application/pdf", ".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg",
	".mp4": "video/mp4", ".webm": "video/webm",
	".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8", ".json": "text/plain; charset=utf-8",
	".log": "text/plain; charset=utf-8", ".csv": "text/plain; charset=utf-8",
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, inline bool) {
	f, e, err := s.opts.Store.Open(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	ctype, ok := previewTypes[strings.ToLower(path.Ext(e.Name))]
	if inline && !ok {
		writeErr(w, http.StatusUnsupportedMediaType, "no preview available for this file type")
		return
	}
	if inline {
		h.Set("Content-Type", ctype)
		if strings.HasSuffix(ctype, "pdf") {
			h.Del("Content-Security-Policy") // sandbox breaks built-in PDF viewers
		}
		h.Set("Content-Disposition", "inline")
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": e.Name}))
	}
	http.ServeContent(w, r, "", e.ModTime, f)
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	var req pathReq
	if !readJSON(w, r, &req) {
		return
	}
	old, err := drive.Clean(req.Path)
	if err != nil {
		fail(w, err)
		return
	}
	e, err := s.opts.Store.Rename(old, req.Name)
	if err != nil {
		fail(w, err)
		return
	}
	s.changed(old, e.Path)
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) move(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	var req pathReq
	if !readJSON(w, r, &req) {
		return
	}
	old, err := drive.Clean(req.Path)
	if err != nil {
		fail(w, err)
		return
	}
	e, err := s.opts.Store.Move(old, req.Dest)
	if err != nil {
		fail(w, err)
		return
	}
	s.changed(old, e.Path)
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) copy(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	var req pathReq
	if !readJSON(w, r, &req) {
		return
	}
	e, err := s.opts.Store.Copy(req.Path, req.Dest)
	if err != nil {
		fail(w, err)
		return
	}
	s.changed(e.Path)
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	var req pathReq
	if !readJSON(w, r, &req) {
		return
	}
	p, err := drive.Clean(req.Path)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.opts.Store.Delete(p); err != nil {
		fail(w, err)
		return
	}
	s.changed(p)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listTorrents(w http.ResponseWriter, _ *http.Request, _ *auth.Session) {
	writeJSON(w, http.StatusOK, map[string]any{"torrents": s.opts.Torrents.List()})
}

func (s *Server) createTorrent(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	var req pathReq
	if !readJSON(w, r, &req) {
		return
	}
	st, err := s.opts.Torrents.Create(req.Path)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, st)
}

func (s *Server) startTorrent(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	st, err := s.opts.Torrents.Start(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) stopTorrent(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	st, err := s.opts.Torrents.Stop(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) deleteTorrent(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	if err := s.opts.Torrents.Delete(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) torrentFile(w http.ResponseWriter, r *http.Request, _ *auth.Session) {
	p, name, err := s.opts.Torrents.File(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeFile(w, r, p)
}
