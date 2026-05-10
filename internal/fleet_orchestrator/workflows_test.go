package fleet_orchestrator

import (
	"testing"

	"go.temporal.io/sdk/testsuite"
)

func TestBuildArtifactWorkflow_Stub(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(BuildArtifactWorkflow)
	env.ExecuteWorkflow(BuildArtifactWorkflow, BuildArtifactInput{
		Component: "acc-agent",
		Ref:       "abc1234",
		Arches:    []string{"linux-x86_64"},
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("expected workflow completion")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}
	var got Result
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.Workflow != "BuildArtifactWorkflow" || got.Status != "stub" {
		t.Errorf("unexpected stub result: %+v", got)
	}
}

func TestRolloutWorkflow_Stub(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RolloutWorkflow)
	env.ExecuteWorkflow(RolloutWorkflow, RolloutInput{
		Component:   "acc-agent",
		Version:     "abc1234",
		Hosts:       []string{"rocky", "natasha"},
		Strategy:    RolloutCanary,
		BakeSeconds: 60,
	})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow failed: completed=%v err=%v",
			env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	var got Result
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.Workflow != "RolloutWorkflow" {
		t.Errorf("unexpected workflow name: %s", got.Workflow)
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
		TaskID:    "t1",
		Component: "acc-agent",
		Ref:       "abc",
		Submitter: "rocky",
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
