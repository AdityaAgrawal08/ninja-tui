// Package eml scans a folder for .eml files and prepares their bytes for
// sending over SMTP: CRLF normalization and correlation-header injection.
package eml

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Ext is the file extension this package imports.
const Ext = ".eml"

// File is one discovered .eml file. SHA256 is the hash of the file bytes on
// disk and is kept only as a correlation fallback: ninja hashes the message it
// reconstructs, not this file, so it normally differs from the webhook's
// message_sha256.
type File struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
	Subject string
	From    string
	SHA256  string
	// Err is set when the file was found but could not be read or parsed.
	// It does not stop the scan.
	Err string
}

// Scan walks dir and returns every *.eml file, sorted by path. Unreadable
// files are included with Err set so the UI can show them instead of hiding
// them. A missing directory is an error.
func Scan(dir string) ([]File, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	var files []File
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Skip hidden directories (mail store internals, .git, and so
			// on) but keep the root itself.
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), Ext) {
			return nil
		}
		f := load(path, d)
		files = append(files, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func load(path string, d fs.DirEntry) File {
	f := File{Path: path, Name: d.Name()}
	if info, err := d.Info(); err == nil {
		f.Size = info.Size()
		f.ModTime = info.ModTime()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		f.Err = err.Error()
		return f
	}
	sum := sha256.Sum256(raw)
	f.SHA256 = hex.EncodeToString(sum[:])
	subject, from := ParseMeta(raw)
	f.Subject = subject
	f.From = from
	return f
}

// ParseMeta extracts the Subject and From header values, decoding RFC 2047
// words. Failures yield empty strings: a file with malformed headers is still
// worth sending to ninja, which does its own tolerant parsing.
func ParseMeta(raw []byte) (subject, from string) {
	msg, err := mail.ReadMessage(bytes.NewReader(messageForParse(raw)))
	if err != nil {
		return "", ""
	}
	subject = decodeWord(msg.Header.Get("Subject"))
	if addrs, err := msg.Header.AddressList("From"); err == nil && len(addrs) > 0 {
		from = formatAddress(addrs[0])
	} else {
		from = strings.TrimSpace(msg.Header.Get("From"))
	}
	return subject, from
}

// formatAddress renders a parsed address for display: "Name <addr>" when a
// display name exists, otherwise just the address. Surrounding quotes are
// removed from the display name.
func formatAddress(a *mail.Address) string {
	if a == nil {
		return ""
	}
	name := a.Name
	if name != "" {
		if unquoted, err := strconv.Unquote(name); err == nil {
			name = unquoted
		}
		return name + " <" + a.Address + ">"
	}
	return a.Address
}

// messageForParse returns the header block followed by an empty body so
// net/mail can parse headers even for truncated or header-only files.
func messageForParse(raw []byte) []byte {
	block := HeaderBlock(raw)
	out := make([]byte, 0, len(block)+4)
	out = append(out, block...)
	out = append(out, '\r', '\n', '\r', '\n')
	return out
}

// HeaderBlock returns raw up to (not including) the first blank line. If there
// is no blank line the whole input is treated as headers.
func HeaderBlock(raw []byte) []byte {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i]
	}
	return raw
}

// Prepare returns the bytes to transmit in the SMTP DATA phase: CRLF line
// endings and a unique correlation header.
func Prepare(raw []byte, id string) []byte {
	out, _ := InjectID(raw, id)
	return out
}

// InjectID normalizes line endings to CRLF and adds the TUI's correlation
// header to the top-level header block.
//
// If the message already carries a Message-ID it is kept exactly as-is and an
// X-TUI-ID header is added. Otherwise a Message-ID: <id> header is inserted.
// The second return value reports whether a Message-ID was already present.
//
// The header is inserted after the last existing header line (first line when
// there are none), so existing bytes are preserved and only the final CRLF
// before the body comes from normalization.
func InjectID(raw []byte, id string) (out []byte, hadMessageID bool) {
	norm := NormalizeCRLF(raw)

	headerEnd := len(norm)
	bodyStart := -1
	if i := bytes.Index(norm, []byte("\r\n\r\n")); i >= 0 {
		headerEnd, bodyStart = i, i
	}
	headers := norm[:headerEnd]

	for _, line := range bytes.Split(headers, []byte("\r\n")) {
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(bytes.ToLower(line), []byte("message-id:")) {
			hadMessageID = true
			break
		}
	}

	var injected string
	if hadMessageID {
		injected = "X-TUI-ID: <" + id + ">\r\n"
	} else {
		injected = "Message-ID: <" + id + ">\r\n"
	}

	var buf bytes.Buffer
	buf.Grow(len(norm) + len(injected) + 2)
	buf.Write(headers)
	if len(headers) > 0 {
		buf.WriteString("\r\n")
	}
	buf.WriteString(injected)
	if bodyStart >= 0 {
		// norm[bodyStart:] starts with the blank-line CRLF that ends the
		// header block. The injected line already ends with CRLF, so skip
		// that first CRLF and keep exactly one blank line before the body.
		buf.Write(norm[bodyStart+2:])
	} else {
		buf.WriteString("\r\n")
	}
	return buf.Bytes(), hadMessageID
}

// NormalizeCRLF converts every line ending to CRLF. Already-CRLF sequences
// are left untouched; lone LF and lone CR become CRLF.
func NormalizeCRLF(b []byte) []byte {
	// Fast path: nothing to change.
	needs := false
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '\n':
			if i == 0 || b[i-1] != '\r' {
				needs = true
			}
		case '\r':
			if i+1 >= len(b) || b[i+1] != '\n' {
				needs = true
			}
		}
		if needs {
			break
		}
	}
	if !needs {
		return b
	}

	out := make([]byte, 0, len(b)+len(b)/16)
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch c {
		case '\n':
			if i == 0 || b[i-1] != '\r' {
				out = append(out, '\r', '\n')
			} else {
				out = append(out, '\n')
			}
		case '\r':
			if i+1 >= len(b) || b[i+1] != '\n' {
				out = append(out, '\r', '\n')
			} else {
				out = append(out, '\r')
			}
		default:
			out = append(out, c)
		}
	}
	return out
}

func decodeWord(s string) string {
	if s == "" {
		return ""
	}
	dec := new(mime.WordDecoder)
	if decoded, err := dec.DecodeHeader(s); err == nil {
		return decoded
	}
	return s
}
