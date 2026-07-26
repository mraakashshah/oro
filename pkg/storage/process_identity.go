package storage

// ProcessIdentity is the immutable process metadata required to authorize an
// operation against a live lease owner. PID alone is never sufficient because
// the operating system can reuse it after the original owner exits.
type ProcessIdentity struct {
	PID          int
	StartMarker  string
	Executable   string
	ProcessGroup int
}

// Matches reports whether two observations identify the same process. All
// fields are compared so a stale identity cannot authorize a reused PID.
func (identity ProcessIdentity) Matches(other ProcessIdentity) bool {
	return identity.PID > 0 &&
		identity.PID == other.PID &&
		identity.StartMarker != "" &&
		identity.StartMarker == other.StartMarker &&
		identity.Executable != "" &&
		identity.Executable == other.Executable &&
		identity.ProcessGroup > 0 &&
		identity.ProcessGroup == other.ProcessGroup
}
