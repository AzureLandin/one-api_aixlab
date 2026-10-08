package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCtxWithHeaders(h map[string]string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c
}

func TestExtractTokenKey_PrefersAPIKey(t *testing.T) {
	c := newCtxWithHeaders(map[string]string{
		"x-api-key":     "sk-aaa-1",
		"Authorization": "Bearer sk-aaa-1",
	})
	key, err := extractTokenKey(c)
	if err != nil {
		t.Fatalf("unexpected: %s", err)
	}
	if key != "sk-aaa-1" {
		t.Fatalf("key = %q", key)
	}
}

func TestExtractTokenKey_FallsBackToBearer(t *testing.T) {
	c := newCtxWithHeaders(map[string]string{
		"Authorization": "Bearer sk-bbb-2",
	})
	key, err := extractTokenKey(c)
	if err != nil {
		t.Fatalf("unexpected: %s", err)
	}
	if key != "sk-bbb-2" {
		t.Fatalf("key = %q", key)
	}
}

func TestExtractTokenKey_XAPIKeyOnly(t *testing.T) {
	c := newCtxWithHeaders(map[string]string{
		"x-api-key": "sk-only",
	})
	key, err := extractTokenKey(c)
	if err != nil {
		t.Fatalf("unexpected: %s", err)
	}
	if key != "sk-only" {
		t.Fatalf("key = %q", key)
	}
}

// §6.5 rule 3: when both headers are present but point to different tokens,
// reject deterministically instead of randomly picking one.
func TestExtractTokenKey_ConflictRejected(t *testing.T) {
	c := newCtxWithHeaders(map[string]string{
		"x-api-key":     "sk-xxx",
		"Authorization": "Bearer sk-yyy",
	})
	_, err := extractTokenKey(c)
	if err == nil {
		t.Fatalf("expected conflict rejection")
	}
}

// Same token expressed via both headers (Bearer prefix / channel selector
// differ) must NOT be treated as a conflict.
func TestExtractTokenKey_EquivalentNotConflict(t *testing.T) {
	c := newCtxWithHeaders(map[string]string{
		"x-api-key":     "sk-secret-5",
		"Authorization": "Bearer sk-secret-9",
	})
	_, err := extractTokenKey(c)
	if err != nil {
		t.Fatalf("same token ref must not conflict: %s", err)
	}
}

func TestExtractTokenKey_NonePresent(t *testing.T) {
	c := newCtxWithHeaders(nil)
	key, err := extractTokenKey(c)
	if err != nil {
		t.Fatalf("no headers should not error (fails later at validate): %s", err)
	}
	if key != "" {
		t.Fatalf("expected empty key, got %q", key)
	}
}
