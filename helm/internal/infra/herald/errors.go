package herald

import "errors"

// Sentinel errors for herald communication failures.
// Use errors.Is to distinguish the three failure categories.
//
//	errors.Is(err, ErrUnavailable) — NATS transport failure: herald not running,
//	  no responders on subject, connection closed, or request timeout.
//	  Transient — the hand may still be created but will lack signals until re-registration.
//
//	errors.Is(err, ErrRejected) — herald is up and responded ok:false.
//	  The request itself is invalid: bad script, unknown symbol, unsupported TF, etc.
//	  Permanent — fix the request before retrying.
var (
	ErrUnavailable = errors.New("herald unavailable")
	ErrRejected    = errors.New("herald rejected")
)
