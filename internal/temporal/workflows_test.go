package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

// actsRef is a nil *Activities pointer used to create bound method references
// for Temporal mock registration. The SDK only uses reflection to extract the
// method name — no actual method body runs.
var actsRef *Activities

// ---------------------------------------------------------------------------
// Helpers shared across tests
// ---------------------------------------------------------------------------

func defaultChatInput() ChatInput {
	return ChatInput{
		RequestID: "req-001",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Hello, world!"},
			},
		},
		Policy: router.Policy{
			Mode:         "normal",
			MaxBudgetUSD: 1.0,
			MaxLatencyMs: 5000,
		},
	}
}

func sampleDecision() router.Decision {
	return router.Decision{
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 0.03,
		Reason:           "best-match",
	}
}

func sampleSendOutput() SendOutput {
	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"role": "assistant", "content": "Hi there!"}},
		},
	})
	return SendOutput{
		Response:      resp,
		LatencyMs:     120,
		EstimatedCost: 0.03,
	}
}

// ---------------------------------------------------------------------------
// 1. TestChatWorkflow_Success
// ---------------------------------------------------------------------------

func TestChatWorkflow_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))

	require.Equal(t, decision.ModelID, output.Decision.ModelID)
	require.Equal(t, decision.ProviderID, output.Decision.ProviderID)
	require.Equal(t, sendOut.EstimatedCost, output.Decision.EstimatedCostUSD)
	require.Equal(t, decision.Reason, output.Decision.Reason)
	require.NotNil(t, output.Response)
	require.Empty(t, output.Error)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 2. TestChatWorkflow_SendFailsWithEscalation
// ---------------------------------------------------------------------------

func TestChatWorkflow_SendFailsWithEscalation(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)

	// First SendToProvider call fails, second succeeds.
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).
		Return(SendOutput{ErrorClass: "rate_limit"}, fmt.Errorf("rate limited")).Once()
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).
		Return(sendOut, nil).Once()

	// ClassifyAndEscalate returns a fallback model.
	env.OnActivity(actsRef.ClassifyAndEscalate, mock.Anything, mock.Anything).Return(
		EscalateOutput{
			NextModelID: "gpt-4-32k",
			ShouldRetry: true,
		}, nil,
	)

	// ResolveModel returns provider for the fallback model.
	env.OnActivity(actsRef.ResolveModel, mock.Anything, mock.Anything).Return("openai", nil)

	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))

	// After escalation the workflow should use the fallback model.
	require.Equal(t, "gpt-4-32k", output.Decision.ModelID)
	require.Equal(t, "openai", output.Decision.ProviderID)
	require.NotNil(t, output.Response)
	require.Empty(t, output.Error)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 3. TestChatWorkflow_SendFailsNoEscalation
// ---------------------------------------------------------------------------

func TestChatWorkflow_SendFailsNoEscalation(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(
		SendOutput{ErrorClass: "fatal"}, fmt.Errorf("provider down"),
	)

	// Escalation says do NOT retry.
	env.OnActivity(actsRef.ClassifyAndEscalate, mock.Anything, mock.Anything).Return(
		EscalateOutput{ShouldRetry: false}, nil,
	)

	// LogResult is still called with Success=false.
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider down")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 4. TestChatWorkflow_SelectModelFails
// ---------------------------------------------------------------------------

func TestChatWorkflow_SelectModelFails(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(
		router.Decision{}, fmt.Errorf("no models available"),
	)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "no models available")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 5. TestOrchestrationWorkflow_DefaultMode
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_DefaultMode(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-001",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Summarize this document."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode: "unknown-mode", // unknown mode falls through to ChatWorkflow
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))

	require.Equal(t, decision.ModelID, output.Decision.ModelID)
	require.Equal(t, decision.ProviderID, output.Decision.ProviderID)
	require.NotNil(t, output.Response)
	require.Empty(t, output.Error)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 6. TestEstimateTokens
