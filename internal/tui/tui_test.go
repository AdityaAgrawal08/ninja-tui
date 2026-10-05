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
		"webhook_url: http://127.0.0.1:18080/hook",
		"ninja smtp --config <file>",
		"NINJA_SMTP_WEBHOOK_URL",
		"pair ninja: webhook_url http://127.0.0.1:18080/hook",
		"sent 0/0",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("setup view missing %q", want)
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
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})
	files := []eml.File{{
		Path: "/mail/a.eml", Name: "a.eml", Size: 2048, ModTime: time.Now(),
		Subject: "Urgent: verify", From: "PayPal <security@paypa1.example>", SHA256: "deadbeef",
	}}
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: files})
	if m.mode != ModeQueue {
		t.Fatalf("mode = %v, want queue", m.mode)
	}
	id := st.Entries()[0].TUIID

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
	content := checkView(t, m, 120, 36)
	for _, want := range []string{
		"a.eml", "processed",
		"classic phishing lure", "phishing", "punycode", "reply-to mismatch",
		"verify your account now",
		"sent 1/1", "results 1",
		"clear scam", "likely scam", "suspicious", "risky", "legit",
		"pending", "unscored",
	} {
		if !strings.Contains(m.detail.GetContent(), want) && !strings.Contains(content, want) {
			t.Errorf("results view missing %q", want)
		}
	}

	// Setup, queue, sending and results must all render within bounds.
	for _, size := range [][2]int{{120, 36}, {60, 20}, {200, 50}, {40, 15}, {30, 12}} {
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
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: []eml.File{{
		Path: "/mail/a.eml", Name: "a.eml", Size: 100, ModTime: time.Now(),
	}}})
	id := st.Entries()[0].TUIID
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Cached:  true,
		Partial: true,
		Facts:   webhook.Facts{Headers: webhook.Headers{MessageID: "<" + id + ">"}},
	}})
	m.mode = ModeResults
	checkView(t, m, 100, 30)
	full := m.detail.GetContent()
	for _, want := range []string{"cached", "partial", "unscored", "(no summary — summarizer disabled)"} {
		if !strings.Contains(full, want) {
			t.Errorf("full email missing %q", want)
		}
	}
	if got := groupOf(st.Entries()[0]); got != "unscored" {
		t.Errorf("groupOf = %q, want unscored", got)
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

func TestGroupsAndDrillDown(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})
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
				CC:        []webhook.Address{{Address: "cc@example"}},
				ReplyTo:   []webhook.Address{{Address: "replies@example"}},
				Date:      "Mon, 5 Oct 2026 10:00:00 +0000",
			},
			Bodies: []webhook.Body{{ContentType: "text/plain", Text: "wire the money"}},
		},
		Summary: "scam",
		Verdict: &webhook.Verdict{Score: 4, ScoreMax: 4, Category: &webhook.Category{Choice: "clear scam", Confidence: 0.9}},
	}})
	m = step(t, m, ResultMsg{Result: webhook.Result{
		Facts: webhook.Facts{Headers: webhook.Headers{
			MessageID: "<" + entries[1].TUIID + ">",
			Subject:   "Lunch",
		}},
		Verdict: &webhook.Verdict{Score: 0, ScoreMax: 4, Category: &webhook.Category{Choice: "legit", Confidence: 0.8}},
	}})

	counts := map[string]int{}
	for _, e := range st.Entries() {
		if g := groupOf(e); g != "" {
			counts[g]++
		}
	}
	if counts["4"] != 1 || counts["0"] != 1 || counts["pending"] != 1 {
		t.Fatalf("group counts = %v, want one each of 4, 0, pending", counts)
	}

	// Select the "4 clear scam" group; the right pane shows its subjects.
	m.selectKey("group:4")
	m.sync()
	if !m.subjectsShown() || len(m.subjIdx) != 1 {
		t.Fatalf("group subjects = %v, shown=%v", m.subjIdx, m.subjectsShown())
	}
	content := checkView(t, m, 120, 36)
	if !strings.Contains(content, "You won") || !strings.Contains(content, "bad@example") {
		t.Errorf("subject list missing row:\n%s", content)
	}

	// Enter focuses the subject list, enter again opens the full email.
	m = step(t, m, press("enter"))
	if m.nav != navSubjects {
		t.Fatalf("nav = %v, want subjects", m.nav)
	}
	m = step(t, m, press("enter"))
	if m.nav != navDetail {
		t.Fatalf("nav = %v, want detail", m.nav)
	}
	checkView(t, m, 120, 36)
	full := m.detail.GetContent()
	for _, want := range []string{
		"wire the money", "bad@example", "victim@example",
		"cc@example", "replies@example", "Mon, 5 Oct 2026 10:00:00 +0000",
		"message-id", "VERDICT", "confidence", "clear scam",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("full email missing %q", want)
		}
	}

	// Esc walks back: full email -> subjects -> files/scores.
	m = step(t, m, press("esc"))
	if m.nav != navSubjects {
		t.Fatalf("after esc nav = %v, want subjects", m.nav)
	}
	m = step(t, m, press("esc"))
	if m.nav != navList {
		t.Fatalf("after second esc nav = %v, want list", m.nav)
	}

	// Subject lists and full emails must render within bounds at any size.
	for _, size := range [][2]int{{120, 36}, {60, 20}, {40, 15}, {30, 12}} {
		m = step(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.selectKey("group:4")
		m.sync()
		if m.nav != navSubjects {
			m.nav = navSubjects
		}
		checkView(t, m, size[0], size[1])
		m.nav = navDetail
		m.detailIdx = m.subjIdx[0]
		m.sync()
		checkView(t, m, size[0], size[1])
		m.nav = navList
		m.sync()
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

func TestLeftPaneOrder(t *testing.T) {
	st := store.New()
	m := New(Config{Dir: "/mail", SMTPAddr: "127.0.0.1:2525", From: "a@b", To: "c@d", Store: st})
	m.autoSend = false
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = step(t, m, scanDoneMsg{Dir: "/mail", Files: []eml.File{{
		Path: "/mail/a.eml", Name: "a.eml", Size: 10, ModTime: time.Now(),
	}}})

	if m.rows[0].kind != rowImport {
		t.Errorf("row 0 = %v, want Import", m.rows[0].kind)
	}
	byScore := -1
	for i, r := range m.rows {
		if r.kind == rowSection && r.label == "Files" {
			if i != 1 {
				t.Errorf("Files section at %d, want 1", i)
			}
		}
		if r.kind == rowSection && r.label == "By score" {
			byScore = i
		}
	}
	if byScore < 2 {
		t.Fatalf("By score section not after Files: index %d", byScore)
	}
	if m.rows[2].kind != rowFile {
		t.Errorf("first Files row = %v, want file", m.rows[2].kind)
	}
	if m.rows[byScore+1].kind != rowGroup {
		t.Errorf("first By score row = %v, want group", m.rows[byScore+1].kind)
	}
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
			Facts:   webhook.Facts{Headers: webhook.Headers{MessageID: "<" + e.TUIID + ">"}},
			Verdict: &webhook.Verdict{Score: 2, ScoreMax: 4, Category: &webhook.Category{Choice: "suspicious", Confidence: 0.5}},
			Summary: "case " + itoa(i),
		}})
	}

	// Left rows: 0 import, 1 Files, 2 a.eml, 3 b.eml, 4 By score, 5.. groups.
	updated, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 1 + 3, Button: tea.MouseLeft}) // b.eml
	m = updated.(*Model)
	if m.nav != navDetail || m.detailIdx != 1 {
		t.Fatalf("click on b.eml -> nav=%v idx=%d, want detail/1", m.nav, m.detailIdx)
	}
	m = step(t, m, press("esc"))

	// Click the "2 suspicious" group header (row 7).
	updated, _ = m.Update(tea.MouseClickMsg{X: 5, Y: 1 + 7, Button: tea.MouseLeft})
	m = updated.(*Model)
	if m.nav != navSubjects || m.subjGroup != "2" || len(m.subjIdx) != 2 {
		t.Fatalf("click on group -> nav=%v group=%q n=%d", m.nav, m.subjGroup, len(m.subjIdx))
	}

	// Subject rows start one line below the right-pane title.
	lw := m.leftPane
	updated, _ = m.Update(tea.MouseClickMsg{X: lw + 3, Y: 1 + 1, Button: tea.MouseLeft})
	m = updated.(*Model)
	if m.nav != navDetail || m.detailIdx != 0 {
		t.Fatalf("click on subject -> nav=%v idx=%d, want detail/0", m.nav, m.detailIdx)
	}

	// The Import button is clickable too and starts the zenity command.
	updated, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("click on [ Import ] produced no command")
	}
	if m.nav != navDetail {
		t.Fatalf("import click changed nav to %v", m.nav)
	}
}

func TestGroupOf(t *testing.T) {
	cases := []struct {
		name string
		e    store.Entry
		want string
	}{
		{"queued", store.Entry{Status: store.Queued}, ""},
		{"sending", store.Entry{Status: store.Sending}, ""},
		{"failed", store.Entry{Status: store.Failed}, ""},
		{"sent no result", store.Entry{Status: store.Sent}, "pending"},
		{"nil verdict", store.Entry{Status: store.Sent, Result: &webhook.Result{}}, "unscored"},
		{"score 4", store.Entry{Status: store.Sent, Result: &webhook.Result{Verdict: &webhook.Verdict{Score: 4, ScoreMax: 4}}}, "4"},
		{"score 0", store.Entry{Status: store.Sent, Result: &webhook.Result{Verdict: &webhook.Verdict{Score: 0, ScoreMax: 4}}}, "0"},
		{"scaled", store.Entry{Status: store.Sent, Result: &webhook.Result{Verdict: &webhook.Verdict{Score: 5, ScoreMax: 10}}}, "2"},
	}
	for _, tc := range cases {
		if got := groupOf(tc.e); got != tc.want {
			t.Errorf("%s: groupOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}
