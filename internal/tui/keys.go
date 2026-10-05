package tui

import "charm.land/bubbles/v2/key"

// KeyMap holds the TUI's bindings. Every binding is always shown in the
// footer so the keys are discoverable in any mode.
type KeyMap struct {
	Quit        key.Binding
	Back        key.Binding
	Start       key.Binding
	Select      key.Binding
	Path        key.Binding
	Rescan      key.Binding
	Resume      key.Binding
	Focus       key.Binding
	CollapseAll key.Binding
}

// DefaultKeyMap returns the default bindings documented in the footer.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Back:        key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "collapse")),
		Start:       key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "send/resume")),
		Select:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand/collapse")),
		Path:        key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "path")),
		Rescan:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rescan")),
		Resume:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "resume")),
		Focus:       key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "pane")),
		CollapseAll: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "collapse all")),
	}
}
