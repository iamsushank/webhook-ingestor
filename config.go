package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"webhook-ingestor/internal/queue"
)

// Config is the full runtime configuration, sourced from environment variables with
// sensible defaults so the service runs out of the box under docker-compose.
type Config struct {
	Mode                string // server | worker | all (default) — which process role to run
	HTTPAddr            string
	DatabaseURL         string
	LLMProvider         string // "" (auto) | openai | ollama | mock
	LLMModel            string // e.g. gpt-4o-mini | llama3.2 | gemma4:latest
	LLMAPIKey           string // required for openai/groq; unused for ollama/mock
	LLMBaseURL          string // endpoint; blank = provider default
	ConfidenceThreshold float64
	WorkerConcurrency   int
	Queue               queue.Config
	LogLevel            string
}

func loadConfig() Config {
	return Config{
		Mode:                strings.ToLower(getenv("MODE", "all")),
		HTTPAddr:            ":" + getenv("PORT", "8080"),
		DatabaseURL:         getenv("DATABASE_URL", "postgresql://postgres:postgres@127.0.0.1:5643/webhooks"),
		LLMProvider:         getenv("LLM_PROVIDER", ""),
		LLMModel:            os.Getenv("LLM_MODEL"),
		LLMAPIKey:           os.Getenv("LLM_API_KEY"),
		LLMBaseURL:          os.Getenv("LLM_BASE_URL"),
		ConfidenceThreshold: getenvFloat("CONFIDENCE_THRESHOLD", 0.6),
		WorkerConcurrency:   getenvInt("WORKER_CONCURRENCY", 4),
		Queue: queue.Config{
			MaxAttempts:  getenvInt("MAX_ATTEMPTS", 5),
			Lease:        getenvDuration("LEASE_TIMEOUT", 30*time.Second),
			BaseBackoff:  getenvDuration("BASE_BACKOFF", 2*time.Second),
			MaxBackoff:   getenvDuration("MAX_BACKOFF", 5*time.Minute),
			PollInterval: getenvDuration("POLL_INTERVAL", time.Second),
		},
		LogLevel: getenv("LOG_LEVEL", "info"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func getenvFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return v
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
