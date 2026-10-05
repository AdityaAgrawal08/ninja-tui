// Package sender transmits prepared .eml messages to ninja's SMTP entrypoint.
//
// Each message is sent in its own SMTP transaction with no authentication.
// When ninja answers 451 (its queue is full) the whole run pauses instead of
// spinning, until the operator resumes from the TUI.
package sender

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"ninja-tui/internal/eml"
)

// DefaultWorkers is the send concurrency used when Config.Workers is unset.
const DefaultWorkers = 4

// Config is the fixed SMTP pairing for one run.
type Config struct {
	Addr    string
	From    string
	To      string
	Workers int
}

// Task is one file to send. TUIID is the correlation id injected into the
// message headers.
type Task struct {
	ID    string
	Path  string
	TUIID string
}

// Phase identifies what happened to a task.
type Phase string

const (
	PhaseStarted  Phase = "started"
	PhaseFinished Phase = "finished"
	PhasePaused   Phase = "paused" // temporary failure; will be retried on resume
	PhaseRunDone  Phase = "run_done"
)

// Event is reported for every state change of a send run. Dial reports that
// the failure was at the TCP connect stage, which the TUI uses to mark the
// SMTP address as unreachable.
type Event struct {
	Task     Task
	Phase    Phase
	Err      error
	Dial     bool
	Duration time.Duration
}

// Sender owns a small worker pool. It is started once per analysis run and
// can be started again after a run completes. Events are delivered on a
// channel owned by the Sender for the lifetime of the process.
type Sender struct {
	cfg    Config
	events chan Event

	mu        sync.Mutex // guards running, runCancel, resumeCh
	running   bool
	runCancel context.CancelFunc
	resumeCh  chan struct{}

	paused atomic.Bool
}

// New returns a Sender for the given pairing.
func New(cfg Config) *Sender {
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkers
	}
	return &Sender{
		cfg:      cfg,
		events:   make(chan Event, 64),
		resumeCh: make(chan struct{}),
	}
}

// Events returns the stream of send events. It is never closed.
func (s *Sender) Events() <-chan Event { return s.events }

// Paused reports whether the run is paused.
func (s *Sender) Paused() bool { return s.paused.Load() }

// Pause stops workers from picking up new messages. In-flight sends finish.
func (s *Sender) Pause() { s.paused.Store(true) }

// Resume clears the paused state and wakes workers waiting to retry.
func (s *Sender) Resume() {
	if !s.paused.CompareAndSwap(true, false) {
		return
	}
	s.mu.Lock()
	close(s.resumeCh)
	s.resumeCh = make(chan struct{})
	s.mu.Unlock()
}

// Running reports whether a run is in flight.
func (s *Sender) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Start launches workers for tasks. It returns an error if a run is already
// in flight.
func (s *Sender) Start(tasks []Task) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("sender: a send run is already in progress")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running = true
	s.runCancel = cancel
	s.mu.Unlock()

	s.paused.Store(false)

	jobs := make(chan Task, s.cfg.Workers)
	var workers sync.WaitGroup
	for i := 0; i < s.cfg.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.worker(ctx, jobs)
		}()
	}

	feedDone := make(chan struct{})
	go func() {
		defer close(jobs)
		defer close(feedDone)
		for _, t := range tasks {
			if !s.waitUnpaused(ctx) {
				return
			}
			select {
			case jobs <- t:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		<-feedDone
		workers.Wait()
		s.mu.Lock()
		s.running = false
		s.runCancel = nil
		s.mu.Unlock()
		s.emit(ctx, Event{Phase: PhaseRunDone})
	}()
	return nil
}

// Stop cancels any running run. It does not wait for workers; callers exit
// the process shortly after.
func (s *Sender) Stop() {
	s.mu.Lock()
	cancel := s.runCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Sender) worker(ctx context.Context, jobs <-chan Task) {
	for t := range jobs {
		s.emit(ctx, Event{Task: t, Phase: PhaseStarted})
		start := time.Now()
		for {
			err, dial := s.sendOne(ctx, t)
			if err == nil {
				s.emit(ctx, Event{Task: t, Phase: PhaseFinished, Duration: time.Since(start)})
				break
			}
			if IsTemporary(err) {
				// ninja's queue is full (typically 451 4.3.2). Pause the
				// whole run and retry this message after a manual resume.
				s.Pause()
				s.emit(ctx, Event{Task: t, Phase: PhasePaused, Err: err, Dial: dial, Duration: time.Since(start)})
				if !s.waitUnpaused(ctx) {
					return
				}
				continue
			}
			s.emit(ctx, Event{Task: t, Phase: PhaseFinished, Err: err, Dial: dial, Duration: time.Since(start)})
			break
		}
	}
}

func (s *Sender) waitUnpaused(ctx context.Context) bool {
	for {
		if !s.paused.Load() {
			return true
		}
		s.mu.Lock()
		ch := s.resumeCh
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}

func (s *Sender) emit(ctx context.Context, ev Event) {
	select {
	case s.events <- ev:
	case <-ctx.Done():
	}
}

// sendOne runs one SMTP transaction. There is no SMTP AUTH: the backend
// accepts any syntactically valid envelope when auth is disabled.
func (s *Sender) sendOne(ctx context.Context, t Task) (err error, dial bool) {
	raw, err := os.ReadFile(t.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", t.Path, err), false
	}
	prepared := eml.Prepare(raw, t.TUIID)

	conn, err := net.DialTimeout("tcp", s.cfg.Addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", s.cfg.Addr, err), true
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	host, _, splitErr := net.SplitHostPort(s.cfg.Addr)
	if splitErr != nil {
		host = s.cfg.Addr
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err), true
	}
	defer func() { _ = c.Close() }()

	if ctx.Err() != nil {
		return ctx.Err(), false
	}
	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("smtp hello: %w", err), false
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err), false
	}
	if err := c.Rcpt(s.cfg.To); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err), false
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err), false
	}
	if _, err := w.Write(prepared); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write body: %w", err), false
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data rejected: %w", err), false
	}
	return nil, false
}

// IsTemporary reports whether err is a 4xx SMTP temporary failure, which for
// ninja means the server-side analysis queue is saturated.
func IsTemporary(err error) bool {
	var te *textproto.Error
	if errors.As(err, &te) {
		return te.Code >= 400 && te.Code < 500
	}
	return false
}
