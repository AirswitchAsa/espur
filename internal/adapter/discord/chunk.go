package discord

import "strings"

// chunk splits body into a minimum number of pieces each <= maxLen runes
// in length. Splits prefer paragraph → sentence → word → byte boundaries,
// and never split inside a fenced code block ("“" / "```"). Spec:
// reply.dog.md "Batch only" and adapter.dog.md "Outbound poster".
func chunk(body string, maxLen int) []string {
	if maxLen <= 0 {
		return []string{body}
	}
	if len([]byte(body)) <= maxLen {
		if body == "" {
			return nil
		}
		return []string{body}
	}
	// Code-fence-aware split: track fence depth as we walk lines.
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
		// detect fence open/close.
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
		// If adding this line would exceed and we're not inside a fence,
		// flush before adding.
		if current.Len()+len(addition) > maxLen && !inFence && current.Len() > 0 {
			flush()
			addition = line
		}
		// If the single line alone exceeds maxLen, hard-split by word.
		if len(addition) > maxLen {
			// Flush whatever we have first.
			flush()
			for _, piece := range hardSplit(addition, maxLen) {
				out = append(out, piece)
			}
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
		// try last space within maxLen
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
