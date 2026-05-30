package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"webhook-ingestor/internal/llm/spec"
)

// OpenAI is a Client for the OpenAI chat-completions API and any OpenAI-compatible
// endpoint (Groq, Gemini's compat endpoint, …). Thin HTTP, no SDK dependency, so it
// stays lean and base-URL agnostic. Prompt and schema come from the shared spec package.
type OpenAI struct {
	apiKey       string
	model        string
	url          string
	strictSchema bool // official OpenAI supports json_schema+strict; others get json_object
	http         *http.Client
}

// NewOpenAI builds an OpenAI-compatible client. baseURL lets you point at ANY
// OpenAI-compatible endpoint; empty = api.openai.com. Only the official OpenAI
// endpoint gets strict json_schema; others use json_object (the normalize layer
// validates the result either way).
func NewOpenAI(apiKey, model, baseURL string) *OpenAI {
	if model == "" {
		model = "gpt-4o-mini"
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAI{
		apiKey:       apiKey,
		model:        model,
		url:          strings.TrimRight(baseURL, "/") + "/chat/completions",
		strictSchema: strings.Contains(baseURL, "api.openai.com"),
		http:         &http.Client{Timeout: 60 * time.Second},
	}
}

func (o *OpenAI) Normalize(ctx context.Context, raw []byte) (Result, error) {
	reqBody := chatRequest{
		Model:       o.model,
		Temperature: 0,
		Messages: []chatMessage{
			{Role: "system", Content: spec.SystemPrompt()},
			{Role: "user", Content: string(raw)},
		},
		ResponseFormat: o.responseFormat(),
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return Result{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(buf))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.http.Do(httpReq)
	if err != nil {
		return Result{}, &APIError{Retryable: true, Message: "openai: " + err.Error()} // network/timeout
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return Result{}, &APIError{
			StatusCode: resp.StatusCode,
			Retryable:  retryableStatus(resp.StatusCode, body),
			Message:    fmt.Sprintf("openai status %d: %s", resp.StatusCode, truncate(body, 300)),
		}
	}

	var env struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Choices) == 0 {
		return Result{}, &APIError{Retryable: true, Message: "openai: malformed response envelope"}
	}

	var out Response
	if err := json.Unmarshal([]byte(env.Choices[0].Message.Content), &out); err != nil {
		return Result{}, &APIError{Retryable: true, Message: "openai: content not valid JSON: " + err.Error()}
	}
	return Result{Response: out, Model: o.model, PromptVer: spec.PromptVersion}, nil
}

// responseFormat picks strict json_schema for OpenAI, json_object for other providers
// that don't support it. The normalize-layer validation guards both.
func (o *OpenAI) responseFormat() responseFormat {
	if o.strictSchema {
		return responseFormat{
			Type:       "json_schema",
			JSONSchema: &jsonSchema{Name: spec.SchemaName(), Strict: spec.SchemaStrict(), Schema: openAIStrictSchema(spec.SchemaBody())},
		}
	}
	return responseFormat{Type: "json_object"}
}

// openAIStrictSchema returns a copy of the shared schema with keywords OpenAI's
// strict mode rejects (minimum/maximum) removed, so the canonical schema can keep
// richer constraints for providers that accept them (Ollama, Gemini).
func openAIStrictSchema(s map[string]any) map[string]any {
	return stripKeys(s, "minimum", "maximum").(map[string]any)
}

// stripKeys deep-copies v, dropping any map keys listed. It never mutates the input.
func stripKeys(v any, keys ...string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if slices.Contains(keys, k) {
				continue
			}
			out[k] = stripKeys(val, keys...)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripKeys(val, keys...)
		}
		return out
	default:
		return v
	}
}

type chatRequest struct {
	Model          string         `json:"model"`
	Temperature    float64        `json:"temperature"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *jsonSchema `json:"json_schema,omitempty"`
}

type jsonSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

// retryableStatus decides whether an OpenAI error is worth retrying. 429 and 5xx are
// normally transient — but a 429 caused by insufficient_quota (no credit / billing)
// or an invalid API key will never recover on retry, so those are treated as permanent
// and dead-lettered immediately instead of burning the whole retry budget.
func retryableStatus(code int, body []byte) bool {
	var env struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	switch env.Error.Type {
	case "insufficient_quota", "invalid_api_key":
		return false
	}
	return code == http.StatusTooManyRequests || code >= 500
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
