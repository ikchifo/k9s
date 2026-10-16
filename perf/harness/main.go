// Command harness drives k9s through a pty against KWOK clusters and
// records user-facing timings. See perf/README.md.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type clusterSpec struct {
	pods    int
	deploys int
}

var clusters = map[string]clusterSpec{
	"s": {pods: 500, deploys: 5},
	"m": {pods: 5000, deploys: 50},
	"l": {pods: 20000, deploys: 200},
}

// Stat summarizes the samples of one metric.
type Stat struct {
	Size   string    `json:"size"`
	Metric string    `json:"metric"`
	Unit   string    `json:"unit"`
	N      int       `json:"n"`
	Median float64   `json:"median"`
	P90    float64   `json:"p90"`
	Min    float64   `json:"min"`
	Max    float64   `json:"max"`
	Raw    []float64 `json:"raw"`
}

// Result is the file everything else is judged against.
type Result struct {
	Label     string    `json:"label"`
	Binary    string    `json:"binary"`
	Started   time.Time `json:"started"`
	Host      string    `json:"host"`
	Runs      int       `json:"runs"`
	Stats     []Stat    `json:"stats"`
	Errors    []string  `json:"errors,omitempty"`
	Violation []string  `json:"violations,omitempty"`
}

func main() {
	var (
		bin     = flag.String("bin", "", "k9s binaries under test, comma separated; several run interleaved")
		label   = flag.String("label", "baseline", "result labels, one per binary")
		sizes   = flag.String("sizes", "s,m,l", "cluster sizes to run")
		runs    = flag.Int("runs", 5, "iterations per scenario")
		out     = flag.String("out", "perf/results", "output directory")
		kube    = flag.String("kube", "perf/.kube", "directory with <cluster>.yaml kubeconfigs")
		idle    = flag.Duration("idle", 20*time.Second, "idle measurement window")
		only    = flag.String("only", "startup,session,ctx", "scenarios to run")
		budgets = flag.String("budgets", "", "budget file; exit 1 if exceeded")
		prof    = flag.String("prof", "", "save idle CPU profiles here (needs K9S_PROF_ADDR and a profiling build)")
	)
	flag.Parse()

	if *bin == "" {
		log.Fatal("-bin is required")
	}
	bins, labels := strings.Split(*bin, ","), strings.Split(*label, ",")
	if len(bins) != len(labels) {
		log.Fatal("-bin and -label need the same number of entries")
	}

	work, err := os.MkdirTemp("", "k9s-harness-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(work)

	results := make([]*Result, len(bins))
	for i, b := range bins {
		abs, err := filepath.Abs(b)
		if err != nil {
			log.Fatal(err)
		}
		results[i] = &Result{
			Label:   labels[i],
			Binary:  abs,
			Started: time.Now().UTC(),
			Host:    fmt.Sprintf("%s/%s ncpu=%d", runtime.GOOS, runtime.GOARCH, runtime.NumCPU()),
			Runs:    *runs,
		}
	}

	for _, size := range strings.Split(*sizes, ",") {
		spec, ok := clusters[size]
		if !ok {
			log.Fatalf("unknown size %q", size)
		}
		envs := make([]Env, len(results))
		for i, r := range results {
			envs[i] = newEnv(r.Binary, size, spec, *kube, work, *idle)
			envs[i].ProfDir = *prof
		}
		recs := runSize(envs, *runs, *only, results)
		for i, r := range results {
			r.Stats = append(r.Stats, summarize(size, recs[i])...)
		}
	}

	failed := false
	for _, res := range results {
		if *budgets != "" {
			res.Violation = checkBudgets(*budgets, res.Stats)
		}
		if err := write(*out, *res); err != nil {
			log.Fatal(err)
		}
		printTable(*res)

		for _, v := range res.Violation {
			log.Printf("BUDGET EXCEEDED: %s: %s", res.Label, v)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func newEnv(bin, size string, spec clusterSpec, kube, work string, idle time.Duration) Env {
	other := "s"
	if size == "s" {
		other = "m"
	}
	kcfg, _ := filepath.Abs(filepath.Join(kube, "k9s-"+size+".yaml"))
	okcfg, _ := filepath.Abs(filepath.Join(kube, "k9s-"+other+".yaml"))

	return Env{
		Bin:        bin,
		Size:       size,
		Context:    currentContext(kcfg),
		Kubeconfig: kcfg,
		Pods:       spec.pods,
		Deploys:    spec.deploys,
		Other:      currentContext(okcfg),
		OtherKcfg:  okcfg,
		WorkDir:    work,
		IdleWindow: idle,
	}
}

// currentContext reads current-context from a kubeconfig, so clusters made
// with any kwokctl runtime (kind-kwok-*, kwok-*) work.
func currentContext(kcfg string) string {
	const key = "current-context:"

	raw, err := os.ReadFile(kcfg)
	if err != nil {
		log.Fatalf("kubeconfig: %v", err)
	}
	for line := range strings.Lines(string(raw)) {
		if v, ok := strings.CutPrefix(line, key); ok {
			return strings.TrimSpace(v)
		}
	}
	log.Fatalf("no %s in %s", key, kcfg)

	return ""
}

// runSize interleaves the binaries run by run, flipping their order on odd
// runs, so machine noise lands on all of them alike.
func runSize(envs []Env, runs int, only string, results []*Result) []*Recorder {
	recs := make([]*Recorder, len(envs))
	for i := range recs {
		recs[i] = newRecorder()
	}
	scenarios := []struct {
		name string
		fn   func(Env, *Recorder, string) error
	}{
		{"startup", runStartup},
		{"session", runSession},
		{"ctx", runContextSwitch},
	}

	for i := range runs {
		for _, s := range scenarios {
			if !strings.Contains(only, s.name) {
				continue
			}
			for k := range envs {
				j := k
				if i%2 == 1 {
					j = len(envs) - 1 - k
				}
				run := fmt.Sprintf("%s-%s-%s-%d", results[j].Label, envs[j].Size, s.name, i)
				log.Printf("run %s", run)
				if err := s.fn(envs[j], recs[j], run); err != nil {
					msg := fmt.Sprintf("%s: %v", run, err)
					log.Print(msg)
					results[j].Errors = append(results[j].Errors, msg)
				}
			}
		}
	}

	return recs
}

func summarize(size string, rec *Recorder) []Stat {
	out := make([]Stat, 0, len(rec.order))
	for _, m := range rec.order {
		vv := append([]float64(nil), rec.samples[m]...)
		sort.Float64s(vv)
		out = append(out, Stat{
			Size:   size,
			Metric: m,
			Unit:   rec.units[m],
			N:      len(vv),
			Median: quantile(vv, 0.5),
			P90:    quantile(vv, 0.9),
			Min:    vv[0],
			Max:    vv[len(vv)-1],
			Raw:    rec.samples[m],
		})
	}

	return out
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo := int(pos)
	if lo+1 >= len(sorted) {
		return sorted[lo]
	}
	frac := pos - float64(lo)

	return sorted[lo]*(1-frac) + sorted[lo+1]*frac
}

func write(dir string, res Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := filepath.Join(dir, res.Label)

	bb, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(base+".json", bb, 0o644); err != nil {
		return err
	}

	f, err := os.Create(base + ".csv")
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	_ = w.Write([]string{"label", "size", "metric", "unit", "n", "median", "p90", "min", "max"})
	for _, s := range res.Stats {
		_ = w.Write([]string{
			res.Label, s.Size, s.Metric, s.Unit, strconv.Itoa(s.N),
			fmtF(s.Median), fmtF(s.P90), fmtF(s.Min), fmtF(s.Max),
		})
	}
	w.Flush()

	return w.Error()
}

func printTable(res Result) {
	fmt.Printf("\n%-5s %-26s %12s %12s %6s\n", "size", "metric", "median", "p90", "unit")
	for _, s := range res.Stats {
		fmt.Printf("%-5s %-26s %12s %12s %6s\n", s.Size, s.Metric, fmtF(s.Median), fmtF(s.P90), s.Unit)
	}
	if len(res.Errors) > 0 {
		fmt.Printf("\n%d errors\n", len(res.Errors))
	}
}

func fmtF(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}
