package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScaleProtocolNativeRequestOptions(t *testing.T) {
	cases := []struct {
		name   string
		body   object
		reason string
	}{
		{"low_verbosity", object{"text": object{"verbosity": "low"}}, "text_verbosity"},
		{"high_verbosity", object{"text": object{"verbosity": "high"}}, "text_verbosity"},
		{"reasoning_summary", object{"reasoning": object{"summary": "detailed"}}, "reasoning_summary"},
		{"no_reasoning_summary", object{"reasoning": object{"summary": "none"}}, "reasoning_summary"},
		{"reasoning_context", object{"reasoning": object{"context": "current_turn"}}, "reasoning_context"},
		{"reasoning_budget", object{"reasoning": object{"effort": json.Number("12000")}}, "reasoning_effort"},
		{"reasoning_none", object{"reasoning": object{"effort": "none"}}, "reasoning_effort"},
		{"reasoning_minimal", object{"reasoning": object{"effort": "minimal"}}, "reasoning_effort"},
		{"additional_include", object{"include": []any{"reasoning.encrypted_content", "output_text.logprobs"}}, "include"},
		{"defaults", object{"text": object{"verbosity": "medium"}, "reasoning": object{"summary": "auto", "context": "auto", "effort": "high"}, "include": []any{"reasoning.encrypted_content"}, "background": false}, ""},
		{"nulls", object{"text": object{"verbosity": nil}, "reasoning": object{"summary": nil, "context": nil, "effort": nil}, "include": nil}, ""},
		{"agent_message", object{"input": []any{object{"type": "agent_message", "author": "one", "recipient": "two", "content": []any{}}}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.body)
			original := string(raw)
			if reason := NativeFallbackReason(raw); reason != tc.reason {
				t.Fatalf("got %q want %q", reason, tc.reason)
			}
			if string(raw) != original {
				t.Fatal("routing mutated source JSON")
			}
		})
	}
}

func TestScaleProtocolIgnoredParametersAreNamesOnly(t *testing.T) {
	raw, _ := json.Marshal(object{"max_output_tokens": 42, "temperature": 0, "top_p": 0.9, "frequency_penalty": 0, "presence_penalty": 1, "truncation": "auto", "stop_sequences": []any{"PRIVATE_TOKEN"}, "store": true, "background": false, "metadata": object{"private": "PRIVATE_TOKEN"}})
	names := IgnoredParameterNames(raw)
	want := "max_output_tokens,temperature,top_p,presence_penalty,truncation,stop_sequences,store"
	if strings.Join(names, ",") != want {
		t.Fatalf("got %v, want fixed protocol names %s", names, want)
	}
	if strings.Contains(strings.Join(names, ","), "PRIVATE_TOKEN") {
		t.Fatal("diagnostics leaked caller data")
	}
	for _, body := range []string{"{}", "null", "invalid", "{\"max_output_tokens\":null,\"temperature\":null,\"frequency_penalty\":0,\"presence_penalty\":0,\"truncation\":\"disabled\",\"store\":false,\"background\":false}"} {
		if got := IgnoredParameterNames([]byte(body)); len(got) != 0 {
			t.Fatalf("defaults generated spurious ignored-parameter diagnostics: %v", got)
		}
	}
}
