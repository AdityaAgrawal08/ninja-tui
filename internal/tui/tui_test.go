package tui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ninja-tui/internal/eml"
	"ninja-tui/internal/sender"
	"ninja-tui/internal/store"
	"ninja-tui/internal/webhook"
)

func step(t *testing.T, m *Model, msg tea.Msg) *Model {
	t.Helper()
	updated, _ := m.Update(msg)
	nm, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	return nm
}

// press builds a key press message the way the runtime would.
func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	default:
		r := []rune(s)
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}

// checkView asserts the rendered view never exceeds the terminal bounds,
// which would make the terminal wrap and break the fixed layout.
func checkView(t *testing.T, m *Model, w, h int) string {
	t.Helper()
	content := m.View().Content
	lines := strings.Split(content, "\n")
	if len(lines) > h {
		t.Fatalf("view has %d lines, terminal height %d", len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got > w {
			t.Fatalf("line %d width %d > terminal width %d: %q", i, got, w, line)
		}
	}
	return content
}

// testModel returns a model with manual sending and a sized window.
func testModel(t *testing.T, w, h int) *Model {
	t.Helper()
	m := New(Config{
		Dir:      "/mail",
		SMTPAddr: "127.0.0.1:2525",
		From:     "tui@localhost",
		To:       "inbox@mx.local",
		Store:    store.New(),
	})
	m.autoSend = false
	return step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
}

// treeIndex finds a selectable tree node by its stable key.
func treeIndex(t *testing.T, m *Model, key string) int {
	t.Helper()
	for i, n := range m.treeNodes {
		if treeKey(n) == key {
			return i
		}
	}
	t.Fatalf("tree node %q not found (%d nodes)", key, len(m.treeNodes))
	return -1
}

// toggleTree selects a node by key and presses enter on it. The tree must
// already hold results.
func toggleTree(t *testing.T, m *Model, key string) *Model {
	t.Helper()
	i := treeIndex(t, m, key)
	m.focus = focusTree
	m.setTreeCursor(i)
	return step(t, m, press("enter"))
}

// treeText is every rendered tree line, regardless of scroll position.
func treeText(m *Model) string { return strings.Join(m.treeLines, "\n") }

// scanAndSend drives a scan of the given subjects and marks every file sent,
// which puts them in the pending group until a result arrives.
func scanAndSend(t *testing.T, m *Model, st *store.Store, names []string) (*Model, []store.Entry) {
	t.Helper()
	files := make([]eml.File, 0, len(names))
	for _, name := range names {
		files = append(files, eml.File{
			Path: "/mail/" + name, Name: name, Size: 10, ModTime: time.Now(),
			Subject: "subject " + name, From: "sender@example",
		})
	}
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: files})
	entries := st.Entries()
	for _, e := range entries {
		task := sender.Task{ID: e.TUIID, Path: e.Path, TUIID: e.TUIID}
		m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseStarted})
		m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseFinished})
	}
	return m, st.Entries()
}

func TestEmptyFolderShowsNinjaConfig(t *testing.T) {
	m := New(Config{
		Dir:      "/no/mail/here",
		SMTPAddr: "127.0.0.1:2525",
		From:     "tui@localhost",
		To:       "inbox@mx.local",
	})
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = step(t, m, scanDoneMsg{Dir: "/no/mail/here"})
	m.SetWebhook("127.0.0.1:18080", nil)

	if m.mode != ModeSetup {
		t.Fatalf("mode = %v, want setup", m.mode)
	}
	content := checkView(t, m, 100, 30)
	for _, want := range []string{
		"[ Import ]",
		"No File uploaded yet",
		"pair ninja: webhook_url http://127.0.0.1:18080/hook",
		"Results will appear here as ninja responds…",
		"sent 0/0",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("empty view missing %q", want)
		}
	}
}

