package temporal

import (
	"encoding/json"
	"fmt"
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
		tokens := EstimateTokens(req)
		require.Equal(t, 500, tokens)
	})

	t.Run("falls back to content length divided by 4", func(t *testing.T) {
		req := router.Request{
			Messages: []router.Message{
				{Role: "user", Content: "abcdefghijklmnop"}, // 16 chars => 4 tokens
				{Role: "assistant", Content: "12345678"},     // 8 chars => 2 tokens
			},
		}
		tokens := EstimateTokens(req)
		require.Equal(t, 6, tokens) // (16/4) + (8/4)
	})

	t.Run("empty messages returns zero", func(t *testing.T) {
		req := router.Request{}
		tokens := EstimateTokens(req)
		require.Equal(t, 0, tokens)
	})
}

// ---------------------------------------------------------------------------
// 7. TestExtractContentFromRaw
// ---------------------------------------------------------------------------

func TestExtractContentFromRaw(t *testing.T) {
	t.Run("OpenAI format", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "Hello from OpenAI"}},
			},
		})
		result := extractContentFromRaw(raw)
		require.Equal(t, "Hello from OpenAI", result)
	})

	t.Run("Anthropic format", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "Hello from Anthropic"},
			},
		})
		result := extractContentFromRaw(raw)
		require.Equal(t, "Hello from Anthropic", result)
	})

	t.Run("raw string fallback", func(t *testing.T) {
		raw := json.RawMessage(`"just a plain string"`)
		result := extractContentFromRaw(raw)
		require.Equal(t, `"just a plain string"`, result)
	})

	t.Run("empty choices returns raw", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{},
		})
		result := extractContentFromRaw(raw)
		require.Equal(t, string(raw), result)
	})

	t.Run("nil raw message", func(t *testing.T) {
		result := extractContentFromRaw(nil)
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
