package eml

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestNormalizeCRLF(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already crlf", "a\r\nb\r\n", "a\r\nb\r\n"},
		{"lone lf", "a\nb", "a\r\nb"},
		{"mixed", "a\r\nb\nc\rd", "a\r\nb\r\nc\r\nd"},
		{"trailing lf", "a\n", "a\r\n"},
		{"trailing cr", "a\r", "a\r\n"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeCRLF([]byte(tt.in))
			if string(got) != tt.want {
				t.Fatalf("NormalizeCRLF(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestInjectIDAddsMessageIDWhenMissing(t *testing.T) {
	raw := []byte("From: a@example.com\nTo: b@example.com\nSubject: hi\n\nbody line\n")
	out, had := InjectID(raw, "abc123@tui.local")
	if had {
		t.Fatal("hadMessageID = true, want false")
	}
	if !bytes.Contains(out, []byte("Subject: hi\r\nMessage-ID: <abc123@tui.local>\r\n\r\nbody line\r\n")) {
		t.Fatalf("injected header placement or body separator wrong in:\n%q", out)
	}
	if bytes.Contains(out, []byte("\r\n\r\n\r\n")) {
		t.Fatalf("blank line duplicated before body:\n%q", out)
	}
	// Existing headers must be preserved byte for byte.
	if !strings.HasPrefix(string(out), "From: a@example.com\r\nTo: b@example.com\r\nSubject: hi\r\n") {
		t.Fatalf("original headers were altered:\n%q", out)
	}
	// The injected header must be in the header block, not the body.
	idxMsg := bytes.Index(out, []byte("Message-ID:"))
	idxSep := bytes.Index(out, []byte("\r\n\r\n"))
	if idxSep < 0 || idxMsg > idxSep {
		t.Fatalf("Message-ID not in header block:\n%q", out)
	}
	// Round trip: the injected id must be recoverable via net/mail.
	subj, from := ParseMeta(out)
	if subj != "hi" || from != "a@example.com" {
		t.Fatalf("ParseMeta after inject = (%q, %q), want (hi, a@example.com)", subj, from)
	}
}

func TestInjectIDKeepsExistingMessageID(t *testing.T) {
	raw := []byte("From: a@example.com\r\nMessage-ID: <original@example.com>\r\nSubject: x\r\n\r\nbody\r\n")
	out, had := InjectID(raw, "abc123@tui.local")
	if !had {
		t.Fatal("hadMessageID = false, want true")
	}
	if !bytes.Contains(out, []byte("Message-ID: <original@example.com>\r\n")) {
		t.Fatalf("original Message-ID was not preserved:\n%q", out)
	}
	if !bytes.Contains(out, []byte("X-TUI-ID: <abc123@tui.local>\r\n")) {
		t.Fatalf("missing injected X-TUI-ID:\n%q", out)
	}
	if bytes.Contains(out, []byte("Message-ID: <abc123@tui.local>")) {
		t.Fatalf("injected a second Message-ID:\n%q", out)
	}
}

func TestInjectIDHeaderOnlyMessage(t *testing.T) {
	raw := []byte("Subject: no body")
	out, _ := InjectID(raw, "id@tui.local")
	if !bytes.HasSuffix(out, []byte("Message-ID: <id@tui.local>\r\n\r\n")) {
		t.Fatalf("header-only message was not terminated correctly:\n%q", out)
	}
}

func TestPrepareNormalizesAndInjects(t *testing.T) {
	raw := []byte("From: a@example.com\nSubject: hi\n\nline1\nline2\n")
	out := Prepare(raw, "xyz@tui.local")
	if bytes.Contains(out, []byte("\n\n")) {
		t.Fatalf("lone LF survived Prepare:\n%q", out)
	}
	if !bytes.Contains(out, []byte("Message-ID: <xyz@tui.local>\r\n")) {
		t.Fatalf("Prepare did not inject the id:\n%q", out)
	}
	if bytes.Contains(out, []byte("\n\nline1")) && !bytes.Contains(out, []byte("\r\n\r\nline1")) {
		t.Fatalf("body separator not CRLF:\n%q", out)
	}
}

func TestParseMetaDecodesWord(t *testing.T) {
	raw := []byte("From: Alice <alice@example.com>\r\nSubject: =?UTF-8?q?w=C3=B6rld?=\r\n\r\nbody\r\n")
	subj, from := ParseMeta(raw)
	if subj != "wörld" {
		t.Fatalf("subject = %q, want wörld", subj)
	}
	if from != "Alice <alice@example.com>" {
		t.Fatalf("from = %q", from)
	}
}

func TestScanSkipsNonEml(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(dir+"/"+name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.eml", "From: a@example.com\n\nbody\n")
	write("b.EML", "From: b@example.com\n\nbody\n")
	write("note.txt", "not mail")

	files, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("Scan found %d files, want 2: %+v", len(files), files)
	}
	for _, f := range files {
		if f.SHA256 == "" {
			t.Errorf("%s: empty SHA256", f.Path)
		}
	}
	if _, err := Scan(dir + "/missing"); err == nil {
		t.Fatal("Scan of missing dir should error")
	}
}
