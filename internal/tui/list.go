package tui

import (
	"strconv"

	"github.com/charmbracelet/x/ansi"

	"ninja-tui/internal/store"
)

// rowKind identifies what a left-pane row is and what activating it does.
type rowKind int

const (
	rowImport rowKind = iota
	rowSection
	rowFile
	rowGroup
)

// listRow is one line of the left pane. selKey is a stable identity used to
// keep the cursor on the same item across rebuilds, because results and send
// events rebuild the rows frequently.
type listRow struct {
	kind   rowKind
	label  string
	file   int
	group  string
	selKey string
}

// groupDef is one classification bucket of the By score section. A negative
// score means the bucket carries no score badge (pending, unscored).
type groupDef struct {
	key   string
	label string
	score int
}

var scoreGroups = []groupDef{
	{key: "4", label: "clear scam", score: 4},
	{key: "3", label: "likely scam", score: 3},
	{key: "2", label: "suspicious", score: 2},
	{key: "1", label: "risky", score: 1},
	{key: "0", label: "legit", score: 0},
	{key: "pending", label: "pending", score: -1},
	{key: "unscored", label: "unscored", score: -1},
}

func groupByKey(key string) (groupDef, bool) {
	for _, g := range scoreGroups {
		if g.key == key {
			return g, true
		}
	}
	return groupDef{}, false
}

// groupOf classifies one entry into a By score bucket. Files that have not
// been transmitted yet (queued, sending, failed) have no bucket and appear
// only in the Files section.
func groupOf(e store.Entry) string {
	if e.Result != nil {
		if e.Result.Verdict == nil {
			return "unscored"
		}
		return strconv.Itoa(scoreBucket(e.Result.Verdict.Score, e.Result.Verdict.ScoreMax))
	}
	if e.Status == store.Sent {
		return "pending"
	}
	return ""
}

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

// groupRowText renders one group header: badge (existing score styling),
// label, and the group's count.
func groupRowText(g groupDef, n int) string {
	chip := styleDim.Render("[ ? ]")
	if g.score >= 0 {
		chip = scoreBadge(float64(g.score), 4)
	}
	return chip + " " + styleText.Render(g.label) + "  " + styleDim.Render(strconv.Itoa(n))
}

// rebuildRows rebuilds the left pane in its fixed top-to-bottom order:
// Import button, Files, By score.
func (m *Model) rebuildRows() {
	sel := ""
	if r, ok := m.rowAt(m.cursor); ok {
		sel = r.selKey
	}

	entries := m.st.Entries()
	counts := make(map[string]int, len(scoreGroups))
	for _, e := range entries {
		if g := groupOf(e); g != "" {
			counts[g]++
		}
	}

	rows := make([]listRow, 0, len(entries)+len(scoreGroups)+2)
	rows = append(rows,
		listRow{kind: rowImport, selKey: "import"},
		listRow{kind: rowSection, label: "Files", selKey: "section:files"},
	)
	for i, e := range entries {
		rows = append(rows, listRow{kind: rowFile, file: i, selKey: "file:" + e.TUIID, label: fileRowText(e)})
	}
	rows = append(rows, listRow{kind: rowSection, label: "By score", selKey: "section:score"})
	for _, g := range scoreGroups {
		rows = append(rows, listRow{kind: rowGroup, group: g.key, selKey: "group:" + g.key, label: groupRowText(g, counts[g.key])})
	}
	m.rows = rows
	m.cursor = m.cursorFor(sel)
	m.ensureLeftVisible()
}

