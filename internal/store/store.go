// Package store keeps the imported file list and the analysis results ninja
// posts back, correlated by the TUI's injected Message-ID / X-TUI-ID header.
//
// All methods are guarded by a mutex so the HTTP webhook goroutine and the UI
// can never race, although the TUI normally applies results on its own event
// loop.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"ninja-tui/internal/eml"
	"ninja-tui/internal/webhook"
)

// Status is the per-file send state shown in the queue table.
type Status int

const (
	Queued Status = iota
	Sending
	Sent
	Failed
)

func (s Status) String() string {
	switch s {
	case Queued:
		return "queued"
	case Sending:
		return "sending"
	case Sent:
		return "sent"
	case Failed:
		return "failed"
	default:
		return "unknown"
	}
}

// Entry is one imported .eml file and, once it arrives, its Result.
type Entry struct {
	Path       string
	Name       string
	Subject    string
	From       string
	Size       int64
	ModTime    time.Time
	TUIID      string // bare id, e.g. "1a2b@tui.local"; headers carry <id>
	FileSHA256 string // hash of the file on disk, last-resort fallback only
	Status     Status
	Err        string
	Result     *webhook.Result
}

// Counts summarizes the whole store for the status bar.
type Counts struct {
	Total   int
	Queued  int
	Sending int
	Sent    int
	Failed  int
	Results int
}

// Store is the concurrency-safe home of the file list and results.
type Store struct {
	mu        sync.Mutex
	entries   []*Entry
	byID      map[string]*Entry
	byPath    map[string]*Entry
	byMsgSHA  map[string]*Entry
	byFileSHA map[string]*Entry
	unmatched []webhook.Result
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		byID:      make(map[string]*Entry),
		byPath:    make(map[string]*Entry),
		byMsgSHA:  make(map[string]*Entry),
		byFileSHA: make(map[string]*Entry),
	}
}

// NewID returns a fresh correlation id. It is used both as the Message-ID
// local part and as the X-TUI-ID value.
func NewID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail; fall back to the clock so the TUI
		// remains usable.
		return fmt.Sprintf("%d@tui.local", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:]) + "@tui.local"
}

// Replace swaps in a new scan result. Entries whose path, size, and mtime are
// unchanged keep their id, status, and result so a rescan does not lose work;
// changed or new files start queued with a fresh id.
func (s *Store) Replace(files []eml.File) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.byPath
	entries := make([]*Entry, 0, len(files))
	byID := make(map[string]*Entry, len(files))
	byPath := make(map[string]*Entry, len(files))
	byMsgSHA := make(map[string]*Entry)
	byFileSHA := make(map[string]*Entry)
	for _, f := range files {
		e := &Entry{
			Path:       f.Path,
			Name:       f.Name,
			Subject:    f.Subject,
			From:       f.From,
			Size:       f.Size,
			ModTime:    f.ModTime,
			TUIID:      NewID(),
			FileSHA256: f.SHA256,
			Status:     Queued,
			Err:        f.Err,
		}
		if o, ok := old[f.Path]; ok && o.Size == f.Size && o.ModTime.Equal(f.ModTime) {
			e.TUIID = o.TUIID
			e.Status = o.Status
			e.Err = o.Err
			e.Result = o.Result
		}
		entries = append(entries, e)
		byID[e.TUIID] = e
		byPath[e.Path] = e
		if e.FileSHA256 != "" {
			byFileSHA[e.FileSHA256] = e
		}
		if e.Result != nil && e.Result.MessageSHA256 != "" {
			byMsgSHA[e.Result.MessageSHA256] = e
		}
	}
	s.entries = entries
	s.byID = byID
	s.byPath = byPath
	s.byMsgSHA = byMsgSHA
	s.byFileSHA = byFileSHA
}

// Entries returns copies of all entries in scan order. The Result pointer is
// shared but results are never mutated after insertion.
func (s *Store) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	for i, e := range s.entries {
		out[i] = *e
	}
	return out
}

