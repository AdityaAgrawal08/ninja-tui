package store

import (
	"testing"
	"time"

	"ninja-tui/internal/eml"
	"ninja-tui/internal/webhook"
)

func seed(t *testing.T, names ...string) *Store {
	t.Helper()
	s := New()
	files := make([]eml.File, 0, len(names))
	base := time.Now()
	for i, n := range names {
		files = append(files, eml.File{
			Path:    "/mail/" + n,
			Name:    n,
			Size:    int64(100 + i),
			ModTime: base,
			SHA256:  "filesha-" + n,
			Subject: "subject " + n,
			From:    "sender@example.com",
		})
	}
	s.Replace(files)
	return s
}

func idOf(t *testing.T, s *Store, i int) string {
	t.Helper()
	e, ok := s.Entry(i)
	if !ok {
		t.Fatalf("no entry at %d", i)
	}
	return e.TUIID
}

func TestApplyResultByMessageID(t *testing.T) {
	s := seed(t, "a.eml", "b.eml")
	id := idOf(t, s, 0)
	s.SetStatus(id, Sent, "")

	res := webhook.Result{
		MessageSHA256: "reconstructed-sha",
		Facts:         webhook.Facts{Headers: webhook.Headers{MessageID: "<" + id + ">"}},
		Verdict:       &webhook.Verdict{Score: 3, ScoreMax: 4},
	}
	got, ok := s.ApplyResult(res)
	if !ok {
		t.Fatal("result did not match")
	}
	if got.TUIID != id {
		t.Fatalf("matched %q, want %q", got.TUIID, id)
	}
	if got.Result == nil || got.Result.Verdict == nil || got.Result.Verdict.Score != 3 {
		t.Fatalf("result not stored: %+v", got.Result)
	}
	if _, ok := s.ApplyResult(res); !ok {
		t.Fatal("duplicate result should re-match by message_sha256")
	}
}

func TestApplyResultByXTUIIDRawHeader(t *testing.T) {
	s := seed(t, "original-message-id.eml")
	id := idOf(t, s, 0)

	// The file already had a Message-ID, so the TUI injected X-TUI-ID.
	res := webhook.Result{
		Facts: webhook.Facts{Headers: webhook.Headers{
			MessageID: "<original@example.com>",
			Raw: [][2]string{
				{"From", "a@example.com"},
				{"Message-ID", "<original@example.com>"},
				{"x-tui-id", "<" + id + ">"},
			},
		}},
	}
	got, ok := s.ApplyResult(res)
	if !ok {
		t.Fatal("result did not match via X-TUI-ID")
	}
	if got.TUIID != id {
		t.Fatalf("matched %q, want %q", got.TUIID, id)
	}
}

func TestApplyResultOutOfOrder(t *testing.T) {
	s := seed(t, "one.eml", "two.eml")
	idOne := idOf(t, s, 0)
	idTwo := idOf(t, s, 1)

	resTwo := webhook.Result{Facts: webhook.Facts{Headers: webhook.Headers{MessageID: "<" + idTwo + ">"}}}
	resOne := webhook.Result{Facts: webhook.Facts{Headers: webhook.Headers{MessageID: "<" + idOne + ">"}}}

	// Second message is reported before the first.
	if got, ok := s.ApplyResult(resTwo); !ok || got.Name != "two.eml" {
		t.Fatalf("out-of-order second result matched wrong entry: %+v ok=%v", got, ok)
	}
	if got, ok := s.ApplyResult(resOne); !ok || got.Name != "one.eml" {
		t.Fatalf("out-of-order first result matched wrong entry: %+v ok=%v", got, ok)
	}
	if c := s.Counts(); c.Results != 2 {
		t.Fatalf("results = %d, want 2", c.Results)
	}
}

func TestApplyResultFileSHAFallback(t *testing.T) {
	s := seed(t, "hashy.eml")
	sha := s.Entries()[0].FileSHA256
	if sha == "" {
		t.Fatal("seed has no file sha")
	}
	res := webhook.Result{MessageSHA256: sha}
	got, ok := s.ApplyResult(res)
	if !ok || got.Name != "hashy.eml" {
		t.Fatalf("file-sha fallback failed: %+v ok=%v", got, ok)
	}
}

func TestApplyResultFIFOFallbackWhenNoIdentifiers(t *testing.T) {
	s := seed(t, "one.eml", "two.eml")
	for i := 0; i < 2; i++ {
		s.SetStatus(idOf(t, s, i), Sent, "")
	}
	if got, ok := s.ApplyResult(webhook.Result{}); !ok || got.Name != "one.eml" {
		t.Fatalf("first anonymous result matched %+v ok=%v, want one.eml", got, ok)
	}
	if got, ok := s.ApplyResult(webhook.Result{}); !ok || got.Name != "two.eml" {
		t.Fatalf("second anonymous result matched %+v ok=%v, want two.eml", got, ok)
	}
}

func TestApplyResultUnmatchedIsKept(t *testing.T) {
	s := seed(t, "one.eml")
	res := webhook.Result{Facts: webhook.Facts{Headers: webhook.Headers{MessageID: "<stranger@elsewhere>"}}}
	if _, ok := s.ApplyResult(res); ok {
		t.Fatal("unknown result should not match")
	}
	if got := s.Unmatched(); len(got) != 1 {
		t.Fatalf("unmatched = %d, want 1", len(got))
	}
}

func TestCachedIsExposed(t *testing.T) {
	s := seed(t, "a.eml")
	id := idOf(t, s, 0)
	got, ok := s.ApplyResult(webhook.Result{
		Cached: true,
		Facts:  webhook.Facts{Headers: webhook.Headers{MessageID: "<" + id + ">"}},
	})
	if !ok || got.Result == nil || !got.Result.Cached {
		t.Fatalf("cached flag lost: %+v ok=%v", got.Result, ok)
	}
}

func TestReplacePreservesUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	files := []eml.File{{
		Path: dir + "/a.eml", Name: "a.eml", Size: 10, ModTime: base, SHA256: "sha",
	}}
	s := New()
	s.Replace(files)
	id := idOf(t, s, 0)
	s.SetStatus(id, Sent, "")

	s.Replace(files) // identical rescan
	if got := idOf(t, s, 0); got != id {
		t.Fatalf("rescan changed id: %q -> %q", id, got)
	}
	if e, _ := s.Entry(0); e.Status != Sent {
		t.Fatalf("rescan lost status: %v", e.Status)
	}

	files[0].Size = 11 // file changed
	s.Replace(files)
	if got := idOf(t, s, 0); got == id {
		t.Fatalf("changed file kept stale id %q", id)
	}
}

func TestNormalizeID(t *testing.T) {
	for in, want := range map[string]string{
		"<abc@tui.local>": "abc@tui.local",
		" abc@tui.local ": "abc@tui.local",
		"abc@tui.local":   "abc@tui.local",
		"":                "",
	} {
		if got := NormalizeID(in); got != want {
			t.Errorf("NormalizeID(%q) = %q, want %q", in, got, want)
		}
	}
}