// cursorFor restores the cursor to the row with the given selKey when it
// still exists, otherwise to the closest selectable row.
func (m *Model) cursorFor(sel string) int {
	if sel != "" {
		for i, r := range m.rows {
			if r.selKey == sel && r.kind != rowSection {
				return i
			}
		}
	}
	if m.cursor >= 0 && m.cursor < len(m.rows) && m.rows[m.cursor].kind != rowSection {
		return m.cursor
	}
	for i, r := range m.rows {
		if r.kind != rowSection {
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

func (m *Model) cursorOnGroup() bool {
	r, ok := m.rowAt(m.cursor)
	return ok && r.kind == rowGroup
}

func (m *Model) cursorOnFile() bool {
	r, ok := m.rowAt(m.cursor)
	return ok && r.kind == rowFile
}

func (m *Model) cursorOnImport() bool {
	r, ok := m.rowAt(m.cursor)
	return ok && r.kind == rowImport
}

// moveLeft moves the cursor by delta rows, skipping section headers, and
// keeps it visible in the pane.
func (m *Model) moveLeft(delta int) {
	if len(m.rows) == 0 {
		return
	}
	i := m.cursor + delta
	for i >= 0 && i < len(m.rows) && m.rows[i].kind == rowSection {
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

// renderLeft renders the left pane: Import button, Files, then By score.
func (m *Model) renderLeft(width, height int) string {
	lines := make([]string, 0, height)
	for i := m.leftOffset; i < len(m.rows) && len(lines) < height; i++ {
		lines = append(lines, m.renderLeftRow(i, width))
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
	default:
		text = r.label
	}
	text = ansi.Truncate(text, width, "…")
	if i == m.cursor {
		return styleSelected.Width(width).Render(text)
	}
	return text
}

// subjectsShown reports whether the right pane currently shows a subject list.
func (m *Model) subjectsShown() bool {
	return m.nav == navSubjects || (m.nav == navList && m.cursorOnGroup())
}

// shownGroup returns the group whose subjects the right pane should show, or
// "" when no subject list is active.
func (m *Model) shownGroup() string {
	switch m.nav {
	case navSubjects:
		return m.subjGroup
	case navDetail:
		if m.detailFrom == navSubjects {
			return m.subjGroup
		}
		return ""
	default:
		if r, ok := m.rowAt(m.cursor); ok && r.kind == rowGroup {
			m.subjGroup = r.group
			return r.group
		}
		return ""
	}
}

// rebuildSubjects recomputes the subject list of the shown group.
func (m *Model) rebuildSubjects() {
	g := m.shownGroup()
	if g == "" {
		m.subjIdx = nil
		m.subjCursor, m.subjOffset = 0, 0
		return
	}
	entries := m.st.Entries()
	idx := make([]int, 0, len(entries))
	for i, e := range entries {
		if groupOf(e) == g {
			idx = append(idx, i)
		}
	}
	m.subjIdx = idx
	if m.subjCursor >= len(idx) {
		m.subjCursor = maxInt(0, len(idx)-1)
	}
	m.ensureSubjectVisible()
}

// subjectHeight is the number of subject rows that fit under the title line.
func (m *Model) subjectHeight() int {
	h := m.bodyHeight - 1
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) moveSubject(delta int) {
	if len(m.subjIdx) == 0 {
		return
	}
	m.subjCursor = clampInt(m.subjCursor+delta, 0, len(m.subjIdx)-1)
	m.ensureSubjectVisible()
}

func (m *Model) ensureSubjectVisible() {
	h := m.subjectHeight()
	if m.subjCursor < m.subjOffset {
		m.subjOffset = m.subjCursor
	}
	if m.subjCursor >= m.subjOffset+h {
		m.subjOffset = m.subjCursor - h + 1
	}
	maxOff := len(m.subjIdx) - h
	if maxOff < 0 {
		maxOff = 0
	}
	if m.subjOffset > maxOff {
		m.subjOffset = maxOff
	}
	if m.subjOffset < 0 {
		m.subjOffset = 0
	}
}

// renderSubjects renders the right-pane subject list: one title line and one
// row per email (subject, from, score chip).
func (m *Model) renderSubjects(width, height int) string {
	g, ok := groupByKey(m.subjGroup)
	if !ok {
		return ""
	}
	lines := make([]string, 0, height)
	lines = append(lines, ansi.Truncate(groupRowText(g, len(m.subjIdx))+
		styleDim.Render("   enter open · esc back"), width, "…"))
	if len(m.subjIdx) == 0 {
		lines = append(lines, styleDim.Render("no emails in this group"))
	}
	for i := m.subjOffset; i < len(m.subjIdx) && len(lines) < height; i++ {
		e, ok := m.st.Entry(m.subjIdx[i])
		if !ok {
			continue
		}
		selected := m.nav == navSubjects && i == m.subjCursor
		lines = append(lines, m.renderSubjectRow(e, width, selected))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return joinLines(lines)
}

func (m *Model) renderSubjectRow(e store.Entry, width int, selected bool) string {
	chip := styleDim.Render("[ ? ]")
	if e.Result != nil && e.Result.Verdict != nil {
		chip = scoreBadge(e.Result.Verdict.Score, e.Result.Verdict.ScoreMax)
	}
	chipW := 5
	if e.Result != nil && e.Result.Verdict != nil {
		chipW = 3
	}
	fromW := width / 3
	if fromW > 28 {
		fromW = 28
	}
	if fromW < 6 {
		fromW = 6
	}
	subjW := width - fromW - chipW - 4
	if subjW < 4 {
		subjW = 4
		fromW = maxInt(4, width-subjW-chipW-4)
	}

	subject := e.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	from := e.From
	if from == "" {
		from = "(unknown sender)"
	}
	line := ansi.Truncate(styleText.Render(subject), subjW, "…") + "  " +
		ansi.Truncate(styleDim.Render(from), fromW, "…") + "  " + chip
	line = ansi.Truncate(line, width, "…")
	if selected {
		return styleSelected.Width(width).Render(line)
	}
	return line
}
