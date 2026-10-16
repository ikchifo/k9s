// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dialog

import (
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
)

// ShowPicker pops a centered fuzzy picker over items.
func ShowPicker(styles *config.Dialog, pages *ui.Pages, title string, items []string, action func(string)) {
	p := ui.NewModalPicker("<"+title+">", items, styles.LabelFgColor.String())
	p.SetColors(
		styles.FgColor.Color(),
		styles.BgColor.Color(),
		styles.ButtonFocusFgColor.Color(),
		styles.ButtonFocusBgColor.Color(),
	)
	p.SetCancelFunc(func() {
		dismiss(pages)
	})
	p.SetSelectedFunc(func(item string) {
		dismiss(pages)
		action(item)
	})

	pages.AddPage(dialogKey, p, false, false)
	pages.ShowPage(dialogKey)
}
