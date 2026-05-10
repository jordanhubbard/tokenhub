// Package fleet_orchestrator hosts the Temporal workflows that run the ACC
// fleet rollout pipeline. See docs in github.com/jordanhubbard/ACC at
// docs/soul-code-boundary.md and bead ACC-jq9z (Temporal-orchestrated fleet
// rollout with soul/code separation).
//
// MVP scope (ACC-etxq): four workflows are registered as STUBS so the worker
// can be stood up and the round-trip plumbing (Temporal cluster ↔ tokenhub
// worker ↔ acc-server artifact store) can be validated end-to-end. The real
// workflow bodies land in:
//
//   - BuildArtifactWorkflow:   ACC-2r4t (artifact store, MERGED) +
//                              follow-on activity wiring
//   - RolloutWorkflow:         ACC-zmj6
//   - SoulPersistenceWorkflow: ACC-j2ft
//   - SelfDevSubmitWorkflow:   ACC-ske4
//
// Today every stub returns a minimal Result indicating it ran. Workflow IDs
// and signal handlers are present so callers can already start workflows and
// observe them via the Temporal UI; the actual side-effects (build, install,
// snapshot) are filled in by the dependent beads.
package fleet_orchestrator

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TaskQueue is the Temporal task queue this worker pool serves. Distinct from
// tokenhub's chat task queue so traffic and retry policies don't bleed across.
const TaskQueue = "fleet-orchestrator"

// Result is the common stub return shape until the real workflow bodies land.
type Result struct {
	Workflow string `json:"workflow"`
	Status   string `json:"status"`
	Note     string `json:"note,omitempty"`
}

func stubResult(name string) Result {
	return Result{
		Workflow: name,
		Status:   "stub",
		Note:     "ACC-etxq scaffolding — real implementation lands in the dependent bead",
	}
}

// ── BuildArtifactWorkflow ────────────────────────────────────────────────────

// BuildArtifactInput names the source ref to build.
type BuildArtifactInput struct {
	Component string   `json:"component"`
	Ref       string   `json:"ref"`
	Arches    []string `json:"arches"`
}

// BuildArtifactWorkflow drives a per-arch build of one component, signs a
// manifest, and uploads to the acc-server artifact store. STUB.
func BuildArtifactWorkflow(ctx workflow.Context, in BuildArtifactInput) (Result, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("BuildArtifactWorkflow stub invoked",
		"component", in.Component, "ref", in.Ref, "arches", in.Arches)
	return stubResult("BuildArtifactWorkflow"), nil
}

// ── RolloutWorkflow ──────────────────────────────────────────────────────────

// RolloutStrategy controls how hosts are updated.
//
//   - "canary": one host first, bake, then the rest in cohort
//   - "cohort": all in parallel
//   - "serial": one at a time (useful for hubs)
type RolloutStrategy string

const (
	RolloutCanary RolloutStrategy = "canary"
	RolloutCohort RolloutStrategy = "cohort"
	RolloutSerial RolloutStrategy = "serial"
)

// RolloutInput names the manifest and target hosts.
type RolloutInput struct {
	Component   string          `json:"component"`
	Version     string          `json:"version"`
	Hosts       []string        `json:"hosts"`
	Strategy    RolloutStrategy `json:"strategy"`
	BakeSeconds int             `json:"bake_seconds"`
}

// HealthSignal is the signal a host's helper sends after install + restart.
const HealthSignal = "fleet.health"

// RolloutWorkflow per-host: download → preflight → atomic install → restart →
// wait for health signal with deadline → commit or rollback. Replayable. STUB.
func RolloutWorkflow(ctx workflow.Context, in RolloutInput) (Result, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("RolloutWorkflow stub invoked",
		"component", in.Component, "version", in.Version,
		"hosts", in.Hosts, "strategy", in.Strategy)
	// Placeholder: spec a tight retry policy so the future activities inherit it.
	_ = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    3,
		},
	})
	return stubResult("RolloutWorkflow"), nil
}

// ── SoulPersistenceWorkflow ──────────────────────────────────────────────────

// SoulPersistenceInput identifies the agent whose soul gets snapshotted.
type SoulPersistenceInput struct {
	AgentID string `json:"agent_id"`
}

// SoulPersistenceWorkflow snapshots an agent's soul (memory, transcripts,
// learned config) to the artifact store. STUB.
func SoulPersistenceWorkflow(ctx workflow.Context, in SoulPersistenceInput) (Result, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("SoulPersistenceWorkflow stub invoked", "agent_id", in.AgentID)
	return stubResult("SoulPersistenceWorkflow"), nil
}

// ── SelfDevSubmitWorkflow ────────────────────────────────────────────────────

// SelfDevSubmitInput names the candidate ref the agent wants to roll out.
type SelfDevSubmitInput struct {
	TaskID    string `json:"task_id"`
	Component string `json:"component"`
	Ref       string `json:"ref"`
	Submitter string `json:"submitter"` // agent name
}

// SelfDevSubmitWorkflow chains BuildArtifactWorkflow → RolloutWorkflow with
// canary group constrained to the submitter's host, then waits for an approve
// signal before promoting to the rest of the fleet. STUB.
func SelfDevSubmitWorkflow(ctx workflow.Context, in SelfDevSubmitInput) (Result, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("SelfDevSubmitWorkflow stub invoked",
		"task_id", in.TaskID, "component", in.Component,
		"ref", in.Ref, "submitter", in.Submitter)
	return stubResult("SelfDevSubmitWorkflow"), nil
}
