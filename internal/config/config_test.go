package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.DataDir != "/data" || c.KeyFile != "/run/keys/alcove.key" || c.TmpDir != "/tmp/alcove" {
		t.Errorf("defaults = %+v", c)
	}
	if c.AllowNonTmpfsTmp {
		t.Error("the tmpfs requirement must be on by default")
	}
	if c.SealEvery != 10*time.Second {
		t.Errorf("SealEvery = %v", c.SealEvery)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"ALCOVE_ADDR": "127.0.0.1:9000", "ALCOVE_DATA_DIR": "/x/data", "ALCOVE_KEY_FILE": "/k/a.key",
		"ALCOVE_TMP_DIR": "/dev/shm/a", "ALCOVE_ALLOW_NONTMPFS_TMPDIR": "true", "ALCOVE_SEAL_EVERY_SECONDS": "30",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:9000" || c.DataDir != "/x/data" || c.KeyFile != "/k/a.key" || c.TmpDir != "/dev/shm/a" || !c.AllowNonTmpfsTmp || c.SealEvery != 30*time.Second {
		t.Errorf("config = %+v", c)
	}
}

func TestRejectsBadValues(t *testing.T) {
	for _, m := range []map[string]string{
		{"ALCOVE_ALLOW_NONTMPFS_TMPDIR": "maybe"},
		{"ALCOVE_SEAL_EVERY_SECONDS": "0"},
		{"ALCOVE_SEAL_EVERY_SECONDS": "99999"},
		{"ALCOVE_SEAL_EVERY_SECONDS": "soon"},
	} {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("accepted %v", m)
		}
	}
}

// There is no environment variable that can carry the key or a passphrase (hard constraint 4).
func TestNoSecretVariablesAreRead(t *testing.T) {
	c, err := Load(env(map[string]string{"ALCOVE_KEY": "abc", "ALCOVE_PASSPHRASE": "abc", "ALCOVE_KEY_PASSPHRASE": "abc"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.KeyFile != "/run/keys/alcove.key" {
		t.Errorf("a secret-looking variable changed the config: %+v", c)
	}
}
