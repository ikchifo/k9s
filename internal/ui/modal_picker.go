// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/sahilm/fuzzy"
)

const (
	pickerMinWidth  = 40
	pickerHChrome   = 6
	pickerVChrome   = 3
	pickerResetTag  = "[-::-]"
	pickerHeightPct = 70
)

// ModalPicker is a centered fuzzy-search list.
type ModalPicker struct {
	*tview.Box

	input    *tview.InputField
	list     *tview.List
	frame    *tview.Frame
	items    []string
	recent   int
	matches  []string
	hlTag    string
	onSelect func(string)
	onCancel func()
}

// NewModalPicker returns a picker over items, kept in the given order until
// filtered. The first recent items stay on top of any filtered results.
func NewModalPicker(title string, items []string, recent int, hlColor string) *ModalPicker {
	m := ModalPicker{
		Box:    tview.NewBox(),
		input:  tview.NewInputField(),
		list:   tview.NewList(),
		items:  items,
		recent: recent,
		hlTag:  "[" + hlColor + "::b]",
	}
	m.input.SetLabel("> ")
	m.input.SetChangedFunc(m.filter)
	m.list.ShowSecondaryText(false)
	m.list.SetHighlightFullLine(true)

	body := tview.NewFlex().SetDirection(tview.FlexRow)
	body.AddItem(m.input, 1, 0, true)
	body.AddItem(m.list, 0, 1, false)

	m.frame = tview.NewFrame(body).SetBorders(0, 0, 0, 0, 1, 1)
	m.frame.SetBorder(true)
	m.frame.SetTitle(title)
	m.filter("")

	return &m
}

// SetColors styles the picker.
func (m *ModalPicker) SetColors(fg, bg, selFg, selBg tcell.Color) *ModalPicker {
	m.frame.SetBackgroundColor(bg)
	m.frame.SetBorderColor(fg)
	m.frame.SetTitleColor(fg)
	m.input.SetBackgroundColor(bg)
	m.input.SetLabelColor(fg)
	m.input.SetFieldBackgroundColor(bg)
	m.input.SetFieldTextColor(fg)
	m.list.SetBackgroundColor(bg)
	m.list.SetMainTextColor(fg)
	m.list.SetSelectedTextColor(selFg)
	m.list.SetSelectedBackgroundColor(selBg)

	return m
}

// SetSelectedFunc sets the callback for the chosen item.
func (m *ModalPicker) SetSelectedFunc(f func(string)) *ModalPicker {
	m.onSelect = f
	return m
}

// SetCancelFunc sets the callback for a dismissal.
func (m *ModalPicker) SetCancelFunc(f func()) *ModalPicker {
	m.onCancel = f
	return m
}

func (m *ModalPicker) filter(q string) {
	m.list.Clear()
	m.matches = m.matches[:0]
	if q == "" {
		for _, it := range m.items {
			m.addItem(it, tview.Escape(it))
		}
		return
	}

	mm := fuzzy.Find(q, m.items)
	slices.SortStableFunc(mm, func(a, b fuzzy.Match) int {
		return cmp.Compare(m.pin(a), m.pin(b))
	})
	for _, mx := range mm {
		m.addItem(mx.Str, m.highlight(mx))
	}
}

// pin keeps recent items in their given order ahead of score-ranked ones.
func (m *ModalPicker) pin(mx fuzzy.Match) int {
	return min(mx.Index, m.recent)
}

func (m *ModalPicker) addItem(item, label string) {
	m.matches = append(m.matches, item)
	m.list.AddItem(label, "", 0, nil)
}

func (m *ModalPicker) highlight(mx fuzzy.Match) string {
	if strings.ContainsAny(mx.Str, "[]") {
		return tview.Escape(mx.Str)
	}

	hits := make(map[int]struct{}, len(mx.MatchedIndexes))
	for _, i := range mx.MatchedIndexes {
		hits[i] = struct{}{}
	}

	var b strings.Builder
	for i, r := range mx.Str {
		if _, ok := hits[i]; !ok {
			b.WriteRune(r)
			continue
		}
		b.WriteString(m.hlTag)
		b.WriteRune(r)
		b.WriteString(pickerResetTag)
	}

	return b.String()
}

func (m *ModalPicker) selectCurrent() {
	idx := m.list.GetCurrentItem()
	if idx < 0 || idx >= len(m.matches) {
		return
	}
	if m.onSelect != nil {
		m.onSelect(m.matches[idx])
	}
}

func (m *ModalPicker) cancel() {
	if m.onCancel != nil {
		m.onCancel()
	}
}

func (m *ModalPicker) move(delta int) {
	n := m.list.GetItemCount()
	if n == 0 {
		return
	}
	m.list.SetCurrentItem((m.list.GetCurrentItem() + delta + n) % n)
}

// Draw draws the picker centered on screen.
func (m *ModalPicker) Draw(screen tcell.Screen) {
	sw, sh := screen.Size()

	width := pickerMinWidth
	for _, it := range m.items {
		width = max(width, len(it)+pickerHChrome)
	}
	width = min(width, sw-2)
	height := min(len(m.items)+pickerVChrome, sh*pickerHeightPct/100)
	height = max(height, pickerVChrome+1)

	x, y := (sw-width)/2, (sh-height)/2
	m.SetRect(x, y, width, height)
	m.frame.SetRect(x, y, width, height)
	m.frame.Draw(screen)
}

// Focus keeps keystrokes in the search field.
func (m *ModalPicker) Focus(delegate func(p tview.Primitive)) {
	delegate(m.input)
}

// HasFocus returns whether the picker has focus.
func (m *ModalPicker) HasFocus() bool {
	return m.input.HasFocus() || m.list.HasFocus()
}

// InputHandler routes navigation keys to the list and the rest to the search field.
func (m *ModalPicker) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return m.WrapInputHandler(func(evt *tcell.EventKey, setFocus func(tview.Primitive)) {
		switch evt.Key() {
		case tcell.KeyEnter:
			m.selectCurrent()
		case tcell.KeyEscape:
			m.cancel()
		case tcell.KeyUp, tcell.KeyCtrlP, tcell.KeyBacktab:
			m.move(-1)
		case tcell.KeyDown, tcell.KeyCtrlN, tcell.KeyTab:
			m.move(1)
		case tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
			m.list.InputHandler()(evt, setFocus)
		default:
			m.input.InputHandler()(evt, setFocus)
		}
	})
}
