// Command magnetor-drive runs the self-hosted Magnetor Drive server.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hekrun/magnetor-drive/internal/auth"
	"github.com/hekrun/magnetor-drive/internal/config"
	"github.com/hekrun/magnetor-drive/internal/drive"
	"github.com/hekrun/magnetor-drive/internal/server"
	"github.com/hekrun/magnetor-drive/internal/torrents"
	"github.com/hekrun/magnetor-drive/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("magnetor-drive: %v", err)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	store, err := drive.New(cfg.FilesDir())
	if err != nil {
		return err
	}
	a, err := auth.New(cfg.AdminUser, cfg.AdminPassword, cfg.SessionTTL)
	if err != nil {
		return err
	}
	tm, err := torrents.New(store, torrents.Options{
		Dir: cfg.TorrentsDir(), Port: cfg.TorrentPort, DHT: cfg.TorrentDHT,
		UPnP: cfg.TorrentUPnP, Trackers: cfg.TorrentTrackers, Disabled: !cfg.TorrentEnabled,
	})
	if err != nil {
		return err
	}
	defer tm.Close()
	ui, err := fs.Sub(web.FS, "static")
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: server.New(server.Options{
			Store: store, Auth: a, Torrents: tm, UI: ui,
			CookieSecure: cfg.CookieSecure, MaxUploadBytes: cfg.MaxUploadBytes,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("listening on %s", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
