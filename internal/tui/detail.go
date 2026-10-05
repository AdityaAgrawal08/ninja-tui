package tui

import (
	"fmt"
	"math"
	"strings"

	"ninja-tui/internal/store"
	"ninja-tui/internal/webhook"
)

// detailContent renders the full email context for the right pane in
// the mandated order: complete headers, every decoded body in order, URLs,
// attachments, images/OCR, auth results, then the verdict block.
func (m *Model) detailContent(e store.Entry) string {
	var b strings.Builder

	res := e.Result
	var h webhook.Headers
	if res != nil {
		h = res.Facts.Headers
	}

	// 1. Complete headers, falling back to what the folder scan parsed.
	b.WriteString(styleSection.Render("HEADERS"))
	b.WriteString("\n")
	writeKV(&b, "subject", dash(firstNonEmpty(h.Subject, e.Subject)))
	writeKV(&b, "from", dash(firstNonEmpty(formatAddresses(h.From), e.From)))
	writeKV(&b, "to", dash(formatAddresses(h.To)))
	writeKV(&b, "cc", dash(formatAddresses(h.CC)))
	writeKV(&b, "reply-to", dash(formatAddresses(h.ReplyTo)))
	writeKV(&b, "date", dash(h.Date))
	writeKV(&b, "message-id", dash(h.MessageID))
	writeKV(&b, "file", fmt.Sprintf("%s  (%s)", e.Name, humanSize(e.Size)))
	if h.Suspicious.ReplyToMismatch {
		b.WriteString(styleBullet.Render("! ") + styleWarn.Render("reply-to mismatch") + "\n")
	}
	if h.Suspicious.DisplayNameSpoof {
		b.WriteString(styleBullet.Render("! ") + styleWarn.Render("display-name spoof") + "\n")
	}
	b.WriteString("\n")

	if res == nil {
		b.WriteString(detailPending(e))
		return b.String()
	}

	// 2. Full decoded bodies, in order, exactly as ninja decoded them. The
	// TUI never truncates; when ninja marked a body truncated it says so.
	bodies := res.Facts.Bodies
	b.WriteString(styleSection.Render("BODY (" + itoa(len(bodies)) + ")"))
	b.WriteString("\n")
	if len(bodies) == 0 {
		b.WriteString(styleDim.Render("(no decoded body)"))
		b.WriteString("\n")
	}
	for i, body := range bodies {
		label := itoa(i+1) + ". " + body.ContentType
		if body.Charset != "" {
			label += " · " + body.Charset
		}
		if body.Truncated {
			label += " · ninja truncated this body"
		}
		b.WriteString(styleDim.Render(label))
		b.WriteString("\n")
		b.WriteString(styleText.Render(body.Text))
		b.WriteString("\n\n")
	}

	// 3. URLs.
	urls := res.Facts.URLs
	b.WriteString(styleSection.Render("URLS (" + itoa(len(urls)) + ")"))
	b.WriteString("\n")
	if len(urls) == 0 {
		b.WriteString(styleDim.Render("none"))
	} else {
		for _, u := range urls {
			line := u.Raw
			if u.DisplayText != "" && u.DisplayText != u.Raw {
				line += "  → " + u.DisplayText
			}
			if u.Punycode || u.IPLiteral {
				var tags []string
				if u.Punycode {
					tags = append(tags, "punycode")
				}
				if u.IPLiteral {
					tags = append(tags, "ip-literal")
				}
				line += "  [" + strings.Join(tags, ", ") + "]"
				b.WriteString(styleWarn.Faint(true).Render(line))
			} else {
				b.WriteString(styleText.Render(line))
			}
			b.WriteString("\n")
		}
	}

	// 4. Attachments.
	atts := res.Facts.Attachments
	b.WriteString("\n")
	b.WriteString(styleSection.Render("ATTACHMENTS (" + itoa(len(atts)) + ")"))
	b.WriteString("\n")
	if len(atts) == 0 {
		b.WriteString(styleDim.Render("none"))
	} else {
		for _, a := range atts {
			line := a.Filename
			if line == "" {
				line = "(unnamed)"
			}
			line += fmt.Sprintf("  (%s, %s)", a.ContentType, humanSize(a.Size))
			if a.Inline {
				line += " [inline]"
			}
			b.WriteString(styleText.Render(line))
			b.WriteString("\n")
			if a.SHA256 != "" {
				b.WriteString(styleDim.Render("  sha256 " + shortSHA(a.SHA256)))
				b.WriteString("\n")
			}
		}
	}

	// 5. Images / OCR.
	imgs := res.Facts.Images
	b.WriteString("\n")
	b.WriteString(styleSection.Render("IMAGES (" + itoa(len(imgs)) + ")"))
	b.WriteString("\n")
	if len(imgs) == 0 {
		b.WriteString(styleDim.Render("none"))
	} else {
		for _, img := range imgs {
			line := img.Source
			if img.ContentType != "" {
				line += " " + img.ContentType
			}
			if img.Size > 0 {
				line += " " + humanSize(img.Size)
			}
			if img.URL != "" {
				line += " " + img.URL
			}
			b.WriteString(styleText.Render(line))
			b.WriteString("\n")
			if img.Source == "remote" {
				if img.Fetched {
					b.WriteString(styleDim.Render("  fetched"))
				} else {
					b.WriteString(styleDim.Render("  not fetched"))
				}
				b.WriteString("\n")
			}
			switch {
			case img.OCR.Text != "":
				b.WriteString(styleDim.Render("  ocr(" + img.OCR.Engine + "): "))
				b.WriteString(styleText.Render(img.OCR.Text))
				b.WriteString("\n")
			case img.OCR.Error != "":
				b.WriteString(styleDim.Render("  ocr error: " + img.OCR.Error))
				b.WriteString("\n")
			}
		}
	}

	// 6. Auth.
	b.WriteString("\n")
	b.WriteString(styleSection.Render("AUTH"))
	b.WriteString("\n")
	if res.Facts.Auth == nil {
		b.WriteString(styleDim.Render("not available (ninja smtp.auth disabled, or no SMTP envelope)"))
		b.WriteString("\n")
	} else {
		a := res.Facts.Auth
		b.WriteString(styleDim.Render("SPF    ") + authValue(a.SPF) + "\n")
		if len(a.DKIM) == 0 {
			b.WriteString(styleDim.Render("DKIM   ") + styleDim.Render("none") + "\n")
		} else {
			var parts []string
			for _, d := range a.DKIM {
				parts = append(parts, d.Domain+"="+d.Result)
			}
			b.WriteString(styleDim.Render("DKIM   ") + authValue(strings.Join(parts, ", ")) + "\n")
		}
		b.WriteString(styleDim.Render("DMARC  ") + authValue(a.DMARC) + "\n")
		if a.Error != "" {
			b.WriteString(styleBad.Render("auth error: "+a.Error) + "\n")
		}
	}

	// 7. Verdict block: score, category, confidence, summary, timings, and
	// the partial/cached/error flags.
	b.WriteString("\n")
	b.WriteString(styleSection.Render("VERDICT"))
	b.WriteString("\n")
	v := res.Verdict
	if v == nil {
		b.WriteString(styleDim.Render("[ ? ]") + "  " + styleDim.Render("unscored — no verdict returned") + "\n")
	} else {
		b.WriteString(scoreBadge(v.Score, v.ScoreMax) + "  " + verdictLine(v) + "\n")
		writeKV(&b, "score", fmt.Sprintf("%g / %d", v.Score, v.ScoreMax))
		category := "none"
		confidence := v.Confidence
		if v.Category != nil {
			category = v.Category.Choice
			if v.Category.Confidence > 0 {
				confidence = v.Category.Confidence
			}
		}
		writeKV(&b, "category", category)
		writeKV(&b, "confidence", fmt.Sprintf("%d%%", int(math.Round(confidence*100))))
		if v.Model != "" {
			writeKV(&b, "model", v.Model)
		}
	}
	b.WriteString("\n")
	b.WriteString(styleSection.Render("SUMMARY"))
	b.WriteString("\n")
	if strings.TrimSpace(res.Summary) == "" {
		b.WriteString(styleDim.Render("(no summary — summarizer disabled)"))
	} else {
		b.WriteString(styleText.Render(res.Summary))
	}
	b.WriteString("\n\n")
	b.WriteString(styleSection.Render("TIMINGS"))
	b.WriteString("\n")
	b.WriteString(styleText.Render(fmt.Sprintf("extract %dms · summarize %dms · score %dms",
		res.Timings.ExtractMS, res.Timings.SummarizeMS, res.Timings.ScoreMS)))
	b.WriteString("\n\n")
	b.WriteString(styleSection.Render("FLAGS & ERRORS"))
	b.WriteString("\n")
	if res.Partial {
		b.WriteString(styleWarn.Render("partial result: a pipeline stage did not complete") + "\n")
	}
	if res.Cached {
		b.WriteString(styleWarn.Render("cached: ninja served this from its store, not recomputed") + "\n")
	}
	errs := collectErrors(res)
	if len(errs) == 0 && !res.Partial && !res.Cached {
		b.WriteString(styleDim.Render("none"))
	} else {
		for _, e := range errs {
			b.WriteString(styleBad.Render("! "+e) + "\n")
		}
	}
	if res.MessageSHA256 != "" {
		b.WriteString(styleDim.Render("message_sha256 " + shortSHA(res.MessageSHA256)))
		b.WriteString("\n")
	}
	return b.String()
}

