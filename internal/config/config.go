// Package config reads Alcove's settings from the environment (12-Factor III).
//
// Hard constraint 4: configuration holds paths and switches only, never a key or a passphrase.
// There is deliberately no environment variable that carries the master key or its passphrase.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/netsec"
)

// Config is everything the server needs to start.
type Config struct {
	Addr    string // listen address, default ":8080"
	DataDir string // ciphertext only: the sealed database, manifest and blobs
	KeyFile string // the master key file; must NOT be inside DataDir
	TmpDir  string // tmpfs scratch for the live database
	// AllowNonTmpfsTmp lets TmpDir be an ordinary directory. For a dev box; never in production.
	AllowNonTmpfsTmp bool
	// SealEvery is how often a changed database is sealed in the background.
	SealEvery time.Duration
	// BackupDir is where instance backups are written (D-28). It must not be inside DataDir. Empty
	// disables the feature.
	BackupDir string
	// BackupKeep is how many of the newest backups a run keeps (never fewer than one).
	BackupKeep int
	// Net holds the transport-security values set in the environment (D-23). An unset variable is a
	// nil field: the admin console decides. A set variable wins and shows as "set by environment".
	Net netsec.Env
}

// Load builds a Config from getenv (os.Getenv in production, a map in tests).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:       get(getenv, "ALCOVE_ADDR", ":8080"),
		DataDir:    get(getenv, "ALCOVE_DATA_DIR", "/data"),
		KeyFile:    get(getenv, "ALCOVE_KEY_FILE", "/run/keys/alcove.key"),
		TmpDir:     get(getenv, "ALCOVE_TMP_DIR", "/tmp/alcove"),
		SealEvery:  10 * time.Second,
		BackupDir:  get(getenv, "ALCOVE_BACKUP_DIR", "/backups"),
		BackupKeep: 7,
	}
	var err error
	if c.AllowNonTmpfsTmp, err = boolEnv(getenv, "ALCOVE_ALLOW_NONTMPFS_TMPDIR", false); err != nil {
		return Config{}, err
	}
	if v := strings.TrimSpace(getenv("ALCOVE_SEAL_EVERY_SECONDS")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 3600 {
			return Config{}, fmt.Errorf("ALCOVE_SEAL_EVERY_SECONDS must be 1..3600, got %q", v)
		}
		c.SealEvery = time.Duration(n) * time.Second
	}
	if v := strings.TrimSpace(getenv("ALCOVE_BACKUP_KEEP")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 365 {
			return Config{}, fmt.Errorf("ALCOVE_BACKUP_KEEP must be 1..365, got %q", v)
		}
		c.BackupKeep = n
	}
	if v := strings.TrimSpace(getenv("ALCOVE_ALLOWED_NETWORKS")); v != "" {
		c.Net.AllowedNets = &v
	}
	if v := strings.TrimSpace(getenv("ALCOVE_TRUSTED_PROXIES")); v != "" {
		c.Net.TrustedProxies = &v
	}
	for key, dst := range map[string]**bool{"ALCOVE_PROXY_MODE": &c.Net.ProxyMode, "ALCOVE_REQUIRE_TLS": &c.Net.RequireTLS} {
		if strings.TrimSpace(getenv(key)) == "" {
			continue
		}
		b, berr := boolEnv(getenv, key, false)
		if berr != nil {
			return Config{}, berr
		}
		*dst = &b
	}
	return c, nil
}

func get(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func boolEnv(getenv func(string) string, key string, def bool) (bool, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", key, v)
	}
	return b, nil
}
