package config

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadRequiresStrongPassword(t *testing.T) {
	if _, err := Load(env(nil)); err == nil {
		t.Error("expected error without password")
	}
	if _, err := Load(env(map[string]string{"MAGNETOR_ADMIN_PASSWORD": "short"})); err == nil {
		t.Error("expected error for short password")
	}
}

func TestLoadDefaultsAndOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"MAGNETOR_ADMIN_PASSWORD":   "a-long-enough-password",
		"MAGNETOR_DATA_DIR":         "/data",
		"MAGNETOR_MAX_UPLOAD_MB":    "5",
		"MAGNETOR_TORRENT_TRACKERS": " udp://a:1 , ,udp://b:2",
		"MAGNETOR_TORRENT_DHT":      "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.FilesDir() != "/data/files" || c.TorrentsDir() != "/data/torrents" {
		t.Errorf("dirs: %s %s", c.FilesDir(), c.TorrentsDir())
	}
	if c.MaxUploadBytes != 5<<20 || c.TorrentDHT || len(c.TorrentTrackers) != 2 || c.AdminUser != "admin" {
		t.Errorf("unexpected config %+v", c)
	}
	if _, err := Load(env(map[string]string{"MAGNETOR_ADMIN_PASSWORD": "a-long-enough-password", "MAGNETOR_TORRENT_PORT": "x"})); err == nil {
		t.Error("expected port error")
	}
}