func TestQueueResultFlow(t *testing.T) {
	st := store.New()
	m := New(Config{
		Dir:      "/mail",
		SMTPAddr: "127.0.0.1:2525",
		From:     "tui@localhost",
		To:       "inbox@mx.local",
		Store:    st,
	})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 140, Height: 50})
	files := []eml.File{{
		Path: "/mail/a.eml", Name: "a.eml", Size: 2048, ModTime: time.Now(),
		Subject: "Urgent: verify", From: "PayPal <security@paypa1.example>", SHA256: "deadbeef",
	}}
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: files})
	if m.mode != ModeQueue {
		t.Fatalf("mode = %v, want queue", m.mode)
	}
	id := st.Entries()[0].TUIID

	// Before any result, every imported file sits in the pending group.
	if got := treeText(m); !strings.Contains(got, "pending (1)") {
		t.Fatalf("right pane missing pending group:\n%s", got)
	}

	task := sender.Task{ID: id, Path: "/mail/a.eml", TUIID: id}
	m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseStarted})
	m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseFinished})

	res := webhook.Result{
		MessageSHA256: "reconstructed",
		Summary:       "classic phishing lure",
		Verdict: &webhook.Verdict{
			Score: 4, ScoreMax: 4,
			Category: &webhook.Category{Choice: "phishing", Confidence: 0.97},
		},
		Facts: webhook.Facts{
			Headers: webhook.Headers{
				MessageID:  "<" + id + ">",
				Suspicious: webhook.Suspicious{ReplyToMismatch: true},
			},
			Bodies: []webhook.Body{{ContentType: "text/plain", Text: "verify your account now"}},
			URLs:   []webhook.URL{{Raw: "https://xn--pypal-4ve.com/verify", Punycode: true}},
		},
	}
	m = step(t, m, ResultMsg{Result: res})
	if got := st.Counts().Results; got != 1 {
		t.Fatalf("results = %d, want 1", got)
	}
	m.mode = ModeResults
	content := checkView(t, m, 140, 50)
	for _, want := range []string{
		"a.eml", "processed",
		"sent 1/1", "results 1",
		"phishing (1)",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("results view missing %q", want)
		}
	}

	// The tree starts collapsed: expand the category, then the subject.
	m = toggleTree(t, m, "group:phishing")
	if got := treeText(m); !strings.Contains(got, "Urgent: verify") {
		t.Errorf("expanded group missing subject row:\n%s", got)
	}
	m = toggleTree(t, m, "msg:"+id)
	full := treeText(m)
	for _, want := range []string{
		"classic phishing lure", "phishing", "punycode", "reply-to mismatch",
		"verify your account now", "security@paypa1.example",
		"HEADERS", "VERDICT",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("full email missing %q", want)
		}
	}
	checkView(t, m, 140, 50)

	// Setup, queue, sending and results must all render within bounds.
	for _, size := range [][2]int{{140, 50}, {120, 36}, {60, 20}, {40, 15}, {30, 12}} {
		m = step(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, mode := range []Mode{ModeSetup, ModeQueue, ModeSending, ModeResults} {
			m.mode = mode
			checkView(t, m, size[0], size[1])
		}
	}
}

func TestCachedAndUnscoredRender(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 110, Height: 44})
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: []eml.File{{
		Path: "/mail/a.eml", Name: "a.eml", Size: 100, ModTime: time.Now(), Subject: "Odd one",
	}}})
	id := st.Entries()[0].TUIID
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Cached:  true,
		Partial: true,
		Facts:   webhook.Facts{Headers: webhook.Headers{MessageID: "<" + id + ">"}},
	}})
	m.mode = ModeResults
	checkView(t, m, 110, 44)
	if got := treeText(m); !strings.Contains(got, "unscored (1)") {
		t.Fatalf("tree missing unscored group:\n%s", got)
	}
	m = toggleTree(t, m, "group:unscored")
	m = toggleTree(t, m, "msg:"+id)
	full := treeText(m)
	for _, want := range []string{"cached", "partial", "unscored", "(no summary — summarizer disabled)"} {
		if !strings.Contains(full, want) {
			t.Errorf("full email missing %q", want)
		}
	}
	checkView(t, m, 110, 44)
	if got := categoryOf(st.Entries()[0]); got != unscoredKey {
		t.Errorf("categoryOf = %q, want %q", got, unscoredKey)
	}
}

func TestAutoSendStartsOnScan(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:1", From: "a@b", To: "c@d", Store: st})
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ := m.Update(scanDoneMsg{Dir: "/mail", Files: []eml.File{{
		Path: "/mail/a.eml", Name: "a.eml", Size: 10, ModTime: time.Now(),
	}}})
	m = updated.(*Model)
	defer m.Stop()
	// The run may already have finished against the dead test port, so the
	// reliable proof it started is the sending mode and run size.
	if m.mode != ModeSending {
		t.Fatalf("mode = %v, want sending (auto-send did not start)", m.mode)
	}
	if m.runTotal != 1 {
		t.Fatalf("runTotal = %d, want 1", m.runTotal)
	}
}

func TestImportFallbackOpensPathInput(t *testing.T) {
	m := testModel(t, 100, 30)
	m = step(t, m, importDoneMsg{Err: errors.New(`exec: "zenity": executable file not found in $PATH`)})
	if !m.editingPath {
		t.Fatal("missing zenity did not fall back to the path input")
	}
	if !strings.Contains(m.importHint, "sudo pacman -S zenity") {
		t.Fatalf("import hint = %q, want the pacman install line", m.importHint)
	}
	content := checkView(t, m, 100, 30)
	if !strings.Contains(content, "sudo pacman -S zenity") {
		t.Errorf("footer missing zenity hint:\n%s", content)
	}
}

