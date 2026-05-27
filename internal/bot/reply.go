package bot

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/punny/espur/internal/vendor"
)

// crockford32 — pinned per docs/specs/reply.dog.md TODO(decision): 8-char Crockford
// base32 token used as the request id on crash replies.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewRequestID returns a fresh 8-char Crockford base32 token.
func NewRequestID() string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	// Map 5 bytes (40 bits) into 8 base32 chars.
	v := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = crockford[v&0x1f]
		v >>= 5
	}
	return string(out)
}

// TimeoutReply is the timeout user-visible message. Wording pinned.
const TimeoutReply = "Took too long, aborted. Try again or rephrase."

// CrashReply builds the user-visible crash message with the request ID.
func CrashReply(requestID string) string {
	return fmt.Sprintf("Internal error. Check logs. Request ID: `%s`.", requestID)
}

// DrainedReply enumerates penalized vendors per spec — auth-only-drained
// leads with a reconfigure hint since waiting won't help.
func DrainedReply(snaps []vendor.PenalizedSnapshot, dashboardURL string, now time.Time) string {
	var allAuth = true
	for _, s := range snaps {
		if s.Status != "auth_locked" {
			allAuth = false
		}
	}
	var b strings.Builder
	if allAuth && len(snaps) > 0 {
		b.WriteString("All configured vendors need reconfiguration (auth failed).\n")
	} else {
		b.WriteString("All vendors exhausted (rate-limited or out of quota).\n")
	}
	for _, s := range snaps {
		switch s.Status {
		case "auth_locked":
			fmt.Fprintf(&b, "- %s — auth failed, needs reconfigure\n", s.VendorID)
		case "cooldown":
			if s.CooldownUntil != nil {
				d := s.CooldownUntil.Sub(now)
				fmt.Fprintf(&b, "- %s — rate-limited, retry in ~%s\n", s.VendorID, humanizeDuration(d))
			} else {
				fmt.Fprintf(&b, "- %s — rate-limited\n", s.VendorID)
			}
		default:
			fmt.Fprintf(&b, "- %s — %s\n", s.VendorID, s.Status)
		}
	}
	if dashboardURL != "" {
		fmt.Fprintf(&b, "Check the dashboard at %s.", dashboardURL)
	} else {
		b.WriteString("Check the dashboard.")
	}
	return b.String()
}

func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
