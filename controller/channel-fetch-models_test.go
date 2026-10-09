package controller

import "testing"

func strPtr(s string) *string { return &s }

// The core rule: a proxy saved on the channel must be used by the model probe,
// and an explicit form value (including explicitly clearing it) must win.
func TestResolveProbeProxy(t *testing.T) {
	const stored = "http://127.0.0.1:7890"

	cases := []struct {
		name     string
		form     *string
		stored   string
		expected string
	}{
		{
			name:     "caller silent uses stored channel proxy",
			form:     nil,
			stored:   stored,
			expected: stored,
		},
		{
			name:     "caller silent and nothing stored stays direct",
			form:     nil,
			stored:   "",
			expected: "",
		},
		{
			name:     "explicit form proxy wins over stored",
			form:     strPtr("socks5://127.0.0.1:1080"),
			stored:   stored,
			expected: "socks5://127.0.0.1:1080",
		},
		{
			name:     "explicitly cleared form proxy forces a direct probe",
			form:     strPtr(""),
			stored:   stored,
			expected: "",
		},
		{
			name:     "whitespace-only form value counts as cleared",
			form:     strPtr("   "),
			stored:   stored,
			expected: "",
		},
		{
			name:     "form value is trimmed",
			form:     strPtr("  http://127.0.0.1:8080  "),
			stored:   stored,
			expected: "http://127.0.0.1:8080",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveProbeProxy(tc.form, tc.stored); got != tc.expected {
				t.Fatalf("want %q, got %q", tc.expected, got)
			}
		})
	}
}

// The probe URL builder must join /v1/models correctly for a base URL that
// already carries /v1, which is the normal OpenAI-compatible configuration.
func TestBuildModelsProbeJoinsCorrectly(t *testing.T) {
	url, headers := buildModelsProbe(50, "https://api.example.com/v1", "sk-test")
	if url != "https://api.example.com/v1/models" {
		t.Fatalf("openai-compatible URL want .../v1/models, got %s", url)
	}
	if headers["Authorization"] != "Bearer sk-test" {
		t.Fatalf("openai-compatible must use bearer auth, got %v", headers)
	}

	url, headers = buildModelsProbe(14, "https://api.anthropic.com", "sk-ant")
	if url != "https://api.anthropic.com/v1/models" {
		t.Fatalf("anthropic URL want .../v1/models, got %s", url)
	}
	if headers["x-api-key"] != "sk-ant" {
		t.Fatalf("anthropic must use x-api-key auth, got %v", headers)
	}

	url, _ = buildModelsProbe(1, "https://api.openai.com", "sk-x")
	if url != "https://api.openai.com/v1/models" {
		t.Fatalf("openai URL want .../v1/models, got %s", url)
	}
}
