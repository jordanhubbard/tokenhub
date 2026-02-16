package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jordanhubbard/tokenhub/internal/providers"
	"github.com/jordanhubbard/tokenhub/internal/router"
)

// Adapter implements router.Sender for OpenAI.
type Adapter struct {
	id      string
	apiKey  string
	baseURL string
	client  *http.Client
}

// New creates a new OpenAI adapter.
func New(id, apiKey, baseURL string) *Adapter {
	return &Adapter{
		id:      id,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{},
	}
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Send(ctx context.Context, model string, req router.Request) (router.ProviderResponse, error) {
	messages := make([]map[string]string, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}

	payload := map[string]any{
		"model":    model,
		"messages": messages,
	}

	return a.makeRequest(ctx, "/v1/chat/completions", payload)
}

func (a *Adapter) ClassifyError(err error) *router.ClassifiedError {
	var se *providers.StatusError
	if errors.As(err, &se) {
		switch {
		case se.StatusCode == 429:
			return &router.ClassifiedError{Err: err, Class: router.ErrRateLimited}
		case se.StatusCode >= 500:
			return &router.ClassifiedError{Err: err, Class: router.ErrTransient}
		case strings.Contains(se.Body, "context_length_exceeded"):
			return &router.ClassifiedError{Err: err, Class: router.ErrContextOverflow}
		}
	}
	return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
}

func (a *Adapter) makeRequest(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+endpoint, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &providers.StatusError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	return body, nil
}
