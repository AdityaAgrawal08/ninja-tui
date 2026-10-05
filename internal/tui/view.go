package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// View implements tea.Model. Layout: one header line, a split body
// (left Files list | separator | right classification tree), and a two-line
// footer (status + key hints). Mouse clicks/wheel are enabled here.
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

	lw, rw := m.split(w)
	m.leftPane = lw

	left := fitBlock(m.renderLeft(lw, bodyH), lw, bodyH)
	right := fitBlock(m.renderTreePane(rw, bodyH), rw, bodyH)
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
	if m.st.Len() == 0 {
		// Keep both the key hints and the pairing hint visible while the
		// folder is still empty.
		line2 = styleDim.Render("pair ninja: webhook_url "+webhookURL(m.webhookAddr)) +
			styleDim.Render("  ·  "+emptyKeyHints())
	}
	if m.paused {
		line2 = styleBad.Render("PAUSED — press s to resume") + styleDim.Render("  ·  ") + line2
	} else if m.send.Running() {
		line2 = styleWarn.Render(m.spin.View()+" sending…") + styleDim.Render("  ·  ") + line2
	}
	if m.notice != "" {
		line2 = styleNotice.Render(m.notice) + styleDim.Render("  ·  ") + line2
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
