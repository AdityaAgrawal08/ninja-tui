package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"ninja-tui/internal/store"
)

// waitingLine is the right pane's empty state before any file is imported.
const waitingLine = "Results will appear here as ninja responds…"

// treeKey is the stable identity of one selectable tree row. Expansion sets
// are keyed by the same ids so they survive every rebuild.
func treeKey(n treeNode) string {
	if n.kind == nodeGroup {
		return "group:" + n.group
	}
	return "msg:" + n.tuiID
}

// Fallback group keys of the right tree. pending collects entries whose result
// has not arrived yet; unscored those whose result arrived without a verdict
// or category. Every other group key is a category choice reported by ninja.
const (
	pendingKey  = "pending"
	unscoredKey = "unscored"
)

// categoryOrder is the taxonomy order ninja's scorer reports its category
// choices in (ninja/internal/score/jev.go). Categories in this list render
// first; any choice ninja adds later renders after them in encounter order.
var categoryOrder = []string{"phishing", "financial_fraud", "malware", "advance_fee", "extortion", "other", "legitimate"}

// knownCategory reports whether k is part of ninja's fixed category taxonomy.
func knownCategory(k string) bool {
	for _, c := range categoryOrder {
		if c == k {
			return true
		}
	}
	return false
}

// categoryOf classifies one entry for the right tree and returns its stable
// group key: the lowercased category choice ninja reported. Anything without a
// result is pending: queued, in flight, sent but not yet answered, or failed
// and waiting for a retry. A result without a verdict, category, or choice is
// unscored.
func categoryOf(e store.Entry) string {
	if e.Result == nil {
		return pendingKey
	}
	if e.Result.Verdict == nil || e.Result.Verdict.Category == nil || e.Result.Verdict.Category.Choice == "" {
		return unscoredKey
	}
	return strings.ToLower(e.Result.Verdict.Category.Choice)
}

// categoryGroup is one category of the right tree: its entries in store order,
// its stable key, and the highest verdict score among its entries.
type categoryGroup struct {
	key      string
	label    string
	entries  []store.Entry
	hasScore bool
	score    float64
	scoreMax int
}

// buildCategoryGroups groups entries by ninja's category choice and returns
// them in taxonomy order: known categories first, then choices this build does
// not know in encounter order, then pending, then unscored. Groups with no
// entries are not shown.
func buildCategoryGroups(entries []store.Entry) []categoryGroup {
	byKey := make(map[string]*categoryGroup)
	var unknown []string
	for _, e := range entries {
		k := categoryOf(e)
		g := byKey[k]
		if g == nil {
			g = &categoryGroup{key: k, label: k}
			byKey[k] = g
			if k != pendingKey && k != unscoredKey && !knownCategory(k) {
				unknown = append(unknown, k)
			}
		}
		g.entries = append(g.entries, e)
		if e.Result != nil && e.Result.Verdict != nil {
			if v := e.Result.Verdict; !g.hasScore || v.Score > g.score {
				g.hasScore, g.score, g.scoreMax = true, v.Score, v.ScoreMax
			}
		}
	}
	order := make([]string, 0, len(categoryOrder)+len(unknown)+2)
	order = append(order, categoryOrder...)
	order = append(order, unknown...)
	order = append(order, pendingKey, unscoredKey)
	groups := make([]categoryGroup, 0, len(byKey))
	for _, k := range order {
		if g := byKey[k]; g != nil {
			groups = append(groups, *g)
		}
	}
	return groups
}

