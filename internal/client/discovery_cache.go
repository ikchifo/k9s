// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package client

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	serverGroupsFile    = "servergroups.json"
	serverResourcesFile = "serverresources.json"
)

// seedEmptyResourceCaches pre-populates discovery cache entries for group
// versions listed in a fresh servergroups.json that have no serverresources.json
// on disk. client-go never caches empty resource lists, so a single aggregated
// API serving no resources (e.g. external.metrics.k8s.io) would otherwise force
// a full live discovery refresh on every start.
func seedEmptyResourceCaches(discCacheDir string) {
	sgFile := filepath.Join(discCacheDir, serverGroupsFile)
	fi, err := os.Stat(sgFile)
	if err != nil || time.Since(fi.ModTime()) > discoveryTTL {
		// Stale or missing groups: a live refresh is due anyway.
		return
	}

	b, err := os.ReadFile(sgFile)
	if err != nil {
		return
	}
	var groups metav1.APIGroupList
	if err := json.Unmarshal(b, &groups); err != nil {
		return
	}

	for i := range groups.Groups {
		g := &groups.Groups[i]
		for j := range g.Versions {
			v := &g.Versions[j]
			gv := path.Join(g.Name, v.Version)
			resFile := filepath.Join(discCacheDir, gv, serverResourcesFile)
			if _, err := os.Stat(resFile); err == nil {
				continue
			}
			seedResourceCache(resFile, gv)
		}
	}
}

// seedResourceCache writes an empty resource list for a group version that is
// known to serve no resources, in the same format client-go writes.
func seedResourceCache(resFile, gv string) {
	if err := os.MkdirAll(filepath.Dir(resFile), 0750); err != nil {
		return
	}
	b, err := json.Marshal(&metav1.APIResourceList{
		TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
		GroupVersion: gv,
		APIResources: []metav1.APIResource{},
	})
	if err != nil {
		return
	}

	f, err := os.CreateTemp(filepath.Dir(resFile), filepath.Base(resFile)+".")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		return
	}
	if err := f.Chmod(0660); err != nil {
		return
	}
	if err := f.Close(); err != nil {
		return
	}

	_ = os.Rename(f.Name(), resFile)
}
