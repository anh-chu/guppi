package state

import "time"

// Command policy constants are defined in one location so command receipt age,
// count bounds, and create retry behavior stay consistent across runtime and
// tests. They intentionally avoid generic job abstractions.
const (
	// MaxCommandReceiptAge is how long a command receipt may be considered
	// live before workers must re-validate the intent.
	MaxCommandReceiptAge = 5 * time.Minute

	// MaxPendingCommands bounds the command backlog per owner.
	MaxPendingCommands = 128

	// MaxPendingCreates limits how many unconfirmed create intents may be
	// outstanding for one owner at a time.
	MaxPendingCreates = 32

	// CreateRetryInitial is the first backoff delay for a failed create intent.
	CreateRetryInitial = 200 * time.Millisecond

	// CreateRetryMax caps the per-attempt backoff for create retries.
	CreateRetryMax = 30 * time.Second

	// MaxCreateRetries is the hard ceiling on retry attempts for a single
	// create intent. Beyond this the intent is rejected and must be re-issued
	// by the user.
	MaxCreateRetries = 5
)
