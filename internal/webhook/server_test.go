package webhook

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServerAcceptsAndDecodesResult(t *testing.T) {
	srv := New("127.0.0.1:0")
	got := make(chan Result, 1)
	srv.SetHandler(func(r Result) { got <- r })
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// A partial result with no verdict and no envelope, exactly the shape
	// ninja produces when scoring is disabled.
	body := `{
	  "schema_version": 1,
	  "message_sha256": "abc123",
	  "facts": {
	    "schema_version": 1,
	    "source": "smtp",
	    "headers": {
	      "raw": [["From", "a@example.com"], ["Message-ID", "<x@tui.local>"]],
	      "from": [{"address": "a@example.com"}],
	      "subject": "hi",
	      "message_id": "<x@tui.local>"
	    },
	    "mime": {"content_type": "text/plain"}
	  },
	  "summary": "",
	  "partial": true
	}`

	resp, err := http.Post("http://"+srv.Addr()+"/hook", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	select {
	case res := <-got:
		if res.MessageSHA256 != "abc123" || res.Facts.Headers.MessageID != "<x@tui.local>" {
			t.Fatalf("decoded result wrong: %+v", res)
		}
		if res.Verdict != nil {
			t.Fatalf("verdict = %+v, want nil", res.Verdict)
		}
		if !res.Partial {
			t.Fatal("partial = false, want true")
		}
		if len(res.Facts.Headers.Raw) != 2 {
			t.Fatalf("raw headers = %d, want 2", len(res.Facts.Headers.Raw))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called")
	}
}

func TestServerRejectsBadRequests(t *testing.T) {
	srv := New("127.0.0.1:0")
	srv.SetHandler(func(Result) {})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	resp, err := http.Get("http://" + srv.Addr() + "/hook")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}

	resp, err = http.Post("http://"+srv.Addr()+"/hook", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad JSON status = %d, want 400", resp.StatusCode)
	}
}
