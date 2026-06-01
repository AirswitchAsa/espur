// Package contextasm implements docs/specs/context-assembly.dog.md: build the
// single user message handed to opencode. The composite message is laid out
// as a cache-friendly stable prefix (thread identity + AGENTS.md memory)
// followed by volatile content (recent transcript tail, current request) so
// upstream prompt caches can match the prefix bytes across turns.
package contextasm

import (
	"strings"

	"github.com/punny/espur/internal/transcript"
)

// DefaultTailN is the transcript-tail length, pinned per spec note.
const DefaultTailN = 15

// MaxBytes is the byte cap on the assembled message before truncation.
// Spec note: line-count + hard byte cap (8 KiB). The cap applies to the
// thread-context block; the current request is always preserved verbatim.
const MaxBytes = 8 * 1024

// MaxAgentsMDBytes caps the inlined AGENTS.md content. If memory grows past
// this, the agent should move detail into fact_*.md files (see memory-seed
// spec); we still truncate as a guardrail rather than risk a runaway prompt.
const MaxAgentsMDBytes = 16 * 1024

// Prefix is the stable, per-thread header content. The same Prefix produces
// the same leading bytes on every turn for a given thread, which is what an
// Anthropic prompt cache needs to land a cache hit.
type Prefix struct {
	Platform string // e.g. "discord"
	ThreadID string // raw (un-encoded) thread id
	AgentsMD string // contents of AGENTS.md at the thread's working dir
}

// Trigger is the inbound message being acted on. Authors come from the
// adapter's normalized event.
type Trigger struct {
	AuthorLabel string
	Body        string
}

// Assemble returns the composite user-message string. tailRecords must be
// pre-filtered to KindUser records, in chronological order.
func Assemble(prefix Prefix, tailRecords []transcript.Record, current Trigger) string {
	var b strings.Builder

	// === stable prefix ===
	b.WriteString(`<thread platform="`)
	b.WriteString(escapeAttr(prefix.Platform))
	b.WriteString(`" id="`)
	b.WriteString(escapeAttr(prefix.ThreadID))
	b.WriteString(`">`)
	b.WriteByte('\n')
	if md := prefix.AgentsMD; md != "" {
		if len(md) > MaxAgentsMDBytes {
			md = md[:MaxAgentsMDBytes]
		}
		b.WriteString("<memory note=\"AGENTS.md for this thread; fact_*.md files live alongside it\">\n")
		b.WriteString(md)
		if !strings.HasSuffix(md, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("</memory>\n")
	}
	b.WriteString("</thread>\n")

	// === volatile suffix ===
	b.WriteString(`<thread-context note="recent user messages on this thread, oldest first">`)
	b.WriteByte('\n')
	ctxStart := b.Len()
	for _, r := range tailRecords {
		b.WriteString(transcript.Format(r))
		b.WriteByte('\n')
	}
	if extra := b.Len() - ctxStart - MaxBytes; extra > 0 {
		body := b.String()[ctxStart:]
		body = trimFromHead(body, extra)
		head := b.String()[:ctxStart]
		b.Reset()
		b.WriteString(head)
		b.WriteString(body)
	}
	b.WriteString("</thread-context>\n")

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
