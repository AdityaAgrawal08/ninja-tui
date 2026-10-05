package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"ninja-tui/internal/sender"
	"ninja-tui/internal/store"
)

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case scanDoneMsg:
		return m.handleScan(msg)

	case importDoneMsg:
		return m.handleImportDone(msg)

	case smtpCheckMsg:
		m.smtpChecked = true
		if msg.Err != nil {
			m.smtpErr = msg.Err.Error()
		} else {
			m.smtpErr = ""
		}
		return m, nil

	case ResultMsg:
		return m.handleResult(msg)

	case sender.Event:
		return m.handleSenderEvent(msg)

	case spinner.TickMsg:
		if !m.send.Running() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		return m.handleMouse(msg)

	case tea.MouseWheelMsg:
		return m.handleWheel(msg)
	}
	return m, nil
}

// sync rebuilds every derived list and the right pane after data changes.
func (m *Model) sync() {
	m.rebuildRows()
	m.rebuildSubjects()
	m.updateDetail()
}

func (m *Model) handleScan(msg scanDoneMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.scanErr = msg.Err.Error()
		m.mode = ModeSetup
		m.sync()
		return m, nil
	}
	m.scanErr = ""
	m.importHint = ""
	m.cfg.Dir = msg.Dir
	m.nav = navList
	importSelected := m.cursorOnImport()
	m.st.Replace(msg.Files)
	m.sync()
	if importSelected && m.st.Len() > 0 {
		entries := m.st.Entries()
		m.selectKey("file:" + entries[0].TUIID)
		m.sync()
	}

	// Auto-send: a completed scan starts the run immediately. Pressing s
	// later re-sends failed files or resumes a paused run.
	if m.autoSend && m.st.Len() > 0 && !m.send.Running() && len(m.st.Pending()) > 0 {
		return m.beginSend()
	}
	if m.st.Len() == 0 {
		m.mode = ModeSetup
	} else if m.mode == ModeSetup {
		m.mode = ModeQueue
	}
	return m, nil
}

// handleImportDone resumes after the zenity dialog. Cancel is silent; a
// missing or failing zenity falls back to the built-in path input with a
// one-line install hint.
func (m *Model) handleImportDone(msg importDoneMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Canceled, msg.Err == nil && msg.Dir == "":
		return m, nil
	case msg.Err != nil:
		m.importHint = zenityHint
		m.notice = ""
		return m, m.beginPathEdit()
	}
	m.importHint = ""
	return m, m.scanCmd(msg.Dir)
}

func (m *Model) handleResult(msg ResultMsg) (tea.Model, tea.Cmd) {
	entry, ok := m.st.ApplyResult(msg.Result)
	if ok {
		m.notice = "result arrived for " + entry.Name
		if m.mode == ModeSending {
			m.mode = ModeResults
		}
	} else {
		m.notice = "result did not match any sent file; kept as unmatched"
	}
	m.sync()
	return m, nil
}

