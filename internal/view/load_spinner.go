// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/client"
)

const (
	loadDelay  = 300 * time.Millisecond
	loadFrame  = 100 * time.Millisecond
	loadFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
)

// loadSpinner animates the flash bar while a view waits on its first load.
type loadSpinner struct {
	app   *App
	msg   string
	start time.Time
	stop  chan struct{}
	shown atomic.Bool
	done  atomic.Bool
}

func newLoadSpinner(app *App, msg string) *loadSpinner {
	s := loadSpinner{
		app:   app,
		msg:   msg,
		start: time.Now(),
		stop:  make(chan struct{}),
	}
	go s.run()

	return &s
}

func loadMsg(gvr *client.GVR, ns string) string {
	if client.IsClusterScoped(ns) {
		return fmt.Sprintf("Loading %s", gvr)
	}

	return fmt.Sprintf("Loading %s in %s", gvr, client.PrintNamespace(ns))
}

func (s *loadSpinner) run() {
	select {
	case <-s.stop:
		return
	case <-time.After(loadDelay):
	}

	t := time.NewTicker(loadFrame)
	defer t.Stop()
	for {
		s.app.QueueUpdateDraw(s.tick)
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}

func (s *loadSpinner) tick() {
	if s.done.Load() {
		return
	}

	s.shown.Store(true)
	elapsed := time.Since(s.start)
	frames := []rune(loadFrames)
	frame := frames[int(elapsed/loadFrame)%len(frames)]
	s.app.Flash().Infof("%c %s... %.1fs", frame, s.msg, elapsed.Seconds())
}

// end stops the animation and clears the spinner if it was showing.
func (s *loadSpinner) end() {
	if s.done.Swap(true) {
		return
	}

	close(s.stop)
	if s.shown.Load() {
		s.app.Flash().Clear()
	}
}