func detailPending(e store.Entry) string {
	var b strings.Builder
	switch e.Status {
	case store.Failed:
		b.WriteString(styleBad.Render("send failed"))
		if e.Err != "" {
			b.WriteString("\n")
			b.WriteString(styleText.Render(e.Err))
		}
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("Press s to retry failed files."))
	case store.Sending:
		b.WriteString(styleWarn.Render("sending to ninja…"))
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("ninja posts the result back to this TUI's webhook when done."))
	case store.Sent:
		b.WriteString(styleTeal.Render("sent — waiting for ninja's result"))
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("Results arrive asynchronously over the webhook; this pane updates on arrival."))
	default:
		b.WriteString(styleDim.Render("queued — not sent yet"))
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("The queue is sent to ninja automatically; s starts a run by hand."))
	}
	return b.String()
}

func writeKV(b *strings.Builder, key, value string) {
	b.WriteString(styleDim.Render(fmt.Sprintf("%-10s", key)))
	b.WriteString(styleText.Render(value))
	b.WriteString("\n")
}

func formatAddresses(addrs []webhook.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Name != "" {
			parts = append(parts, a.Name+" <"+a.Address+">")
		} else {
			parts = append(parts, a.Address)
		}
	}
	return strings.Join(parts, ", ")
}

func dash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "—"
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func authValue(v string) string {
	switch strings.ToLower(v) {
	case "pass", "passed":
		return styleGood.Render(v)
	case "fail", "failed", "softfail", "permerror", "temperror":
		return styleBad.Render(v)
	case "":
		return styleDim.Render("none")
	default:
		return styleText.Render(v)
	}
}

func shortSHA(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}

func collectErrors(res *webhook.Result) []string {
	var out []string
	out = append(out, res.Facts.Errors...)
	if res.Facts.Auth != nil && res.Facts.Auth.Error != "" {
		out = append(out, "auth: "+res.Facts.Auth.Error)
	}
	for _, img := range res.Facts.Images {
		if img.OCR.Error != "" {
			out = append(out, "ocr: "+img.OCR.Error)
		}
	}
	return out
}
