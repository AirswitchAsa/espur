// Package contextasm implements docs/specs/context-assembly.dog.md: build the
// single user message handed to opencode, wrapping the transcript tail in
// <thread-context> and the current trigger in <request>.
package contextasm

import (
	"strings"

	"github.com/punny/espur/internal/transcript"
)

// DefaultTailN is the transcript-tail length, pinned per spec note.
const DefaultTailN = 30

// MaxBytes is the byte cap on the assembled message before truncation.
// Spec note: line-count + hard byte cap (8 KiB). The cap applies to the
// thread-context block; the current request is always preserved verbatim.
const MaxBytes = 8 * 1024

// Trigger is the inbound message being acted on. Authors come from the
// adapter's normalized event.
type Trigger struct {
	AuthorLabel string
	Body        string
}

// Assemble returns the composite user-message string. tailRecords must be
// pre-filtered to KindUser records, in chronological order.
func Assemble(tailRecords []transcript.Record, current Trigger) string {
	var b strings.Builder

	// Thread-context block. Wrapper tags are stable per spec note.
	b.WriteString(`<thread-context note="recent user messages on this thread, oldest first">`)
	b.WriteByte('\n')
	ctxStart := b.Len()
	for _, r := range tailRecords {
		b.WriteString(transcript.Format(r))
		b.WriteByte('\n')
	}
	// Enforce the byte cap on the thread-context body only.
	if extra := b.Len() - ctxStart - MaxBytes; extra > 0 {
		// Drop oldest lines until we're under the cap. Rebuild from the tail.
		body := b.String()[ctxStart:]
		body = trimFromHead(body, extra)
		// rewind b to ctxStart
		head := b.String()[:ctxStart]
		b.Reset()
		b.WriteString(head)
		b.WriteString(body)
	}
	b.WriteString("</thread-context>\n")

	// Request block.
	author := current.AuthorLabel
	if author == "" {
		author = "user"
	}
	b.WriteString(`<request from="`)
	b.WriteString(escapeAttr(author))
	b.WriteString(`">`)
	b.WriteByte('\n')
	b.WriteString(current.Body)
	b.WriteByte('\n')
	b.WriteString("</request>\n")
	return b.String()
}

func escapeAttr(s string) string {
	r := strings.NewReplacer(`"`, `&quot;`, `\`, `\\`)
	return r.Replace(s)
}

// trimFromHead drops bytes from the start of s, rounding up to the next
// newline so we don't split a line.
func trimFromHead(s string, drop int) string {
	if drop >= len(s) {
		return ""
	}
	out := s[drop:]
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	return out
}
