package models

import "time"

// contexts:
//   - name:
//     server:
//     user:
//
// current-context:
// prompts-enabled: false
// servers:
//   - grpc-web-root-path: /
//     server:
//
// users:
//   - auth-token:
//     name:
type Cluster struct {
	ContextName     string
	Server          string
	Insecure        bool
	AuthToken       string
	GRPCWeb         bool
	GRPCWebRootPath string

	ServerVersion string
	ServerMajor   int
}

// AppKey is cross-cluster identifier used for grouping.
type AppKey struct {
	Name string
}

// AppRef identifies one application inside a single Argo CD instance.
type AppRef struct {
	Name      string
	Namespace string
}

type Application struct {
	Key       AppKey
	Project   string
	Namespace string

	// application.status.sync.status ("Synced", "OutOfSync").
	SyncStatus string

	// application.status.health.status ("Healthy", "Degraded", "Progressing").
	HealthStatus string

	// application.status.operationState.phase ("Running", "Succeeded", "Failed").
	OperationPhase string

	// application.status.operationState.message.
	OperationMessage string

	// application.status.resources (kind, namespace, name, group).
	Resources []SyncResource
}

// SyncResource identifies a single Kubernetes resource for partial sync.
// It mirrors argocd's SyncOperationResource fields.
type SyncResource struct {
	Group     string
	Kind      string
	Name      string
	Namespace string
}

// Inventory contains all discovered apps grouped by AppKey.
type Inventory map[AppKey]map[string]Application

type Action string

const (
	ActionSync        Action = "sync"
	ActionRefresh     Action = "refresh"
	ActionHardRefresh Action = "hard-refresh"
)

type RunOptions struct {
	// PreRefresh triggers a Refresh before Sync actions.
	PreRefresh bool
	// PreHardRefresh triggers a Hard Refresh before Sync actions.
	PreHardRefresh bool
	// Prune enables prune for sync.
	Prune  bool
	DryRun bool
	// ApplyOnly switches sync strategy to "apply".
	ApplyOnly bool
	// Force enables "force apply".
	Force bool
	// Wait makes sync wait until operation completes.
	Wait bool
	// WaitHealthy makes sync wait for health=Healthy.
	WaitHealthy  bool
	WaitTimeout  time.Duration
	PollInterval time.Duration

	// Resources limits sync to a subset of application resources.
	// If empty, Argo CD will sync all resources (default behavior).
	Resources []SyncResource
}

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskSuccess   TaskStatus = "success"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

type Target struct {
	ClusterContext string
	App            AppKey
}

// ProgressEvent streams execution progress to the UI.
type ProgressEvent struct {
	At     time.Time
	Target Target

	Phase  TaskStatus
	Action Action

	Message string
	Err     error
}

type Result struct {
	Target Target
	Action Action
	Status TaskStatus
	Err    error
}
