// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model1

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

// sortRefSorter reproduces the original Less-on-raw-fields sorter. It sorts a
// clone with the exact pre-key algorithm so tests can assert the key-based
// Sort yields the very same row order.
type sortRefSorter struct {
	ee         []RowEvent
	index      int
	isNumber   bool
	isDuration bool
	isCapacity bool
	asc        bool
}

func (s sortRefSorter) Len() int { return len(s.ee) }

func (s sortRefSorter) Swap(i, j int) {
	s.ee[i], s.ee[j] = s.ee[j], s.ee[i]
}

func (s sortRefSorter) Less(i, j int) bool {
	less := Less(s.isNumber, s.isDuration, s.isCapacity, s.ee[i].Row.ID, s.ee[j].Row.ID, s.ee[i].Row.Fields[s.index], s.ee[j].Row.Fields[s.index])
	if s.asc {
		return less
	}

	return !less
}

var sortValuePools = map[string][]string{
	"string":   {"", "n/a", "Running", "Pending", "Evicted", "Completed", "svc", "pod-1", "pod-01", "Pod-9", "a", "z"},
	"number":   {"", "n/a", "0", "1", "2", "10", "-5", "999", "42", "1234", "1,234", "1000", "1,000"},
	"duration": {"", "n/a", "0s", "1m", "60s", "3m", "5h", "2d22h3m50s", "1y6d", "10s", "9999d", "abc", "-5m"},
	"capacity": {"", "  ", "0", "1Ki", "1024", "500Mi", "1Gi", "1.1G", "10Gi", "0.5Ti", "0.1Ei", "12e6", "1234", "-1Gi"},
}

func pick(rng *rand.Rand, pp []string) string {
	return pp[rng.Intn(len(pp))]
}

// Sort must keep the exact row order of the old Less-based sort, including
// tie-breaks on id, ascending/descending and unparsable/equal values.
func TestSortMatchesLessSort(t *testing.T) {
	modes := []struct {
		name       string
		col        int
		isNumber   bool
		isDuration bool
		isCapacity bool
	}{
		{"string", 5, false, false, false},
		{"number", 3, true, false, false},
		{"duration", 2, false, true, false},
		{"capacity", 4, false, false, true},
	}

	for seed := range 25 {
		rng := rand.New(rand.NewSource(int64(seed))) //nolint:gosec // deterministic fixture data
		n := rng.Intn(200)
		if seed < 5 {
			n = seed
		}
		ee := make([]RowEvent, 0, n)
		for i := range n {
			ns := fmt.Sprintf("ns-%d", rng.Intn(4))
			ee = append(ee, RowEvent{Row: Row{
				ID: ns + "/" + fmt.Sprintf("pod-%05d", i),
				Fields: Fields{
					ns,
					fmt.Sprintf("pod-%05d", i),
					pick(rng, sortValuePools["duration"]),
					pick(rng, sortValuePools["number"]),
					pick(rng, sortValuePools["capacity"]),
					pick(rng, sortValuePools["string"]),
				},
			}})
		}

		for _, m := range modes {
			for _, asc := range []bool{true, false} {
				got := NewRowEventsWithEvts(ee...)
				ref := NewRowEventsWithEvts(ee...)
				refSorter := sortRefSorter{ee: ref.events, index: m.col, isNumber: m.isNumber, isDuration: m.isDuration, isCapacity: m.isCapacity, asc: asc}
				sort.Sort(refSorter)
				ref.reindex()

				got.Sort("", m.col, m.isDuration, m.isNumber, m.isCapacity, asc)

				assert.Equal(t, ref.events, got.events, "seed=%d mode=%s asc=%t", seed, m.name, asc)
				assert.Equal(t, ref.index, got.index, "seed=%d mode=%s asc=%t", seed, m.name, asc)
			}
		}
	}
}

const benchRows = 20_000

// benchEvents mirrors informer model order: namespace-major blocks of
// ascending pod names, like the 1000-pods x 20-namespaces fixture.
func benchEvents() []RowEvent {
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic fixture data
	suffixes := []string{"Mi", "Gi", "Ti"}

	ee := make([]RowEvent, 0, benchRows)
	for i := range benchRows {
		ns, name := fmt.Sprintf("team-%02d", i/1000), fmt.Sprintf("svc-%05d", i%1000)
		ee = append(ee, RowEvent{Row: Row{
			ID: ns + "/" + name,
			Fields: Fields{
				ns,
				name,
				benchAge(rng.Int63n(30 * 24 * 3600)),
				strconv.Itoa(rng.Intn(90)),
				fmt.Sprintf("%d%s", rng.Int63n(1024), suffixes[rng.Intn(3)]),
				"Running",
			},
		}})
	}

	return ee
}

func benchAge(secs int64) string {
	switch d, h, m := secs/86400, secs%86400/3600, secs%3600/60; {
	case d > 0:
		return strconv.FormatInt(d, 10) + "d"
	case h > 0:
		return strconv.FormatInt(h, 10) + "h"
	case m > 0:
		return strconv.FormatInt(m, 10) + "m"
	default:
		return strconv.FormatInt(secs, 10) + "s"
	}
}

// benchSort sorts from model order, as every refresh tick does on the
// freshly cloned data.
func benchSort(b *testing.B, col int, isDuration, isNumber, isCapacity bool) {
	ee := benchEvents()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		re := NewRowEventsWithEvts(ee...)
		b.StartTimer()
		re.Sort("", col, isDuration, isNumber, isCapacity, true)
	}
}

func BenchmarkSortAge(b *testing.B)      { benchSort(b, 2, true, false, false) }
func BenchmarkSortRestarts(b *testing.B) { benchSort(b, 3, false, true, false) }
func BenchmarkSortCapacity(b *testing.B) { benchSort(b, 4, false, false, true) }
func BenchmarkSortName(b *testing.B)     { benchSort(b, 1, false, false, false) }
