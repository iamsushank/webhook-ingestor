package llm

import (
	"testing"

	"webhook-ingestor/internal/llm/spec"
)

func TestRetryableStatus(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want bool
	}{
		{"rate_limit_429_retries", 429, `{"error":{"type":"rate_limit_exceeded"}}`, true},
		{"insufficient_quota_429_permanent", 429, `{"error":{"type":"insufficient_quota"}}`, false},
		{"invalid_key_401_permanent", 401, `{"error":{"type":"invalid_api_key"}}`, false},
		{"server_500_retries", 500, ``, true},
		{"bad_request_400_permanent", 400, `{"error":{"type":"invalid_request_error"}}`, false},
	}
	for _, c := range cases {
		if got := retryableStatus(c.code, []byte(c.body)); got != c.want {
			t.Errorf("%s: retryableStatus(%d) = %v, want %v", c.name, c.code, got, c.want)
		}
	}
}

// OpenAI strict mode rejects numeric-range keywords; the shared schema keeps them
// (Ollama/Gemini accept them), so the OpenAI client must strip them from a copy
// without mutating the original.
func TestOpenAIStrictSchema_DropsRangeKeywordsWithoutMutating(t *testing.T) {
	body := spec.SchemaBody()
	confidence := func(s map[string]any) map[string]any {
		return s["properties"].(map[string]any)["confidence"].(map[string]any)
	}

	// precondition: the shared schema constrains confidence with minimum/maximum
	if _, ok := confidence(body)["minimum"]; !ok {
		t.Fatal("precondition failed: spec schema should set confidence.minimum")
	}

	strict := openAIStrictSchema(body)

	if _, ok := confidence(strict)["minimum"]; ok {
		t.Error("strict schema must not contain minimum")
	}
	if _, ok := confidence(strict)["maximum"]; ok {
		t.Error("strict schema must not contain maximum")
	}
	// the shared schema must be untouched (deep copy)
	if _, ok := confidence(body)["minimum"]; !ok {
		t.Error("openAIStrictSchema mutated the shared schema")
	}
}