// Entry returns a copy of the entry at index i.
func (s *Store) Entry(i int) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.entries) {
		return Entry{}, false
	}
	return *s.entries[i], true
}

// Len returns the number of imported files.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// PendingItem identifies a file that should be sent.
type PendingItem struct {
	ID    string
	Path  string
	TUIID string
}

// Pending returns every entry that should be transmitted: queued files and,
// for convenience, previously failed ones so pressing s retries them.
func (s *Store) Pending() []PendingItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PendingItem
	for _, e := range s.entries {
		if e.Status == Queued || e.Status == Failed {
			out = append(out, PendingItem{ID: e.TUIID, Path: e.Path, TUIID: e.TUIID})
		}
	}
	return out
}

// SetStatus updates one entry's send state. id is the bare TUI id.
func (s *Store) SetStatus(id string, st Status, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.byID[id]; e != nil {
		e.Status = st
		e.Err = errMsg
	}
}

// ApplyResult correlates a webhook result with an imported file and stores it.
// It returns the updated entry and true when a match was found. Matching is
// tried in this order:
//
//  1. facts.headers.message_id against the injected id,
//  2. raw header pairs named X-TUI-ID (for messages that already carried a
//     Message-ID),
//  3. the result's message_sha256 for a resend or duplicate of a known
//     message,
//  4. the file sha256 as a last resort,
//  5. the oldest sent file still awaiting a result.
//
// Out-of-order arrivals are safe: matching never depends on arrival order.
func (s *Store) ApplyResult(res webhook.Result) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e := s.matchLocked(res); e != nil {
		resCopy := res
		e.Result = &resCopy
		e.Err = ""
		if res.MessageSHA256 != "" {
			s.byMsgSHA[res.MessageSHA256] = e
		}
		return *e, true
	}
	s.unmatched = append(s.unmatched, res)
	return Entry{}, false
}

func (s *Store) matchLocked(res webhook.Result) *Entry {
	if id := NormalizeID(res.Facts.Headers.MessageID); id != "" {
		if e := s.byID[id]; e != nil {
			return e
		}
	}
	for _, kv := range res.Facts.Headers.Raw {
		if strings.EqualFold(strings.TrimSpace(kv[0]), "X-TUI-ID") {
			if e := s.byID[NormalizeID(kv[1])]; e != nil {
				return e
			}
		}
	}
	if res.MessageSHA256 != "" {
		if e := s.byMsgSHA[res.MessageSHA256]; e != nil {
			return e
		}
	}
	if res.MessageSHA256 != "" {
		if e := s.byFileSHA[res.MessageSHA256]; e != nil {
			return e
		}
	}
	for _, e := range s.entries {
		if e.Status == Sent && e.Result == nil {
			return e
		}
	}
	return nil
}

// NormalizeID strips whitespace and the angle brackets around a message id so
// "<abc@tui.local>", " abc@tui.local " and "abc@tui.local" compare equal.
func NormalizeID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// Counts returns the aggregate status totals.
func (s *Store) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Counts
	c.Total = len(s.entries)
	for _, e := range s.entries {
		switch e.Status {
		case Queued:
			c.Queued++
		case Sending:
			c.Sending++
		case Sent:
			c.Sent++
		case Failed:
			c.Failed++
		}
		if e.Result != nil {
			c.Results++
		}
	}
	return c
}

// Unmatched returns results that could not be correlated to a file. They are
// kept for display so nothing silently disappears.
func (s *Store) Unmatched() []webhook.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]webhook.Result, len(s.unmatched))
	copy(out, s.unmatched)
	return out
}

// Reset clears every entry and result.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.byID = make(map[string]*Entry)
	s.byPath = make(map[string]*Entry)
	s.byMsgSHA = make(map[string]*Entry)
	s.byFileSHA = make(map[string]*Entry)
	s.unmatched = nil
}
