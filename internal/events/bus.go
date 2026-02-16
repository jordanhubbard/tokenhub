package events

import (
	"encoding/json"
	"sync"
	"time"
)

// EventType identifies the kind of event.
type EventType string

const (
	EventRouteSuccess       EventType = "route_success"
	EventRouteError         EventType = "route_error"
	EventEscalation         EventType = "escalation"
	EventHealthChange       EventType = "health_change"
	EventWorkflowStarted   EventType = "workflow_started"
	EventActivityCompleted  EventType = "activity_completed"
	EventWorkflowCompleted  EventType = "workflow_completed"
	EventWorkflowFailed     EventType = "workflow_failed"
)

// Event is a single routing event published on the bus.
type Event struct {
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`

	// Routing fields (populated for route events).
	ModelID     string  `json:"model_id,omitempty"`
	ProviderID  string  `json:"provider_id,omitempty"`
	LatencyMs   float64 `json:"latency_ms,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
	ErrorClass  string  `json:"error_class,omitempty"`
	ErrorMsg    string  `json:"error_msg,omitempty"`
	Reason      string  `json:"reason,omitempty"`

	// Health fields (populated for health_change events).
	OldState string `json:"old_state,omitempty"`
	NewState string `json:"new_state,omitempty"`

	// Workflow fields (populated for workflow events).
	WorkflowID   string `json:"workflow_id,omitempty"`
	WorkflowType string `json:"workflow_type,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	APIKeyName   string `json:"api_key_name,omitempty"`
	Activity     string `json:"activity,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
}

// JSON returns the event as a JSON byte slice.
func (e *Event) JSON() []byte {
	b, _ := json.Marshal(e)
	return b
}

// Subscriber receives events on a channel.
type Subscriber struct {
	C    chan Event
	done chan struct{}
}

// Bus is an in-memory pub/sub event bus for routing events.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[*Subscriber]struct{}
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{
		subscribers: make(map[*Subscriber]struct{}),
	}
}

// Subscribe creates a new subscriber with a buffered channel.
func (b *Bus) Subscribe(bufSize int) *Subscriber {
	if bufSize <= 0 {
		bufSize = 64
	}
	s := &Subscriber{
		C:    make(chan Event, bufSize),
		done: make(chan struct{}),
	}
	b.mu.Lock()
	b.subscribers[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Bus) Unsubscribe(s *Subscriber) {
	b.mu.Lock()
	delete(b.subscribers, s)
	b.mu.Unlock()
	close(s.done)
}

// Publish sends an event to all subscribers (non-blocking).
func (b *Bus) Publish(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subscribers {
		select {
		case s.C <- e:
		default:
			// Drop event if subscriber is slow (back-pressure).
		}
	}
}

// SubscriberCount returns the number of active subscribers.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
