// Package spec is the single source of truth for the LLM contract: the system
// prompt and the response JSON schema, both kept as standalone files and embedded.
// Every provider (OpenAI, Ollama, …) reads from here, so adding or changing a
// provider never duplicates the prompt or the schema. State is exposed through
// functions (not bare vars) to keep it read-only to callers.
package spec

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// PromptVersion tags every persisted fact for audit. Bump it whenever the prompt or
// schema changes so a normalized record can be traced to what produced it.
const PromptVersion = "v1"

//go:embed system_prompt.txt
var systemPrompt string

//go:embed normalize.json
var schemaJSON []byte

// schema mirrors the OpenAI json_schema envelope ({name, schema, strict}).
type schema struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict"`
}

var normalization = loadSchema()

func loadSchema() schema {
	var s schema
	if err := json.Unmarshal(schemaJSON, &s); err != nil {
		panic(fmt.Sprintf("spec: invalid embedded normalize.json: %v", err))
	}
	return s
}

func SystemPrompt() string { return systemPrompt }
func SchemaName() string   { return normalization.Name }
func SchemaStrict() bool   { return normalization.Strict }

// SchemaBody drives OpenAI strict output and Ollama constrained decoding; treat the
// returned map as read-only.
func SchemaBody() map[string]any { return normalization.Schema }
