// Package domain owns the complete R → P/E iteration and its business invariants.
// It has no transport, database, model provider or execution-runtime dependencies.
package domain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Code uint8

const (
	Unknown Code = iota
	InvalidArgument
	PermissionDenied
	FailedPrecondition
	Unavailable
	NotFound
	Aborted
	ResourceExhausted
	Unauthenticated
	DeadlineExceeded
	Canceled
	AlreadyExists
	Internal
	OutOfRange
	Unimplemented
	DataLoss
)

type Failure struct {
	Code   Code
	Reason string
}

func (e *Failure) Error() string            { return e.Reason }
func Reject(code Code, reason string) error { return &Failure{Code: code, Reason: reason} }
func ErrorCode(err error) Code {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return Canceled
	}
	return Unknown
}
func Reason(err error) string {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Reason
	}
	return ""
}
func Invalid(reason string) error  { return Reject(InvalidArgument, reason) }
func Denied(reason string) error   { return Reject(PermissionDenied, reason) }
func Conflict(reason string) error { return Reject(FailedPrecondition, reason) }
func UnavailableError() error      { return Reject(Unavailable, "challenge_unavailable") }
func ValidID(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func Bounded(v string, n int) bool { return strings.TrimSpace(v) != "" && len(v) <= n }
func Terminal(s string) bool       { return s == "completed" || s == "failed" || s == "cancelled" }
func Hash(b []byte) string         { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func NewID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func TimePtr(t time.Time) *time.Time { return &t }

// TimeValue retains the original protocol's absent-timestamp value at the boundary.
func TimeValue(t *time.Time) time.Time {
	if t == nil {
		return time.Unix(0, 0).UTC()
	}
	return t.UTC()
}
func ValidTime(t *time.Time) bool {
	return t != nil && t.Year() >= 1 && t.Year() <= 9999
}

type WorkKind int32

const (
	WorkKind_WORK_KIND_UNSPECIFIED WorkKind = iota
	WorkKind_WORK_KIND_VALIDATE_RANKER
	WorkKind_WORK_KIND_TRAIN_RANKER
	WorkKind_WORK_KIND_REFINE_RANKER
	WorkKind_WORK_KIND_PACK_TASK
	WorkKind_WORK_KIND_EXECUTE_PACKAGE
	WorkKind_WORK_KIND_RANK_WORKS
	WorkKind_WORK_KIND_EXPLAIN_OWN_WORK
)

func (k WorkKind) String() string {
	names := [...]string{"WORK_KIND_UNSPECIFIED", "WORK_KIND_VALIDATE_RANKER", "WORK_KIND_TRAIN_RANKER", "WORK_KIND_REFINE_RANKER", "WORK_KIND_PACK_TASK", "WORK_KIND_EXECUTE_PACKAGE", "WORK_KIND_RANK_WORKS", "WORK_KIND_EXPLAIN_OWN_WORK"}
	if k < 0 || int(k) >= len(names) {
		return fmt.Sprint(int32(k))
	}
	return names[k]
}

// Finish revokes progression immediately; the application persists the close command
// and only acknowledges the terminal state after Content has closed registration.
func Finish(st *State, target, reason string) {
	st.Run.Status = "finalizing"
	if target == "cancelled" {
		st.Run.Status = "cancelling"
	}
	st.Run.FailureReason = reason
	st.TerminalTarget = target
	st.Pending = "close"
	st.Assignment = nil
	st.GrantHash = ""
	st.ExecutionInstanceId = ""
}
