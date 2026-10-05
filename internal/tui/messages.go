package tui

import (
	"ninja-tui/internal/eml"
	"ninja-tui/internal/webhook"
)

// Mode is the top-level screen state: setup (import) -> queue -> sending ->
// results.
type Mode int

const (
	ModeSetup Mode = iota
	ModeQueue
	ModeSending
	ModeResults
)

func (m Mode) String() string {
	switch m {
	case ModeSetup:
		return "setup"
	case ModeQueue:
		return "queue"
	case ModeSending:
		return "sending"
	case ModeResults:
		return "results"
	default:
		return "unknown"
	}
}

// ResultMsg carries one decoded webhook result into the Bubble Tea loop. It is
// exported so main can forward results with Program.Send from the HTTP
// goroutine.
type ResultMsg struct {
	Result webhook.Result
}

// scanDoneMsg reports the outcome of a folder scan.
type scanDoneMsg struct {
	Dir   string
	Files []eml.File
	Err   error
}

// smtpCheckMsg reports the result of the startup SMTP reachability probe.
type smtpCheckMsg struct {
	Addr string
	Err  error
}

// importDoneMsg reports the outcome of the zenity folder picker. Dir is the
// chosen folder on success. Canceled is true when the user dismissed the
// dialog without choosing (silent, no fallback).
type importDoneMsg struct {
	Dir      string
	Err      error
	Canceled bool
}
