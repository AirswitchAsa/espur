// Package vendor implements the vendor pool: selection, classified failure
// detection, fallthrough, and the penalty box. See specs/vendor-pool.dog.md.
package vendor

import "strings"

// FailureClass is the bucket a single attempt falls into. Spec: vendor-pool.dog.md.
type FailureClass int

const (
	ClassNone      FailureClass = iota // not a vendor failure
	ClassRateLimit                     // 429 / quota / usage / overloaded — cooldown
	ClassServer5xx                     // upstream 5xx — cooldown (shortest step)
	ClassAuth                          // 401/403 / clear auth-phrase — auth_locked
)

// String renders the class for logs (used as the failure_class attribute).
func (c FailureClass) String() string {
	switch c {
	case ClassRateLimit:
		return "rate_limit"
	case ClassServer5xx:
		return "server_5xx"
	case ClassAuth:
		return "auth"
	default:
		return "none"
	}
}

// rateLimitPhrases are case-insensitive substrings cribbed from the
// opencode-rate-limit-fallback plugin and the spec text. Lives in code, not
// in the spec (the spec fixes the categories; patterns can evolve).
var rateLimitPhrases = []string{
	"429",
	"quota exceeded",
	"usage limit",
	"rate limit",
	"rate-limit",
	"rate_limit",
	"high concurrency",
	"overloaded",
	"try again later",
	"too many requests",
	"resource has been exhausted",
}

var authPhrases = []string{
	"401",
	"403",
	"invalid api key",
	"unauthorized",
	"expired token",
	"revoked",
	"authentication_error",
	"authentication failed",
}

// Classify inspects opencode's stdout (JSON event stream, often containing
// upstream API errors verbatim) and stderr and returns the FailureClass.
// On ClassNone the attempt is not eligible for fallthrough or penalty.
func Classify(stdout, stderr string) FailureClass {
	hay := strings.ToLower(stdout + "\n" + stderr)

	// Auth wins over rate-limit when both phrases appear, since auth is
	// permanent and warrants reconfigure, not a backoff.
	for _, p := range authPhrases {
		if strings.Contains(hay, p) {
			// Disambiguate 401/403 from incidental "401" digits — require a
			// nearby keyword to reduce false positives.
			if p == "401" || p == "403" {
				if !strings.Contains(hay, "statuscode") && !strings.Contains(hay, "status code") &&
					!strings.Contains(hay, "http") {
					continue
				}
			}
			return ClassAuth
		}
	}
	for _, p := range rateLimitPhrases {
		if strings.Contains(hay, p) {
			return ClassRateLimit
		}
	}
	// Persistent 5xx: look for status code 5xx in error JSON.
	if has5xx(hay) {
		return ClassServer5xx
	}
	return ClassNone
}

func has5xx(hay string) bool {
	for _, code := range []string{"500", "502", "503", "504", "529"} {
		if strings.Contains(hay, `"statuscode":`+code) || strings.Contains(hay, "status code "+code) {
			return true
		}
	}
	return false
}