func TestImportCancelIsSilent(t *testing.T) {
	m := testModel(t, 100, 30)
	m = step(t, m, importDoneMsg{Canceled: true})
	if m.editingPath || m.importHint != "" {
		t.Fatalf("cancel changed state: editing=%v hint=%q", m.editingPath, m.importHint)
	}
}

func TestImportSuccessStartsScan(t *testing.T) {
	m := testModel(t, 100, 30)
	updated, cmd := m.Update(importDoneMsg{Dir: "/picked/folder"})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("chosen folder did not schedule a scan")
	}
	if m.importHint != "" {
		t.Fatalf("hint not cleared on success: %q", m.importHint)
	}
}

func TestZenityCancelDetection(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 1").Run()
	if !isZenityCancel(err, "", "") {
		t.Error("exit 1 with empty output must be treated as cancel")
	}
	if isZenityCancel(err, "/picked", "") {
		t.Error("a chosen folder is not a cancel")
	}
	if isZenityCancel(err, "", "Gtk-WARNING: cannot open display") {
		t.Error("an error with stderr output is not a cancel")
	}
	if isZenityCancel(nil, "", "") {
		t.Error("nil error is not a cancel")
	}
}

func TestImportRowIsFirstAndActivatable(t *testing.T) {
	m := testModel(t, 100, 30)
	if len(m.rows) == 0 || m.rows[0].kind != rowImport {
		t.Fatalf("first left row = %+v, want Import button", m.rows[0])
	}
	// Enter on Import runs the zenity ExecCommand.
	_, cmd := m.Update(press("enter"))
	if cmd == nil {
		t.Fatal("enter on [ Import ] produced no command")
	}
}

func TestLeftPaneEmptyState(t *testing.T) {
	m := testModel(t, 100, 30)
	left := m.renderLeft(m.leftPane, m.bodyHeight)
	if !strings.Contains(left, emptyFilesLine) {
		t.Fatalf("empty left pane missing %q:\n%s", emptyFilesLine, left)
	}
	if !strings.Contains(left, "pair ninja: webhook_url") {
		t.Fatalf("empty left pane missing the pairing hint:\n%s", left)
	}
	content := checkView(t, m, 100, 30)
	if !strings.Contains(content, emptyFilesLine) {
		t.Errorf("view missing left empty state:\n%s", content)
	}
	if !strings.Contains(content, "pair ninja: webhook_url") {
		t.Errorf("view missing pairing hint:\n%s", content)
	}
	if m.treeNodes != nil || m.treeLines != nil {
		t.Errorf("empty tree has %d nodes, want none", len(m.treeNodes))
	}

	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: []eml.File{{
		Path: "/mail/a.eml", Name: "a.eml", Size: 10, ModTime: time.Now(),
	}}})
	if strings.Contains(m.renderLeft(m.leftPane, m.bodyHeight), emptyFilesLine) {
		t.Errorf("left pane still shows %q after import", emptyFilesLine)
	}
	if !m.cursorOnFile() {
		t.Errorf("cursor = %d, want the imported file after Import-driven scan", m.cursor)
	}
}

func TestLeftPaneOrderAndNoGroups(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: []eml.File{
		{Path: "/mail/a.eml", Name: "a.eml", Size: 10, ModTime: time.Now()},
		{Path: "/mail/b.eml", Name: "b.eml", Size: 10, ModTime: time.Now()},
	}})

	if len(m.rows) != 4 {
		t.Fatalf("rows = %d, want Import + Files + 2 files", len(m.rows))
	}
	if m.rows[0].kind != rowImport {
		t.Errorf("row 0 = %v, want Import", m.rows[0].kind)
	}
	if m.rows[1].kind != rowSection || m.rows[1].label != "Files" {
		t.Errorf("row 1 = %+v, want Files section", m.rows[1])
	}
	if m.rows[2].kind != rowFile || m.rows[3].kind != rowFile {
		t.Errorf("rows 2..3 = %v/%v, want file rows", m.rows[2].kind, m.rows[3].kind)
	}

	left := m.renderLeft(m.leftPane, m.bodyHeight)
	for _, banned := range []string{"By score", "clear scam", "likely scam", "suspicious", "risky", "legit", "unscored"} {
		if strings.Contains(left, banned) {
			t.Errorf("left pane still shows classification text %q:\n%s", banned, left)
		}
	}
}