func (m *Model) handleSenderEvent(ev sender.Event) (tea.Model, tea.Cmd) {
	switch ev.Phase {
	case sender.PhaseStarted:
		m.st.SetStatus(ev.Task.ID, store.Sending, "")
		m.lastEvent = "sending " + filepath.Base(ev.Task.Path)
	case sender.PhasePaused:
		// ninja's analysis queue is full; the message stays queued and the
		// worker retries it after a manual resume.
		m.st.SetStatus(ev.Task.ID, store.Queued, "")
		m.paused = true
		m.pauseMsg = "ninja SMTP queue full (451 4.3.2) — sending paused"
		if ev.Err != nil {
			m.pauseMsg += ": " + ev.Err.Error()
		}
		m.pauseMsg += " — press s to resume"
		m.lastEvent = m.pauseMsg
	case sender.PhaseFinished:
		if ev.Err != nil {
			m.st.SetStatus(ev.Task.ID, store.Failed, ev.Err.Error())
			m.lastEvent = "failed " + filepath.Base(ev.Task.Path) + ": " + ev.Err.Error()
			if ev.Dial {
				m.smtpErr = ev.Err.Error()
			}
		} else {
			m.st.SetStatus(ev.Task.ID, store.Sent, "")
			m.lastEvent = "sent " + filepath.Base(ev.Task.Path)
			m.smtpErr = ""
		}
	case sender.PhaseRunDone:
		m.paused = false
		m.pauseMsg = ""
		m.lastEvent = ""
		m.notice = "send run finished"
		switch {
		case m.st.Counts().Results > 0:
			m.mode = ModeResults
		case m.st.Len() > 0:
			m.mode = ModeQueue
		default:
			m.mode = ModeSetup
		}
	}
	m.sync()
	return m, m.waitEventCmd()
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.editingPath {
		switch msg.String() {
		case "enter":
			dir := strings.TrimSpace(m.pathBox.Value())
			m.editingPath = false
			m.pathBox.Blur()
			if dir == "" {
				return m, nil
			}
			m.cfg.Dir = dir
			return m, m.scanCmd(dir)
		case "esc":
			m.editingPath = false
			m.pathBox.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.pathBox, cmd = m.pathBox.Update(msg)
		return m, cmd
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Path):
		if m.send.Running() {
			m.notice = "cannot change folder while sending"
			return m, nil
		}
		m.notice = ""
		return m, m.beginPathEdit()

	case key.Matches(msg, m.keys.Rescan):
		if m.send.Running() {
			m.notice = "cannot rescan while sending"
			return m, nil
		}
		m.notice = ""
		return m, m.scanCmd(m.cfg.Dir)

	case key.Matches(msg, m.keys.Back):
		return m.goBack()

	case key.Matches(msg, m.keys.Resume):
		if m.paused {
			m.send.Resume()
			m.paused = false
			m.notice = "resumed sending"
			m.sync()
			return m, nil
		}
		return m.beginSend()

	case key.Matches(msg, m.keys.Select):
		return m.activateCurrent()

	case msg.String() == "up", msg.String() == "k":
		return m.moveFocus(-1, msg)

	case msg.String() == "down", msg.String() == "j":
		return m.moveFocus(1, msg)

	case msg.String() == "pgup", msg.String() == "b":
		return m.pageFocus(-1, msg)

	case msg.String() == "pgdown", msg.String() == "f", msg.String() == " ":
		return m.pageFocus(1, msg)
	}
	return m, nil
}

func (m *Model) beginPathEdit() tea.Cmd {
	m.editingPath = true
	m.pathBox.SetValue(m.cfg.Dir)
	m.pathBox.CursorEnd()
	return m.pathBox.Focus()
}

// moveFocus moves within whichever pane has focus: left list, subject list,
// or the full-email viewport.
func (m *Model) moveFocus(delta int, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.nav {
	case navList:
		m.moveLeft(delta)
		return m, nil
	case navSubjects:
		m.moveSubject(delta)
		return m, nil
	default:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}
}

func (m *Model) pageFocus(delta int, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.nav {
	case navList:
		m.moveLeft(delta * maxInt(1, m.bodyHeight-1))
		return m, nil
	case navSubjects:
		m.moveSubject(delta * m.subjectHeight())
		return m, nil
	default:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}
}

// goBack walks the drill-down the other way: full email -> subjects ->
// files/scores.
func (m *Model) goBack() (tea.Model, tea.Cmd) {
	switch m.nav {
	case navDetail:
		if m.detailFrom == navSubjects {
			m.nav = navSubjects
		} else {
			m.nav = navList
		}
	case navSubjects:
		m.nav = navList
	default:
		if m.mode == ModeResults || m.mode == ModeSending {
			m.mode = ModeQueue
		}
	}
	m.sync()
	return m, nil
}

