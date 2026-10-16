// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func writeGroups(t *testing.T, dir string, groups metav1.APIGroupList) {
	t.Helper()

	b, err := json.Marshal(groups)
	if err != nil {
		t.Fatalf("marshal groups: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, serverGroupsFile), b, 0o600); err != nil {
		t.Fatalf("write groups: %v", err)
	}
}

func TestSeedEmptyResourceCaches(t *testing.T) {
	groups := metav1.APIGroupList{
		TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"},
		Groups: []metav1.APIGroup{
			{Name: "apps", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "apps/v1", Version: "v1"}}},
			{Name: "external.metrics.k8s.io", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "external.metrics.k8s.io/v1beta1", Version: "v1beta1"}}},
		},
	}

	t.Run("fresh groups seed missing group versions only", func(t *testing.T) {
		dir := t.TempDir()
		writeGroups(t, dir, groups)

		// apps/v1 already cached on disk.
		appsDir := filepath.Join(dir, "apps", "v1")
		if err := os.MkdirAll(appsDir, 0o755); err != nil {
			t.Fatalf("mkdir apps: %v", err)
		}
		if err := os.WriteFile(filepath.Join(appsDir, serverResourcesFile), []byte(`{"cached":true}`), 0o600); err != nil {
			t.Fatalf("write apps resources: %v", err)
		}

		seedEmptyResourceCaches(dir)

		// Missing group version got seeded with an empty resource list.
		b, err := os.ReadFile(filepath.Join(dir, "external.metrics.k8s.io", "v1beta1", serverResourcesFile))
		if err != nil {
			t.Fatalf("seeded file missing: %v", err)
		}
		var rl metav1.APIResourceList
		if err = json.Unmarshal(b, &rl); err != nil {
			t.Fatalf("decode seeded file: %v", err)
		}
		if rl.GroupVersion != "external.metrics.k8s.io/v1beta1" {
			t.Errorf("seeded GroupVersion = %q, want external.metrics.k8s.io/v1beta1", rl.GroupVersion)
		}
		if len(rl.APIResources) != 0 {
			t.Errorf("seeded resources = %d, want 0", len(rl.APIResources))
		}

		// Existing cache entries are left untouched.
		b, err = os.ReadFile(filepath.Join(appsDir, serverResourcesFile))
		if err != nil {
			t.Fatalf("read apps resources: %v", err)
		}
		if string(b) != `{"cached":true}` {
			t.Errorf("apps resources overwritten: %s", b)
		}
	})

	t.Run("stale groups are not seeded", func(t *testing.T) {
		dir := t.TempDir()
		writeGroups(t, dir, groups)

		sg := filepath.Join(dir, serverGroupsFile)
		stale := time.Now().Add(-discoveryTTL - time.Minute)
		if err := os.Chtimes(sg, stale, stale); err != nil {
			t.Fatalf("chtimes: %v", err)
		}

		seedEmptyResourceCaches(dir)

		if _, err := os.Stat(filepath.Join(dir, "external.metrics.k8s.io", "v1beta1", serverResourcesFile)); !os.IsNotExist(err) {
			t.Errorf("stale groups should not be seeded")
		}
	})

	t.Run("missing groups file is a no-op", func(t *testing.T) {
		dir := t.TempDir()

		seedEmptyResourceCaches(dir)

		if _, err := os.Stat(filepath.Join(dir, "external.metrics.k8s.io", "v1beta1", serverResourcesFile)); !os.IsNotExist(err) {
			t.Errorf("missing groups file should not be seeded")
		}
	})
}
