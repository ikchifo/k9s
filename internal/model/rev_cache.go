// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/data"
)

const latestRevFile = "latest-rev.json"

// revCache persists the latest k9s release across restarts.
type revCache struct {
	Rev       string    `json:"rev"`
	FetchedAt time.Time `json:"fetchedAt"`
}

func revCacheFile() (string, bool) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", false
	}

	return filepath.Join(dir, config.AppName, latestRevFile), true
}

// loadRevCache returns the persisted rev when fresher than cacheExpiry; any
// disk error falls through to a live fetch.
func loadRevCache() (string, bool) {
	f, ok := revCacheFile()
	if !ok {
		return "", false
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "", false
	}
	var rc revCache
	if err := json.Unmarshal(b, &rc); err != nil || rc.Rev == "" {
		return "", false
	}
	if time.Since(rc.FetchedAt) >= cacheExpiry {
		return "", false
	}

	return rc.Rev, true
}

// saveRevCache persists the rev best-effort; temp file + rename keeps readers
// from ever seeing a torn write.
func saveRevCache(rev string) {
	f, ok := revCacheFile()
	if !ok {
		return
	}
	if err := os.MkdirAll(filepath.Dir(f), data.DefaultDirMod); err != nil {
		return
	}
	b, err := json.Marshal(revCache{Rev: rev, FetchedAt: time.Now()})
	if err != nil {
		return
	}

	tmp, err := os.CreateTemp(filepath.Dir(f), latestRevFile+".")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), f)
}
