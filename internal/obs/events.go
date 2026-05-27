// Package obs holds the structured log event-name registry. Per
// docs/specs/observability.dog.md, every structured record carries an `event`
// attribute drawn from this list. New events may be added freely; renames
// are a breaking change to log consumers.
//
// These constants are referenced from call sites as the value passed to
// slog as the `event` attribute, e.g.:
//
//	logger.Info("vendor entered cooldown",
//	    "event", obs.VendorCooldownEntered, ...)
//
// Keeping them in one place gives the operator a single grep target and
// prevents string drift across packages.
package obs

const (
	// Boot / lifecycle.
	BootReady       = "boot.ready"
	ShutdownStart   = "shutdown.start"
	ShutdownComp    = "shutdown.complete"
	ShutdownAdapter = "shutdown.adapter.done"
	ShutdownBot     = "shutdown.bot.done"
	ShutdownWeb     = "shutdown.web.done"
	ShutdownStore   = "shutdown.store.done"
	SecretsSelfTest = "secrets.self_test"

	// Adapter.
	AdapterConnected    = "adapter.connected"
	AdapterDisconnected = "adapter.disconnected"
	AdapterReconnecting = "adapter.reconnecting"
	AdapterLifecycle    = "adapter.lifecycle"
	AdapterPostFailed   = "adapter.post.failed"

	// Trigger / dedup.
	TriggerAccepted = "trigger.accepted"
	TriggerDropped  = "trigger.dropped"  // burst-coalesce slot already full
	TriggerObserved = "trigger.observed" // non-mention message recorded only
	TriggerCoalesce = "trigger.coalesced"

	// Invocation / vendor pool.
	InvocationSuccess     = "invocation.success"
	InvocationTimeout     = "invocation.timeout"
	InvocationCrash       = "invocation.crash"
	InvocationAllDrained  = "invocation.all_drained"
	VendorCooldownEntered = "vendor.cooldown.entered"
	VendorAuthLocked      = "vendor.auth_locked"
	VendorRecovered       = "vendor.recovered"

	// Reply / transcript.
	ReplyPosted        = "reply.posted"
	TranscriptError    = "transcript.error"
	MemorySeedWrote    = "memory.seed.wrote"
	MemorySeedExisting = "memory.seed.existing"
)
