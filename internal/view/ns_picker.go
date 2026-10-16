// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"log/slog"
	"maps"
	"slices"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
)

const nsPickerTitle = "Switch Namespace"

func (a *App) nsPickerCmd(evt *tcell.EventKey) *tcell.EventKey {
	if a.InCmdMode() {
		return evt
	}
	if a.Conn() == nil {
		a.Flash().Warn("No connection available")
		return nil
	}

	go func() {
		names := a.namespaceNames()
		a.QueueUpdateDraw(func() {
			a.showNSPicker(names)
		})
	}()

	return nil
}

// namespaceNames lists namespaces, falling back to favorites when the user
// can't list them all.
func (a *App) namespaceNames() []string {
	nss, err := a.Conn().ValidNamespaceNames()
	if err != nil || len(nss) == 0 {
		slog.Debug("Namespace picker using favorites", slogs.Error, err)
		return a.Config.FavNamespaces()
	}

	names := slices.Collect(maps.Keys(nss))

	return append(names, client.NamespaceAll)
}

func (a *App) showNSPicker(names []string) {
	if len(names) == 0 {
		a.Flash().Warn("No namespaces found")
		return
	}

	current := nsKey(a.Config.ActiveNamespace())
	ranked, recent := loadNSRecency(a.Config.ActiveContextName()).rank(names, current)
	d := a.Styles.Dialog()
	dialog.ShowPicker(&d, a.Content.Pages, nsPickerTitle, ranked, recent, func(ns string) {
		if ns == current {
			return
		}
		a.useNS(ns)
	})
}

// useNS re-scopes the current resource view to ns, or just sets the active
// namespace when the view isn't namespaced.
func (a *App) useNS(ns string) {
	if line, ok := a.rescopedCmd(ns); ok {
		a.gotoResource(line, "", true, true)
		return
	}

	if err := a.switchNS(ns); err != nil {
		a.Flash().Err(err)
		return
	}
	a.Flash().Infof("Namespace set to %q", ns)
}

// rescopedCmd returns the current view's command scoped to ns.
func (a *App) rescopedCmd(ns string) (string, bool) {
	v, ok := a.Content.Top().(ResourceViewer)
	if !ok || v.GetTable().Path != "" {
		return "", false
	}
	if nsd, err := dao.MetaAccess.IsNamespaced(v.GVR()); !nsd || err != nil {
		return "", false
	}

	line, ok := a.cmdHistory.Top()
	if !ok {
		return "", false
	}
	p := cmd.NewInterpreter(line)
	p.SwitchNS(ns)

	return p.GetLine(), true
}
