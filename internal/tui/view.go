package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// View implements tea.Model. Layout: one header line, a split body
// (left list | separator | right pane), and a two-line footer
// (status + key hints). Mouse clicks/releases/wheel are enabled here.
func (m *Model) View() tea.View {
	if !m.ready {
		v := tea.NewView("starting ninja-tui…")
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	w, h := m.width, m.height
	header := ansi.Truncate(m.renderHeader(), w, "…")
	footer := m.renderFooter(w)
	headerH := lipgloss.Height(header)
	footerH := lipgloss.Height(footer)
	bodyH := h - headerH - footerH
	if bodyH < 3 {
		bodyH = 3
	}
	m.bodyHeight = bodyH

	lw := m.leftWidth(w)
	rw := w - lw - 1
	if rw < 12 {
		rw = 12
		lw = maxInt(10, w-rw-1)
	}
	m.leftPane = lw

	left := fitBlock(m.renderLeft(lw, bodyH), lw, bodyH)
	right := fitBlock(m.renderRight(rw, bodyH), rw, bodyH)
	sep := styleDim.Render(strings.Repeat("│\n", bodyH-1) + "│")

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, sep, right)
	content := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	content = lipgloss.NewStyle().MaxWidth(w).Render(content)

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// leftWidth is shared by View, layout, and mouse hit-testing so all three
// agree on the split.
func (m *Model) leftWidth(total int) int {
	lw := int(float64(total) * 0.42)
	if lw < 28 {
		lw = 28
	}
	if lw > 64 {
		lw = 64
	}
	if lw > total-24 {
		lw = total - 24
	}
	if lw < 10 {
		lw = 10
	}
	return lw
}

func (m *Model) renderHeader() string {
	return styleTitle.Render("ninja-tui") +
		"  " + styleDim.Render("["+m.mode.String()+"]") +
		"  " + styleDim.Render("dir ") + styleText.Render(m.cfg.Dir)
}

// renderRight picks the right-pane content: the subject list of the selected
// group, the full email context, or the setup/pairing text.
func (m *Model) renderRight(rw, bodyH int) string {
	switch {
	case m.subjectsShown():
		return m.renderSubjects(rw, bodyH)
	case m.nav == navDetail || m.cursorOnFile():
		return m.renderDetail(rw, bodyH)
	default:
		return m.renderSetupPane(rw, bodyH)
	}
}

// renderDetail renders the full email context in a scrollable viewport,
// with the send progress above it while a run is in flight.
func (m *Model) renderDetail(rw, bodyH int) string {
	prefix := m.progressBlock(rw)
	if m.nav == navDetail {
		hint := styleDim.Render("full email — ↑/↓ scroll · esc back")
		if prefix == "" {
			prefix = fitBlock(hint, rw, 1)
		} else {
			prefix += "\n" + hint
		}
	}
	prefixH := 0
	if prefix != "" {
		prefixH = lipgloss.Height(prefix)
		if prefixH >= bodyH {
			return fitBlock(prefix, rw, bodyH)
		}
	}
	m.detail.SetWidth(rw)
	m.detail.SetHeight(bodyH - prefixH)
	return prefix + m.detail.View()
}

func (m *Model) progressBlock(rw int) string {
	counts := m.st.Counts()
	var lines []string
	if m.send.Running() {
		done := counts.Sent + counts.Failed
		pct := 0.0
		if m.runTotal > 0 {
			pct = float64(done) / float64(m.runTotal)
		}
		lines = append(lines,
			m.spin.View()+" "+styleText.Render(fmt.Sprintf("sending %d/%d  results %d", done, m.runTotal, counts.Results)),
			m.prog.ViewAs(pct),
		)
		if m.paused {
			lines = append(lines, styleBad.Render(m.pauseMsg))
		}
	} else if m.notice != "" {
		lines = append(lines, styleNotice.Render(m.notice))
	}
	if len(lines) == 0 {
		return ""
	}
	return fitBlock(joinLines(lines), rw, len(lines))
}

func (m *Model) renderSetupPane(rw, bodyH int) string {
	v := viewport.New(viewport.WithWidth(rw), viewport.WithHeight(bodyH))
	v.SoftWrap = true
	v.SetContent(m.setupText())
	return v.View()
}

// setupText is the first-run and empty-folder screen. It shows the exact
// ninja-side configuration the user needs, because the TUI never starts ninja.
func (m *Model) setupText() string {
	var b strings.Builder
	hook := webhookURL(m.webhookAddr)

	b.WriteString(styleTitle.Render("Import .eml files"))
	b.WriteString("\n\n")
	b.WriteString(styleText.Render("Press enter on [ Import ] to pick the folder with the system dialog."))
	b.WriteString("\n")
	b.WriteString(styleDim.Render("zenity missing? install it: sudo pacman -S zenity — or press / and type a path."))
	b.WriteString("\n\n")

	switch {
	case m.scanErr != "":
		b.WriteString(styleBad.Render("Could not scan folder:"))
		b.WriteString("\n")
		b.WriteString(styleText.Render(m.cfg.Dir))
		b.WriteString("\n")
		b.WriteString(styleDim.Render(m.scanErr))
		b.WriteString("\n\n")
	case m.st.Len() == 0:
		b.WriteString(styleText.Render("No .eml files in this folder yet."))
		b.WriteString("\n")
		b.WriteString(styleDim.Render("Put exported mail there, press / to type another folder, or r to rescan."))
		b.WriteString("\n\n")
	}

	b.WriteString(styleText.Render("This TUI talks to a ninja SMTP server that you run yourself."))
	b.WriteString("\n")
	b.WriteString(styleText.Render("It never starts or manages ninja."))
	b.WriteString("\n\n")

	b.WriteString(styleSection.Render("PAIRING WITH NINJA"))
	b.WriteString("\n")
	b.WriteString(styleText.Render("Point ninja's SMTP webhook at this TUI:"))
	b.WriteString("\n\n")
	b.WriteString(styleText.Render("  smtp:"))
	b.WriteString("\n")
	b.WriteString(styleWarn.Render("    webhook_url: " + hook))
	b.WriteString("\n\n")
	b.WriteString(styleDim.Render("  # equivalent environment variable:"))
	b.WriteString("\n")
	b.WriteString(styleDim.Render("  NINJA_SMTP_WEBHOOK_URL=" + hook))
	b.WriteString("\n\n")
	b.WriteString(styleText.Render("Run it yourself, then leave it running:"))
	b.WriteString("\n\n")
	b.WriteString(styleWarn.Render("  ninja smtp --config <file>"))
	b.WriteString("\n\n")

	b.WriteString(styleSection.Render("STATUS"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s %s %s\n",
		styleDim.Render("smtp   "),
		styleText.Render(m.cfg.SMTPAddr),
		m.smtpLabel())
	if m.webhookErr != "" {
		fmt.Fprintf(&b, "%s %s\n",
			styleDim.Render("hook   "),
			styleBad.Render("not listening: "+m.webhookErr))
	} else {
		fmt.Fprintf(&b, "%s %s\n",
			styleDim.Render("hook   "),
			styleGood.Render(hook))
	}
	fmt.Fprintf(&b, "%s %s\n",
		styleDim.Render("folder "),
		styleText.Render(m.cfg.Dir))

	b.WriteString("\n")
	b.WriteString(styleDim.Render("After import, files are sent to ninja automatically."))
	b.WriteString("\n")
	b.WriteString(styleDim.Render("Results group into By score; enter drills into subjects and full emails."))
	return b.String()
}

func (m *Model) smtpLabel() string {
	switch {
	case !m.smtpChecked:
		return styleWarn.Render("checking…")
	case m.smtpErr != "":
		return styleBad.Render("unreachable (" + m.smtpErr + ")")
	default:
		return styleGood.Render("reachable")
	}
}

// renderFooter is the two-line status bar: dir | SMTP | sent n/N | results n |
// webhook, then key hints (plus the ninja pairing hint while no files are
// imported).
func (m *Model) renderFooter(w int) string {
	if m.editingPath {
		line1 := ansi.Truncate(m.pathBox.View(), w, "…")
		line2 := styleDim.Render("enter apply  ·  esc cancel")
		if m.importHint != "" {
			line2 = styleWarn.Render(m.importHint) + styleDim.Render("  ·  ") + line2
		}
		return fitBlock(line1+"\n"+line2, w, 2)
	}

	counts := m.st.Counts()

	smtp := "checking"
	switch {
	case m.smtpChecked && m.smtpErr == "":
		smtp = "up"
	case m.smtpChecked:
		smtp = "down"
	}

	hook := m.webhookAddr
	switch {
	case m.webhookErr != "":
		hook = styleBad.Render("error: " + m.webhookErr)
	case hook == "":
		hook = styleDim.Render("starting")
	default:
		hook = styleText.Render(hook)
	}

	sep := styleDim.Render(" | ")
	line1 := strings.Join([]string{
		styleDim.Render("dir ") + styleText.Render(m.cfg.Dir),
		styleDim.Render("smtp ") + styleText.Render(m.cfg.SMTPAddr) + " " + smtpColor(smtp),
		styleDim.Render(fmt.Sprintf("sent %d/%d", counts.Sent, counts.Total)),
		styleDim.Render(fmt.Sprintf("results %d", counts.Results)),
		styleDim.Render("webhook ") + hook,
	}, sep)

	line2 := styleDim.Render(keyHints())
	if m.st.Len() == 0 && !m.editingPath {
		// Keep both the key hints and the pairing hint visible while the
		// folder is still empty.
		line2 = styleDim.Render("pair ninja: webhook_url "+webhookURL(m.webhookAddr)) +
			styleDim.Render("  ·  "+emptyKeyHints())
	}
	if m.paused {
		line2 = styleBad.Render("PAUSED — press s to resume") + styleDim.Render("  ·  ") + line2
	} else if m.send.Running() {
		line2 = styleWarn.Render("sending…") + styleDim.Render("  ·  ") + line2
	}
	if m.importHint != "" {
		line2 = styleWarn.Render(m.importHint) + styleDim.Render("  ·  ") + line2
	}

	return fitBlock(line1+"\n"+line2, w, 2)
}

// emptyKeyHints is the compact legend shown while no folder is imported, so
// the ninja pairing hint fits on the same line.
func emptyKeyHints() string {
	return "enter import · s start · q quit"
}

func smtpColor(state string) string {
	switch state {
	case "up":
		return styleGood.Render(state)
	case "down":
		return styleBad.Render(state)
	default:
		return styleWarn.Render(state)
	}
}

// fitBlock clamps a rendered block to exactly width x height cells: every
// line is truncated (ANSI-aware) and padded, and missing lines are added.
func fitBlock(s string, width, height int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = ansi.Truncate(line, width, "…")
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lipgloss.NewStyle().Width(width).Render(joinLines(lines))
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") }
