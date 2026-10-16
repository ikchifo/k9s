package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	profAddrEnv = profEnvPrefix + "ADDR"
	stepProfile = 5 * time.Second
)

// idleWait sleeps through the idle window. With -prof and a profiling build,
// it grabs a CPU profile of that same window instead.
func idleWait(e Env) error {
	if !e.profiling() {
		time.Sleep(e.IdleWindow)
		return nil
	}

	return e.cpuProfile("idle", e.IdleWindow)
}

// profiled runs a step while a CPU profile of stepProfile covers it.
func profiled(name string, step func(*Term, Env, *Recorder) error) func(*Term, Env, *Recorder) error {
	return func(t *Term, e Env, rec *Recorder) error {
		if !e.profiling() {
			return step(t, e, rec)
		}

		errc := make(chan error, 1)
		go func() { errc <- e.cpuProfile(name, stepProfile) }()
		time.Sleep(100 * time.Millisecond)

		if err := step(t, e, rec); err != nil {
			return err
		}

		return <-errc
	}
}

func (e Env) profiling() bool {
	return e.ProfDir != "" && os.Getenv(profAddrEnv) != ""
}

func (e Env) cpuProfile(name string, d time.Duration) error {
	secs := int(d.Seconds())
	url := fmt.Sprintf("http://%s/debug/pprof/profile?seconds=%d", os.Getenv(profAddrEnv), secs)
	cl := http.Client{Timeout: d + 30*time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := os.MkdirAll(e.ProfDir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(e.ProfDir, e.run+"."+name+".cpu.pb.gz"))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)

	return err
}
