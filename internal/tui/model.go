package tui

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"ninja-tui/internal/eml"
	"ninja-tui/internal/sender"
	"ninja-tui/internal/store"
)

// Config is the fixed wiring for a Model.
type Config struct {
	// Dir is the folder scanned for .eml files.
	Dir string
	// SMTPAddr is ninja's SMTP entrypoint, e.g. 127.0.0.1:2525.
	SMTPAddr string
	// From and To are the SMTP envelope addresses. ninja accepts any
	// syntactically valid pair when its SMTP auth is disabled.
	From string
	To   string
	// Store is required; main creates it so tests and main can share it.
	Store *store.Store
}

// navLevel is where keyboard focus and the right pane are: the left list, the
// subject list of one score group, or one full email context.
type navLevel int

const (
	navList navLevel = iota
	navSubjects
	navDetail
)

// Model is the single Bubble Tea model for ninja-tui.
type Model struct {
	cfg  Config
	st   *store.Store
	send *sender.Sender
	keys KeyMap

	mode          Mode
	width, height int
	ready         bool

	detail  viewport.Model
	spin    spinner.Model
	prog    progress.Model
	pathBox textinput.Model

	editingPath bool
	importHint  string

	// Navigation: left list -> subject list -> full email.
	nav        navLevel
	detailFrom navLevel
	detailIdx  int
	detailKey  string

	// Left pane rows (Import button, Files, By score).
	rows       []listRow
	cursor     int
	leftOffset int
	leftPane   int
	bodyHeight int

	// Subject list of the group currently shown in the right pane.
	subjGroup  string
	subjIdx    []int
	subjCursor int
	subjOffset int

	scanErr string
	notice  string

	smtpChecked bool
	smtpErr     string

	webhookAddr string
	webhookErr  string

	runTotal  int
	paused    bool
	pauseMsg  string
	lastEvent string

	// autoSend starts a send run right after a folder scan. Tests that drive
	// the sender by hand turn it off.
	autoSend bool
}

// New builds the model. Call Init through tea.Program.
func New(cfg Config) *Model {
	if cfg.Store == nil {
		cfg.Store = store.New()
	}
	if cfg.Dir == "" {
		cfg.Dir = "."
	}

	v := viewport.New(viewport.WithWidth(60), viewport.WithHeight(12))
	v.SoftWrap = true
	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	pr := progress.New(progress.WithWidth(40), progress.WithoutPercentage())
	ti := textinput.New()
	ti.Prompt = "path> "
	ti.Placeholder = "folder with .eml files"
	ti.CharLimit = 1024

	m := &Model{
		cfg:        cfg,
		st:         cfg.Store,
		send:       sender.New(sender.Config{Addr: cfg.SMTPAddr, From: cfg.From, To: cfg.To}),
		keys:       DefaultKeyMap(),
		mode:       ModeSetup,
		detail:     v,
		spin:       sp,
		prog:       pr,
		pathBox:    ti,
		detailIdx:  -1,
		bodyHeight: 12,
		autoSend:   true,
	}
	m.rebuildRows()
	return m
}

// SetWebhook records the webhook listener's state for the status bar and the
// setup instructions.
func (m *Model) SetWebhook(addr string, err error) {
	if addr != "" {
		m.webhookAddr = addr
	}
	if err != nil {
		m.webhookErr = err.Error()
	}
}

// Stop cancels any in-flight send run. Call after the program exits.
func (m *Model) Stop() {
	m.send.Stop()
}

// Init starts the first scan, the SMTP reachability probe, and the sender
// event pump.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.scanCmd(m.cfg.Dir),
		m.smtpCheckCmd(),
		m.waitEventCmd(),
	)
}

func (m *Model) scanCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		files, err := eml.Scan(dir)
		return scanDoneMsg{Dir: dir, Files: files, Err: err}
	}
}

func (m *Model) smtpCheckCmd() tea.Cmd {
	addr := m.cfg.SMTPAddr
	return func() tea.Msg {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return smtpCheckMsg{Addr: addr, Err: err}
		}
		_ = conn.Close()
		return smtpCheckMsg{Addr: addr}
	}
}

func (m *Model) waitEventCmd() tea.Cmd {
	ch := m.send.Events()
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return ev
	}
}

// layout recomputes pane sizes after a resize.
func (m *Model) layout() {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return
	}
	bodyH := h - 3 // one header line, two footer lines
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
	m.detail.SetWidth(rw)
	m.detail.SetHeight(bodyH)
	m.prog.SetWidth(maxInt(10, rw-2))
	m.ensureLeftVisible()
	m.ensureSubjectVisible()
}

// humanSize formats byte counts for the detail pane.
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// webhookURL renders the URL ninja must be configured with. A wildcard or
// empty host is shown as 127.0.0.1 because that is what a local ninja needs.
func webhookURL(addr string) string {
	if addr == "" {
		addr = "127.0.0.1:18080"
	}
	host := "127.0.0.1"
	port := addr
	if h, p, err := net.SplitHostPort(addr); err == nil {
		if h != "" {
			host = h
		}
		port = p
	}
	return "http://" + host + ":" + port + "/hook"
}

// keyHints is the always-visible key legend in the footer.
func keyHints() string {
	return strings.Join([]string{
		"enter select/open",
		"s send/resume",
		"esc back",
		"/ path",
		"r rescan",
		"q quit",
	}, "  ·  ")
}

func itoa(n int) string { return strconv.Itoa(n) }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
