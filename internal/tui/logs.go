package tui

import (
	"fmt"
	"path"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mediaplayer/internal/applog"
)

type logRow struct {
	header  bool
	key     string // group key (set on both headers and entry rows)
	session string
	file    string
	count   int          // header only: number of entries in the group (the "sum")
	entry   applog.Entry // entry rows only
}

func groupKey(e applog.Entry) string {
	s := e.Session
	if s == "" {
		s = "general"
	}
	return s + "\x00" + e.File
}

// rebuildLogRows regroups the log buffer into the flat, collapse-aware row list.
func (m *Model) rebuildLogRows() {
	entries := m.logs.Snapshot()

	order := make([]string, 0)
	groups := map[string][]applog.Entry{}
	for _, e := range entries {
		k := groupKey(e)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}

	rows := make([]logRow, 0, len(entries)+len(order))
	for _, k := range order {
		// Groups start collapsed; only an explicit expand toggles them open.
		if _, seen := m.collapsed[k]; !seen {
			m.collapsed[k] = true
		}
		es := groups[k]
		first := es[0]
		sess := first.Session
		if sess == "" {
			sess = "general"
		}
		rows = append(rows, logRow{
			header:  true,
			key:     k,
			session: sess,
			file:    first.File,
			count:   len(es),
		})
		if !m.collapsed[k] {
			for _, e := range es {
				rows = append(rows, logRow{key: k, entry: e})
			}
		}
	}

	m.logRows = rows
	if m.lSel >= len(rows) {
		m.lSel = max(0, len(rows)-1)
	}
}

func (m Model) updateLogs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.logRows)
	switch msg.String() {
	case "j", "down":
		if m.lSel < n-1 {
			m.lSel++
		}
	case "k", "up":
		if m.lSel > 0 {
			m.lSel--
		}
	case "g", "home":
		m.lSel = 0
	case "G", "end":
		m.lSel = max(0, n-1)
	case "ctrl+d":
		m.lSel = min(n-1, m.lSel+m.contentHeight()/2)
	case "ctrl+u":
		m.lSel = max(0, m.lSel-m.contentHeight()/2)
	case "l", "right":
		m.setCollapsedAtSel(false)
	case "h", "left":
		m.setCollapsedAtSel(true)
	case "enter", " ", "o":
		m.toggleAtSel()
	case "E":
		for _, r := range m.logRows {
			if r.header {
				m.collapsed[r.key] = false
			}
		}
		m.rebuildLogRows()
	case "C":
		for _, r := range m.logRows {
			if r.header {
				m.collapsed[r.key] = true
			}
		}
		m.rebuildLogRows()
	case "c":
		m.logs.Clear()
		m.logVersion = m.logs.Version()
		m.rebuildLogRows()
		m.status = "logs cleared"
	}
	return m, nil
}

// selKey returns the group key of the currently selected row, headers and
// entries alike, and the header row index for that group.
func (m Model) selKey() (string, int, bool) {
	if m.lSel < 0 || m.lSel >= len(m.logRows) {
		return "", -1, false
	}
	key := m.logRows[m.lSel].key
	for i, r := range m.logRows {
		if r.header && r.key == key {
			return key, i, true
		}
	}
	return key, -1, false
}

func (m *Model) setCollapsedAtSel(v bool) {
	key, hdr, ok := m.selKey()
	if !ok {
		return
	}
	m.collapsed[key] = v
	m.rebuildLogRows()
	if v && hdr >= 0 { // moving focus to the header we just folded
		m.lSel = min(hdr, len(m.logRows)-1)
	}
}

func (m *Model) toggleAtSel() {
	key, hdr, ok := m.selKey()
	if !ok {
		return
	}
	m.collapsed[key] = !m.collapsed[key]
	m.rebuildLogRows()
	if hdr >= 0 {
		m.lSel = min(hdr, len(m.logRows)-1)
	}
}

func (m Model) viewLogs() string {
	var b strings.Builder
	total := 0
	groups := 0
	for _, r := range m.logRows {
		if r.header {
			groups++
			total += r.count
		}
	}
	b.WriteString(titleStyle.Render("Logs") + "  " +
		dimStyle.Render(fmt.Sprintf("(%d entries · %d groups)", total, groups)) + "\n\n")

	if len(m.logRows) == 0 {
		b.WriteString(dimStyle.Render("  no log entries yet"))
		return b.String()
	}

	rows := make([]string, len(m.logRows))
	for i, r := range m.logRows {
		rows[i] = m.renderLogRow(r, i == m.lSel)
	}
	b.WriteString(window(rows, m.lSel, m.contentHeight()))
	return b.String()
}

func (m Model) renderLogRow(r logRow, selected bool) string {
	if r.header {
		marker := "▾"
		if m.collapsed[r.key] {
			marker = "▸"
		}
		file := r.file
		if file == "" {
			file = "—"
		} else {
			file = path.Base(file)
		}
		label := fmt.Sprintf("%s %s  %s", marker, r.session, file)
		count := countStyle.Render(fmt.Sprintf("(%d)", r.count))
		if selected {
			return selStyle.Render(fmt.Sprintf("%s %s  %s (%d)", marker, r.session, file, r.count))
		}
		return magStyle.Render(label) + " " + count
	}

	ts := r.entry.Time.Format("15:04:05")
	line := fmt.Sprintf("    %s  %s", ts, r.entry.Msg)
	if selected {
		return selStyle.Render(line)
	}
	return dimStyle.Render("    "+ts) + "  " + r.entry.Msg
}

// window renders rows into at most height lines, scrolling so that sel stays
// visible (roughly centered).
func window(rows []string, sel, height int) string {
	if height < 1 {
		height = 1
	}
	if len(rows) <= height {
		return strings.Join(rows, "\n")
	}
	off := sel - height/2
	if off < 0 {
		off = 0
	}
	if off > len(rows)-height {
		off = len(rows) - height
	}
	return strings.Join(rows[off:off+height], "\n")
}
