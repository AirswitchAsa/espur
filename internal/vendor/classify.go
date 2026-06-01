// Package vendor implements the vendor pool: selection, classified failure
// detection, fallthrough, and the penalty box. See docs/specs/vendor-pool.dog.md.
package vendor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

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
	// opencode-side classification: these mean the operator has to
	// fix the vendor row (wrong model id, provider not authed via
	// `opencode auth login`). Same remediation as a real auth failure,
	// so they go in the auth_locked bucket. Without this, the pool would
	// treat them as genuine crashes and refuse to fall through. Observed
	// against opencode 1.15.x with an unauthed provider:
	//   "Model not found: openai/gpt-4o-mini..."
	"model not found",
	"unknown provider",
	"provider not configured",
	"no provider for",
}

// Classify decides the FailureClass for a crashed attempt. On ClassNone the
// attempt is not eligible for fallthrough or penalty.
//
// Crucially, it inspects only opencode's *error envelope* — the
// `{"type":"error",...,"error":{...}}` events on stdout (plus all of stderr) —
// and never the assistant text or tool-result parts of the NDJSON stream.
// A web-search tool that crawls a page returning HTTP 401/403 embeds strings
// like `"httpStatusCode":401` / `"CRAWL_HTTP_401"` in a *message part*; scanning
// the whole stream would read that website's rejection as the *vendor's* auth
// failing and permanently auth-lock a perfectly healthy vendor. Scoping to the
// error envelope is what keeps a crawl-side 401 from locking the pool.
// See docs/specs/vendor-pool.dog.md and TestClassify_IgnoresToolOutput.
func Classify(stdout, stderr string) FailureClass {
	hay := strings.ToLower(errorEnvelope(stdout) + "\n" + stderr)

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

// errorEnvelope distils opencode's NDJSON stdout down to just its error-bearing
// content. `opencode run --format json` emits one JSON event per line; a
// provider/runtime failure surfaces as an event carrying a top-level `error`
// object (observed against opencode 1.15.x):
//
//	{"type":"error","sessionID":"...","error":{"name":"...","data":{"message":"...","statusCode":401}}}
//
// We keep only those error objects. Normal events (`text`, `tool`, `step_*`)
// hold the assistant answer and tool results — including any HTTP errors a tool
// hit while crawling — and are dropped so they can't trip the auth/rate-limit
// phrase match. Lines that don't parse as JSON are kept verbatim: opencode's
// own NDJSON is always valid, so a non-JSON line means abnormal output (a panic
// or fatal log) worth classifying, and tool/web content never appears raw.
func errorEnvelope(stdout string) string {
	var b strings.Builder
	sc := bufio.NewScanner(strings.NewReader(stdout))
	// Match extractSessionID's bound: a single event line can be large.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev map[string]json.RawMessage
		if err := json.Unmarshal(line, &ev); err != nil {
			b.Write(line)
			b.WriteByte('\n')
			continue
		}
		raw, ok := ev["error"]
		if !ok {
			continue
		}
		if s := strings.TrimSpace(string(raw)); s != "" && s != "null" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func has5xx(hay string) bool {
	for _, code := range []string{"500", "502", "503", "504", "529"} {
		if strings.Contains(hay, `"statuscode":`+code) || strings.Contains(hay, "status code "+code) {
			return true
		}
	}
	return false
}
