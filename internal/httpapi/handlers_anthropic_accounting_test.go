package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jordanhubbard/tokenhub/internal/pricing/nemorelay"
	"github.com/jordanhubbard/tokenhub/internal/router"
	"github.com/jordanhubbard/tokenhub/internal/stats"
	"github.com/stretchr/testify/require"
)

type mockAnthropicRawSender struct {
	id         string
	response   []byte
	statusCode int
	stream     string
}

func (m *mockAnthropicRawSender) ID() string { return m.id }

func (m *mockAnthropicRawSender) Send(_ context.Context, _ string, _ router.Request) (router.ProviderResponse, error) {
	return json.RawMessage(m.response), nil
}

func (m *mockAnthropicRawSender) ClassifyError(err error) *router.ClassifiedError {
	return &router.ClassifiedError{Err: err, Class: router.ErrFatal}
}

func (m *mockAnthropicRawSender) ForwardRaw(_ context.Context, _ []byte) ([]byte, int, error) {
	return m.response, m.statusCode, nil
}

func (m *mockAnthropicRawSender) ForwardRawStream(_ context.Context, _ []byte) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(m.stream)), nil
}

func configureAnthropicAccounting(t *testing.T, eng *router.Engine, sender *mockAnthropicRawSender) {
	t.Helper()
	catalog, err := nemorelay.ParseCatalog([]byte(`{
  "version": 1,
  "entries": [{
    "provider": "anthropic", "model_id": "claude-test",
    "rates": {
      "input_per_million": 3,
      "output_per_million": 15,
      "cache_read_per_million": 0.3,
      "cache_write_per_million": 3.75
    },
    "prompt_cache": {"read_accounting": "included_in_prompt_tokens"},
    "pricing_as_of": "2026-08-01", "pricing_source": "test-catalog"
  }]
}`))
	require.NoError(t, err)
	resolver, err := nemorelay.NewResolver(catalog)
	require.NoError(t, err)

	eng.RegisterAdapter(sender)
	eng.RegisterModel(router.Model{ID: "claude-test", ProviderID: "anthropic", Enabled: true})
	eng.SetCostEstimator(resolver)
}

func TestAnthropicMessagesAccountsNonStreamingUsage(t *testing.T) {
	ts, eng, _ := setupTestServer(t)
	defer ts.Close()

	sender := &mockAnthropicRawSender{
		id:         "anthropic",
		statusCode: http.StatusOK,
		response: []byte(`{
  "type": "message",
  "usage": {
    "input_tokens": 1000,
    "output_tokens": 200,
    "cache_read_input_tokens": 400,
    "cache_creation_input_tokens": 100
  }
}`),
	}
	configureAnthropicAccounting(t, eng, sender)

	resp, err := authPost(ts.URL+"/v1/messages", "application/json", bytes.NewReader([]byte(`{
  "model": "claude-test",
  "max_tokens": 200,
  "messages": [{"role":"user","content":"hello"}]
}`)))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "model_pricing", resp.Header.Get("X-TokenHub-Cost-Source"))
	cost, err := strconv.ParseFloat(resp.Header.Get("X-TokenHub-Cost-USD"), 64)
	require.NoError(t, err)
	require.InDelta(t, 0.005295, cost, 1e-12)
}

func TestAnthropicMessagesAccountsStreamingUsage(t *testing.T) {
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":400,"cache_creation_input_tokens":100}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":200}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	sender := &mockAnthropicRawSender{id: "anthropic", stream: stream}
	eng := router.NewEngine(router.EngineConfig{})
	configureAnthropicAccounting(t, eng, sender)
	collector := stats.NewCollector()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
  "model": "claude-test",
  "stream": true,
  "max_tokens": 200,
  "messages": [{"role":"user","content":"hello"}]
}`))
	w := httptest.NewRecorder()
	AnthropicMessagesHandler(Dependencies{Engine: eng, Stats: collector}).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, stream, w.Body.String())
	global := collector.Global()
	require.NotEmpty(t, global)
	require.Equal(t, 1000, global[0].InputTokens)
	require.Equal(t, 200, global[0].OutputTokens)
	require.Equal(t, 400, global[0].CacheReadTokens)
	require.Equal(t, 100, global[0].CacheWriteTokens)
	require.InDelta(t, 0.005295, global[0].TotalCostUSD, 1e-12)

	result := w.Result()
	require.Equal(t, "model_pricing", result.Trailer.Get("X-TokenHub-Cost-Source"))
}