func TestTreeCategoryGroups(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, entries := scanAndSend(t, m, st, []string{"a.eml", "b.eml", "c.eml"})

	// Files without a result already sit in the pending group.
	got := treeText(m)
	if !strings.Contains(got, "pending (3)") {
		t.Fatalf("tree missing pending group:\n%s", got)
	}
	if strings.Contains(got, waitingLine) {
		t.Errorf("tree shows the waiting line while files are pending:\n%s", got)
	}
	if len(m.treeNodes) != 1 || m.treeNodes[0].kind != nodeGroup || m.treeNodes[0].group != pendingKey {
		t.Fatalf("nodes = %+v, want the pending group only", m.treeNodes)
	}

	// A scored result creates its category group; empty groups are not shown.
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Facts: webhook.Facts{Headers: webhook.Headers{MessageID: "<" + entries[0].TUIID + ">"}},
		Verdict: &webhook.Verdict{Score: 1, ScoreMax: 4,
			Category: &webhook.Category{Choice: "phishing", Confidence: 0.9}},
	}})
	checkView(t, m, 100, 30)
	got = treeText(m)
	for _, want := range []string{"phishing (1)", "pending (2)"} {
		if !strings.Contains(got, want) {
			t.Errorf("tree missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{
		"financial_fraud (", "malware (", "advance_fee (", "extortion (",
		"other (", "legitimate (", "unscored (",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("empty group rendered: %q\n%s", banned, got)
		}
	}

	// A result that arrived without a verdict is unscored, not pending.
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Partial: true,
		Facts:   webhook.Facts{Headers: webhook.Headers{MessageID: "<" + entries[1].TUIID + ">"}},
	}})
	got = treeText(m)
	for _, want := range []string{"phishing (1)", "pending (1)", "unscored (1)"} {
		if !strings.Contains(got, want) {
			t.Errorf("tree missing %q after partial result:\n%s", want, got)
		}
	}
	if groups := buildCategoryGroups(st.Entries()); len(groups) != 3 ||
		groups[0].key != "phishing" || groups[1].key != pendingKey || groups[2].key != unscoredKey {
		t.Fatalf("groups = %+v, want phishing then pending then unscored", groups)
	}
	if hdr := categoryHeaderText(buildCategoryGroups(st.Entries())[0]); !strings.Contains(hdr, "phishing (1)") {
		t.Errorf("header %q missing name and count", hdr)
	}
}

