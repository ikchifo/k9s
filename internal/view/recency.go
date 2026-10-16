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

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/data"
)

const (
	ctxRecencyFile = "ctx-recency.json"
	nsRecencyFile  = "ns-recency.json"
	recencyMax     = 100
)

// recency maps names to their last-used unix time.
type recency map[string]int64

// nsRecency tracks namespace recency per context.
type nsRecency map[string]recency

func cachePath(file string) (string, bool) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", false
	}

	return filepath.Join(dir, config.AppName, file), true
}

// readCache decodes a cache file into v, reporting success.
func readCache(file string, v any) bool {
	f, ok := cachePath(file)
	if !ok {
		return false
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return false
	}

	return json.Unmarshal(b, v) == nil
}

// writeCache atomically persists v, best-effort.
func writeCache(file string, v any) {
	f, ok := cachePath(file)
	if !ok {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}

	if e := os.MkdirAll(filepath.Dir(f), data.DefaultDirMod); e != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), file+".")
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

// loadCtxRecency returns an empty map on any disk error.
func loadCtxRecency() recency {
	rr := make(recency)
	if !readCache(ctxRecencyFile, &rr) {
		return make(recency)
	}

	return rr
}

// recordSwitch stamps both contexts so the one left behind ranks right after
// the new one, best-effort.
func recordSwitch(prev, next string) {
	rr := loadCtxRecency()
	rr.stamp(prev, next)
	writeCache(ctxRecencyFile, rr)
}

// loadNSRecency returns the namespace recency for a context.
func loadNSRecency(ctx string) recency {
	nn := make(nsRecency)
	if !readCache(nsRecencyFile, &nn) || nn[ctx] == nil {
		return make(recency)
	}

	return nn[ctx]
}

// recordNSSwitch stamps both namespaces within a context, best-effort.
func recordNSSwitch(ctx, prev, next string) {
	nn := make(nsRecency)
	if !readCache(nsRecencyFile, &nn) {
		nn = make(nsRecency)
	}
	rr := nn[ctx]
	if rr == nil {
		rr = make(recency)
	}
	rr.stamp(nsKey(prev), nsKey(next))
	nn[ctx] = rr
	writeCache(nsRecencyFile, nn)
}

// nsKey names all namespaces the way the picker shows them.
func nsKey(ns string) string {
	if ns == client.BlankNamespace || ns == client.ClusterScope {
		return client.NamespaceAll
	}

	return ns
}

func (rr recency) stamp(prev, next string) {
	now := time.Now().Unix()
	if prev != "" && prev != next {
		rr[prev] = now - 1
	}
	rr[next] = now
	rr.trim()
}

// trim drops the oldest entries past recencyMax.
func (rr recency) trim() {
	if len(rr) <= recencyMax {
		return
	}

	nn := make([]string, 0, len(rr))
	for n := range rr {
		nn = append(nn, n)
	}
	slices.SortFunc(nn, func(a, b string) int {
		return cmp.Compare(rr[b], rr[a])
	})
	for _, n := range nn[recencyMax:] {
		delete(rr, n)
	}
}

// rank orders names most recent first, then by name; current goes last.
func (rr recency) rank(names []string, current string) []string {
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
