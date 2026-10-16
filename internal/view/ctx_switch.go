// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"errors"
	"log/slog"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/k9s/internal/view/cmd"
)

const switchTitle = "Switching Context"

// switchAsync runs the network-bound part of a context switch off the UI thread
// behind a progress dialog, then finishes the switch and calls then on the UI thread.
func (a *App) switchAsync(ci *cmd.Interpreter, force bool, then func()) {
	name, _ := ci.HasContext()
	if top := a.Content.Top(); top != nil {
		top.Stop()
	}

	switcher, err := a.ctxSwitcher()
	if err != nil {
		a.Flash().Err(err)
		return
	}

	// Save config prior to context switch...
	if err := a.Config.Save(true); err != nil {
		slog.Error("Fail to save config to disk", slogs.Subsys, "config", slogs.Error, err)
	}
	a.Config.K9s.ToggleContextSwitch(true)

	steps := []string{"Connecting to " + name, "Checking namespaces", "Discovering resources", "Loading view"}
	p, closeFn := dialog.ShowProgress(a.App, a.Content.Pages, switchTitle, steps)
	go func() {
		err := a.prepContext(switcher, name, p)
		a.QueueUpdateDraw(func() {
			if err == nil {
				err = a.switchContext(ci, force)
			}
			a.Config.K9s.ToggleContextSwitch(false)
			closeFn()

			if err != nil {
				slog.Error("Context switch failed", slogs.Context, name, slogs.Error, err)
				a.Flash().Err(err)
				return
			}
			if then != nil {
				then()
			}
		})
	}()
}

func (a *App) ctxSwitcher() (dao.Switchable, error) {
	res, err := dao.AccessorFor(a.factory, client.CtGVR)
	if err != nil {
		return nil, err
	}
	switcher, ok := res.(dao.Switchable)
	if !ok {
		return nil, errors.New("expecting a switchable resource")
	}

	return switcher, nil
}

// prepContext connects to the new context and warms the caches switchContext reads.
func (a *App) prepContext(s dao.Switchable, name string, p *ui.ModalProgress) error {
	if err := s.Switch(name); err != nil {
		return err
	}
	p.Advance()

	if _, err := a.Conn().ValidNamespaceNames(); err != nil {
		slog.Debug("Namespace prefetch failed", slogs.Error, err)
	}
	p.Advance()

	dial, err := a.Conn().CachedDiscovery()
	if err != nil {
		slog.Debug("Discovery prefetch failed", slogs.Error, err)
		p.Advance()
		return nil
	}
	if _, err := dial.ServerPreferredResources(); err != nil {
		slog.Debug("Discovery prefetch failed", slogs.Error, err)
	}
	p.Advance()

	return nil
}