func TestTreeExpandCollapseAndMultiExpand(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 130, Height: 60})
	files := []eml.File{
		{Path: "/mail/scam.eml", Name: "scam.eml", Size: 10, ModTime: time.Now(), Subject: "You won", From: "Bad <bad@example>"},
		{Path: "/mail/legit.eml", Name: "legit.eml", Size: 10, ModTime: time.Now(), Subject: "Lunch", From: "Friend <friend@example>"},
		{Path: "/mail/pending.eml", Name: "pending.eml", Size: 10, ModTime: time.Now(), Subject: "Wait"},
	}
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: files})
	entries := st.Entries()
	for _, e := range entries {
		task := sender.Task{ID: e.TUIID, Path: e.Path, TUIID: e.TUIID}
		m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseStarted})
		m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseFinished})
	}
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Facts: webhook.Facts{
			Headers: webhook.Headers{
				MessageID: "<" + entries[0].TUIID + ">",
				Subject:   "You won",
				From:      []webhook.Address{{Name: "Bad", Address: "bad@example"}},
				To:        []webhook.Address{{Address: "victim@example"}},
			},
			Bodies: []webhook.Body{{ContentType: "text/plain", Text: "wire the money"}},
		},
		Summary: "scam",
		Verdict: &webhook.Verdict{Score: 4, ScoreMax: 4, Category: &webhook.Category{Choice: "phishing", Confidence: 0.9}},
	}})
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Facts: webhook.Facts{
			Headers: webhook.Headers{MessageID: "<" + entries[1].TUIID + ">", Subject: "Lunch"},
			Bodies:  []webhook.Body{{ContentType: "text/plain", Text: "let us do lunch"}},
		},
		Verdict: &webhook.Verdict{Score: 0, ScoreMax: 4, Category: &webhook.Category{Choice: "legitimate", Confidence: 0.8}},
	}})

	counts := map[string]int{}
	for _, e := range st.Entries() {
		counts[categoryOf(e)]++
	}
	if counts["phishing"] != 1 || counts["legitimate"] != 1 || counts[pendingKey] != 1 {
		t.Fatalf("group counts = %v, want one each of phishing, legitimate, pending", counts)
	}

	// Collapsed tree: headers only, subjects hidden.
	got := treeText(m)
	for _, want := range []string{"phishing (1)", "legitimate (1)", "pending (1)"} {
		if !strings.Contains(got, want) {
			t.Errorf("tree missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "You won") || strings.Contains(got, "Lunch") {
		t.Errorf("collapsed tree leaks subjects:\n%s", got)
	}

	// Keyboard: tab focuses the tree, enter expands the group under cursor.
	m = step(t, m, press("tab"))
	if m.focus != focusTree {
		t.Fatalf("focus = %v, want tree", m.focus)
	}
	m.setTreeCursor(treeIndex(t, m, "group:phishing"))
	m = step(t, m, press("enter"))
	got = treeText(m)
	if !strings.Contains(got, "You won") || !strings.Contains(got, "bad@example") {
		t.Fatalf("expanded group missing subject row:\n%s", got)
	}
	if !m.expandedGroups["phishing"] {
		t.Errorf("expandedGroups[phishing] = false, want true")
	}

	// Down selects the subject row; enter inlines the full email.
	m = step(t, m, press("down"))
	m = step(t, m, press("enter"))
	got = treeText(m)
	if !strings.Contains(got, "wire the money") || !strings.Contains(got, "victim@example") {
		t.Fatalf("full email not inlined:\n%s", got)
	}

	// Multi-expand: two groups open at once.
	m = toggleTree(t, m, "group:legitimate")
	got = treeText(m)
	if !strings.Contains(got, "You won") || !strings.Contains(got, "Lunch") {
		t.Fatalf("multi-expanded groups not both visible:\n%s", got)
	}
	// Multi-expand: two full emails open at once.
	m = toggleTree(t, m, "msg:"+entries[1].TUIID)
	got = treeText(m)
	if !strings.Contains(got, "wire the money") || !strings.Contains(got, "let us do lunch") {
		t.Fatalf("multi-expanded full emails not both visible:\n%s", got)
	}

	// Collapsing one group hides only that group.
	m = toggleTree(t, m, "group:phishing")
	got = treeText(m)
	if strings.Contains(got, "You won") || strings.Contains(got, "wire the money") {
		t.Errorf("collapsed group still visible:\n%s", got)
	}
	if !strings.Contains(got, "Lunch") || !strings.Contains(got, "let us do lunch") {
		t.Errorf("other group collapsed too:\n%s", got)
	}

	// esc collapses the current node only when it is expanded.
	m.setTreeCursor(treeIndex(t, m, "msg:"+entries[1].TUIID))
	m = step(t, m, press("esc"))
	got = treeText(m)
	if strings.Contains(got, "let us do lunch") {
		t.Errorf("esc did not collapse the full email:\n%s", got)
	}
	if !strings.Contains(got, "Lunch") {
		t.Errorf("esc collapsed the subject row too:\n%s", got)
	}
	// Second esc collapses the containing group — exactly one level, so the
	// other group stays open.
	m = step(t, m, press("esc"))
	got = treeText(m)
	if strings.Contains(got, "Lunch") || m.expandedGroups["legitimate"] {
		t.Errorf("esc did not collapse the legitimate group:\n%s", got)
	}
	if !strings.Contains(got, "legitimate (1)") {
		t.Errorf("category header lost after esc:\n%s", got)
	}

	// c collapses everything from any state.
	m = toggleTree(t, m, "group:phishing")
	m = toggleTree(t, m, "group:legitimate")
	if !strings.Contains(treeText(m), "You won") {
		t.Fatal("setup for collapse-all failed")
	}
	m = step(t, m, press("c"))
	got = treeText(m)
	if strings.Contains(got, "You won") || strings.Contains(got, "Lunch") {
		t.Errorf("c did not collapse all:\n%s", got)
	}

	// Expanded trees render within bounds at any size.
	m = toggleTree(t, m, "group:phishing")
	m = toggleTree(t, m, "msg:"+entries[0].TUIID)
	m = toggleTree(t, m, "group:legitimate")
	for _, size := range [][2]int{{130, 60}, {120, 36}, {60, 20}, {40, 15}, {30, 12}, {24, 10}, {20, 8}} {
		m = step(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		checkView(t, m, size[0], size[1])
	}
}

func TestExpansionPreservedOnResultArrival(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 130, Height: 60})
	files := []eml.File{
		{Path: "/mail/a.eml", Name: "a.eml", Size: 10, ModTime: time.Now(), Subject: "Alpha"},
		{Path: "/mail/b.eml", Name: "b.eml", Size: 10, ModTime: time.Now(), Subject: "Beta"},
		{Path: "/mail/c.eml", Name: "c.eml", Size: 10, ModTime: time.Now(), Subject: "Gamma"},
	}
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: files})
	entries := st.Entries()
	a, b, c := entries[0], entries[1], entries[2]
	for _, e := range entries {
		task := sender.Task{ID: e.TUIID, Path: e.Path, TUIID: e.TUIID}
		m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseStarted})
		m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseFinished})
	}

	m = step(t, m, ResultMsg{Result: webhook.Result{
		Facts: webhook.Facts{
			Headers: webhook.Headers{MessageID: "<" + a.TUIID + ">", Subject: "Alpha"},
			Bodies:  []webhook.Body{{ContentType: "text/plain", Text: "alpha body"}},
		},
		Verdict: &webhook.Verdict{Score: 4, ScoreMax: 4,
			Category: &webhook.Category{Choice: "phishing", Confidence: 0.9}},
	}})
	m = toggleTree(t, m, "group:phishing")
	m = toggleTree(t, m, "msg:"+a.TUIID)
	// Pending holds b and c; keep it open across the next arrival too.
	m = toggleTree(t, m, "group:pending")
	if got := treeText(m); !strings.Contains(got, "alpha body") ||
		!strings.Contains(got, "Beta") || !strings.Contains(got, "Gamma") {
		t.Fatalf("expanded rows missing before second result:\n%s", got)
	}

	// A later result for another file must not collapse what is open.
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Facts: webhook.Facts{
			Headers: webhook.Headers{MessageID: "<" + b.TUIID + ">", Subject: "Beta"},
			Bodies:  []webhook.Body{{ContentType: "text/plain", Text: "beta body"}},
		},
		Verdict: &webhook.Verdict{Score: 0, ScoreMax: 4,
			Category: &webhook.Category{Choice: "legitimate", Confidence: 0.8}},
	}})
	if !m.expandedGroups["phishing"] || !m.expandedGroups[pendingKey] || !m.expandedMsgs[a.TUIID] {
		t.Fatalf("expansion lost after result: groups=%v msgs=%v", m.expandedGroups, m.expandedMsgs)
	}
	if got := treeText(m); !strings.Contains(got, "alpha body") ||
		!strings.Contains(got, "legitimate (1)") || !strings.Contains(got, "Gamma") {
		t.Fatalf("tree wrong after second result:\n%s", got)
	}

	// Progress updates rebuild the rows but keep expansion too.
	taskC := sender.Task{ID: c.TUIID, Path: c.Path, TUIID: c.TUIID}
	m = step(t, m, sender.Event{Task: taskC, Phase: sender.PhaseStarted})
	m = step(t, m, sender.Event{Task: taskC, Phase: sender.PhaseFinished})
	if !m.expandedGroups["phishing"] || !m.expandedGroups[pendingKey] || !m.expandedMsgs[a.TUIID] {
		t.Fatalf("expansion lost after progress update: groups=%v msgs=%v", m.expandedGroups, m.expandedMsgs)
	}
	if got := treeText(m); !strings.Contains(got, "alpha body") {
		t.Fatalf("full email lost after progress update:\n%s", got)
	}
	checkView(t, m, 130, 60)
}

