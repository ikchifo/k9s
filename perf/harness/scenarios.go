package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	keyEsc      = "\x1b"
	keyEnter    = "\r"
	keyPageDown = "\x06"

	loadTimeout   = 3 * time.Minute
	actionTimeout = 60 * time.Second
	burstGap      = 250 * time.Millisecond
	settleGap     = 1200 * time.Millisecond
	refreshRate   = 2 * time.Second

	podsTitle   = "pods(all)"
	deployTitle = "deployments(all)"
	filterText  = "svc-1-"
	filterRows  = 100
	scrollPages = 10

	profEnvPrefix = "K9S_PROF_"
)

// Env describes one target cluster and the k9s binary under test.
type Env struct {
	Bin        string
	Size       string
	Context    string
	Kubeconfig string
	Pods       int
	Deploys    int
	Other      string
	OtherKcfg  string
	WorkDir    string
	IdleWindow time.Duration
	ProfDir    string
	run        string
}

// k9sEnv builds an isolated environment so runs don't share k9s state.
func (e Env) k9sEnv(kcfg, run string) []string {
	cfgDir := filepath.Join(e.WorkDir, "cfg-"+run)
	_ = os.RemoveAll(cfgDir)
	_ = os.MkdirAll(cfgDir, 0o755)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(k9sConfig), 0o644)
	for _, ctx := range []string{e.Context, e.Other} {
		dir := filepath.Join(cfgDir, "clusters", ctx, ctx)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(ctxConfig, ctx)), 0o644)
	}

	env := []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=en_US.UTF-8",
		"KUBECONFIG=" + kcfg,
		"KUBECACHEDIR=" + filepath.Join(e.WorkDir, "kubecache"),
		"K9S_CONFIG_DIR=" + cfgDir,
		"K9S_LOGS_DIR=" + cfgDir,
	}

	// Profiling knobs for builds made with perf/prof/build.sh.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, profEnvPrefix) {
			env = append(env, kv)
		}
	}

	return env
}

const k9sConfig = `k9s:
  refreshRate: 2
  skipLatestRevCheck: true
  ui:
    splashless: true
    enableMouse: false
`

const ctxConfig = `k9s:
  cluster: %[1]s
  namespace:
    active: all
    favorites: [all, default]
  view:
    active: pods
`

// Recorder collects samples for one size.
type Recorder struct {
	samples map[string][]float64
	units   map[string]string
	order   []string
}

func newRecorder() *Recorder {
	return &Recorder{samples: map[string][]float64{}, units: map[string]string{}}
}

func (r *Recorder) add(metric, unit string, v float64) {
	if _, ok := r.samples[metric]; !ok {
		r.order = append(r.order, metric)
	}
	r.samples[metric] = append(r.samples[metric], v)
	r.units[metric] = unit
}

func (r *Recorder) ms(metric string, d time.Duration) {
	r.add(metric, "ms", float64(d.Microseconds())/1000)
}

func countAtLeast(prefix string, n int) func(string) bool {
	return func(s string) bool {
		c, ok := titleCount(s, prefix)
		return ok && c >= n
	}
}

func countEquals(prefix string, n int) func(string) bool {
	return func(s string) bool {
		c, ok := titleCount(s, prefix)
		return ok && c == n
	}
}

// runStartup measures launch to first byte, first frame and fully loaded table.
func runStartup(e Env, rec *Recorder, run string) error {
	t, err := startTerm(e.Bin, []string{"--context", e.Context, "-c", "pods", "-A"}, e.k9sEnv(e.Kubeconfig, run))
	if err != nil {
		return err
	}
	defer t.Close()

	first, err := t.FirstOutputAfter(t.start.Add(-time.Second), loadTimeout)
	if err != nil {
		return err
	}
	frame, err := t.WaitFor(loadTimeout, func(s string) bool { return strings.Contains(s, podsTitle) })
	if err != nil {
		return err
	}
	full, err := t.WaitFor(loadTimeout, countAtLeast(podsTitle, e.Pods))
	if err != nil {
		return err
	}

	rec.ms("startup_first_byte", first.Sub(t.start))
	rec.ms("startup_first_frame", frame.Sub(t.start))
	rec.ms("startup_full_table", full.Sub(t.start))

	return nil
}

// runSession runs the interactive scenarios inside one k9s process.
func runSession(e Env, rec *Recorder, run string) error {
	t, err := startTerm(e.Bin, []string{"--context", e.Context, "-c", "pods", "-A"}, e.k9sEnv(e.Kubeconfig, run))
	if err != nil {
		return err
	}
	defer t.Close()

	if _, err := t.WaitFor(loadTimeout, countAtLeast(podsTitle, e.Pods)); err != nil {
		return err
	}
	t.Quiet(settleGap, 10*time.Second)

	e.run = run
	steps := []func(*Term, Env, *Recorder) error{
		measureIdle,
		measureViewSwitch,
		profiled("filter", measureFilter),
		measureScroll,
		profiled("sort", measureSort),
	}
	for _, step := range steps {
		if err := step(t, e, rec); err != nil {
			return err
		}
	}

	return nil
}

