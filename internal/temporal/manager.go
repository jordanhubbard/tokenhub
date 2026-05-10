package temporal

import (
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Config holds Temporal connection settings.
type Config struct {
	HostPort  string
	Namespace string
	TaskQueue string
}

// Manager owns the Temporal client and worker lifecycle.
type Manager struct {
	client client.Client
	worker worker.Worker
	cfg    Config
}

// New creates a Temporal client and worker, registering all workflows and activities.
func New(cfg Config, acts *Activities) (*Manager, error) {
	c, err := client.Dial(client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("temporal client dial: %w", err)
	}

	w := worker.New(c, cfg.TaskQueue, worker.Options{})

	// Register workflows.
	w.RegisterWorkflow(ChatWorkflow)
	w.RegisterWorkflow(OrchestrationWorkflow)
	w.RegisterWorkflow(StreamLogWorkflow)

	// Register activities.
	w.RegisterActivity(acts.SelectModel)
	w.RegisterActivity(acts.SendToProvider)
	w.RegisterActivity(acts.ClassifyAndEscalate)
	w.RegisterActivity(acts.LogResult)
	w.RegisterActivity(acts.ResolveModel)
	w.RegisterActivity(acts.StreamSelectModel)
	w.RegisterActivity(acts.StreamLogResult)

	return &Manager{
		client: c,
		worker: w,
		cfg:    cfg,
	}, nil
}

// Start begins the worker polling for tasks.
func (m *Manager) Start() error {
	return m.worker.Start()
}

// Client returns the Temporal client for starting workflows.
func (m *Manager) Client() client.Client {
	return m.client
}

// TaskQueue returns the configured task queue name.
func (m *Manager) TaskQueue() string {
	return m.cfg.TaskQueue
}

// Stop gracefully stops the worker and closes the client.
func (m *Manager) Stop() {
	if m.worker != nil {
		m.worker.Stop()
	}
	if m.client != nil {
		m.client.Close()
	}
}