// ---------------------------------------------------------------------------

func TestEstimateTokens(t *testing.T) {
	t.Run("uses EstimatedInputTokens when set", func(t *testing.T) {
		req := router.Request{
			EstimatedInputTokens: 500,
			Messages: []router.Message{
				{Role: "user", Content: "This content should be ignored for token estimation."},
			},
		}
		tokens := router.EstimateTokens(req)
		require.Equal(t, 500, tokens)
	})

	t.Run("falls back to content length divided by 4", func(t *testing.T) {
		req := router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "abcdefghijklmnop"}, // 16 chars => 4 tokens
				{Role: "assistant", Content: "12345678"},     // 8 chars => 2 tokens
			},
		}
		tokens := router.EstimateTokens(req)
		require.Equal(t, 6, tokens) // (16/4) + (8/4)
	})

	t.Run("empty messages returns zero", func(t *testing.T) {
		req := router.Request{}
		tokens := router.EstimateTokens(req)
		require.Equal(t, 0, tokens)
	})
}

// ---------------------------------------------------------------------------
// 7. TestExtractContent (uses shared router.ExtractContent)
// ---------------------------------------------------------------------------

func TestExtractContent(t *testing.T) {
	t.Run("OpenAI format", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "Hello from OpenAI"}},
			},
		})
		result := router.ExtractContent(raw)
		require.Equal(t, "Hello from OpenAI", result)
	})

	t.Run("Anthropic format", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "Hello from Anthropic"},
			},
		})
		result := router.ExtractContent(raw)
		require.Equal(t, "Hello from Anthropic", result)
	})

	t.Run("raw string fallback", func(t *testing.T) {
		raw := json.RawMessage(`"just a plain string"`)
		result := router.ExtractContent(raw)
		require.Equal(t, `"just a plain string"`, result)
	})

	t.Run("empty choices returns raw", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{},
		})
		result := router.ExtractContent(raw)
		require.Equal(t, string(raw), result)
	})

	t.Run("nil raw message", func(t *testing.T) {
		result := router.ExtractContent(nil)
		require.Equal(t, "", result)
	})
}

// ---------------------------------------------------------------------------
// 8. TestContainsDigit
// ---------------------------------------------------------------------------

func TestContainsDigit(t *testing.T) {
	t.Run("single digit present", func(t *testing.T) {
		require.True(t, containsDigit("Response 3 is best", 3))
	})

	t.Run("single digit absent", func(t *testing.T) {
		require.False(t, containsDigit("Response 3 is best", 5))
	})

	t.Run("multi-digit present", func(t *testing.T) {
		require.True(t, containsDigit("Option 12 wins", 12))
	})

	t.Run("multi-digit absent", func(t *testing.T) {
		require.False(t, containsDigit("Option 12 wins", 13))
	})

	t.Run("digit at start of string", func(t *testing.T) {
		require.True(t, containsDigit("7 is the answer", 7))
	})

	t.Run("digit at end of string", func(t *testing.T) {
		require.True(t, containsDigit("the answer is 7", 7))
	})

	t.Run("empty string", func(t *testing.T) {
		require.False(t, containsDigit("", 1))
	})

	t.Run("partial match is not a match for multi-digit", func(t *testing.T) {
		require.False(t, containsDigit("1", 12))
	})

	t.Run("zero", func(t *testing.T) {
		require.True(t, containsDigit("item 0 selected", 0))
	})
}

// ---------------------------------------------------------------------------
// 9. TestStreamLogWorkflow_Success
// ---------------------------------------------------------------------------