func TestMouseClicks(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})
	files := []eml.File{
		{Path: "/mail/a.eml", Name: "a.eml", Size: 10, ModTime: time.Now(), Subject: "Alpha"},
		{Path: "/mail/b.eml", Name: "b.eml", Size: 10, ModTime: time.Now(), Subject: "Beta"},
	}
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: files})
	entries := st.Entries()
	for i, e := range entries {
		m = step(t, m, ResultMsg{Result: webhook.Result{
			Facts: webhook.Facts{
				Headers: webhook.Headers{MessageID: "<" + e.TUIID + ">"},
				Bodies:  []webhook.Body{{ContentType: "text/plain", Text: "case " + itoa(i)}},
			},
			Verdict: &webhook.Verdict{Score: 2, ScoreMax: 4, Category: &webhook.Category{Choice: "suspicious", Confidence: 0.5}},
			Summary: "case " + itoa(i),
		}})
	}

	// Left rows: 0 import, 1 Files, 2 a.eml, 3 b.eml. Clicking a file row
	// selects and highlights it without leaving the left pane.
	updated, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 1 + 3, Button: tea.MouseLeft})
	m = updated.(*Model)
	if m.focus != focusLeft || m.cursor != 3 {
		t.Fatalf("click on b.eml -> focus=%v cursor=%d, want left/3", m.focus, m.cursor)
	}

	// The Import button is clickable too and starts the zenity command.
	updated, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("click on [ Import ] produced no command")
	}

	// Click the "suspicious" category header to expand it.
	lw, _ := m.split(m.width)
	gi := treeIndex(t, m, "group:suspicious")
	line := m.treeNodes[gi].line
	updated, _ = m.Update(tea.MouseClickMsg{X: lw + 3, Y: 1 + line, Button: tea.MouseLeft})
	m = updated.(*Model)
	if m.focus != focusTree || !m.expandedGroups["suspicious"] {
		t.Fatalf("click on header -> focus=%v expanded=%v", m.focus, m.expandedGroups["suspicious"])
	}
	if len(m.treeNodes) != 3 { // 1 category header + 2 subjects
		t.Fatalf("tree nodes = %d, want 3 after expanding a group", len(m.treeNodes))
	}

	// Click a subject row to inline its full email, click it again to hide it.
	si := treeIndex(t, m, "msg:"+entries[0].TUIID)
	line = m.treeNodes[si].line
	updated, _ = m.Update(tea.MouseClickMsg{X: lw + 3, Y: 1 + line, Button: tea.MouseLeft})
	m = updated.(*Model)
	if !m.expandedMsgs[entries[0].TUIID] {
		t.Fatal("click on subject did not expand the full email")
	}
	if got := treeText(m); !strings.Contains(got, "case 0") {
		t.Fatalf("clicked subject did not inline the email:\n%s", got)
	}
	updated, _ = m.Update(tea.MouseClickMsg{X: lw + 3, Y: 1 + line, Button: tea.MouseLeft})
	m = updated.(*Model)
	if m.expandedMsgs[entries[0].TUIID] {
		t.Fatal("second click did not collapse the full email")
	}

	// The wheel over the right pane scrolls the tree.
	updated, _ = m.Update(tea.MouseClickMsg{X: lw + 3, Y: 1 + line, Button: tea.MouseLeft})
	m = updated.(*Model)
	updated, _ = m.Update(tea.MouseWheelMsg{X: lw + 2, Y: 5, Button: tea.MouseWheelDown})
	m = updated.(*Model)
	if m.treeOffset == 0 {
		t.Fatal("wheel over the right pane did not scroll the tree")
	}
	updated, _ = m.Update(tea.MouseWheelMsg{X: lw + 2, Y: 5, Button: tea.MouseWheelUp})
	m = updated.(*Model)
	if m.treeOffset != 0 {
		t.Fatalf("wheel up did not scroll back: offset = %d", m.treeOffset)
	}
	checkView(t, m, 120, 36)
}

