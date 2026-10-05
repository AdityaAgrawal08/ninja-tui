package tui

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
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

// focusPane is which pane receives up/down, enter, and esc: the left file list
// or the right classification tree.
type focusPane int

const (
	focusLeft focusPane = iota
	focusTree
)

// treeNodeKind is the kind of one selectable row in the right tree.
type treeNodeKind int

const (
	nodeGroup treeNodeKind = iota
	nodeSubject
)

// treeNode is one selectable row of the flattened classification tree. line is
// the index of its first rendered line in Model.treeLines.
type treeNode struct {
	kind  treeNodeKind
	group string // group key, for nodeGroup
	tuiID string // message tui-id, for nodeSubject
	line  int
}

// Model is the single Bubble Tea model for ninja-tui.
type Model struct {
	cfg  Config
	st   *store.Store
	send *sender.Sender
	keys KeyMap

	mode          Mode
	width, height int
	ready         bool

	spin    spinner.Model
	pathBox textinput.Model

	editingPath bool
	importHint  string

	// focus is the pane that up/down/enter/esc act on.
	focus focusPane

	// Left pane rows: Import button, Files section, one row per imported file.
	rows       []listRow
	cursor     int
	leftOffset int
	leftPane   int
	bodyHeight int

	// Right pane: the classification tree. Expansion is tracked by stable ids
	// (group key and message tui-id) so webhook arrivals and progress updates
	// never collapse a node.
	expandedGroups map[string]bool
	expandedMsgs   map[string]bool
	treeNodes      []treeNode
	treeLines      []string
	treeCursor     int
	treeOffset     int
	treeSel        string

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

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	ti := textinput.New()
	ti.Prompt = "path> "
	ti.Placeholder = "folder with .eml files"
	ti.CharLimit = 1024

	m := &Model{
		cfg:            cfg,
		st:             cfg.Store,
		send:           sender.New(sender.Config{Addr: cfg.SMTPAddr, From: cfg.From, To: cfg.To}),
		keys:           DefaultKeyMap(),
		mode:           ModeSetup,
		spin:           sp,
		pathBox:        ti,
		bodyHeight:     12,
		autoSend:       true,
		expandedGroups: make(map[string]bool),
		expandedMsgs:   make(map[string]bool),
	}
	m.rebuildRows()
	m.rebuildTree()
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

// split divides the terminal width into the left pane, the separator column,
// and the right pane. View, layout, and mouse hit-testing all use it so they
// always agree on the boundary.
func (m *Model) split(total int) (left, right int) {
	if total < 4 {
		total = 4
	}
	lw := m.leftWidth(total)
	rw := total - lw - 1
	if rw < 12 {
		rw = 12
		lw = total - rw - 1
		if lw < 10 {
			lw = 10
		}
	}
	if lw+rw+1 > total {
		rw = total - lw - 1
		if rw < 1 {
			rw = 1
			lw = maxInt(1, total-2)
		}
	}
	return lw, rw
}

// rightWidth is the usable width of the right pane.
func (m *Model) rightWidth() int {
	if m.width <= 0 {
		return 12
	}
	_, rw := m.split(m.width)
	return rw
}

// layout recomputes pane sizes after a resize.
func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	bodyH := m.height - 3 // one header line, two footer lines
	if bodyH < 3 {
		bodyH = 3
	}
	m.bodyHeight = bodyH
	lw, _ := m.split(m.width)
	m.leftPane = lw
	m.rebuildRows()
	m.rebuildTree()
	m.ensureLeftVisible()
	m.ensureTreeVisible()
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
		"↑/↓ move",
		"enter expand/collapse",
		"esc collapse",
		"c collapse all",
		"tab pane",
		"s send/resume",
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
