// tracestat: per-goroutine state time for goroutines whose stack mentions a pattern.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/exp/trace"
)

type acc struct {
	name  string
	state map[string]trace.Time
	since trace.Time
	cur   string
}

func main() {
	f, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	r, err := trace.NewReader(f)
	if err != nil {
		panic(err)
	}
	gs := map[trace.GoID]*acc{}
	var start, end trace.Time
	for {
		ev, err := r.ReadEvent()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		if start == 0 {
			start = ev.Time()
		}
		end = ev.Time()
		if ev.Kind() != trace.EventStateTransition {
			continue
		}
		st := ev.StateTransition()
		if st.Resource.Kind != trace.ResourceGoroutine {
			continue
		}
		id := st.Resource.Goroutine()
		g := gs[id]
		if g == nil {
			g = &acc{state: map[string]trace.Time{}, since: ev.Time()}
			gs[id] = g
		}
		_, to := st.Goroutine()
		if g.cur != "" {
			g.state[g.cur] += ev.Time() - g.since
		}
		g.cur, g.since = to.String(), ev.Time()
		if g.name == "" {
			for fr := range st.Stack.Frames() {
				if strings.Contains(fr.Func, os.Args[2]) {
					g.name = fr.Func
				}
			}
		}
	}
	fmt.Printf("trace span %.2fs\n", float64(end-start)/1e9)
	var out []string
	for id, g := range gs {
		if g.name == "" {
			continue
		}
		var parts []string
		for k, v := range g.state {
			parts = append(parts, fmt.Sprintf("%s=%.2fs", k, float64(v)/1e9))
		}
		sort.Strings(parts)
		out = append(out, fmt.Sprintf("g%d %s %s", id, g.name, strings.Join(parts, " ")))
	}
	sort.Strings(out)
	fmt.Println(strings.Join(out, "\n"))
}
