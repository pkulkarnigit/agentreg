package main

import (
	"encoding/json"
	"os"
	"time"
)

// lockfileName lives in the current directory, the same convention
// package-lock.json follows: one file per project tracking what apreg
// install has put on disk, regardless of any individual install's --dir,
// so `apreg list` and `apreg uninstall` work no matter where a given
// plugin actually landed.
const lockfileName = "apreg-lock.json"

type lockEntry struct {
	Version     string    `json:"version"`
	Checksum    string    `json:"checksum"`
	Registry    string    `json:"registry"`
	Dir         string    `json:"dir"`
	InstalledAt time.Time `json:"installed_at"`
}

type lockfile struct {
	Packages map[string]lockEntry `json:"packages"` // key: "@scope/name"
}

func loadLockfile() (*lockfile, error) {
	b, err := os.ReadFile(lockfileName)
	if err != nil {
		if os.IsNotExist(err) {
			return &lockfile{Packages: map[string]lockEntry{}}, nil
		}
		return nil, err
	}
	var lf lockfile
	if err := json.Unmarshal(b, &lf); err != nil {
		return nil, err
	}
	if lf.Packages == nil {
		lf.Packages = map[string]lockEntry{}
	}
	return &lf, nil
}

func saveLockfile(lf *lockfile) error {
	b, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(lockfileName, b, 0o644)
}
