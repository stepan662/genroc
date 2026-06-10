package model

import "time"

// Status represents the lifecycle state of a process instance.
type Status string

const (
	StatusRunning    Status = "running"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusWaiting    Status = "waiting"
	StatusCancelling Status = "cancelling"
	StatusCancelled  Status = "cancelled"
)

// ProcessInstance is a single running execution of a ProcessDefinition.
// ProcessVersion is pinned at creation — process definition changes
// never affect existing instances.
type ProcessInstance struct {
	ID             string
	ProcessName    string
	ProcessVersion int

	// StepQueue holds the remaining steps to execute, serialized as JSON.
	// A switch goto replaces this slice with the target step and all steps after it.
	StepQueue []*Step

	// ContextData is the accumulated key/value state passed between steps.
	ContextData map[string]any

	// ParentID is set when this instance was started by a child_process step.
	// Empty string means this is a root instance.
	ParentID string

	// CallStack is the ordered list of ancestor instance IDs (root first).
	// Used for O(1) ancestor lookup during error cascade.
	CallStack []string

	RetryCount    int
	NextRetryAt   *time.Time
	Status        Status
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	WorkerID      *string
	LeaseExpiresAt *time.Time
}
