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
	"errors"
	"fmt"
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

// HostSpec names a fleet host plus the per-host knobs the workflow needs.
type HostSpec struct {
	Name     string `json:"name"`
	Arch     string `json:"arch"`      // "linux-x86_64" | "linux-aarch64" | "darwin-arm64"
	Unit     string `json:"unit"`      // systemd unit, e.g. "acc-agent.service"
	UnitUser bool   `json:"unit_user"` // true → systemctl --user; false → sudo systemctl
	BinPath  string `json:"bin_path"`  // install path, e.g. /home/jkh/.acc/bin/acc-agent
}

// RolloutInput names the manifest and target hosts.
type RolloutInput struct {
	Component string          `json:"component"`
	Version   string          `json:"version"`
	Hosts     []HostSpec      `json:"hosts"`
	Strategy  RolloutStrategy `json:"strategy"`
	// BakeSeconds is the soak time after the canary host upgrades before the
	// cohort starts. Ignored for non-canary strategies.
	BakeSeconds int `json:"bake_seconds"`
	// HealthTimeoutSeconds caps how long WaitForHealth waits for the agent
	// to report the new ccc_version. Defaults to 300s when zero.
	HealthTimeoutSeconds int `json:"health_timeout_seconds"`
	// ArchSHA maps arch token (e.g. "linux-x86_64") → SHA-256 of the binary
	// for that arch. Comes from the signed manifest in the artifact store.
	// Mandatory: the workflow refuses to run if any host's arch is missing.
	ArchSHA map[string]string `json:"arch_sha"`
}

// HealthSignal is the signal a host's helper sends after install + restart
// (currently unused — health is polled, not signaled, in the MVP).
const HealthSignal = "fleet.health"

// HostOutcome captures what happened on a single host.
type HostOutcome struct {
	Host      string `json:"host"`
	Skipped   bool   `json:"skipped"`   // already at target
	Installed bool   `json:"installed"` // new binary written
	Healthy   bool   `json:"healthy"`
	Reason    string `json:"reason,omitempty"`
}

// RolloutResult extends Result with per-host outcomes.
type RolloutResult struct {
	Result
	Hosts []HostOutcome `json:"hosts"`
}

// RolloutWorkflow rolls a component+version across a set of hosts using the
// selected strategy. Per host: Preflight → (DownloadArtifact → InstallBinary
// → RestartService when not already at target) → WaitForHealth. Replayable:
// re-running on a converged fleet hits AlreadyAtTarget for every host and
// becomes a no-op on the install side while still verifying health.
func RolloutWorkflow(ctx workflow.Context, in RolloutInput) (RolloutResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("RolloutWorkflow start",
		"component", in.Component, "version", in.Version,
		"hosts", hostNames(in.Hosts), "strategy", in.Strategy,
		"bake_seconds", in.BakeSeconds)

	if err := validateRolloutInput(in); err != nil {
		return RolloutResult{}, err
	}

	groups, err := groupHosts(in)
	if err != nil {
		return RolloutResult{}, err
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		HeartbeatTimeout:    30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    1 * time.Minute,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var outcomes []HostOutcome
	for groupIdx, group := range groups {
		// Run hosts in this group concurrently.
		futures := make([]workflow.Future, 0, len(group))
		for _, host := range group {
			host := host
			f := workflow.ExecuteActivity(ctx, "RolloutHost", rolloutHostInput{
				Host:                 host,
				Component:            in.Component,
				Version:              in.Version,
				ArchSHA:              in.ArchSHA,
				HealthTimeoutSeconds: in.HealthTimeoutSeconds,
			})
			futures = append(futures, f)
		}
		for i, f := range futures {
			var out HostOutcome
			if err := f.Get(ctx, &out); err != nil {
				outcomes = append(outcomes, HostOutcome{
					Host: group[i].Name, Reason: err.Error(),
				})
				return RolloutResult{
					Result: Result{
						Workflow: "RolloutWorkflow",
						Status:   "failed",
						Note:     "host activity failed; halting before subsequent groups",
					},
					Hosts: outcomes,
				}, err
			}
			outcomes = append(outcomes, out)
		}
		// Canary bake between group 0 (canary) and group 1 (cohort).
		if in.Strategy == RolloutCanary && groupIdx == 0 && len(groups) > 1 && in.BakeSeconds > 0 {
			logger.Info("canary baked; sleeping before cohort",
				"bake_seconds", in.BakeSeconds)
			if err := workflow.Sleep(ctx, time.Duration(in.BakeSeconds)*time.Second); err != nil {
				return RolloutResult{}, err
			}
		}
	}

	return RolloutResult{
		Result: Result{Workflow: "RolloutWorkflow", Status: "ok"},
		Hosts:  outcomes,
	}, nil
}

func validateRolloutInput(in RolloutInput) error {
	if in.Component == "" {
		return errors.New("component required")
	}
	if in.Version == "" {
		return errors.New("version required")
	}
	if len(in.Hosts) == 0 {
		return errors.New("at least one host required")
	}
	if len(in.ArchSHA) == 0 {
		return errors.New("arch_sha map required (from signed manifest)")
	}
	for _, h := range in.Hosts {
		if h.Name == "" || h.Arch == "" || h.Unit == "" || h.BinPath == "" {
			return fmt.Errorf("incomplete HostSpec: %+v", h)
		}
		if _, ok := in.ArchSHA[h.Arch]; !ok {
			return fmt.Errorf("no manifest entry for arch %q (host %s)", h.Arch, h.Name)
		}
	}
	switch in.Strategy {
	case RolloutCanary, RolloutCohort, RolloutSerial:
	default:
		return fmt.Errorf("unknown strategy: %q", in.Strategy)
	}
	return nil
}

func groupHosts(in RolloutInput) ([][]HostSpec, error) {
	switch in.Strategy {
	case RolloutCanary:
		if len(in.Hosts) == 1 {
			return [][]HostSpec{in.Hosts}, nil
		}
		return [][]HostSpec{{in.Hosts[0]}, in.Hosts[1:]}, nil
	case RolloutCohort:
		return [][]HostSpec{in.Hosts}, nil
	case RolloutSerial:
		groups := make([][]HostSpec, 0, len(in.Hosts))
		for _, h := range in.Hosts {
			groups = append(groups, []HostSpec{h})
		}
		return groups, nil
	}
	return nil, fmt.Errorf("unknown strategy: %q", in.Strategy)
}

func hostNames(hosts []HostSpec) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Name
	}
	return out
}

// rolloutHostInput carries all per-host context across activity boundaries.
type rolloutHostInput struct {
	Host                 HostSpec
	Component            string
	Version              string
	ArchSHA              map[string]string
	HealthTimeoutSeconds int
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