func TestTinyTerminalBounds(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false

	sizes := [][2]int{{120, 36}, {60, 20}, {40, 15}, {30, 12}, {24, 10}, {20, 8}}
	// Empty state first.
	for _, size := range sizes {
		m = step(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		checkView(t, m, size[0], size[1])
	}

	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: []eml.File{
		{Path: "/mail/a.eml", Name: "a.eml", Size: 10, ModTime: time.Now(), Subject: "A very long subject line that must wrap or truncate"},
		{Path: "/mail/b.eml", Name: "b.eml", Size: 10, ModTime: time.Now(), Subject: "Beta"},
	}})
	entries := st.Entries()
	for _, e := range entries {
		task := sender.Task{ID: e.TUIID, Path: e.Path, TUIID: e.TUIID}
		m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseStarted})
		m = step(t, m, sender.Event{Task: task, Phase: sender.PhaseFinished})
	}
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Facts: webhook.Facts{
			Headers: webhook.Headers{MessageID: "<" + entries[0].TUIID + ">"},
			Bodies:  []webhook.Body{{ContentType: "text/plain", Text: strings.Repeat("long body word ", 30)}},
			URLs:    []webhook.URL{{Raw: "https://example.com/" + strings.Repeat("x", 80)}},
		},
		Summary: strings.Repeat("summary ", 40),
		Verdict: &webhook.Verdict{Score: 4, ScoreMax: 4, Category: &webhook.Category{Choice: "phishing", Confidence: 1}},
	}})
	m = toggleTree(t, m, "group:phishing")
	m = toggleTree(t, m, "msg:"+entries[0].TUIID)

	for _, size := range sizes {
		m = step(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, mode := range []Mode{ModeQueue, ModeResults, ModeSending} {
			m.mode = mode
			checkView(t, m, size[0], size[1])
		}
	}
}

func TestFileRowStates(t *testing.T) {
	e := store.Entry{Name: "a.eml"}
	for status, want := range map[store.Status]string{
		store.Queued:  "queued",
		store.Sending: "sending",
		store.Failed:  "failed",
	} {
		e.Status = status
		if got := fileRowText(e); !strings.Contains(got, want) {
			t.Errorf("status %v: %q missing %q", status, got, want)
		}
	}
	e.Status = store.Sent
	if got := fileRowText(e); !strings.Contains(got, "sent") {
		t.Errorf("sent: %q missing sent", got)
	}
	e.Result = &webhook.Result{Verdict: &webhook.Verdict{Score: 3, ScoreMax: 4}}
	if got := fileRowText(e); !strings.Contains(got, "processed") {
		t.Errorf("processed: %q missing processed", got)
	}
}

