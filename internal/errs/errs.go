// Package errs provides the shared error classification used by every
// subsystem, so the UI can render an actionable message instead of a raw
// errno.
package errs

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// Kind classifies a failure.
type Kind string

// Error kinds.
const (
	KindUnauthorized Kind = "unauthorized"
	KindPermission   Kind = "permission_denied"
	KindSIP          Kind = "sip_protected"
	KindProcessGone  Kind = "process_gone"
	KindFileBusy     Kind = "file_busy"
	KindNotFound     Kind = "not_found"
	KindInvalid      Kind = "invalid_request"
	KindUnsupported  Kind = "unsupported"
	KindInternal     Kind = "internal"
)

// Error carries a kind plus the original cause.
type Error struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Message
	}
	return e.Message + ": " + e.Detail
}

// New builds a classified error.
func New(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// Wrap classifies err and appends context.
func Wrap(err error, kind Kind, format string, args ...any) *Error {
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf(format, args...)
	return &Error{Kind: kind, Message: msg, Detail: err.Error()}
}

// KindOf returns the classification of err, defaulting to KindInternal.
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return Classify(err)
}

// Classify inspects a raw error and assigns a kind.
func Classify(err error) Kind {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return KindNotFound
	case errors.Is(err, os.ErrPermission):
		return KindPermission
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return KindPermission
	case errors.Is(err, syscall.ESRCH):
		return KindProcessGone
	case errors.Is(err, syscall.ENOENT):
		return KindNotFound
	case errors.Is(err, syscall.EBUSY):
		return KindFileBusy
	case errors.Is(err, os.ErrExist):
		return KindFileBusy
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "operation not permitted"):
		return KindSIP
	case strings.Contains(s, "permission denied"):
		return KindPermission
	case strings.Contains(s, "not permitted"):
		return KindPermission
	case strings.Contains(s, "no such file"):
		return KindNotFound
	case strings.Contains(s, "resource busy"):
		return KindFileBusy
	}
	return KindInternal
}

// As extracts the *Error from err, converting unknown errors as needed.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Kind: Classify(err), Message: err.Error(), Detail: err.Error()}
}
