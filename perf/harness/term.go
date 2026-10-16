package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

const (
	termCols  = 200
	termRows  = 50
	pollEvery = 2 * time.Millisecond
)

// chunk is one read from the pty, stamped on arrival.
type chunk struct {
	at time.Time
	n  int
}

// Term drives one k9s process through a pseudo terminal.
type Term struct {
	cmd   *exec.Cmd
	ptmx  *os.File
	vt    vt10x.Terminal
	start time.Time

	mx     sync.Mutex
	chunks []chunk
	total  int64
	done   chan struct{}
}

func startTerm(bin string, args, env []string) (*Term, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: termCols, Rows: termRows})
	if err != nil {
		return nil, err
	}

	t := Term{
		cmd:   cmd,
		ptmx:  ptmx,
		vt:    vt10x.New(vt10x.WithSize(termCols, termRows), vt10x.WithWriter(ptmx)),
		start: time.Now(),
		done:  make(chan struct{}),
	}
	go t.pump()

	return &t, nil
}

func (t *Term) pump() {
	defer close(t.done)

	r := bufio.NewReaderSize(t.ptmx, 1<<16)
	buf := make([]byte, 1<<16)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			t.mx.Lock()
			t.chunks = append(t.chunks, chunk{at: time.Now(), n: n})
			t.total += int64(n)
			t.mx.Unlock()
			_, _ = t.vt.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// Screen returns the current emulated screen as text.
func (t *Term) Screen() string {
	return t.vt.String()
}

// Send writes raw keys to the pty.
func (t *Term) Send(keys string) time.Time {
	at := time.Now()
	_, _ = io.WriteString(t.ptmx, keys)

	return at
}

// WaitFor polls the screen until fn matches or the timeout expires.
func (t *Term) WaitFor(timeout time.Duration, fn func(string) bool) (time.Time, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn(t.Screen()) {
			return time.Now(), nil
		}
		time.Sleep(pollEvery)
	}

	return time.Time{}, fmt.Errorf("timeout after %s; screen:\n%s", timeout, head(t.Screen(), 14))
}

// Bytes returns total bytes and chunks received after since.
func (t *Term) Bytes(since time.Time) (int64, []chunk) {
	t.mx.Lock()
	defer t.mx.Unlock()

	var (
		n  int64
		cc []chunk
	)
	for _, c := range t.chunks {
		if c.at.After(since) {
			n += int64(c.n)
			cc = append(cc, c)
		}
	}

	return n, cc
}

// FirstOutputAfter returns when the first byte arrived after at.
func (t *Term) FirstOutputAfter(at time.Time, timeout time.Duration) (time.Time, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		t.mx.Lock()
		for i := len(t.chunks) - 1; i >= 0 && t.chunks[i].at.After(at); i-- {
			if i == 0 || !t.chunks[i-1].at.After(at) {
				ts := t.chunks[i].at
				t.mx.Unlock()
				return ts, nil
			}
		}
		t.mx.Unlock()
		time.Sleep(pollEvery)
	}

	return time.Time{}, errors.New("no output")
}

// Quiet waits until no output arrived for gap.
func (t *Term) Quiet(gap, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		t.mx.Lock()
		last := t.start
		if len(t.chunks) > 0 {
			last = t.chunks[len(t.chunks)-1].at
		}
		t.mx.Unlock()
		if time.Since(last) >= gap {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Usage returns cumulative CPU time and RSS of the k9s process.
func (t *Term) Usage() (time.Duration, int64, error) {
	if cpu, rss, err := procUsage(t.cmd.Process.Pid); err == nil {
		return cpu, rss, nil
	}
	out, err := exec.Command("ps", "-o", "time=,rss=", "-p", strconv.Itoa(t.cmd.Process.Pid)).Output()
	if err != nil {
		return 0, 0, err
	}
	ff := strings.Fields(string(out))
	if len(ff) != 2 {
		return 0, 0, fmt.Errorf("bad ps output %q", out)
	}
	cpu, err := parseCPUTime(ff[0])
	if err != nil {
		return 0, 0, err
	}
	rss, err := strconv.ParseInt(ff[1], 10, 64)

	return cpu, rss * 1024, err
}

// Linux exposes CPU time in clock ticks and RSS in pages via /proc.
const (
	procTick = 10 * time.Millisecond // USER_HZ=100
	procPage = 4096

	// Field indices counted from state, the first field after "(comm)".
	statUtime = 11
	statStime = 12
	statRSS   = 21
)

// procUsage reads CPU time and RSS from /proc; ps only gives whole seconds
// of CPU time on Linux.
func procUsage(pid int) (time.Duration, int64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	ff := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
	if len(ff) <= statRSS {
		return 0, 0, fmt.Errorf("bad stat %q", raw)
	}

	var ticks int64
	for _, f := range []string{ff[statUtime], ff[statStime]} {
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return 0, 0, err
		}
		ticks += v
	}
	pages, err := strconv.ParseInt(ff[statRSS], 10, 64)

	return time.Duration(ticks) * procTick, pages * procPage, err
}

// Close quits k9s and reaps it.
func (t *Term) Close() {
	t.Send("\x1b")
	t.Send(":q\r")
	select {
	case <-t.done:
	case <-time.After(3 * time.Second):
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	_ = t.ptmx.Close()
}

// parseCPUTime parses ps time in [[hh:]mm:]ss.cc form.
func parseCPUTime(s string) (time.Duration, error) {
	var total float64
	for _, p := range strings.Split(s, ":") {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, err
		}
		total = total*60 + v
	}

	return time.Duration(total * float64(time.Second)), nil
}

var countRX = regexp.MustCompile(`\[([0-9,]+)\]`)

// titleCount extracts the row count of a table title like "pods(all)[5,050]".
func titleCount(screen, prefix string) (int, bool) {
	for _, line := range strings.Split(screen, "\n") {
		i := strings.Index(line, prefix)
		if i < 0 {
			continue
		}
		m := countRX.FindStringSubmatch(line[i:])
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
		if err == nil {
			return n, true
		}
	}

	return 0, false
}

func head(s string, n int) string {
	ll := strings.SplitN(s, "\n", n+1)
	if len(ll) > n {
		ll = ll[:n]
	}

	return strings.Join(ll, "\n")
}
