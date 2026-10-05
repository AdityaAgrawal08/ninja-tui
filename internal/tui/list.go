package tui

import (
	"github.com/charmbracelet/x/ansi"

	"ninja-tui/internal/store"
)

// rowKind identifies what a left-pane row is and what activating it does.
type rowKind int

const (
	rowImport rowKind = iota
	rowSection
	rowFile
	rowEmpty
)

// listRow is one line of the left pane. selKey is a stable identity used to
// keep the cursor on the same item across rebuilds, because results and send
// events rebuild the rows frequently.
type listRow struct {
	kind   rowKind
	label  string
	file   int
	selKey string
}

// emptyFilesLine is the left pane's placeholder until a folder with .eml files
// has been imported.
const emptyFilesLine = "No File uploaded yet"

// fileRowText renders one Files row: the filename followed by its live send
// state, which becomes "processed" plus the score badge once the webhook
// result has arrived.
func fileRowText(e store.Entry) string {
	name := styleText.Render(e.Name)
	switch e.Status {
	case store.Queued:
		return name + "  " + styleDim.Render("queued")
	case store.Sending:
		return name + "  " + styleWarn.Render("sending")
	case store.Failed:
		return name + "  " + styleBad.Render("failed")
	case store.Sent:
		if e.Result == nil {
			return name + "  " + styleTeal.Render("sent")
		}
		processed := styleTeal.Render("processed")
		if e.Result.Verdict == nil {
			return name + "  " + processed + " " + styleDim.Render("[ ? ]")
		}
		return name + "  " + processed + " " + scoreBadge(e.Result.Verdict.Score, e.Result.Verdict.ScoreMax)
	}
	return name + "  " + styleDim.Render("?")
}

// rebuildRows rebuilds the left pane in its fixed top-to-bottom order:
// the Import button, then the Files section.
func (m *Model) rebuildRows() {
	sel := ""
	if r, ok := m.rowAt(m.cursor); ok {
		sel = r.selKey
	}

	entries := m.st.Entries()
	rows := make([]listRow, 0, len(entries)+3)
	rows = append(rows,
		listRow{kind: rowImport, selKey: "import"},
		listRow{kind: rowSection, label: "Files", selKey: "section:files"},
	)
	if len(entries) == 0 {
		rows = append(rows, listRow{kind: rowEmpty, label: emptyFilesLine, selKey: "empty"})
	}
	for i, e := range entries {
		rows = append(rows, listRow{kind: rowFile, file: i, selKey: "file:" + e.TUIID, label: fileRowText(e)})
	}
	m.rows = rows
	m.cursor = m.cursorFor(sel)
	m.ensureLeftVisible()
}

// selectable reports whether the cursor may stop on a row. Section headers and
// the empty-state line are display-only.
func selectable(r listRow) bool {
	return r.kind != rowSection && r.kind != rowEmpty
}

// cursorFor restores the cursor to the row with the given selKey when it still
// exists, otherwise to the closest selectable row.
func (m *Model) cursorFor(sel string) int {
	if sel != "" {
		for i, r := range m.rows {
			if r.selKey == sel && selectable(r) {
				return i
			}
		}
	}
	if m.cursor >= 0 && m.cursor < len(m.rows) && selectable(m.rows[m.cursor]) {
		return m.cursor
	}
	for i, r := range m.rows {
		if selectable(r) {
			return i
		}
	}
	return 0
}

// selectKey moves the cursor to the row with the given selKey when present.
func (m *Model) selectKey(key string) {
	for i, r := range m.rows {
		if r.selKey == key {
			m.cursor = i
			m.ensureLeftVisible()
			return
		}
	}
}

func (m *Model) rowAt(i int) (listRow, bool) {
	if i < 0 || i >= len(m.rows) {
		return listRow{}, false
	}
	return m.rows[i], true
}

func (m *Model) cursorOnFile() bool {
	r, ok := m.rowAt(m.cursor)
	return ok && r.kind == rowFile
}

func (m *Model) cursorOnImport() bool {
	r, ok := m.rowAt(m.cursor)
	return ok && r.kind == rowImport
}

// moveLeft moves the cursor by delta selectable rows and keeps it visible in
// the pane.
func (m *Model) moveLeft(delta int) {
	if len(m.rows) == 0 {
		return
	}
	i := m.cursor + delta
	for i >= 0 && i < len(m.rows) && !selectable(m.rows[i]) {
		i += delta
	}
	if i < 0 || i >= len(m.rows) {
		return
	}
	m.cursor = i
	m.ensureLeftVisible()
}

// ensureLeftVisible scrolls the left pane so the cursor is inside it.
func (m *Model) ensureLeftVisible() {
	h := m.bodyHeight
	if h <= 0 {
		return
	}
	if m.cursor < m.leftOffset {
		m.leftOffset = m.cursor
	}
	if m.cursor >= m.leftOffset+h {
		m.leftOffset = m.cursor - h + 1
	}
	maxOff := len(m.rows) - h
	if maxOff < 0 {
		maxOff = 0
	}
	if m.leftOffset > maxOff {
		m.leftOffset = maxOff
	}
	if m.leftOffset < 0 {
		m.leftOffset = 0
	}
}

// renderLeft renders the left pane: Import button, Files header, then one row
// per imported file (or the placeholder plus the ninja pairing hint when
// nothing is imported).
func (m *Model) renderLeft(width, height int) string {
	lines := make([]string, 0, height)
	for i := m.leftOffset; i < len(m.rows) && len(lines) < height; i++ {
		lines = append(lines, m.renderLeftRow(i, width))
	}
	if m.st.Len() == 0 {
		hint := "pair ninja: webhook_url " + webhookURL(m.webhookAddr)
		for _, ln := range wrapTree(hint, maxInt(1, width)) {
			if len(lines) >= height {
				break
			}
			lines = append(lines, styleDim.Render(ln))
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return joinLines(lines)
}

func (m *Model) renderLeftRow(i, width int) string {
	r := m.rows[i]
	var text string
	switch r.kind {
	case rowImport:
		text = styleAccent.Render("[ Import ]")
	case rowSection:
		text = styleSection.Render(r.label)
	case rowEmpty:
		text = styleDim.Render(r.label)
	default:
		text = r.label
	}
	text = ansi.Truncate(text, width, "…")
	if i == m.cursor && selectable(r) {
		return styleSelected.Width(width).Render(text)
	}
	return text
}