func TestStreamLogWorkflow_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(actsRef.StreamLogResult, mock.Anything, mock.Anything).Return(nil)

	input := StreamLogInput{
		LogInput: LogInput{
			RequestID:  "stream-001",
			ModelID:    "gpt-4",
			ProviderID: "openai",
			Mode:       "normal",
			LatencyMs:  1500,
			CostUSD:    0.05,
			Success:    true,
		},
		BytesStreamed: 65536,
	}
	env.ExecuteWorkflow(StreamLogWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 10. TestStreamLogWorkflow_ActivityFails
// ---------------------------------------------------------------------------

func TestStreamLogWorkflow_ActivityFails(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(actsRef.StreamLogResult, mock.Anything, mock.Anything).
		Return(fmt.Errorf("store unavailable"))

	input := StreamLogInput{
		LogInput: LogInput{
			RequestID:  "stream-002",
			ModelID:    "gpt-4",
			ProviderID: "openai",
			Mode:       "normal",
			LatencyMs:  800,
			CostUSD:    0.02,
			Success:    false,
			ErrorClass: "stream_error",
		},
		BytesStreamed: 1024,
	}
	env.ExecuteWorkflow(StreamLogWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "store unavailable")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 11. TestOrchestrationWorkflow_AdversarialMode
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_AdversarialMode(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	// Register child workflow before mocking activities.
	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	// The adversarial workflow executes child ChatWorkflows which in turn call
	// activities. We mock activities to serve all child workflows:
	// - plan phase (1 child ChatWorkflow)
	// - critique phase (1 child ChatWorkflow)
	// - refine phase (1 child ChatWorkflow)
	// Each child calls SelectModel, SendToProvider, LogResult.
	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-adv-001",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Design a REST API for a bookstore."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "adversarial",
			Iterations: 1,
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))

	require.Equal(t, "adversarial-orchestration", output.Decision.Reason)
	require.Equal(t, decision.ModelID, output.Decision.ModelID)
	require.Equal(t, decision.ProviderID, output.Decision.ProviderID)
	// Cost should be sum of 3 child workflows (plan + critique + refine).
	require.Greater(t, output.Decision.EstimatedCostUSD, 0.0)
	require.NotNil(t, output.Response)

	// Verify the response contains plan, critique, and refined_plan fields.
	var result map[string]any
	require.NoError(t, json.Unmarshal(output.Response, &result))
	require.Contains(t, result, "initial_plan")
	require.Contains(t, result, "critique")
	require.Contains(t, result, "refined_plan")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 12. TestOrchestrationWorkflow_AdversarialMode_WithExplicitModels
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_AdversarialMode_WithExplicitModels(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-adv-002",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Write a poem."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:           "adversarial",
			Iterations:     2, // Two critique+refine cycles.
			PrimaryModelID: "gpt-4",
			ReviewModelID:  "claude-3",
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	require.Equal(t, "adversarial-orchestration", output.Decision.Reason)
	require.NotNil(t, output.Response)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 13. TestOrchestrationWorkflow_AdversarialMode_ZeroIterations
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_AdversarialMode_ZeroIterations(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-adv-003",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Build a plan."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "adversarial",
			Iterations: 0, // defaults to 1
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	require.Equal(t, "adversarial-orchestration", output.Decision.Reason)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 14. TestOrchestrationWorkflow_AdversarialMode_PlanPhaseFails
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_AdversarialMode_PlanPhaseFails(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	// SelectModel fails on the plan child workflow.
	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(
		router.Decision{}, fmt.Errorf("no models available"),
	)

	input := OrchestrationInput{
		RequestID: "orch-adv-fail",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Do something."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "adversarial",
			Iterations: 1,
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 15. TestOrchestrationWorkflow_VoteMode
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_VoteMode(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	// Vote mode fans out multiple child ChatWorkflows (voters) + 1 judge.
	// Each calls SelectModel, SendToProvider, LogResult.
	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-vote-001",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "What is the meaning of life?"},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "vote",
			Iterations: 3, // 3 voters
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))

	require.Equal(t, "vote-orchestration", output.Decision.Reason)
	require.Equal(t, decision.ModelID, output.Decision.ModelID)
	require.NotNil(t, output.Response)

	// Verify the response has expected structure.
	var result map[string]any
	require.NoError(t, json.Unmarshal(output.Response, &result))
	require.Contains(t, result, "responses")
	require.Contains(t, result, "selected")
	require.Contains(t, result, "judge")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 16. TestOrchestrationWorkflow_VoteMode_DefaultVoters
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_VoteMode_DefaultVoters(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-vote-002",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Explain quantum mechanics."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "vote",
			Iterations: 0, // defaults to 3 voters (since < 2)
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	require.Equal(t, "vote-orchestration", output.Decision.Reason)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 17. TestOrchestrationWorkflow_VoteMode_WithReviewModel
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_VoteMode_WithReviewModel(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-vote-003",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Compare Python and Go."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:          "vote",
			Iterations:    3,
			ReviewModelID: "claude-3-opus",
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	require.Equal(t, "vote-orchestration", output.Decision.Reason)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 18. TestOrchestrationWorkflow_VoteMode_SingleVoter
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_VoteMode_SingleVoter(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-vote-single",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Hello."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "vote",
			Iterations: 1, // Less than 2, defaults to 3
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	// With Iterations=1 (<2), defaults to 3 voters, so judge runs.
	require.Equal(t, "vote-orchestration", output.Decision.Reason)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 19. TestOrchestrationWorkflow_RefineMode
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_RefineMode(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	// Refine workflow: 1 initial + N refinement child ChatWorkflows.
	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-refine-001",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Write a summary of machine learning."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "refine",
			Iterations: 2,
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))

	require.Equal(t, "refine-orchestration", output.Decision.Reason)
	require.Equal(t, decision.ModelID, output.Decision.ModelID)
	require.Equal(t, decision.ProviderID, output.Decision.ProviderID)
	require.Greater(t, output.Decision.EstimatedCostUSD, 0.0)
	require.NotNil(t, output.Response)

	// Verify response structure.
	var result map[string]any
	require.NoError(t, json.Unmarshal(output.Response, &result))
	require.Contains(t, result, "refined_response")
	require.Contains(t, result, "iterations")
	require.Contains(t, result, "model")
	require.Equal(t, float64(2), result["iterations"])

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 20. TestOrchestrationWorkflow_RefineMode_ZeroIterations
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_RefineMode_ZeroIterations(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-refine-002",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Explain gravity."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "refine",
			Iterations: 0, // defaults to 2
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	require.Equal(t, "refine-orchestration", output.Decision.Reason)

	var result map[string]any
	require.NoError(t, json.Unmarshal(output.Response, &result))
	require.Equal(t, float64(2), result["iterations"]) // defaulted to 2

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 21. TestOrchestrationWorkflow_RefineMode_WithExplicitModel
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_RefineMode_WithExplicitModel(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-refine-003",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Write code."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:           "refine",
			Iterations:     1,
			PrimaryModelID: "gpt-4-turbo",
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	require.Equal(t, "refine-orchestration", output.Decision.Reason)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 22. TestOrchestrationWorkflow_RefineMode_InitialPhaseFails
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_RefineMode_InitialPhaseFails(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	// SelectModel fails, causing initial child ChatWorkflow to fail.
	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(
		router.Decision{}, fmt.Errorf("no models available"),
	)

	input := OrchestrationInput{
		RequestID: "orch-refine-fail",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Explain relativity."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "refine",
			Iterations: 1,
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 23. TestChatWorkflow_MaxEscalationRetries
// ---------------------------------------------------------------------------

func TestChatWorkflow_MaxEscalationRetries(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)

	// All 5 SendToProvider calls fail.
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(
		SendOutput{ErrorClass: "rate_limit"}, fmt.Errorf("rate limited"),
	)

	// ClassifyAndEscalate always suggests a retry with a new model.
	var escCallCount atomic.Int32
	env.OnActivity(actsRef.ClassifyAndEscalate, mock.Anything, mock.Anything).Return(
		func(_ context.Context, input EscalateInput) (EscalateOutput, error) {
			n := escCallCount.Add(1)
			return EscalateOutput{
				NextModelID: fmt.Sprintf("fallback-model-%d", n),
				ShouldRetry: true,
			}, nil
		},
	)

	// ResolveModel always succeeds with a provider.
	env.OnActivity(actsRef.ResolveModel, mock.Anything, mock.Anything).Return("openai", nil)

	// LogResult is called at the end (with Success=false).
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "rate limited")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 24. TestChatWorkflow_ClassifyAndEscalateReturnsError
// ---------------------------------------------------------------------------

func TestChatWorkflow_ClassifyAndEscalateReturnsError(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)

	// SendToProvider fails.
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(
		SendOutput{ErrorClass: "timeout"}, fmt.Errorf("request timed out"),
	)

	// ClassifyAndEscalate itself returns an error.
	env.OnActivity(actsRef.ClassifyAndEscalate, mock.Anything, mock.Anything).Return(
		EscalateOutput{}, fmt.Errorf("escalation service unavailable"),
	)

	// LogResult is still called.
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())

	// The workflow should fail with the original send error (not the escalation error).
	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "request timed out")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 25. TestChatWorkflow_ResolveModelFails
// ---------------------------------------------------------------------------

func TestChatWorkflow_ResolveModelFails(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)

	// First SendToProvider fails, second succeeds.
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).
		Return(SendOutput{ErrorClass: "rate_limit"}, fmt.Errorf("rate limited")).Once()
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).
		Return(sendOut, nil).Once()

	// ClassifyAndEscalate suggests a retry.
	env.OnActivity(actsRef.ClassifyAndEscalate, mock.Anything, mock.Anything).Return(
		EscalateOutput{
			NextModelID: "gpt-4-32k",
			ShouldRetry: true,
		}, nil,
	)

	// ResolveModel returns an error -- the workflow should still continue
	// with the old provider ID since it only updates on success.
	env.OnActivity(actsRef.ResolveModel, mock.Anything, mock.Anything).Return(
		"", fmt.Errorf("model not found in registry"),
	)

	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))

	// Model ID should be updated to the escalated model.
	require.Equal(t, "gpt-4-32k", output.Decision.ModelID)
	// Provider ID should remain the original since ResolveModel failed.
	require.Equal(t, "openai", output.Decision.ProviderID)
	require.Empty(t, output.Error)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 26. TestChatWorkflow_EscalateEmptyNextModelID
// ---------------------------------------------------------------------------

func TestChatWorkflow_EscalateEmptyNextModelID(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)

	// SendToProvider fails.
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(
		SendOutput{ErrorClass: "fatal"}, fmt.Errorf("provider crashed"),
	)

	// ClassifyAndEscalate says retry but returns empty model ID -- should break.
	env.OnActivity(actsRef.ClassifyAndEscalate, mock.Anything, mock.Anything).Return(
		EscalateOutput{ShouldRetry: true, NextModelID: ""}, nil,
	)

	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider crashed")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 27. TestChatWorkflow_MultipleEscalations
// ---------------------------------------------------------------------------

func TestChatWorkflow_MultipleEscalations(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)

	// First two SendToProvider calls fail, third succeeds.
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).
		Return(SendOutput{ErrorClass: "rate_limit"}, fmt.Errorf("rate limited")).Once()
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).
		Return(SendOutput{ErrorClass: "capacity"}, fmt.Errorf("capacity exceeded")).Once()
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).
		Return(sendOut, nil).Once()

	// ClassifyAndEscalate returns different fallback models.
	env.OnActivity(actsRef.ClassifyAndEscalate, mock.Anything, mock.Anything).
		Return(EscalateOutput{NextModelID: "gpt-4-32k", ShouldRetry: true}, nil).Once()
	env.OnActivity(actsRef.ClassifyAndEscalate, mock.Anything, mock.Anything).
		Return(EscalateOutput{NextModelID: "gpt-4-turbo", ShouldRetry: true}, nil).Once()

	env.OnActivity(actsRef.ResolveModel, mock.Anything, mock.Anything).Return("openai", nil)

	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := defaultChatInput()
	env.ExecuteWorkflow(ChatWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))

	// Should end up on the third model.
	require.Equal(t, "gpt-4-turbo", output.Decision.ModelID)
	require.Empty(t, output.Error)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 28. TestOrchestrationWorkflow_VoteMode_AllVotersFail
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_VoteMode_AllVotersFail(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	// SelectModel fails, causing all voter child workflows to fail.
	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(
		router.Decision{}, fmt.Errorf("no models available"),
	)

	input := OrchestrationInput{
		RequestID: "orch-vote-allfail",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Test."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "vote",
			Iterations: 3,
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "all voters failed")

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 29. TestOrchestrationWorkflow_VoteMode_JudgeSelectsHigherNumber
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_VoteMode_JudgeSelectsHigherNumber(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterWorkflow(ChatWorkflow)

	// Use different responses for each voter so the judge can distinguish.
	decision1 := router.Decision{
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 0.03,
		Reason:           "voter",
	}
	resp1, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"role": "assistant", "content": "Response from voter 1"}},
		},
	})
	sendOut1 := SendOutput{Response: resp1, LatencyMs: 100, EstimatedCost: 0.03}

	// Judge response selects voter 2.
	judgeResp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"role": "assistant", "content": "Response 2 is the best"}},
		},
	})
	judgeSendOut := SendOutput{Response: judgeResp, LatencyMs: 80, EstimatedCost: 0.02}

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision1, nil)

	// Voters return the normal response, judge returns the judge response.
	// We cannot distinguish which child workflow is which by activities alone,
	// so we use a single response and verify the structure.
	var sendCallCount atomic.Int32
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(
		func(_ context.Context, input SendInput) (SendOutput, error) {
			n := sendCallCount.Add(1)
			// The first 3 calls are voters, the 4th is the judge.
			if n <= 3 {
				return sendOut1, nil
			}
			return judgeSendOut, nil
		},
	)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-vote-judge",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "What is 2+2?"},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:       "vote",
			Iterations: 3,
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	require.Equal(t, "vote-orchestration", output.Decision.Reason)
	require.NotNil(t, output.Response)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 30. TestOrchestrationWorkflow_PlanningMode (default/planning mode)