func TestCategoryOf(t *testing.T) {
	cases := []struct {
		name string
		e    store.Entry
		want string
	}{
		{"queued", store.Entry{Status: store.Queued}, pendingKey},
		{"sending", store.Entry{Status: store.Sending}, pendingKey},
		{"failed", store.Entry{Status: store.Failed}, pendingKey},
		{"sent no result", store.Entry{Status: store.Sent}, pendingKey},
		{"nil verdict", store.Entry{Result: &webhook.Result{}}, unscoredKey},
		{"partial result", store.Entry{Result: &webhook.Result{Partial: true}}, unscoredKey},
		{"nil category", store.Entry{Result: &webhook.Result{
			Verdict: &webhook.Verdict{Score: 3, ScoreMax: 4}}}, unscoredKey},
		{"empty choice", store.Entry{Result: &webhook.Result{
			Verdict: &webhook.Verdict{Category: &webhook.Category{}}}}, unscoredKey},
		{"phishing", store.Entry{Result: &webhook.Result{
			Verdict: &webhook.Verdict{Category: &webhook.Category{Choice: "phishing"}}}}, "phishing"},
		{"choice lowercased", store.Entry{Result: &webhook.Result{
			Verdict: &webhook.Verdict{Category: &webhook.Category{Choice: "Financial_Fraud"}}}}, "financial_fraud"},
		{"unknown choice", store.Entry{Result: &webhook.Result{
			Verdict: &webhook.Verdict{Category: &webhook.Category{Choice: "romance"}}}}, "romance"},
	}
	for _, tc := range cases {
		if got := categoryOf(tc.e); got != tc.want {
			t.Errorf("%s: categoryOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBuildCategoryGroups(t *testing.T) {
	scored := func(choice string, score float64) *webhook.Result {
		return &webhook.Result{Verdict: &webhook.Verdict{
			Score: score, ScoreMax: 4, Category: &webhook.Category{Choice: choice},
		}}
	}
	entries := []store.Entry{
		{TUIID: "a", Result: scored("phishing", 1)},
		{TUIID: "b", Status: store.Sent},
		{TUIID: "c", Result: scored("phishing", 4)},
		{TUIID: "d", Result: &webhook.Result{Partial: true}},
		{TUIID: "e", Result: scored("malware", 2)},
		{TUIID: "f", Result: scored("legitimate", 0)},
		{TUIID: "g", Result: scored("advance_fee", 3)},
		{TUIID: "h", Result: scored("romance", 9)},
		{TUIID: "i", Result: scored("crypto", 2)},
		{TUIID: "j", Result: scored("romance", 1)},
		{TUIID: "k", Result: scored("financial_fraud", 2)},
		{TUIID: "l", Result: &webhook.Result{Verdict: &webhook.Verdict{Score: 2, ScoreMax: 4}}},
	}
	groups := buildCategoryGroups(entries)
	var keys []string
	for _, g := range groups {
		keys = append(keys, g.key)
	}
	want := []string{
		"phishing", "financial_fraud", "malware", "advance_fee", "legitimate",
		"romance", "crypto", pendingKey, unscoredKey,
	}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("group order = %v, want %v", keys, want)
	}
	if len(groups[0].entries) != 2 || !groups[0].hasScore || groups[0].score != 4 ||
		groups[0].scoreMax != 4 || groups[0].label != "phishing" {
		t.Errorf("phishing group = %+v, want 2 entries (a, c) with max score 4", groups[0])
	}
	if groups[1].label != "financial_fraud" {
		t.Errorf("known category encountered late = %q, want financial_fraud before unknown choices", groups[1].label)
	}
	if groups[4].label != "legitimate" {
		t.Errorf("known category order = %q at index 4, want legitimate last among known", groups[4].label)
	}
	if len(groups[5].entries) != 2 || groups[5].label != "romance" ||
		!groups[5].hasScore || groups[5].score != 9 || groups[5].scoreMax != 4 {
		t.Errorf("romance group = %+v, want 2 entries (h, j) with max score 9", groups[5])
	}
	if groups[6].label != "crypto" {
		t.Errorf("unknown category order = %q at index 6, want crypto in encounter order", groups[6].label)
	}
	if len(groups[7].entries) != 1 || groups[7].label != pendingKey || groups[7].hasScore {
		t.Errorf("pending group = %+v, want 1 entry and no score", groups[7])
	}
	if len(groups[8].entries) != 2 || groups[8].label != unscoredKey ||
		!groups[8].hasScore || groups[8].score != 2 || groups[8].scoreMax != 4 {
		t.Errorf("unscored group = %+v, want 2 entries (d, l) with max score 2 from the verdict without a category", groups[8])
	}
	if hdr := categoryHeaderText(groups[0]); !strings.Contains(hdr, "phishing (2)") {
		t.Errorf("header %q missing name and count", hdr)
	}
	if hdr := categoryHeaderText(groups[5]); !strings.Contains(hdr, "romance (2)") {
		t.Errorf("header %q missing unknown category name and count", hdr)
	}
	if hdr := categoryHeaderText(groups[7]); !strings.Contains(hdr, "pending (1)") {
		t.Errorf("pending header %q missing name and count", hdr)
	}
}
