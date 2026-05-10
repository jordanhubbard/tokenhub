package fleet_orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// ── stub-workflow round-trip tests ──────────────────────────────────────────

func TestBuildArtifactWorkflow_Stub(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(BuildArtifactWorkflow)
	env.ExecuteWorkflow(BuildArtifactWorkflow, BuildArtifactInput{
		Component: "acc-agent", Ref: "abc1234",
		Arches: []string{"linux-x86_64"},
	})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatal("workflow failed")
	}
	var got Result
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.Workflow != "BuildArtifactWorkflow" || got.Status != "stub" {
		t.Errorf("unexpected stub result: %+v", got)
	}
}

func TestSoulPersistenceWorkflow_Stub(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(SoulPersistenceWorkflow)
	env.ExecuteWorkflow(SoulPersistenceWorkflow, SoulPersistenceInput{AgentID: "rocky"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatal("workflow failed")
	}
}

func TestSelfDevSubmitWorkflow_Stub(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(SelfDevSubmitWorkflow)
	env.ExecuteWorkflow(SelfDevSubmitWorkflow, SelfDevSubmitInput{
		TaskID: "t1", Component: "acc-agent",
		Ref: "abc", Submitter: "rocky",
	})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatal("workflow failed")
	}
}

func TestTaskQueueConstantIsStable(t *testing.T) {
	if TaskQueue != "fleet-orchestrator" {
		t.Errorf("TaskQueue constant changed unexpectedly to %q — it is part of the deploy contract; require coordinated env change before bumping", TaskQueue)
	}
}

// ── RolloutWorkflow tests ───────────────────────────────────────────────────

// mockRolloutHost is a controllable per-host activity used by the tests. It
// replaces the real activities.RolloutHost so the workflow logic can be
// exercised without ssh, an artifact store, or a real hub.
type mockRolloutHost struct {
	mu     sync.Mutex
	calls  []rolloutHostInput
	hostFn map[string]func(rolloutHostInput) (HostOutcome, error)
}

func newMockRolloutHost() *mockRolloutHost {
	return &mockRolloutHost{hostFn: map[string]func(rolloutHostInput) (HostOutcome, error){}}
}

func (m *mockRolloutHost) RolloutHost(ctx context.Context, in rolloutHostInput) (HostOutcome, error) {
	m.mu.Lock()
	m.calls = append(m.calls, in)
	fn := m.hostFn[in.Host.Name]
	m.mu.Unlock()
	if fn == nil {
		// default: succeed without install (already at target)
		return HostOutcome{Host: in.Host.Name, Skipped: true, Healthy: true}, nil
	}
	return fn(in)
}

func (m *mockRolloutHost) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockRolloutHost) callsByHost(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.Host.Name == name {
			n++
		}
	}
	return n
}

func sampleHosts() []HostSpec {
	return []HostSpec{
		{Name: "rocky", Arch: "linux-x86_64", Unit: "acc-agent.service",
			BinPath: "/home/jkh/.acc/bin/acc-agent"},
		{Name: "natasha", Arch: "linux-aarch64", Unit: "acc-agent.service",
			BinPath: "/home/jkh/.acc/bin/acc-agent"},
		{Name: "bullwinkle", Arch: "darwin-arm64", Unit: "acc-agent.service",
			UnitUser: true, BinPath: "/Users/jkh/.acc/bin/acc-agent"},
	}
}

func sampleArchSHA() map[string]string {
	return map[string]string{
		"linux-x86_64":  fakeSHA(0xa1),
		"linux-aarch64": fakeSHA(0xa2),
		"darwin-arm64":  fakeSHA(0xa3),
	}
}

func fakeSHA(seed byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		if i%2 == 0 {
			out[i] = hex[seed>>4]
		} else {
			out[i] = hex[seed&0x0f]
		}
	}
	return string(out)
}

func registerMock(env *testsuite.TestActivityEnvironment, m *mockRolloutHost) {
	env.RegisterActivityWithOptions(m.RolloutHost, activity.RegisterOptions{Name: "RolloutHost"})
}

func registerMockWF(env *testsuite.TestWorkflowEnvironment, m *mockRolloutHost) {
	env.RegisterActivityWithOptions(m.RolloutHost, activity.RegisterOptions{Name: "RolloutHost"})
}

func TestRolloutWorkflow_AlreadyConverged_NoInstallActivities(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RolloutWorkflow)
	mock := newMockRolloutHost()
	for _, h := range sampleHosts() {
		name := h.Name
		mock.hostFn[name] = func(_ rolloutHostInput) (HostOutcome, error) {
			return HostOutcome{Host: name, Skipped: true, Healthy: true}, nil
		}
	}
	registerMockWF(env, mock)

	env.ExecuteWorkflow(RolloutWorkflow, RolloutInput{
		Component: "acc-agent", Version: "v1",
		Hosts: sampleHosts(), Strategy: RolloutCanary,
		BakeSeconds: 0, ArchSHA: sampleArchSHA(),
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got RolloutResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" {
		t.Errorf("status: got %q want ok", got.Status)
	}
	if len(got.Hosts) != 3 {
		t.Errorf("expected 3 host outcomes, got %d", len(got.Hosts))
	}
	for _, o := range got.Hosts {
		if !o.Skipped || !o.Healthy {
			t.Errorf("expected skipped+healthy for %s, got %+v", o.Host, o)
		}
	}
	// 3 hosts × 1 RolloutHost call each.
	if mock.callCount() != 3 {
		t.Errorf("expected 3 RolloutHost calls, got %d", mock.callCount())
	}
}