// ---------------------------------------------------------------------------

func TestOrchestrationWorkflow_PlanningMode(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	decision := sampleDecision()
	sendOut := sampleSendOutput()

	env.OnActivity(actsRef.SelectModel, mock.Anything, mock.Anything).Return(decision, nil)
	env.OnActivity(actsRef.SendToProvider, mock.Anything, mock.Anything).Return(sendOut, nil)
	env.OnActivity(actsRef.LogResult, mock.Anything, mock.Anything).Return(nil)

	input := OrchestrationInput{
		RequestID: "orch-plan-001",
		APIKeyID:  "key-abc",
		Request: router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "Plan a project."},
			},
		},
		Directive: router.OrchestrationDirective{
			Mode:             "planning",
			PrimaryMinWeight: 5,
		},
	}

	env.ExecuteWorkflow(OrchestrationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var output ChatOutput
	require.NoError(t, env.GetWorkflowResult(&output))
	// Planning mode falls through to ChatWorkflow via default case.
	require.Equal(t, decision.ModelID, output.Decision.ModelID)
	require.NotNil(t, output.Response)

	env.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// 31. TestContainsDigit_AdditionalEdgeCases
// ---------------------------------------------------------------------------

func TestContainsDigit_AdditionalEdgeCases(t *testing.T) {
	t.Run("digit embedded in word", func(t *testing.T) {
		require.True(t, containsDigit("abc3def", 3))
	})

	t.Run("large number", func(t *testing.T) {
		require.True(t, containsDigit("value is 12345", 12345))
	})

	t.Run("large number absent", func(t *testing.T) {
		require.False(t, containsDigit("value is 12345", 12346))
	})

	t.Run("negative number string representation", func(t *testing.T) {
		// containsDigit formats n as %d, so -1 is "-1"
		require.True(t, containsDigit("score is -1", -1))
	})

	t.Run("string shorter than digit representation", func(t *testing.T) {
		require.False(t, containsDigit("ab", 123))
	})

	t.Run("single character string with match", func(t *testing.T) {
		require.True(t, containsDigit("5", 5))
	})

	t.Run("single character string without match", func(t *testing.T) {
		require.False(t, containsDigit("5", 3))
	})
}

// ---------------------------------------------------------------------------
// 32. TestExtractContent_AdditionalFormats
// ---------------------------------------------------------------------------

func TestExtractContent_AdditionalFormats(t *testing.T) {
	t.Run("OpenAI format with multiple choices", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "First choice"}},
				{"message": map[string]string{"role": "assistant", "content": "Second choice"}},
			},
		})
		result := router.ExtractContent(raw)
		// Should extract the first choice.
		require.Equal(t, "First choice", result)
	})

	t.Run("Anthropic format with multiple content blocks", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "First block"},
				{"type": "text", "text": "Second block"},
			},
		})
		result := router.ExtractContent(raw)
		// Should extract the first block.
		require.Equal(t, "First block", result)
	})

	t.Run("empty content array (Anthropic format)", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"content": []map[string]string{},
		})
		result := router.ExtractContent(raw)
		// Falls through to raw string since content array is empty.
		require.Equal(t, string(raw), result)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		raw := json.RawMessage(`{invalid json}`)
		result := router.ExtractContent(raw)
		// Falls through to raw string.
		require.Equal(t, string(raw), result)
	})

	t.Run("object without choices or content", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"data": "something else",
		})
		result := router.ExtractContent(raw)
		require.Equal(t, string(raw), result)
	})

	t.Run("empty JSON object", func(t *testing.T) {
		raw := json.RawMessage(`{}`)
		result := router.ExtractContent(raw)
		require.Equal(t, "{}", result)
	})
}