func measureIdle(t *Term, e Env, rec *Recorder) error {
	cpu0, _, err := t.Usage()
	if err != nil {
		return err
	}
	from := time.Now()
	if err := idleWait(e); err != nil {
		return err
	}
	cpu1, rss, err := t.Usage()
	if err != nil {
		return err
	}
	wall := time.Since(from)

	_, cc := t.Bytes(from)
	bytes := sum(groupBursts(cc))
	ticks := wall.Seconds() / refreshRate.Seconds()
	rec.add("idle_cpu_pct", "%", 100*float64(cpu1-cpu0)/float64(wall))
	rec.add("idle_rss", "MiB", float64(rss)/(1<<20))
	rec.add("refresh_cpu", "ms", float64((cpu1-cpu0).Microseconds())/1000/ticks)
	rec.add("refresh_bytes", "B", float64(bytes)/ticks)

	return nil
}

func measureViewSwitch(t *Term, e Env, rec *Recorder) error {
	at := t.Send(":deploy" + keyEnter)
	done, err := t.WaitFor(actionTimeout, countAtLeast(deployTitle, e.Deploys))
	if err != nil {
		return err
	}
	rec.ms("switch_to_deploy", done.Sub(at))
	t.Quiet(settleGap, 10*time.Second)

	at = t.Send(":pods" + keyEnter)
	done, err = t.WaitFor(actionTimeout, countAtLeast(podsTitle, e.Pods))
	if err != nil {
		return err
	}
	rec.ms("switch_to_pods", done.Sub(at))
	t.Quiet(settleGap, 10*time.Second)

	return nil
}

func measureFilter(t *Term, _ Env, rec *Recorder) error {
	t.Send("/")
	if _, err := t.WaitFor(actionTimeout, func(s string) bool { return strings.Contains(s, "/ ") }); err != nil {
		return err
	}

	var last time.Time
	for i := 1; i <= len(filterText); i++ {
		typed := filterText[:i]
		at := t.Send(filterText[i-1 : i])
		done, err := t.WaitFor(actionTimeout, func(s string) bool { return strings.Contains(s, "/ "+typed) })
		if err != nil {
			return err
		}
		rec.ms("filter_key_echo", done.Sub(at))
		last = at
	}

	done, err := t.WaitFor(actionTimeout, countEquals(podsTitle, filterRows))
	if err != nil {
		return err
	}
	rec.ms("filter_apply", done.Sub(last))

	t.Send(keyEnter)
	t.Quiet(settleGap, 10*time.Second)
	t.Send(keyEsc)
	if _, err := t.WaitFor(actionTimeout, func(s string) bool {
		return !strings.Contains(s, "</"+filterText+">")
	}); err != nil {
		return err
	}
	t.Quiet(settleGap, 10*time.Second)

	return nil
}

func measureScroll(t *Term, _ Env, rec *Recorder) error {
	for range scrollPages {
		before := firstRow(t.Screen())
		at := t.Send(keyPageDown)
		done, err := t.WaitFor(actionTimeout, func(s string) bool { return firstRow(s) != before })
		if err != nil {
			return err
		}
		rec.ms("scroll_page_down", done.Sub(at))
		time.Sleep(50 * time.Millisecond)
	}
	t.Quiet(settleGap, 10*time.Second)

	return nil
}

func measureSort(t *Term, _ Env, rec *Recorder) error {
	for _, want := range []string{"AGE↑", "AGE↓"} {
		at := t.Send("A")
		done, err := t.WaitFor(actionTimeout, func(s string) bool { return strings.Contains(s, want) })
		if err != nil {
			return err
		}
		rec.ms("sort_toggle", done.Sub(at))
		t.Quiet(settleGap, 10*time.Second)
	}

	return nil
}

// runContextSwitch starts on the other cluster and switches to the target.
func runContextSwitch(e Env, rec *Recorder, run string) error {
	kcfg := e.OtherKcfg + string(os.PathListSeparator) + e.Kubeconfig
	t, err := startTerm(e.Bin, []string{"--context", e.Other, "-c", "pods", "-A"}, e.k9sEnv(kcfg, run))
	if err != nil {
		return err
	}
	defer t.Close()

	if _, err := t.WaitFor(loadTimeout, func(s string) bool {
		_, ok := titleCount(s, podsTitle)
		return ok && strings.Contains(s, "Context: "+e.Other)
	}); err != nil {
		return err
	}
	t.Quiet(settleGap, 20*time.Second)

	at := t.Send(":ctx " + e.Context + keyEnter)
	done, err := t.WaitFor(loadTimeout, func(s string) bool {
		return strings.Contains(s, "Context: "+e.Context) && countAtLeast(podsTitle, e.Pods)(s)
	})
	if err != nil {
		return err
	}
	rec.ms("context_switch", done.Sub(at))

	return nil
}

// firstRow returns the first data row below the table header.
func firstRow(screen string) string {
	ll := strings.Split(screen, "\n")
	for i, l := range ll {
		if strings.Contains(l, "NAMESPACE") && i+1 < len(ll) {
			return ll[i+1]
		}
	}

	return ""
}

// groupBursts sums chunk sizes into bursts separated by burstGap.
func groupBursts(cc []chunk) []int64 {
	var (
		out  []int64
		prev time.Time
	)
	for _, c := range cc {
		if len(out) == 0 || c.at.Sub(prev) > burstGap {
			out = append(out, 0)
		}
		out[len(out)-1] += int64(c.n)
		prev = c.at
	}

	return out
}

func sum(vv []int64) int64 {
	var s int64
	for _, v := range vv {
		s += v
	}

	return s
}
