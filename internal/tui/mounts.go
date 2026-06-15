package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mediaplayer/internal/config"
)

func (m Model) updateMounts(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.mSel < len(m.mounts)-1 {
			m.mSel++
		}
	case "k", "up":
		if m.mSel > 0 {
			m.mSel--
		}
	case "g", "home":
		m.mSel = 0
	case "G", "end":
		m.mSel = max(0, len(m.mounts)-1)
	case "a":
		if len(m.mounts) >= config.MaxMounts {
			m.status = fmt.Sprintf("mount limit reached (%d)", config.MaxMounts)
			return m, nil
		}
		m.beginEdit(-1)
		return m, textinputFocus(&m.nameInput)
	case "e", "enter", "i":
		if len(m.mounts) > 0 {
			m.beginEdit(m.mSel)
			return m, textinputFocus(&m.nameInput)
		}
	case "d", "x":
		if len(m.mounts) > 0 {
			m.confirm = cfDeleteMount
			m.confirmMsg = fmt.Sprintf("Delete mount %q → %s ? (y/n)",
				m.mounts[m.mSel].Name, m.mounts[m.mSel].Path)
		}
	}
	return m, nil
}

// beginEdit opens the add/edit form. idx == -1 means adding.
func (m *Model) beginEdit(idx int) {
	m.editing = true
	m.editIdx = idx
	m.editField = 0
	if idx >= 0 {
		m.nameInput.SetValue(m.mounts[idx].Name)
		m.pathInput.SetValue(m.mounts[idx].Path)
	} else {
		m.nameInput.SetValue("")
		m.pathInput.SetValue("")
	}
	m.nameInput.Focus()
	m.pathInput.Blur()
}

func (m Model) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
		return m, nil
	case "tab", "down":
		m.editField = (m.editField + 1) % 2
		m.focusField()
		return m, nil
	case "shift+tab", "up":
		m.editField = (m.editField + 1) % 2
		m.focusField()
		return m, nil
	case "enter":
		return m.saveEdit()
	}

	var cmd tea.Cmd
	if m.editField == 0 {
		m.nameInput, cmd = m.nameInput.Update(msg)
	} else {
		m.pathInput, cmd = m.pathInput.Update(msg)
	}
	return m, cmd
}

func (m *Model) focusField() {
	if m.editField == 0 {
		m.nameInput.Focus()
		m.pathInput.Blur()
	} else {
		m.pathInput.Focus()
		m.nameInput.Blur()
	}
}

func (m Model) saveEdit() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.nameInput.Value())
	path := strings.TrimSpace(m.pathInput.Value())
	if name == "" || path == "" {
		m.status = "name and path are both required"
		return m, nil
	}

	mounts := append([]config.Mount(nil), m.mounts...)
	if m.editIdx >= 0 && m.editIdx < len(mounts) {
		mounts[m.editIdx] = config.Mount{Name: name, Path: path}
	} else {
		mounts = append(mounts, config.Mount{Name: name, Path: path})
	}
	if err := m.cfg.Replace(mounts); err != nil {
		m.status = "save failed: " + err.Error()
		return m, nil
	}
	m.editing = false
	m.reloadMounts()
	if m.editIdx < 0 {
		m.mSel = max(0, len(m.mounts)-1)
	}
	m.status = "saved"
	return m, nil
}

func (m Model) doDeleteMount() Model {
	if m.mSel >= len(m.mounts) {
		return m
	}
	mounts := append([]config.Mount(nil), m.mounts...)
	mounts = append(mounts[:m.mSel], mounts[m.mSel+1:]...)
	if err := m.cfg.Replace(mounts); err != nil {
		m.status = "delete failed: " + err.Error()
		return m
	}
	m.reloadMounts()
	m.status = "mount deleted"
	return m
}

func (m Model) viewMounts() string {
	if m.editing {
		return m.viewEdit()
	}
	var b strings.Builder
	title := "Mount points"
	b.WriteString(titleStyle.Render(title) + "  " +
		dimStyle.Render(fmt.Sprintf("(%d/%d)", len(m.mounts), config.MaxMounts)) + "\n\n")

	if len(m.mounts) == 0 {
		b.WriteString(dimStyle.Render("  no mounts — press 'a' to add one"))
		return b.String()
	}

	rows := make([]string, len(m.mounts))
	for i, mt := range m.mounts {
		key := dimStyle.Render(fmt.Sprintf("%d", (i+1)%10))
		line := fmt.Sprintf(" %s  %-16s %s", key, mt.Name, dimStyle.Render(mt.Path))
		if i == m.mSel {
			line = selStyle.Render(fmt.Sprintf(" %d  %-16s %s", (i+1)%10, mt.Name, mt.Path))
		}
		rows[i] = line
	}
	b.WriteString(window(rows, m.mSel, m.contentHeight()))
	return b.String()
}

func (m Model) viewEdit() string {
	heading := "Edit mount"
	if m.editIdx < 0 {
		heading = "Add mount"
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(heading) + "\n\n")
	b.WriteString(inputLabel.Render("Name") + "\n")
	b.WriteString(m.nameInput.View() + "\n\n")
	b.WriteString(inputLabel.Render("Path") + "\n")
	b.WriteString(m.pathInput.View() + "\n")
	return b.String()
}

// textinputFocus returns the blink command for a freshly focused input.
func textinputFocus(ti interface{ Focus() tea.Cmd }) tea.Cmd {
	return ti.Focus()
}
