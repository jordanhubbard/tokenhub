package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// VLLMProvider implements the Provider interface for vLLM instances
type VLLMProvider struct {
	name    string
	baseURL string
}

// NewVLLMProvider creates a new vLLM provider
func NewVLLMProvider(name, baseURL string) *VLLMProvider {
	return &VLLMProvider{
		name:    name,
		baseURL: baseURL,
	}
}

func (p *VLLMProvider) Name() string {
	return p.name
}

func (p *VLLMProvider) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	payload := map[string]interface{}{
		"model":       req.Model,
		"prompt":      req.Prompt,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
	}

	respBody, err := p.makeRequest(ctx, "/v1/completions", payload)
	if err != nil {
		return nil, err
	}

	var result struct {
		Choices []struct {
			Text         string `json:"text"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	return &CompletionResponse{
		Text:         result.Choices[0].Text,
		TokensUsed:   result.Usage.TotalTokens,
		FinishReason: result.Choices[0].FinishReason,
	}, nil
}

func (p *VLLMProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	messages := make([]map[string]string, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}

	payload := map[string]interface{}{
		"model":       req.Model,
		"messages":    messages,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
	}

	respBody, err := p.makeRequest(ctx, "/v1/chat/completions", payload)
	if err != nil {
		return nil, err
	}

	var result struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	return &ChatResponse{
		Message: Message{
			Role:    result.Choices[0].Message.Role,
			Content: result.Choices[0].Message.Content,
		},
		TokensUsed:   result.Usage.TotalTokens,
		FinishReason: result.Choices[0].FinishReason,
	}, nil
}

func (p *VLLMProvider) makeRequest(ctx context.Context, endpoint string, payload interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}
