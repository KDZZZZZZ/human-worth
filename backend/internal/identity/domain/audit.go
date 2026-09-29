package domain

type AuditEvent struct {
	ID, ActorID, Action, TargetID, Reason string
	AuthVersion                           int64
}
