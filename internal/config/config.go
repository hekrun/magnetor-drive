// Package config loads Magnetor Drive settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MinPasswordLength is the minimum accepted length of the owner password.
const MinPasswordLength = 12

// Config holds all runtime settings.
type Config struct {
	ListenAddr     string
	DataDir        string
	AdminUser      string
	AdminPassword  string
	CookieSecure   bool
	SessionTTL     time.Duration
	MaxUploadBytes int64 // 0 means unlimited

	TorrentEnabled  bool
	TorrentPort     int
	TorrentDHT      bool
	TorrentUPnP     bool
	TorrentTrackers []string
}

// Load reads configuration using getenv (usually os.Getenv).
func Load(getenv func(string) string) (*Config, error) {
	c := &Config{
		ListenAddr:      str(getenv, "MAGNETOR_LISTEN_ADDR", ":8080"),
		DataDir:         str(getenv, "MAGNETOR_DATA_DIR", "./data"),
		AdminUser:       str(getenv, "MAGNETOR_ADMIN_USER", "admin"),
		AdminPassword:   getenv("MAGNETOR_ADMIN_PASSWORD"),
		SessionTTL:      12 * time.Hour,
		TorrentEnabled:  true,
		TorrentPort:     42069,
		TorrentDHT:      true,
		MaxUploadBytes:  10 << 30,
		TorrentTrackers: nil,
	}
	var err error
	if c.CookieSecure, err = boolVar(getenv, "MAGNETOR_COOKIE_SECURE", false); err != nil {
		return nil, err
	}
	if c.TorrentEnabled, err = boolVar(getenv, "MAGNETOR_TORRENT_ENABLED", true); err != nil {
		return nil, err
	}
	if c.TorrentDHT, err = boolVar(getenv, "MAGNETOR_TORRENT_DHT", true); err != nil {
		return nil, err
	}
	if c.TorrentUPnP, err = boolVar(getenv, "MAGNETOR_TORRENT_UPNP", false); err != nil {
		return nil, err
	}
	if v := getenv("MAGNETOR_TORRENT_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 0 || p > 65535 {
			return nil, fmt.Errorf("MAGNETOR_TORRENT_PORT: invalid port %q", v)
		}
		c.TorrentPort = p
	}
	if v := getenv("MAGNETOR_MAX_UPLOAD_MB"); v != "" {
		mb, err := strconv.ParseInt(v, 10, 64)
		if err != nil || mb < 0 {
			return nil, fmt.Errorf("MAGNETOR_MAX_UPLOAD_MB: invalid value %q", v)
		}
		c.MaxUploadBytes = mb << 20
	}
	if v := getenv("MAGNETOR_SESSION_HOURS"); v != "" {
		h, err := strconv.Atoi(v)
		if err != nil || h <= 0 {
			return nil, fmt.Errorf("MAGNETOR_SESSION_HOURS: invalid value %q", v)
		}
		c.SessionTTL = time.Duration(h) * time.Hour
	}
	for _, t := range strings.Split(getenv("MAGNETOR_TORRENT_TRACKERS"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			c.TorrentTrackers = append(c.TorrentTrackers, t)
		}
	}
	if len(c.AdminPassword) < MinPasswordLength {
		return nil, fmt.Errorf("MAGNETOR_ADMIN_PASSWORD must be set and at least %d characters", MinPasswordLength)
	}
	if c.AdminUser == "" {
		return nil, errors.New("MAGNETOR_ADMIN_USER must not be empty")
	}
	if c.DataDir, err = filepath.Abs(c.DataDir); err != nil {
		return nil, fmt.Errorf("MAGNETOR_DATA_DIR: %w", err)
	}
	return c, nil
}

// FilesDir is where user files live (the only tree users can browse).
func (c *Config) FilesDir() string { return filepath.Join(c.DataDir, "files") }

// TorrentsDir holds .torrent files and torrent records; it is never browsable.
func (c *Config) TorrentsDir() string { return filepath.Join(c.DataDir, "torrents") }

func str(getenv func(string) string, k, def string) string {
	if v := getenv(k); v != "" {
		return v
	}
	return def
}

func boolVar(getenv func(string) string, k string, def bool) (bool, error) {
	v := getenv(k)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", k, v)
	}
	return b, nil
}