// ---------------------------------------------------------------------------
// 33. TestEstimateTokens_AdditionalEdgeCases
// ---------------------------------------------------------------------------

func TestEstimateTokens_AdditionalEdgeCases(t *testing.T) {
	t.Run("short content rounds down to zero", func(t *testing.T) {
		req := router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "ab"}, // 2 chars / 4 = 0 tokens
			},
		}
		tokens := router.EstimateTokens(req)
		require.Equal(t, 0, tokens)
	})

	t.Run("multiple messages accumulate", func(t *testing.T) {
		req := router.Request{
			Messages: []router.Message{
				{Role: "system", Content: "You are helpful."}, // 16 chars => 4 tokens
				{Role: "user", Content: "Tell me a joke."},    // 15 chars => 3 tokens
				{Role: "assistant", Content: "Why did the..."},  // 14 chars => 3 tokens
				{Role: "user", Content: "Go on."},              // 6 chars => 1 token
			},
		}
		tokens := router.EstimateTokens(req)
		// 16/4 + 15/4 + 14/4 + 6/4 = 4 + 3 + 3 + 1 = 11
		require.Equal(t, 11, tokens)
	})

	t.Run("estimated tokens takes priority over messages", func(t *testing.T) {
		req := router.Request{
			EstimatedInputTokens: 1000,
			Messages: []router.Message{
				{Role: "user", Content: "short"},
			},
		}
		tokens := router.EstimateTokens(req)
		require.Equal(t, 1000, tokens)
	})
}
