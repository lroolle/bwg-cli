package kiwivm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// KiwiVM error codes observed in the wild. The API answers HTTP 200
// and puts the real outcome in the "error" field, so these are the
// only reliable signal for branching.
const (
	// CodeOK is the success value of the "error" field.
	CodeOK = 0
	// CodeMissingParam is returned when a required parameter is absent,
	// including the veid itself.
	CodeMissingParam = 700001
	// CodeAuthFailure is returned for a bad veid/api_key pair. It does
	// NOT distinguish "wrong key" from "wrong veid" — both look the same.
	CodeAuthFailure = 700005
	// CodeLocked is returned while the VPS is busy with another task
	// (snapshot, reinstall, migration).
	CodeLocked = 788888
)

// APIError is a non-zero "error" field in a KiwiVM response.
type APIError struct {
	// Op is the endpoint that failed.
	Op string `json:"op"`
	// Code is the value of the response's "error" field.
	Code int `json:"code"`
	// Message is KiwiVM's human-readable detail, when present.
	Message string `json:"message,omitempty"`
	// Additional is the response's additionalErrorInfo field.
	Additional string `json:"additionalErrorInfo,omitempty"`
	// Locking carries task progress when the VPS is locked.
	Locking *LockingInfo `json:"additionalLockingInfo,omitempty"`
}

// LockingInfo reports progress of the task currently holding the VPS.
type LockingInfo struct {
	LastStatusUpdateSecondsAgo int    `json:"last_status_update_s_ago"`
	CompletedPercent           int    `json:"completed_percent"`
	FriendlyProgressMessage    string `json:"friendly_progress_message"`
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: KiwiVM error %d", e.Op, e.Code)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Additional != "" {
		fmt.Fprintf(&b, " (%s)", e.Additional)
	}
	if e.Locking != nil && e.Locking.FriendlyProgressMessage != "" {
		fmt.Fprintf(&b, " — in progress: %s, %d%% complete",
			e.Locking.FriendlyProgressMessage, e.Locking.CompletedPercent)
	}
	return b.String()
}

// TransportError is a failure to get a usable answer out of the API:
// a dial error, a timeout, a 5xx, or a body that is not JSON. It says
// nothing about whether the credentials are valid.
type TransportError struct {
	Op     string
	Status int // 0 when the request never completed
	Err    error
}

func (e *TransportError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: HTTP %d from KiwiVM: %v", e.Op, e.Status, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// ReadOnlyError is returned instead of performing a non-read operation
// on a client built with [ReadOnly]. No HTTP request is made.
type ReadOnlyError struct {
	Op Op
}

func (e *ReadOnlyError) Error() string {
	return fmt.Sprintf("%s is a %s operation and this client is read-only",
		e.Op.Endpoint, e.Op.Risk)
}

// ErrReadOnly matches any [ReadOnlyError] via errors.Is.
var ErrReadOnly = errors.New("client is read-only")

// Is lets errors.Is(err, ErrReadOnly) match a *ReadOnlyError.
func (e *ReadOnlyError) Is(target error) bool { return target == ErrReadOnly }

// IsAuth reports whether err is KiwiVM rejecting the credentials.
// Note that KiwiVM returns the same code for a wrong api_key and a
// wrong veid, so a true result means "this pair does not work",
// not "this key is bad".
func IsAuth(err error) bool { return codeIs(err, CodeAuthFailure) }

// IsLocked reports whether the VPS is busy with another task. The
// error's Locking field carries progress when KiwiVM supplies it.
func IsLocked(err error) bool { return codeIs(err, CodeLocked) }

// IsMissingParam reports whether a required parameter was absent.
func IsMissingParam(err error) bool { return codeIs(err, CodeMissingParam) }

// IsRateLimited reports whether KiwiVM dropped the request for rate
// pressure. KiwiVM signals this with HTTP 429 rather than an error
// code; call [Client.RateLimitStatus] to see the remaining budget.
func IsRateLimited(err error) bool {
	var t *TransportError
	return errors.As(err, &t) && t.Status == http.StatusTooManyRequests
}

// IsTransient reports whether err is worth retrying unchanged.
// Rate limiting counts: the budget refills on its own.
func IsTransient(err error) bool {
	var t *TransportError
	if errors.As(err, &t) {
		return t.Status == 0 || t.Status >= 500 || t.Status == http.StatusTooManyRequests
	}
	return false
}

// IsReadOnly reports whether err is a read-only refusal.
func IsReadOnly(err error) bool { return errors.Is(err, ErrReadOnly) }

// APIErrorFrom extracts the *APIError from err, if there is one.
func APIErrorFrom(err error) (*APIError, bool) {
	var e *APIError
	ok := errors.As(err, &e)
	return e, ok
}

func codeIs(err error, code int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Code == code
}
