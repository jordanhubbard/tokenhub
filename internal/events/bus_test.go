package events

import (
	"testing"
	"time"
)

func TestPublishAndSubscribe(t *testing.T) {
	bus := NewBus()
	sub := bus.Subscribe(10)
	defer bus.Unsubscribe(sub)

	bus.Publish(Event{
		Type:       EventRouteSuccess,
		ModelID:    "gpt-4",
		ProviderID: "openai",
		LatencyMs:  150,
	})

	select {
	case e := <-sub.C:
		if e.Type != EventRouteSuccess {
			t.Errorf("expected route_success, got %s", e.Type)
		}
		if e.ModelID != "gpt-4" {
			t.Errorf("expected gpt-4, got %s", e.ModelID)
		}
		if e.Timestamp.IsZero() {
			t.Error("expected timestamp to be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestMultipleSubscribers(t *testing.T) {
	bus := NewBus()
	sub1 := bus.Subscribe(10)
	sub2 := bus.Subscribe(10)
	defer bus.Unsubscribe(sub1)
	defer bus.Unsubscribe(sub2)

	bus.Publish(Event{Type: EventRouteError, ModelID: "m1"})

	for _, sub := range []*Subscriber{sub1, sub2} {
		select {
		case e := <-sub.C:
			if e.Type != EventRouteError {
				t.Errorf("expected route_error, got %s", e.Type)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for event")
		}
	}
}

func TestUnsubscribe(t *testing.T) {
	bus := NewBus()
	sub := bus.Subscribe(10)
	bus.Unsubscribe(sub)

	if bus.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers, got %d", bus.SubscriberCount())
	}

	// Publishing after unsubscribe should not panic.
	bus.Publish(Event{Type: EventRouteSuccess})
}

func TestSlowSubscriberDropsEvents(t *testing.T) {
	bus := NewBus()
	sub := bus.Subscribe(1) // tiny buffer
	defer bus.Unsubscribe(sub)

	// Fill the buffer.
	bus.Publish(Event{Type: EventRouteSuccess, ModelID: "first"})
	// This should be dropped (buffer full).
	bus.Publish(Event{Type: EventRouteSuccess, ModelID: "second"})

	e := <-sub.C
	if e.ModelID != "first" {
		t.Errorf("expected first event, got %s", e.ModelID)
	}

	// Channel should be empty now.
	select {
	case <-sub.C:
		t.Error("expected no more events")
	default:
		// OK - no event available.
	}
}

func TestSubscriberCount(t *testing.T) {
	bus := NewBus()
	if bus.SubscriberCount() != 0 {
		t.Errorf("expected 0, got %d", bus.SubscriberCount())
	}

	s1 := bus.Subscribe(10)
	s2 := bus.Subscribe(10)
	if bus.SubscriberCount() != 2 {
		t.Errorf("expected 2, got %d", bus.SubscriberCount())
	}

	bus.Unsubscribe(s1)
	if bus.SubscriberCount() != 1 {
		t.Errorf("expected 1, got %d", bus.SubscriberCount())
	}

	bus.Unsubscribe(s2)
	if bus.SubscriberCount() != 0 {
		t.Errorf("expected 0, got %d", bus.SubscriberCount())
	}
}

func TestEventJSON(t *testing.T) {
	e := Event{
		Type:       EventRouteSuccess,
		Timestamp:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ModelID:    "gpt-4",
		ProviderID: "openai",
		LatencyMs:  42.5,
	}
	b := e.JSON()
	if len(b) == 0 {
		t.Fatal("expected non-empty JSON")
	}
}

func TestWorkflowEvents(t *testing.T) {
	bus := NewBus()
	sub := bus.Subscribe(10)
	defer bus.Unsubscribe(sub)

	// Publish workflow_started event.
	bus.Publish(Event{
		Type:         EventWorkflowStarted,
		WorkflowID:   "chat-req-123",
		WorkflowType: "ChatWorkflow",
		RequestID:    "req-123",
	})

	// Publish activity_completed event.
	bus.Publish(Event{
		Type:         EventActivityCompleted,
		ActivityType: "SelectModel",
		ModelID:      "gpt-4",
		ProviderID:   "openai",
		RequestID:    "req-123",
	})

	// Publish workflow_completed event.
	bus.Publish(Event{
		Type:         EventWorkflowCompleted,
		WorkflowID:   "chat-req-123",
		WorkflowType: "ChatWorkflow",
		ModelID:      "gpt-4",
		ProviderID:   "openai",
		LatencyMs:    250,
		CostUSD:      0.003,
	})

	// Verify workflow_started.
	select {
	case e := <-sub.C:
		if e.Type != EventWorkflowStarted {
			t.Errorf("expected workflow_started, got %s", e.Type)
		}
		if e.WorkflowID != "chat-req-123" {
			t.Errorf("expected workflow_id chat-req-123, got %s", e.WorkflowID)
		}
		if e.WorkflowType != "ChatWorkflow" {
			t.Errorf("expected workflow_type ChatWorkflow, got %s", e.WorkflowType)
		}
		if e.RequestID != "req-123" {
			t.Errorf("expected request_id req-123, got %s", e.RequestID)
		}
		if e.Timestamp.IsZero() {
			t.Error("expected timestamp to be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for workflow_started event")
	}

	// Verify activity_completed.
	select {
	case e := <-sub.C:
		if e.Type != EventActivityCompleted {
			t.Errorf("expected activity_completed, got %s", e.Type)
		}
		if e.ActivityType != "SelectModel" {
			t.Errorf("expected activity_type SelectModel, got %s", e.ActivityType)
		}
		if e.ModelID != "gpt-4" {
			t.Errorf("expected model_id gpt-4, got %s", e.ModelID)
		}
		if e.ProviderID != "openai" {
			t.Errorf("expected provider_id openai, got %s", e.ProviderID)
		}
		if e.RequestID != "req-123" {
			t.Errorf("expected request_id req-123, got %s", e.RequestID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for activity_completed event")
	}

	// Verify workflow_completed.
	select {
	case e := <-sub.C:
		if e.Type != EventWorkflowCompleted {
			t.Errorf("expected workflow_completed, got %s", e.Type)
		}
		if e.WorkflowID != "chat-req-123" {
			t.Errorf("expected workflow_id chat-req-123, got %s", e.WorkflowID)
		}
		if e.WorkflowType != "ChatWorkflow" {
			t.Errorf("expected workflow_type ChatWorkflow, got %s", e.WorkflowType)
		}
		if e.ModelID != "gpt-4" {
			t.Errorf("expected model_id gpt-4, got %s", e.ModelID)
		}
		if e.ProviderID != "openai" {
			t.Errorf("expected provider_id openai, got %s", e.ProviderID)
		}
		if e.LatencyMs != 250 {
			t.Errorf("expected latency_ms 250, got %f", e.LatencyMs)
		}
		if e.CostUSD != 0.003 {
			t.Errorf("expected cost_usd 0.003, got %f", e.CostUSD)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for workflow_completed event")
	}
}
