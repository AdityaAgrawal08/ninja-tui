package sender

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server sufficient for net/smtp clients. It
// records the DATA payload and can reject the first DATA with ninja's
// queue-full 451 4.3.2 response.
type fakeSMTP struct {
	ln net.Listener

	mu       sync.Mutex
	data     []byte
	failOnce bool
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTP{ln: ln}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTP) addr() string { return s.ln.Addr().String() }

func (s *fakeSMTP) payload() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.data)
}

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	_, _ = fmt.Fprint(conn, "220 fake ESMTP\r\n")

	inData := false
	var buf strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				s.mu.Lock()
				s.data = []byte(buf.String())
				fail := s.failOnce
				s.failOnce = false
				s.mu.Unlock()
				buf.Reset()
				if fail {
					_, _ = fmt.Fprint(conn, "451 4.3.2 queue full, try again later\r\n")
				} else {
					_, _ = fmt.Fprint(conn, "250 2.0.0 OK\r\n")
				}
				continue
			}
			buf.WriteString(line)
			buf.WriteString("\r\n")
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToUpper(line), "EHLO"):
			_, _ = fmt.Fprint(conn, "250-fake\r\n250 8BITMIME\r\n")
		case strings.HasPrefix(strings.ToUpper(line), "HELO"):
			_, _ = fmt.Fprint(conn, "250 fake\r\n")
		case strings.HasPrefix(strings.ToUpper(line), "MAIL"),
			strings.HasPrefix(strings.ToUpper(line), "RCPT"),
			strings.HasPrefix(strings.ToUpper(line), "RSET"):
			_, _ = fmt.Fprint(conn, "250 2.0.0 OK\r\n")
		case strings.ToUpper(line) == "DATA":
			inData = true
			_, _ = fmt.Fprint(conn, "354 go ahead\r\n")
		case strings.ToUpper(line) == "QUIT":
			_, _ = fmt.Fprint(conn, "221 bye\r\n")
			return
		default:
			_, _ = fmt.Fprint(conn, "250 2.0.0 OK\r\n")
		}
	}
}

func waitPhases(t *testing.T, ch <-chan Event, timeout time.Duration) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-ch:
			got = append(got, ev)
			if ev.Phase == PhaseRunDone {
				return got
			}
		case <-deadline:
			t.Fatalf("timeout waiting for events; got %+v", got)
		}
	}
}

func TestSendInjectsHeaderAndNormalizesCRLF(t *testing.T) {
	srv := startFakeSMTP(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.eml")
	if err := os.WriteFile(path, []byte("From: a@example.com\nSubject: hi\n\nbody line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(Config{Addr: srv.addr(), From: "tui@localhost", To: "inbox@mx.local", Workers: 1})
	events := s.Events()
	if err := s.Start([]Task{{ID: "one", Path: path, TUIID: "one@tui.local"}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := waitPhases(t, events, 5*time.Second)
	for _, ev := range got {
		if ev.Phase == PhaseFinished && ev.Err != nil {
			t.Fatalf("send failed: %v", ev.Err)
		}
	}

	payload := srv.payload()
	if !strings.Contains(payload, "Message-ID: <one@tui.local>\r\n") {
		t.Fatalf("injected Message-ID missing from wire payload:\n%q", payload)
	}
	if !strings.Contains(payload, "\r\n\r\nbody line\r\n") {
		t.Fatalf("body or CRLF normalization wrong on the wire:\n%q", payload)
	}
	if strings.Contains(payload, "Subject: hi\n") {
		t.Fatalf("lone LF reached the wire:\n%q", payload)
	}
}

func TestQueueFullPausesThenResumeRetries(t *testing.T) {
	srv := startFakeSMTP(t)
	srv.failOnce = true

	dir := t.TempDir()
	path := filepath.Join(dir, "a.eml")
	if err := os.WriteFile(path, []byte("From: a@example.com\n\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(Config{Addr: srv.addr(), From: "tui@localhost", To: "inbox@mx.local", Workers: 1})
	events := s.Events()
	if err := s.Start([]Task{{ID: "one", Path: path, TUIID: "one@tui.local"}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the pause event.
	var paused Event
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev := <-events:
			if ev.Phase == PhasePaused {
				paused = ev
				break loop
			}
		case <-deadline:
			t.Fatal("timeout waiting for PhasePaused")
		}
	}
	if !IsTemporary(paused.Err) {
		t.Fatalf("pause error %v is not a 4xx temporary failure", paused.Err)
	}
	if !s.Paused() {
		t.Fatal("sender should be paused after a 451")
	}

	s.Resume()
	got := waitPhases(t, events, 5*time.Second)
	var finished bool
	for _, ev := range got {
		if ev.Phase == PhaseFinished {
			if ev.Err != nil {
				t.Fatalf("retry failed: %v", ev.Err)
			}
			finished = true
		}
	}
	if !finished {
		t.Fatalf("no successful retry after resume; events: %+v", got)
	}
	if !strings.Contains(srv.payload(), "Message-ID: <one@tui.local>") {
		t.Fatalf("retry payload missing id:\n%q", srv.payload())
	}
}
