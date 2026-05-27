// Package adapter defines the IM-platform port for the bot core. Each
// concrete IM platform (Discord, WeChat, ...) lives in a sub-package and
// implements the Adapter interface. See docs/specs/adapter.dog.md.
package adapter

import (
	"context"
	"errors"
	"time"
)

// Adapter is implemented per IM platform.
type Adapter interface {
	// Platform is the stable platform identifier ("discord", "wechat", ...).
	Platform() string
	// Start begins listening on the platform; returns the inbound event channel.
	// On ctx cancel, the adapter shuts down and closes the channel.
	Start(ctx context.Context) (<-chan Event, error)
	// Post is the sole outbound surface. Returns the platform-native id of the
	// first delivered chunk so the transcript can correlate.
	Post(ctx context.Context, threadID, body string) (platformMessageID string, err error)
	// Healthy is a cheap atomic snapshot for the web UI.
	Healthy() bool
}

// ErrThreadGone is returned by Post when the destination thread no longer
// exists (channel deleted, permissions revoked). The core downgrades log
// severity and proceeds as a crash outcome per spec.
var ErrThreadGone = errors.New("adapter: thread gone")

// Event is the sum-type carried on the inbound channel. Exactly one of
// Message / Lifecycle is set.
type Event struct {
	Message   *MessageEvent
	Lifecycle *LifecycleEvent
}

// MessageEvent is the normalized inbound message.
type MessageEvent struct {
	Platform          string
	ThreadID          string
	PlatformMessageID string
	Author            Author
	Body              string
	Mention           bool
	ReceivedAt        time.Time
}

// Author is the snapshot the adapter captured at receive time.
type Author struct {
	ID    string
	Label string
}

// LifecycleKind enumerates transport-state changes the adapter emits.
type LifecycleKind string

const (
	LifecycleConnected    LifecycleKind = "connected"
	LifecycleDisconnected LifecycleKind = "disconnected"
	LifecycleReconnecting LifecycleKind = "reconnecting"
	LifecycleAuthRevoked  LifecycleKind = "auth_revoked"
)

// LifecycleEvent surfaces transport state for the web UI and logs.
type LifecycleEvent struct {
	Platform string
	Kind     LifecycleKind
	Cause    string
	Attempt  int
	At       time.Time
}
