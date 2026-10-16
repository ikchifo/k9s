// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/data"
)

const (
	ctxRecencyFile = "ctx-recency.json"
	ctxRecencyMax  = 100
)

// ctxRecency maps context names to their last-used unix time.
type ctxRecency map[string]int64

func ctxRecencyPath() (string, bool) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", false
	}

	return filepath.Join(dir, config.AppName, ctxRecencyFile), true
}

// loadCtxRecency returns an empty map on any disk error.
func loadCtxRecency() ctxRecency {
	rr := make(ctxRecency)
	f, ok := ctxRecencyPath()
	if !ok {
		return rr
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return rr
	}
	if err := json.Unmarshal(b, &rr); err != nil {
		return make(ctxRecency)
	}

	return rr
}

// recordSwitch stamps both contexts so the one left behind ranks right after
// the new one, best-effort.
func recordSwitch(prev, next string) {
	f, ok := ctxRecencyPath()
	if !ok {
		return
	}

	rr := loadCtxRecency()
	now := time.Now().Unix()
	if prev != "" {
		rr[prev] = now - 1
	}
	rr[next] = now
	rr.trim()
	b, err := json.Marshal(rr)
	if err != nil {
		return
	}

	if e := os.MkdirAll(filepath.Dir(f), data.DefaultDirMod); e != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), ctxRecencyFile+".")
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

// trim drops the oldest entries past ctxRecencyMax.
func (rr ctxRecency) trim() {
	if len(rr) <= ctxRecencyMax {
		return
	}

	nn := make([]string, 0, len(rr))
	for n := range rr {
		nn = append(nn, n)
	}
	slices.SortFunc(nn, func(a, b string) int {
		return cmp.Compare(rr[b], rr[a])
	})
	for _, n := range nn[ctxRecencyMax:] {
		delete(rr, n)
	}
}

// rank orders names most recent first, then by name; current goes last.
func (rr ctxRecency) rank(names []string, current string) []string {
	out := slices.Clone(names)
	slices.SortFunc(out, func(a, b string) int {
		if a == current || b == current {
			return cmp.Compare(boolRank(a == current), boolRank(b == current))
		}
		if c := cmp.Compare(rr[b], rr[a]); c != 0 {
			return c
		}

		return cmp.Compare(a, b)
	})

	return out
}

func boolRank(b bool) int {
	if b {
		return 1
	}

	return 0
}
