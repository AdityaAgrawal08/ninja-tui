package tui

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// zenityHint is the one-line fallback hint shown when zenity cannot be used.
const zenityHint = "zenity unavailable — install it with: sudo pacman -S zenity"

// importCmd opens the system folder picker. zenity runs through
// tea.ExecProcess, so the program releases the terminal while the dialog is
// up and resumes afterwards; cancel is silent. When zenity is missing or
// fails, importDoneMsg carries the error and the model falls back to its
// built-in path input.
func (m *Model) importCmd() tea.Cmd {
	var out, errBuf bytes.Buffer
	c := exec.Command("zenity",
		"--file-selection",
		"--directory",
		"--title=ninja-tui - choose a folder of .eml files")
	c.Stdout = &out
	c.Stderr = &errBuf
	return tea.ExecProcess(c, func(err error) tea.Msg {
		dir := strings.TrimSpace(out.String())
		return importDoneMsg{
			Dir:      dir,
			Err:      err,
			Canceled: isZenityCancel(err, dir, errBuf.String()),
		}
	})
}

// isZenityCancel reports the exit status zenity uses when the user closes the
// dialog without choosing a folder. A real failure (no display, missing
// binary) writes to stderr or exits with another code and takes the fallback
// path instead.
func isZenityCancel(err error, dir, stderr string) bool {
	if err == nil {
		return false
	}
	if dir != "" || strings.TrimSpace(stderr) != "" {
		return false
	}
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 1
}
