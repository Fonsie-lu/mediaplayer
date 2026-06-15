// Package tui is a terminal control panel for the running mediaplayer server.
//
// It runs in the foreground (the HTTP server runs in a background goroutine)
// and offers three tabs — Mounts, Stars and Logs — driven by vim-style keys.
// ctrl+r re-launches the executable.
package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"mediaplayer/internal/api"
	"mediaplayer/internal/applog"
	"mediaplayer/internal/config"
)

type tab int

const (
	tabMounts tab = iota
	tabStars
	tabLogs
	numTabs
)

var tabNames = [numTabs]string{"Mounts", "Stars", "Logs"}

type confirmKind int

const (
	cfNone confirmKind = iota
	cfDeleteMount
	cfUnstar
	cfRestart
)

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(700*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Model holds all TUI state.
type Model struct {
	cfg   *config.Config
	stars *api.StarStore
	logs  *applog.Store
	addr  string

	width, height int
	tab           tab
	status        string // transient status line message
	restart       bool   // set true when the user asks to re-exec

	// mounts
	mounts    []config.Mount
	mSel      int
	editing   bool
	editIdx   int // -1 = adding a new mount
	editField int // 0 = name, 1 = path
	nameInput textinput.Model
	pathInput textinput.Model

	// stars
	starItems []api.StarRef
	sSel      int

	// logs
	logVersion uint64
	logRows    []logRow
	collapsed  map[string]bool
	lSel       int
	lOffset    int

	// confirm modal
	confirm    confirmKind
	confirmMsg string
}

// Run starts the TUI and blocks until the user quits. It returns restart=true
// when the user requested a re-exec of the process.
func Run(cfg *config.Config, stars *api.StarStore, logs *applog.Store, addr string) (bool, error) {
	name := textinput.New()
	name.Placeholder = "name"
	name.CharLimit = 64
	path := textinput.New()
	path.Placeholder = "/absolute/path"
	path.CharLimit = 512

	m := Model{
		cfg:       cfg,
		stars:     stars,
		logs:      logs,
		addr:      addr,
		tab:       tabMounts,
		editIdx:   -1,
		nameInput: name,
		pathInput: path,
		collapsed: map[string]bool{},
	}
	m.reloadMounts()
	m.reloadStars()

	p := tea.NewProgram(m, tea.WithAltScreen())
	fm, err := p.Run()
	if err != nil {
		return false, err
	}
	return fm.(Model).restart, nil
}

func (m Model) Init() tea.Cmd { return tick() }

func (m *Model) reloadMounts() {
	m.mounts = m.cfg.Snapshot().Mounts
	if m.mSel >= len(m.mounts) {
		m.mSel = max(0, len(m.mounts)-1)
	}
}

func (m *Model) reloadStars() {
	m.starItems = m.stars.List()
	if m.sSel >= len(m.starItems) {
		m.sSel = max(0, len(m.starItems)-1)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		if v := m.logs.Version(); v != m.logVersion {
			m.logVersion = v
			if m.tab == tabLogs {
				m.rebuildLogRows()
			}
		}
		return m, tick()

	case tea.KeyMsg:
		// Modal states capture all input.
		if m.confirm != cfNone {
			return m.updateConfirm(msg)
		}
		if m.editing {
			return m.updateEdit(msg)
		}
		return m.updateNormal(msg)
	}
	return m, nil
}

// updateNormal handles keys when no modal/edit form is open.
func (m Model) updateNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "ctrl+r":
		m.confirm = cfRestart
		m.confirmMsg = "Restart the mediaplayer executable? (y/n)"
		return m, nil
	case "tab", "L", "]":
		m.switchTab((m.tab + 1) % numTabs)
		return m, nil
	case "shift+tab", "H", "[":
		m.switchTab((m.tab + numTabs - 1) % numTabs)
		return m, nil
	case "1":
		m.switchTab(tabMounts)
		return m, nil
	case "2":
		m.switchTab(tabStars)
		return m, nil
	case "3":
		m.switchTab(tabLogs)
		return m, nil
	}

	switch m.tab {
	case tabMounts:
		return m.updateMounts(msg)
	case tabStars:
		return m.updateStars(msg)
	case tabLogs:
		return m.updateLogs(msg)
	}
	return m, nil
}

func (m *Model) switchTab(t tab) {
	m.tab = t
	switch t {
	case tabMounts:
		m.reloadMounts()
	case tabStars:
		m.reloadStars()
	case tabLogs:
		m.rebuildLogRows()
	}
}

func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		kind := m.confirm
		m.confirm = cfNone
		switch kind {
		case cfRestart:
			m.restart = true
			return m, tea.Quit
		case cfDeleteMount:
			return m.doDeleteMount(), nil
		case cfUnstar:
			return m.doUnstar(), nil
		}
	case "n", "N", "esc", "q":
		m.confirm = cfNone
	}
	return m, nil
}

// contentHeight is the number of rows available to a tab body.
func (m Model) contentHeight() int {
	h := m.height - 5 // tab bar (2) + help (2) + status (1)
	if h < 3 {
		h = 3
	}
	return h
}

func (m Model) View() string {
	if m.width == 0 {
		return "loading…"
	}

	// Tab bar.
	tabs := make([]string, numTabs)
	for i := 0; i < int(numTabs); i++ {
		label := tabNames[i]
		if tab(i) == m.tab {
			tabs[i] = tabActive.Render(label)
		} else {
			tabs[i] = tabInactive.Render(label)
		}
	}
	bar := tabBar.Render(lipgloss.JoinHorizontal(lipgloss.Top, tabs...))

	var body string
	switch m.tab {
	case tabMounts:
		body = m.viewMounts()
	case tabStars:
		body = m.viewStars()
	case tabLogs:
		body = m.viewLogs()
	}

	status := dimStyle.Render(m.addr)
	if m.status != "" {
		status = greenStyle.Render(m.status)
	}

	help := helpStyle.Render(m.helpLine())

	view := lipgloss.JoinVertical(lipgloss.Left, bar, body, status, help)

	if m.confirm != cfNone {
		modal := modalStyle.Render(m.confirmMsg)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}
	return view
}

func (m Model) helpLine() string {
	switch {
	case m.editing:
		return "tab: next field · enter: save · esc: cancel"
	case m.tab == tabMounts:
		return "j/k: move · a: add · e/enter: edit · d: delete · 1·2·3/tab: tabs · ctrl+r: restart · q: quit"
	case m.tab == tabStars:
		return "j/k: move · d/x: unstar · 1·2·3/tab: tabs · ctrl+r: restart · q: quit"
	case m.tab == tabLogs:
		return "j/k: move · l/h: open/close · enter/space: toggle · E/C: all · c: clear · ctrl+r: restart · q: quit"
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