// activateCurrent is enter: it activates the selected left row or opens the
// selected subject.
func (m *Model) activateCurrent() (tea.Model, tea.Cmd) {
	switch m.nav {
	case navList:
		r, ok := m.rowAt(m.cursor)
		if !ok {
			return m, nil
		}
		return m.activateRow(r)
	case navSubjects:
		if m.subjCursor >= 0 && m.subjCursor < len(m.subjIdx) {
			m.openDetail(m.subjIdx[m.subjCursor], navSubjects)
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) activateRow(r listRow) (tea.Model, tea.Cmd) {
	switch r.kind {
	case rowImport:
		if m.send.Running() {
			m.notice = "cannot change folder while sending"
			return m, nil
		}
		m.notice = ""
		return m, m.importCmd()
	case rowFile:
		m.openDetail(r.file, navList)
	case rowGroup:
		m.nav = navSubjects
		m.subjGroup = r.group
		m.subjCursor, m.subjOffset = 0, 0
		m.sync()
	}
	return m, nil
}

func (m *Model) openDetail(storeIdx int, from navLevel) {
	if _, ok := m.st.Entry(storeIdx); !ok {
		return
	}
	m.nav = navDetail
	m.detailFrom = from
	m.detailIdx = storeIdx
	m.sync()
	m.detail.GotoTop()
}

// handleMouse maps a left click to a row: left-pane rows (Import button, file
// rows, group headers) and right-pane subject rows.
func (m *Model) handleMouse(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.editingPath {
		return m, nil
	}
	lw := m.leftPane
	if lw <= 0 {
		lw = m.leftWidth(m.width)
	}
	if msg.X < lw {
		const bodyTop = 1 // one header line
		y := msg.Y - bodyTop
		if y < 0 {
			return m, nil
		}
		r, ok := m.rowAt(m.leftOffset + y)
		if !ok || r.kind == rowSection {
			return m, nil
		}
		m.cursor = m.leftOffset + y
		m.ensureLeftVisible()
		return m.activateRow(r)
	}

	if m.subjectsShown() {
		const bodyTop, titleLines = 1, 1
		y := msg.Y - bodyTop - titleLines
		if y < 0 {
			return m, nil
		}
		idx := m.subjOffset + y
		if idx >= 0 && idx < len(m.subjIdx) {
			m.nav = navSubjects
			m.subjCursor = idx
			m.openDetail(m.subjIdx[idx], navSubjects)
		}
	}
	return m, nil
}

func (m *Model) handleWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	delta := 1
	if msg.Button == tea.MouseWheelUp {
		delta = -1
	}
	switch {
	case m.nav == navDetail:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	case m.subjectsShown():
		m.moveSubject(delta)
	default:
		m.moveLeft(delta)
	}
	return m, nil
}

// updateDetail refreshes the full-email viewport from the current selection.
func (m *Model) updateDetail() {
	var e store.Entry
	var ok bool
	switch {
	case m.nav == navDetail:
		e, ok = m.st.Entry(m.detailIdx)
	case m.nav == navList:
		if r, rok := m.rowAt(m.cursor); rok && r.kind == rowFile {
			e, ok = m.st.Entry(r.file)
		}
	}
	if !ok {
		m.detail.SetContent("")
		m.detailKey = ""
		return
	}
	m.detail.SetContent(m.detailContent(e))
	if e.TUIID != m.detailKey {
		m.detail.GotoTop()
	}
	m.detailKey = e.TUIID
}

// beginSend starts a run for every queued or previously failed file.
func (m *Model) beginSend() (tea.Model, tea.Cmd) {
	pending := m.st.Pending()
	if len(pending) == 0 {
		if m.st.Len() == 0 {
			m.notice = "no .eml files imported yet — press enter on [ Import ] or / to choose a folder"
		} else {
			m.notice = "nothing to send: every file already has a result (press r to rescan)"
		}
		return m, nil
	}
	if m.send.Running() {
		m.notice = "a send run is already in progress"
		return m, nil
	}

	tasks := make([]sender.Task, 0, len(pending))
	for _, p := range pending {
		m.st.SetStatus(p.ID, store.Queued, "")
		tasks = append(tasks, sender.Task{ID: p.ID, Path: p.Path, TUIID: p.TUIID})
	}
	if err := m.send.Start(tasks); err != nil {
		m.notice = err.Error()
		return m, nil
	}
	m.mode = ModeSending
	m.runTotal = len(tasks)
	m.paused = false
	m.pauseMsg = ""
	m.notice = fmt.Sprintf("sending %d file(s) to %s", len(tasks), m.cfg.SMTPAddr)
	m.sync()
	return m, m.spin.Tick
}
