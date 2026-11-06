package protocol

import "fmt"

// Error codes of dzcollab 1.
const (
	ProtocolUnsupported = "protocol_unsupported"
	ClientOutdated      = "client_outdated"
	AuthRequired        = "auth_required"
	TokenInvalid        = "token_invalid"
	TokenExpired        = "token_expired"
	NotAllowed          = "not_allowed"
	SessionUnknown      = "session_unknown"
	SessionEnded        = "session_ended"
	SessionFull         = "session_full"
	NotParticipant      = "not_participant"
	InstanceKeyTaken    = "instance_key_taken"
	SnapshotNotReady    = "snapshot_not_ready"
	SnapshotUnknown     = "snapshot_unknown"
	ClientSeqGap        = "client_seq_gap"
	BatchPending        = "batch_pending"
	CursorTooOld        = "cursor_too_old"
	TooLarge            = "too_large"
	Throttled           = "throttled"
	InviteInvalid       = "invite_invalid"
	InviteExpired       = "invite_expired"
	InviteUsed          = "invite_used"
	AlreadyRunning      = "already_running"
	StoreChanged        = "store_changed"
	NotConfirmed        = "not_confirmed"
	ProjectUnknown      = "project_unknown"
	RevisionUnknown     = "revision_unknown"
	ChecksumMismatch    = "checksum_mismatch"
	AttachmentUnknown   = "attachment_unknown"
	CapabilityMissing   = "capability_missing"
	Busy                = "busy"
	BadRequest          = "bad_request"
	Internal            = "internal"
)

// Error is a protocol error: an answer with ok false.
type Error struct {
	Code    string
	Message string
	Extra   *Obj
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

// Body is the answer for this error.
func (e *Error) Body() *Obj {
	errObj := O("code", e.Code)
	if e.Message != "" {
		errObj.Set("message", e.Message)
	}
	if e.Extra != nil {
		for _, k := range e.Extra.keys {
			errObj.Set(k, e.Extra.vals[k])
		}
	}
	return O("ok", false, "error", errObj)
}

func NewError(code, message string, extra ...any) *Error {
	e := &Error{Code: code, Message: message}
	if len(extra) > 0 {
		e.Extra = O(extra...)
	}
	return e
}

// Fail aborts the current request with a protocol error. Dispatch recovers it into an answer.
func Fail(code, message string, extra ...any) {
	panic(NewError(code, message, extra...))
}

func Failf(code, format string, args ...any) {
	panic(NewError(code, fmt.Sprintf(format, args...)))
}

func BadRequestf(format string, args ...any) {
	panic(NewError(BadRequest, fmt.Sprintf(format, args...)))
}
