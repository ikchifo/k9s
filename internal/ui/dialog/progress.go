// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dialog

import (
	"time"

	"github.com/derailed/k9s/internal/ui"
)

const (
	progressKey    = "progress"
	progressRedraw = 100 * time.Millisecond
)

// ShowProgress pops an animated step tracker. Call the returned func on the UI thread to close it.
func ShowProgress(app *ui.App, pages *ui.Pages, title string, steps []string) (p *ui.ModalProgress, closeFn func()) {
	styles := app.Styles.Dialog()
	p = ui.NewModalProgress("<"+title+">", steps)
	p.SetColors(styles.FgColor.Color(), styles.BgColor.Color(), styles.LabelFgColor.Color())
	pages.AddPage(progressKey, p, false, false)
	pages.ShowPage(progressKey)

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(progressRedraw)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				app.QueueUpdateDraw(func() {})
			}
		}
	}()

	closeFn = func() {
		close(stop)
		pages.RemovePage(progressKey)
	}

	return p, closeFn
}
