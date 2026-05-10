package temporal

import (
	"fmt"

	"github.com/jordanhubbard/tokenhub/internal/fleet_orchestrator"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Config holds Temporal connection settings.
//
// `TaskQueue` is the chat-routing task queue tokenhub has always used.
// `FleetTaskQueue`, when non-empty, registers a second worker on that queue
// hosting the ACC fleet rollout workflows (ACC-jq9z). It is independent so
// chat retries and fleet operations don't share retry/budget policies.
type Config struct {
	HostPort       string
	Namespace      string
	TaskQueue      string
	FleetTaskQueue string
}

// Manager owns the Temporal client and the worker pool.
type Manager struct {
	client       client.Client
	chatWorker   worker.Worker
	fleetWorker  worker.Worker
	cfg          Config
}

// New creates a Temporal client and the chat worker. If cfg.FleetTaskQueue is
// non-empty, a second worker is registered on that queue with the fleet
// orchestrator workflows (BuildArtifact / Rollout / SoulPersistence /
// SelfDevSubmit).
func New(cfg Config, acts *Activities) (*Manager, error) {
	c, err := client.Dial(client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("temporal client dial: %w", err)
	}

	w := worker.New(c, cfg.TaskQueue, worker.Options{})

	// Register chat workflows.
	w.RegisterWorkflow(ChatWorkflow)
	w.RegisterWorkflow(OrchestrationWorkflow)
	w.RegisterWorkflow(StreamLogWorkflow)

	// Register chat activities.
	w.RegisterActivity(acts.SelectModel)
	w.RegisterActivity(acts.SendToProvider)
	w.RegisterActivity(acts.ClassifyAndEscalate)
	w.RegisterActivity(acts.LogResult)
	w.RegisterActivity(acts.ResolveModel)
	w.RegisterActivity(acts.StreamSelectModel)
	w.RegisterActivity(acts.StreamLogResult)

	m := &Manager{
		client:     c,
		chatWorker: w,
		cfg:        cfg,
	}

	if cfg.FleetTaskQueue != "" {
		fw := worker.New(c, cfg.FleetTaskQueue, worker.Options{})
		fw.RegisterWorkflow(fleet_orchestrator.BuildArtifactWorkflow)
		fw.RegisterWorkflow(fleet_orchestrator.RolloutWorkflow)
		fw.RegisterWorkflow(fleet_orchestrator.SoulPersistenceWorkflow)
		fw.RegisterWorkflow(fleet_orchestrator.SelfDevSubmitWorkflow)
		m.fleetWorker = fw
	}

	return m, nil
}

// Start begins each registered worker polling for tasks.
func (m *Manager) Start() error {
	if err := m.chatWorker.Start(); err != nil {
		return fmt.Errorf("chat worker start: %w", err)
	}
	if m.fleetWorker != nil {
		if err := m.fleetWorker.Start(); err != nil {
			return fmt.Errorf("fleet worker start: %w", err)
		}
	}
	return nil
}

// Client returns the Temporal client for starting workflows.
func (m *Manager) Client() client.Client {
	return m.client
}

// TaskQueue returns the chat task queue name.
func (m *Manager) TaskQueue() string {
	return m.cfg.TaskQueue
}

// FleetTaskQueue returns the fleet orchestrator task queue name (or "").
func (m *Manager) FleetTaskQueue() string {
	return m.cfg.FleetTaskQueue
}

// HasFleetWorker reports whether a fleet-orchestrator worker is running.
// Used by the /fleet/orchestrator/health endpoint to surface the worker pool
// state to operators (ACC-etxq acceptance criteria).
func (m *Manager) HasFleetWorker() bool {
	return m.fleetWorker != nil
}

// Stop gracefully stops every worker and closes the client.
func (m *Manager) Stop() {
	if m.chatWorker != nil {
		m.chatWorker.Stop()
	}
	if m.fleetWorker != nil {
		m.fleetWorker.Stop()
	}
	if m.client != nil {
		m.client.Close()
	}
}
