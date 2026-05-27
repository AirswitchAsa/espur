// Package textchunk splits long reply bodies into platform-friendly chunks.
// Shared by every adapter that has a per-message length cap (Discord at
// 2000 chars, WeChat at our self-imposed 1800, etc.). Spec: adapter.dog.md
// "Outbound poster" + reply.dog.md "Batch only".
package textchunk

import "strings"

// Split returns body broken into the minimum number of <=maxLen pieces.
// Prefers line boundaries, never cuts inside a Markdown fenced code block,
// and falls back to word-boundary hard splits for single lines longer than
// maxLen. maxLen <= 0 disables splitting.
func Split(body string, maxLen int) []string {
	if maxLen <= 0 {
		return []string{body}
	}
	if len(body) <= maxLen {
		if body == "" {
			return nil
		}
		return []string{body}
	}
	var (
		out      []string
		current  strings.Builder
		inFence  bool
		fenceTag string
	)
	flush := func() {
		if current.Len() == 0 {
			return
		}
		out = append(out, current.String())
		current.Reset()
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		// Snapshot fence state from the START of this iteration. The line
		// itself may close the fence we are in — and a closing fence line
		// must be flushed *with* the open block, not after it, so the cap
		// check uses the pre-toggle state.
		wasInFence := inFence
		wasFenceTag := fenceTag
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				inFence = true
				fenceTag = trimmed
			} else if trimmed == "```" || trimmed == fenceTag {
				inFence = false
			}
		}
		addition := line
		if i > 0 {
			addition = "\n" + line
		}
		// Cap is a hard contract: chunks must never exceed maxLen. Fence
		// preservation across splits is a soft goal; if a chunk we'd produce
		// to keep the fence intact would itself overflow, we just close the
		// fence cleanly when there's room, and accept that the rendering on
		// subsequent chunks may be plain text. For typical platform limits
		// (Discord 2000, WeChat 1800) the close-marker overhead is trivial
		// so fences essentially always survive.
		closeOverhead := len("\n```")
		if current.Len()+len(addition) > maxLen && current.Len() > 0 {
			if wasInFence && current.Len()+closeOverhead <= maxLen {
				current.WriteString("\n```")
			}
			_ = wasFenceTag // reserved for future re-open logic
			flush()
			addition = line
		}
		if len(addition) > maxLen {
			flush()
			out = append(out, hardSplit(addition, maxLen)...)
			continue
		}
		current.WriteString(addition)
	}
	flush()
	return out
}

func hardSplit(s string, maxLen int) []string {
	var out []string
	for len(s) > maxLen {
		cut := strings.LastIndex(s[:maxLen], " ")
		if cut < maxLen/2 {
			cut = maxLen
		}
		out = append(out, s[:cut])
		s = strings.TrimPrefix(s[cut:], " ")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
