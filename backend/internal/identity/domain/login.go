package domain

import "time"

type FlowStatus string

const (
	Pending    FlowStatus = "pending"
	Exchanging FlowStatus = "exchanging"
	Succeeded  FlowStatus = "succeeded"
	Failed     FlowStatus = "failed"
	Cancelled  FlowStatus = "cancelled"
	Expired    FlowStatus = "expired"
)

type LoginFamily struct{ ID string }
type FlowData struct {
	ID, FamilyID, Attempt string
	Status                FlowStatus
	ExpiresAt             time.Time
}
type LoginFlow struct{ data FlowData }

func RestoreFlow(data FlowData) (LoginFlow, error) {
	switch data.Status {
	case Pending, Exchanging, Succeeded, Failed, Cancelled, Expired:
	default:
		return LoginFlow{}, ErrInvalidFlow
	}
	if data.ID == "" || data.FamilyID == "" || data.ExpiresAt.IsZero() || (data.Status == Exchanging && data.Attempt == "") {
		return LoginFlow{}, ErrInvalidFlow
	}
	return LoginFlow{data: data}, nil
}
func (f LoginFlow) Snapshot() FlowData { return f.data }
func (f LoginFlow) CanClaim(now time.Time) error {
	if f.data.Status != Pending || !now.Before(f.data.ExpiresAt) {
		return ErrInvalidFlow
	}
	return nil
}
func (f *LoginFlow) Claim(attempt string, now time.Time) error {
	if err := f.CanClaim(now); err != nil {
		return err
	}
	if attempt == "" {
		return ErrInvalidFlow
	}
	f.data.Status, f.data.Attempt = Exchanging, attempt
	return nil
}
func (f *LoginFlow) RejectProvider(denied bool, now time.Time) error {
	if err := f.CanClaim(now); err != nil {
		return err
	}
	f.data.Status = Failed
	if denied {
		f.data.Status = Cancelled
	}
	return nil
}
func (f *LoginFlow) Cancel() bool {
	if f.data.Status != Pending && f.data.Status != Exchanging {
		return false
	}
	f.data.Status = Cancelled
	return true
}
func (f LoginFlow) CanComplete(attempt string, now time.Time) error {
	if f.data.Status != Exchanging || f.data.Attempt != attempt || !now.Before(f.data.ExpiresAt) {
		return ErrInvalidFlow
	}
	return nil
}
func (f *LoginFlow) Complete(attempt string, now time.Time) error {
	if err := f.CanComplete(attempt, now); err != nil {
		return err
	}
	f.data.Status = Succeeded
	return nil
}
func (f *LoginFlow) Fail(attempt string) bool {
	if f.data.Status != Exchanging || f.data.Attempt != attempt {
		return false
	}
	f.data.Status = Failed
	return true
}
