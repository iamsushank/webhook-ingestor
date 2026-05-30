package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"webhook-ingestor/internal/llm/spec"
)

// Ollama is a Client backed by a local Ollama server, via its native /api/chat
// endpoint with format=json for guaranteed-valid JSON output. It runs fully offline —
// no API key, no cost — and is a second concrete proof that the pipeline is
// provider-agnostic: it satisfies the same llm.Client interface as OpenAI and Mock.
type Ollama struct {
	model string
	url   string
	http  *http.Client
}

// NewOllama builds a client against an Ollama server. Empty host defaults to the
// local daemon; empty model to llama3.2.
func NewOllama(model, host string) *Ollama {
	if model == "" {
		model = "llama3.2"
	}
	if host == "" {
		host = "http://localhost:11434"
	}
	return &Ollama{
		model: model,
		url:   strings.TrimRight(host, "/") + "/api/chat",
		http:  &http.Client{Timeout: 120 * time.Second}, // local models can be slow
	}
}

func (o *Ollama) Normalize(ctx context.Context, raw []byte) (Result, error) {
	reqBody := ollamaRequest{
		Model:   o.model,
		Stream:  false,
		Format:  spec.SchemaBody(), // schema-constrained decoding: forces valid enums, not just valid JSON
		Options: ollamaOptions{Temperature: 0},
		Messages: []chatMessage{
			{Role: "system", Content: spec.SystemPrompt()},
			{Role: "user", Content: string(raw)},
		},
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

	resp, err := o.http.Do(httpReq)
	if err != nil {
		return Result{}, &APIError{Retryable: true, Message: "ollama: " + err.Error()} // network/timeout
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return Result{}, &APIError{
			StatusCode: resp.StatusCode,
			Retryable:  resp.StatusCode >= 500,
			Message:    fmt.Sprintf("ollama status %d: %s", resp.StatusCode, truncate(body, 300)),
		}
	}

	var env ollamaResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return Result{}, &APIError{Retryable: true, Message: "ollama: malformed response envelope"}
	}
	var out Response
	if err := json.Unmarshal([]byte(env.Message.Content), &out); err != nil {
		return Result{}, &APIError{Retryable: true, Message: "ollama: content not valid JSON: " + err.Error()}
	}
	return Result{Response: out, Model: o.model, PromptVer: spec.PromptVersion}, nil
}

type ollamaRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Format   any           `json:"format,omitempty"` // "json" or a JSON schema object
	Options  ollamaOptions `json:"options"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
}

type ollamaResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}
