// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const (
	progressMinWidth = 40
	progressHChrome  = 14
	progressVChrome  = 4
	progressFrame    = 100 * time.Millisecond
	progressDoneMark = "✓"
	progressDimTag   = "[::d]"
)

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// ModalProgress is a centered step tracker with a spinner on the running step.
type ModalProgress struct {
	*tview.Box

	steps   []string
	current atomic.Int32
	start   time.Time
	fg      tcell.Color
	doneFg  tcell.Color
}

// NewModalProgress returns a tracker with the first step running.
func NewModalProgress(title string, steps []string) *ModalProgress {
	m := ModalProgress{
		Box:    tview.NewBox(),
		steps:  steps,
		start:  time.Now(),
		fg:     tcell.ColorDefault,
		doneFg: tcell.ColorDefault,
	}
	m.SetBorder(true)
	m.SetTitle(title)

	return &m
}

// SetColors styles the tracker.
func (m *ModalProgress) SetColors(fg, bg, doneFg tcell.Color) *ModalProgress {
	m.fg, m.doneFg = fg, doneFg
	m.SetBackgroundColor(bg)
	m.SetBorderColor(fg)
	m.SetTitleColor(fg)

	return m
}

// Advance marks the running step done. Safe to call from any goroutine.
func (m *ModalProgress) Advance() {
	m.current.Add(1)
}

// Draw draws the tracker centered on screen.
func (m *ModalProgress) Draw(screen tcell.Screen) {
	sw, sh := screen.Size()

	width := progressMinWidth
	for _, s := range m.steps {
		width = max(width, len(s)+progressHChrome)
	}
	width = min(width, sw-2)
	height := len(m.steps) + progressVChrome

	m.SetRect((sw-width)/2, (sh-height)/2, width, height)
	m.DrawForSubclass(screen, m)

	x, y, w, _ := m.GetInnerRect()
	cur := int(m.current.Load())
	for i, s := range m.steps {
		m.drawStep(screen, i-cur, tview.Escape(s), x+1, y+1+i, w-2)
	}
}

// drawStep renders one step; pos < 0 is done, 0 is running, > 0 is pending.
func (m *ModalProgress) drawStep(screen tcell.Screen, pos int, label string, x, y, w int) {
	if pos < 0 {
		tview.Print(screen, progressDoneMark+" "+label, x, y, w, tview.AlignLeft, m.doneFg)
		return
	}
	if pos > 0 {
		tview.Print(screen, progressDimTag+"  "+label, x, y, w, tview.AlignLeft, m.fg)
		return
	}

	elapsed := time.Since(m.start)
	frame := spinnerFrames[int(elapsed/progressFrame)%len(spinnerFrames)]
	tview.Print(screen, string(frame)+" "+label, x, y, w, tview.AlignLeft, m.fg)
	tview.Print(screen, fmt.Sprintf("%.1fs", elapsed.Seconds()), x, y, w, tview.AlignRight, m.fg)
}
