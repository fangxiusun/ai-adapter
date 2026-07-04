package config

import (
	"strings"
	"testing"
)

func TestParseHeaderKeyCasePolicy(t *testing.T) {
	data := []byte(`
server:
  host: "127.0.0.1"
  port: 8080
database:
  path: "./data/test.db"
headers:
  request:
    enabled: true
    key_case_policy: configured
    pass:
      - "x-custom-auth"
channels:
  - id: "test"
    name: "Test"
    enabled: true
    chat_url: "https://example.com"
    models:
      - id: "model"
    keys:
      - value: "sk-test"
`)

	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got := cfg.Headers.Request.KeyCasePolicy; got != KeyCaseConfigured {
		t.Fatalf("expected key_case_policy configured, got %q", got)
	}
}

func TestParseHeaderKeyCasePolicyRejectsInvalidValue(t *testing.T) {
	data := []byte(`
server:
  host: "127.0.0.1"
  port: 8080
database:
  path: "./data/test.db"
headers:
  request:
    enabled: true
    key_case_policy: original_wire_case
    pass:
      - "x-custom-auth"
channels:
  - id: "test"
    name: "Test"
    enabled: true
    chat_url: "https://example.com"
    models:
      - id: "model"
    keys:
      - value: "sk-test"
`)

	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected invalid key_case_policy to fail")
	}
	if !strings.Contains(err.Error(), "key_case_policy") {
		t.Fatalf("expected key_case_policy error, got %v", err)
	}
}
