package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) updateStars(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.sSel < len(m.starItems)-1 {
			m.sSel++
		}
	case "k", "up":
		if m.sSel > 0 {
			m.sSel--
		}
	case "g", "home":
		m.sSel = 0
	case "G", "end":
		m.sSel = max(0, len(m.starItems)-1)
	case "d", "x", "u":
		if len(m.starItems) > 0 {
			it := m.starItems[m.sSel]
			m.confirm = cfUnstar
			m.confirmMsg = fmt.Sprintf("Unstar [mount %s] %s ? (y/n)", it.Mount, it.Path)
		}
	}
	return m, nil
}

func (m Model) doUnstar() Model {
	if m.sSel >= len(m.starItems) {
		return m
	}
	ref := m.starItems[m.sSel]
	if err := m.stars.Remove(ref); err != nil {
		m.status = "unstar failed: " + err.Error()
		return m
	}
	m.reloadStars()
	m.status = "unstarred"
	return m
}

// mountLabel resolves a star's mount index to its configured name when possible.
func (m Model) mountLabel(idx string) string {
	mounts := m.mounts
	if len(mounts) == 0 {
		mounts = m.cfg.Snapshot().Mounts
	}
	var i int
	if _, err := fmt.Sscanf(idx, "%d", &i); err == nil && i >= 0 && i < len(mounts) {
		return mounts[i].Name
	}
	return "mount " + idx
}

func (m Model) viewStars() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Starred entries") + "  " +
		dimStyle.Render(fmt.Sprintf("(%d)", len(m.starItems))) + "\n\n")

	if len(m.starItems) == 0 {
		b.WriteString(dimStyle.Render("  no stars yet — star files from the web browser"))
		return b.String()
	}

	// Group consecutively by mount for a header line; the list is already
	// sorted by (mount, path) in the store.
	rows := make([]string, 0, len(m.starItems)+4)
	rowRefs := make([]int, 0, len(m.starItems)) // map display row -> star index (-1 = header)
	lastMount := "\x00"
	for i, it := range m.starItems {
		if it.Mount != lastMount {
			rows = append(rows, magStyle.Render(" "+m.mountLabel(it.Mount)))
			rowRefs = append(rowRefs, -1)
			lastMount = it.Mount
		}
		marker := greenStyle.Render(" ★ ")
		line := marker + it.Path
		if i == m.sSel {
			line = selStyle.Render(" ★ " + it.Path)
		}
		rows = append(rows, line)
		rowRefs = append(rowRefs, i)
	}

	// Find the display row of the selected star so windowing keeps it visible.
	selRow := 0
	for r, ref := range rowRefs {
		if ref == m.sSel {
			selRow = r
			break
		}
	}
	b.WriteString(window(rows, selRow, m.contentHeight()))
	return b.String()
}