// rebuildTree flattens the category tree into treeLines plus a parallel list
// of selectable nodes. Level 1 is the always-visible category headers;
// expanding a header inlines its emails' subject rows; expanding a subject row
// inlines the full email using the existing detail renderer.
func (m *Model) rebuildTree() {
	w := m.rightWidth()
	groups := buildCategoryGroups(m.st.Entries())
	if len(groups) == 0 {
		m.treeLines = nil
		m.treeNodes = nil
		m.treeSel = ""
		m.treeCursor, m.treeOffset = 0, 0
		return
	}

	lines := make([]string, 0, len(groups))
	nodes := make([]treeNode, 0, len(groups))
	addNode := func(n treeNode) {
		n.line = len(lines)
		nodes = append(nodes, n)
	}
	for _, g := range groups {
		expanded := m.expandedGroups[g.key]
		marker := "▸ "
		if expanded {
			marker = "▾ "
		}
		addNode(treeNode{kind: nodeGroup, group: g.key})
		lines = append(lines, styleDim.Render(marker)+categoryHeaderText(g))
		if !expanded {
			continue
		}
		for _, e := range g.entries {
			addNode(treeNode{kind: nodeSubject, group: g.key, tuiID: e.TUIID})
			lines = append(lines, m.subjectTreeLine(e, w))
			if m.expandedMsgs[e.TUIID] {
				for _, ln := range wrapTree(m.detailContent(e), maxInt(1, w-4)) {
					lines = append(lines, "    "+ln)
				}
			}
		}
	}
	m.treeLines = lines
	m.treeNodes = nodes
	m.treeCursor = m.treeCursorFor(m.treeSel)
	m.ensureTreeVisible()
}

// categoryHeaderText renders one level-1 row: the category name, the count of
// its entries, and the highest score chip among them ([ ? ] when none scored).
func categoryHeaderText(g categoryGroup) string {
	chip := styleDim.Render("[ ? ]")
	if g.hasScore {
		chip = scoreBadge(g.score, g.scoreMax)
	}
	return styleText.Render(g.label+" ("+itoa(len(g.entries))+")") + "  " + chip
}

// subjectTreeLine is one level-2 row: an expand marker, subject, from, and
// score chip.
func (m *Model) subjectTreeLine(e store.Entry, width int) string {
	marker := "  ▸ "
	if m.expandedMsgs[e.TUIID] {
		marker = "  ▾ "
	}
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	return styleDim.Render(marker) + subjectLine(e, inner)
}

// subjectLine renders the subject, sender, and score chip of one email at the
// given width.
func subjectLine(e store.Entry, width int) string {
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
	if fromW < 4 {
		fromW = 4
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
	return ansi.Truncate(line, width, "…")
}

// wrapTree wraps one full-email block to width, preserving ANSI styling, so
// the inline detail never exceeds the right pane.
func wrapTree(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if ansi.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(line, width, " "), "\n")...)
	}
	return out
}

// treeCursorFor restores the cursor to the node with the given key when it
// still exists, otherwise clamps to the nearest node.
func (m *Model) treeCursorFor(sel string) int {
	if len(m.treeNodes) == 0 {
		return 0
	}
	if sel != "" {
		for i, n := range m.treeNodes {
			if treeKey(n) == sel {
				return i
			}
		}
	}
	if m.treeCursor >= len(m.treeNodes) {
		return len(m.treeNodes) - 1
	}
	if m.treeCursor < 0 {
		return 0
	}
	return m.treeCursor
}

// currentNode returns the node under the tree cursor.
func (m *Model) currentNode() (treeNode, bool) {
	if m.treeCursor < 0 || m.treeCursor >= len(m.treeNodes) {
		return treeNode{}, false
	}
	return m.treeNodes[m.treeCursor], true
}

// setTreeCursor selects one node by index and keeps it visible.
func (m *Model) setTreeCursor(i int) {
	if i < 0 || i >= len(m.treeNodes) {
		return
	}
	m.treeCursor = i
	m.treeSel = treeKey(m.treeNodes[i])
	m.ensureTreeVisible()
}

// moveTree moves the tree cursor by delta nodes.
func (m *Model) moveTree(delta int) {
	if len(m.treeNodes) == 0 {
		return
	}
	m.treeCursor = clampInt(m.treeCursor+delta, 0, len(m.treeNodes)-1)
	m.treeSel = treeKey(m.treeNodes[m.treeCursor])
	m.ensureTreeVisible()
}

