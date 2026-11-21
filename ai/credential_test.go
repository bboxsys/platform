package ai

import "testing"

func TestOllamaBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                               OllamaDefaultEndpoint,
		"  ":                             OllamaDefaultEndpoint,
		"http://localhost:11434":         "http://localhost:11434",
		"http://localhost:11434/":        "http://localhost:11434",
		"http://localhost:11434/api":     "http://localhost:11434",
		"http://localhost:11434/api/":    "http://localhost:11434",
		"http://localhost:11434/v1":      "http://localhost:11434",
		"https://llm.example/ollama/v1/": "https://llm.example/ollama",
		"https://llm.example/apiary":     "https://llm.example/apiary",
	}
	for in, want := range cases {
		if got := OllamaBaseURL(in); got != want {
			t.Errorf("OllamaBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
