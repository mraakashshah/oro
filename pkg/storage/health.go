package storage

// DevCleanupHealth is the catalog-derived health projection for scheduled
// developer-tool cache maintenance.
type DevCleanupHealth struct {
	LastAttempt      string                     `json:"last_attempt,omitempty"`
	LastSuccess      string                     `json:"last_success,omitempty"`
	NextDue          string                     `json:"next_due,omitempty"`
	OverdueBySeconds int64                      `json:"overdue_by_seconds,omitempty"`
	FreedBytes       int64                      `json:"freed_bytes,omitempty"`
	Providers        []DevCleanupProviderResult `json:"providers,omitempty"`
	Pause            DevCleanupPauseStatus      `json:"pause"`
}

// DevCleanupProviderResult records the newest maintenance result for one
// provider in the scheduled developer-tool cleanup.
type DevCleanupProviderResult struct {
	ProviderID  string `json:"provider_id"`
	Status      string `json:"status"`
	AttemptedAt string `json:"attempted_at,omitempty"`
	FreedBytes  int64  `json:"freed_bytes,omitempty"`
	ExitCode    int    `json:"exit_code"`
}

// DevCleanupPauseStatus reports the latest catalogued admission pause and its
// drain acknowledgements.
type DevCleanupPauseStatus struct {
	Epoch                   int64      `json:"epoch,omitempty"`
	State                   PauseState `json:"state,omitempty"`
	Drained                 bool       `json:"drained,omitempty"`
	AcknowledgedControllers int        `json:"acknowledged_controllers,omitempty"`
}
