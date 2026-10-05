// Package webhook runs the HTTP listener that receives ninja's analysis
// results and holds the wire types ninja POSTs.
//
// The structs below mirror the JSON produced by github.com/synqronlabs/ninja's
// internal/email.Result. They are copied, not imported: ninja-tui is a separate
// module and must not depend on the backend source.
package webhook

// Category is the scam classification chosen by ninja's scorer.
type Category struct {
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence"`
}

// Verdict is ninja's scoring output. It is nil when scoring was disabled or
// failed, in which case Result.Partial may still be true.
type Verdict struct {
	Score         float64            `json:"score"`
	ScoreMax      int                `json:"score_max"`
	Normalized    float64            `json:"normalized"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Category      *Category          `json:"category,omitempty"`
	Model         string             `json:"model"`
	Levels        []string           `json:"levels,omitempty"`
}

// Timings records wall-clock milliseconds spent in each pipeline stage.
type Timings struct {
	ExtractMS   int64 `json:"extract_ms"`
	SummarizeMS int64 `json:"summarize_ms"`
	ScoreMS     int64 `json:"score_ms"`
}

// Envelope is the SMTP transaction envelope. It is nil for non-SMTP sources.
type Envelope struct {
	From     string   `json:"from"`
	To       []string `json:"to"`
	Helo     string   `json:"helo,omitempty"`
	RemoteIP string   `json:"remote_ip,omitempty"`
}

// Size records byte counts observed while materializing the email.
type Size struct {
	RawBytes         int64 `json:"raw_bytes"`
	DecodedBodyBytes int64 `json:"decoded_body_bytes"`
	AttachmentBytes  int64 `json:"attachment_bytes"`
}

// Address is a parsed mail address with an optional display name.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Suspicious flags header-level anomalies found by ninja.
type Suspicious struct {
	ReplyToMismatch  bool `json:"reply_to_mismatch"`
	DisplayNameSpoof bool `json:"display_name_spoof"`
}

// Headers holds the raw header order and ninja's normalized fields.
type Headers struct {
	Raw             [][2]string `json:"raw"`
	From            []Address   `json:"from"`
	To              []Address   `json:"to"`
	CC              []Address   `json:"cc"`
	ReplyTo         []Address   `json:"reply_to"`
	Subject         string      `json:"subject"`
	Date            string      `json:"date,omitempty"`
	MessageID       string      `json:"message_id,omitempty"`
	Received        []string    `json:"received,omitempty"`
	ListUnsubscribe []string    `json:"list_unsubscribe,omitempty"`
	Suspicious      Suspicious  `json:"suspicious"`
}

// MIMEPart is one node in the MIME structure tree.
type MIMEPart struct {
	ContentType string     `json:"content_type"`
	Disposition string     `json:"disposition,omitempty"`
	Filename    string     `json:"filename,omitempty"`
	Size        int64      `json:"size"`
	Parts       []MIMEPart `json:"parts,omitempty"`
}

// MIME describes the message's MIME tree.
type MIME struct {
	ContentType string     `json:"content_type"`
	Structure   []MIMEPart `json:"structure,omitempty"`
}

// Body is a decoded message body part.
type Body struct {
	ContentType string `json:"content_type"`
	Charset     string `json:"charset,omitempty"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated"`
}

// URL is a URL found anywhere in the message.
type URL struct {
	Raw         string   `json:"raw"`
	Scheme      string   `json:"scheme"`
	Host        string   `json:"host"`
	Path        string   `json:"path,omitempty"`
	DisplayText string   `json:"display_text,omitempty"`
	In          []string `json:"in"`
	Punycode    bool     `json:"punycode"`
	IPLiteral   bool     `json:"ip_literal"`
}

// Attachment describes a non-body MIME part. Contents are never retained.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Inline      bool   `json:"inline"`
	ContentID   string `json:"content_id,omitempty"`
}

// OCR is text extracted from an image, if any.
type OCR struct {
	Engine    string `json:"engine"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

// Image is an image referenced by the message, embedded or remote.
type Image struct {
	Source      string `json:"source"`
	URL         string `json:"url,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Fetched     bool   `json:"fetched"`
	OCR         OCR    `json:"ocr"`
}

// DKIMResult is one DKIM signature's verification outcome.
type DKIMResult struct {
	Domain string `json:"domain"`
	Result string `json:"result"`
}

// Auth holds SPF, DKIM, and DMARC results. It is nil unless ninja's SMTP
// server has authentication verification enabled.
type Auth struct {
	SPF   string       `json:"spf"`
	DKIM  []DKIMResult `json:"dkim,omitempty"`
	DMARC string       `json:"dmarc"`
	Error string       `json:"error,omitempty"`
}

// Facts is ninja's fixed-shape representation of one email.
type Facts struct {
	SchemaVersion int          `json:"schema_version"`
	Source        string       `json:"source"`
	Envelope      *Envelope    `json:"envelope"`
	Size          Size         `json:"size"`
	Headers       Headers      `json:"headers"`
	MIME          MIME         `json:"mime"`
	Bodies        []Body       `json:"bodies,omitempty"`
	URLs          []URL        `json:"urls,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
	Images        []Image      `json:"images,omitempty"`
	Auth          *Auth        `json:"auth,omitempty"`
	Errors        []string     `json:"errors,omitempty"`
}

// Result is ninja's complete output for one analyzed email, as POSTed to the
// webhook. Verdict is nil-safe: it may be absent when Partial is true.
type Result struct {
	SchemaVersion int      `json:"schema_version"`
	MessageSHA256 string   `json:"message_sha256"`
	Facts         Facts    `json:"facts"`
	Summary       string   `json:"summary"`
	Verdict       *Verdict `json:"verdict,omitempty"`
	Timings       Timings  `json:"timings"`
	Partial       bool     `json:"partial"`
	// Cached reports that ninja served the result from its embedded store
	// instead of recomputing. Both a cached and a fresh result are valid;
	// the TUI labels cached ones so they are not mistaken for fresh work.
	Cached bool `json:"cached,omitempty"`
}