// ensureTreeVisible scrolls the tree so the cursor's row is inside the pane.
func (m *Model) ensureTreeVisible() {
	if len(m.treeNodes) == 0 {
		m.treeCursor, m.treeOffset = 0, 0
		return
	}
	if m.treeCursor < 0 {
		m.treeCursor = 0
	}
	if m.treeCursor >= len(m.treeNodes) {
		m.treeCursor = len(m.treeNodes) - 1
	}
	h := m.bodyHeight
	if h < 1 {
		h = 1
	}
	line := m.treeNodes[m.treeCursor].line
	if line < m.treeOffset {
		m.treeOffset = line
	}
	if line >= m.treeOffset+h {
		m.treeOffset = line - h + 1
	}
	maxOff := len(m.treeLines) - h
	if maxOff < 0 {
		maxOff = 0
	}
	m.treeOffset = clampInt(m.treeOffset, 0, maxOff)
}

// scrollTree moves the tree viewport by delta lines.
func (m *Model) scrollTree(delta int) {
	h := m.bodyHeight
	if h < 1 {
		h = 1
	}
	maxOff := len(m.treeLines) - h
	if maxOff < 0 {
		maxOff = 0
	}
	m.treeOffset = clampInt(m.treeOffset+delta, 0, maxOff)
}

// treeNodeIndexAtLine returns the last selectable node at or above a rendered
// line; clicking a line uses it to find the row under the pointer.
func (m *Model) treeNodeIndexAtLine(line int) (int, bool) {
	idx := -1
	for i, n := range m.treeNodes {
		if n.line > line {
			break
		}
		idx = i
	}
	if idx < 0 {
		return 0, false
	}
	return idx, true
}

// toggleCurrent expands or collapses the node under the tree cursor.
func (m *Model) toggleCurrent() {
	n, ok := m.currentNode()
	if !ok {
		return
	}
	switch n.kind {
	case nodeGroup:
		m.expandedGroups[n.group] = !m.expandedGroups[n.group]
	case nodeSubject:
		m.expandedMsgs[n.tuiID] = !m.expandedMsgs[n.tuiID]
	}
	m.rebuildTree()
}

// collapseCurrent is esc: it collapses exactly one level around the cursor —
// an expanded full email first, then the expanded group around it. A row with
// nothing open under it is left alone.
func (m *Model) collapseCurrent() {
	n, ok := m.currentNode()
	if !ok {
		return
	}
	switch {
	case n.kind == nodeSubject && m.expandedMsgs[n.tuiID]:
		delete(m.expandedMsgs, n.tuiID)
	case n.kind == nodeSubject && m.expandedGroups[n.group]:
		delete(m.expandedGroups, n.group)
		m.treeSel = "group:" + n.group
	case n.kind == nodeGroup && m.expandedGroups[n.group]:
		delete(m.expandedGroups, n.group)
	default:
		return
	}
	m.rebuildTree()
}

// collapseAll is the c key: every group and every full email closes.
func (m *Model) collapseAll() {
	m.expandedGroups = make(map[string]bool)
	m.expandedMsgs = make(map[string]bool)
	m.treeSel = ""
	m.treeCursor, m.treeOffset = 0, 0
	m.rebuildTree()
}

// revealEntry expands the entry's category group, selects its subject row in
// the tree, and moves focus there.
func (m *Model) revealEntry(e store.Entry) {
	m.expandedGroups[categoryOf(e)] = true
	m.focus = focusTree
	m.treeSel = "msg:" + e.TUIID
	m.rebuildTree()
	for i, n := range m.treeNodes {
		if n.kind == nodeSubject && n.tuiID == e.TUIID {
			m.setTreeCursor(i)
			return
		}
	}
}

// renderTreePane renders the visible slice of the classification tree.
func (m *Model) renderTreePane(width, height int) string {
	lines := make([]string, 0, height)
	if len(m.treeLines) == 0 {
		lines = append(lines, styleDim.Render(waitingLine))
	} else {
		sel := -1
		if m.treeCursor >= 0 && m.treeCursor < len(m.treeNodes) {
			sel = m.treeNodes[m.treeCursor].line
		}
		for i := m.treeOffset; i < len(m.treeLines) && len(lines) < height; i++ {
			line := m.treeLines[i]
			if i == sel {
				line = styleSelected.Width(width).Render(line)
			}
			lines = append(lines, line)
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return joinLines(lines)
}
