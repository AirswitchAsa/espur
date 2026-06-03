// Package transcript implements the per-thread JSONL log described in
// docs/specs/transcript.dog.md.
package transcript

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kind enumerates the record types.
type Kind string

const (
	KindUser   Kind = "user"
	KindBot    Kind = "bot"
	KindSystem Kind = "system"
)

// Author snapshot of the message's sender.
type Author struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Meta is the kind-conditional metadata. Optional fields use omitempty.
type Meta struct {
	Mention       bool   `json:"mention,omitempty"`
	CoalescedInto string `json:"coalesced_into,omitempty"`
	ReplyOutcome  string `json:"reply_outcome,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	VendorID      string `json:"vendor_id,omitempty"` // spec note: pinned, see transcript.dog.md
	Note          string `json:"note,omitempty"`
}

// Record is one line in the transcript file.
type Record struct {
	TS                string `json:"ts"`
	Kind              Kind   `json:"kind"`
	PlatformMessageID string `json:"platform_message_id"`
	Author            Author `json:"author"`
	Body              string `json:"body"`
	Meta              Meta   `json:"meta"`
}

// Store manages transcript files under a base data directory.
type Store struct{ BaseDir string }

func NewStore(baseDir string) *Store { return &Store{BaseDir: baseDir} }

// ThreadDir returns the working directory for a given (platform, threadID).
// The encoded id is URL-safe base64 with a 64-char cap; spec pins reversibility.
func (s *Store) ThreadDir(platform, threadID string) string {
	return filepath.Join(s.BaseDir, "threads", platform, EncodeThreadID(threadID))
}

// EncodeThreadID renders the platform-native id into a filesystem-safe form.
// Pinned per spec: URL-safe base64 of the raw id, capped at 64 chars with a
// sha256-hex suffix when truncation occurs.
func EncodeThreadID(raw string) string {
	enc := base64.RawURLEncoding.EncodeToString([]byte(raw))
	const cap_ = 64
	if len(enc) <= cap_ {
		return enc
	}
	// Truncate and append sha-style suffix for uniqueness on collisions.
	sum := sha256Hex(raw)[:8]
	return enc[:cap_-9] + "-" + sum
}

// Append writes one record to <threadDir>/transcript.jsonl, creating dirs.
// Caller serializes writes per-thread (see docs/specs/transcript.dog.md).
func (s *Store) Append(platform, threadID string, r Record) error {
	if r.TS == "" {
		r.TS = time.Now().UTC().Format(time.RFC3339)
	}
	dir := s.ThreadDir(platform, threadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "transcript.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// TailUserMessages returns the last n records with Kind == KindUser, in
// chronological order. Bot replies and system records are filtered out per spec.
func (s *Store) TailUserMessages(platform, threadID string, n int) ([]Record, error) {
	return s.tail(platform, threadID, n, func(r Record) bool { return r.Kind == KindUser })
}

// TailAll returns the last n records of any kind. Used by the web UI thread
// peek view.
func (s *Store) TailAll(platform, threadID string, n int) ([]Record, error) {
	return s.tail(platform, threadID, n, func(Record) bool { return true })
}

// tail reads every record from the thread's transcript, keeps those matching
// keep, and returns the last n in chronological order.
//
// Robustness matters here: this runs on every trigger, and a single corrupt or
// oversized line must never crash the turn. We use a bufio.Reader (not a
// Scanner with a fixed max-token cap), so an arbitrarily long line is read
// whole rather than poisoning the read with bufio.ErrTooLong; malformed JSON is
// skipped. (For v0.1 the whole file is streamed linearly; transcripts are small.
// A tail-from-end optimization is straightforward when files grow.)
func (s *Store) tail(platform, threadID string, n int, keep func(Record) bool) ([]Record, error) {
	if n <= 0 {
		return nil, nil
	}
	path := filepath.Join(s.ThreadDir(platform, threadID), "transcript.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var all []Record
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			var r Record
			if json.Unmarshal(line, &r) == nil && keep(r) {
				all = append(all, r)
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return nil, rerr
		}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

func sha256Hex(s string) string {
	// Lazy: implemented via fmt + the crypto/sha256 stdlib without importing
	// in this file by delegating. Keep transcript dependency surface small.
	return sha256HexImpl(s)
}

// sha256HexImpl avoids importing crypto/sha256 at the file top to keep imports
// alphabetized cleanly; defined in helper.go.
var _ = strings.Builder{}

// Format renders a user record for the thread-context block. Spec:
// context-assembly.dog.md — `author label + message body`, preserving newlines.
func Format(r Record) string {
	label := r.Author.Label
	if label == "" {
		label = r.Author.ID
	}
	if label == "" {
		label = "user"
	}
	return fmt.Sprintf("%s: %s", label, r.Body)
}