func TestRolloutWorkflow_CanaryUpgradesThenCohort(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RolloutWorkflow)
	mock := newMockRolloutHost()
	for _, h := range sampleHosts() {
		name := h.Name
		_ = name
		mock.hostFn[h.Name] = func(in rolloutHostInput) (HostOutcome, error) {
			return HostOutcome{Host: in.Host.Name, Installed: true, Healthy: true}, nil
		}
	}
	registerMockWF(env, mock)

	env.ExecuteWorkflow(RolloutWorkflow, RolloutInput{
		Component: "acc-agent", Version: "v2",
		Hosts: sampleHosts(), Strategy: RolloutCanary,
		BakeSeconds: 60, ArchSHA: sampleArchSHA(),
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("error: %v", err)
	}
	var got RolloutResult
	_ = env.GetWorkflowResult(&got)
	if len(got.Hosts) != 3 || got.Status != "ok" {
		t.Errorf("unexpected result: %+v", got)
	}
	for _, o := range got.Hosts {
		if !o.Installed || !o.Healthy {
			t.Errorf("expected installed+healthy for %s, got %+v", o.Host, o)
		}
	}
}

func TestRolloutWorkflow_CanaryHealthFails_StopsBeforeCohort(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RolloutWorkflow)
	mock := newMockRolloutHost()
	mock.hostFn["rocky"] = func(in rolloutHostInput) (HostOutcome, error) {
		return HostOutcome{Host: "rocky"}, errors.New("health timeout")
	}
	mock.hostFn["natasha"] = func(in rolloutHostInput) (HostOutcome, error) {
		t.Errorf("natasha should not run after canary fails")
		return HostOutcome{}, nil
	}
	mock.hostFn["bullwinkle"] = mock.hostFn["natasha"]
	registerMockWF(env, mock)

	env.ExecuteWorkflow(RolloutWorkflow, RolloutInput{
		Component: "acc-agent", Version: "v3",
		Hosts: sampleHosts(), Strategy: RolloutCanary,
		BakeSeconds: 30, ArchSHA: sampleArchSHA(),
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error")
	}
	if mock.callsByHost("natasha") != 0 || mock.callsByHost("bullwinkle") != 0 {
		t.Errorf("cohort hosts ran despite canary failure")
	}
}

func TestRolloutWorkflow_SerialRunsOneAtATime(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RolloutWorkflow)
	mock := newMockRolloutHost()
	for _, h := range sampleHosts() {
		mock.hostFn[h.Name] = func(in rolloutHostInput) (HostOutcome, error) {
			return HostOutcome{Host: in.Host.Name, Installed: true, Healthy: true}, nil
		}
	}
	registerMockWF(env, mock)
	env.ExecuteWorkflow(RolloutWorkflow, RolloutInput{
		Component: "acc-agent", Version: "v4",
		Hosts: sampleHosts(), Strategy: RolloutSerial,
		ArchSHA: sampleArchSHA(),
	})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow failed: %v", env.GetWorkflowError())
	}
	if mock.callCount() != 3 {
		t.Errorf("expected 3 calls, got %d", mock.callCount())
	}
}

func TestRolloutWorkflow_ValidatesInput(t *testing.T) {
	cases := []struct {
		name string
		in   RolloutInput
		want string
	}{
		{"missing component", RolloutInput{Version: "v", Hosts: sampleHosts(), Strategy: RolloutCohort, ArchSHA: sampleArchSHA()}, "component required"},
		{"missing version", RolloutInput{Component: "c", Hosts: sampleHosts(), Strategy: RolloutCohort, ArchSHA: sampleArchSHA()}, "version required"},
		{"empty hosts", RolloutInput{Component: "c", Version: "v", Strategy: RolloutCohort, ArchSHA: sampleArchSHA()}, "host"},
		{"missing arch_sha", RolloutInput{Component: "c", Version: "v", Hosts: sampleHosts(), Strategy: RolloutCohort}, "arch_sha"},
		{"unknown strategy", RolloutInput{Component: "c", Version: "v", Hosts: sampleHosts(), Strategy: RolloutStrategy("bogus"), ArchSHA: sampleArchSHA()}, "strategy"},
		{"missing arch entry", RolloutInput{Component: "c", Version: "v", Hosts: sampleHosts(), Strategy: RolloutCohort, ArchSHA: map[string]string{"linux-x86_64": fakeSHA(1)}}, "no manifest entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			suite := &testsuite.WorkflowTestSuite{}
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterWorkflow(RolloutWorkflow)
			mock := newMockRolloutHost()
			registerMockWF(env, mock)
			env.ExecuteWorkflow(RolloutWorkflow, tc.in)
			if !env.IsWorkflowCompleted() {
				t.Fatal("did not complete")
			}
			err := env.GetWorkflowError()
			if err == nil {
				t.Fatalf("expected validation error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Quick sanity that grouping is correct.
func TestGroupHosts_Cases(t *testing.T) {
	hosts := sampleHosts()
	cases := map[RolloutStrategy]int{
		RolloutCanary: 2, // canary + cohort
		RolloutCohort: 1,
		RolloutSerial: 3, // one per host
	}
	for s, want := range cases {
		groups, err := groupHosts(RolloutInput{Hosts: hosts, Strategy: s})
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if len(groups) != want {
			t.Errorf("strategy %s: got %d groups, want %d", s, len(groups), want)
		}
	}
	// Canary with one host: degrades to a single group.
	groups, _ := groupHosts(RolloutInput{Hosts: hosts[:1], Strategy: RolloutCanary})
	if len(groups) != 1 {
		t.Errorf("single-host canary: expected 1 group, got %d", len(groups))
	}
}
