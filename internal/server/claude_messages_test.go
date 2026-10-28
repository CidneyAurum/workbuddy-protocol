package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

func TestClaudeMessagesRouteAuthAndStream(t *testing.T) {
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return 200, sseOK, true
	})
	p := testPoolWith(&auth.Auth{UID: "fixture", AccessToken: "fixture", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, APIKey: "fixture-client"})
	for _, stream := range []string{"true", "false"} {
		r := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(`{"model":"cn:glm-5.2","max_tokens":128,"stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`))
		r.Header.Set("x-api-key", "fixture-client")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "你好") || !strings.Contains(w.Body.String(), "end_turn") {
			t.Fatalf("%d: %s", w.Code, w.Body)
		}
		if stream == "true" && !strings.Contains(w.Body.String(), "message_stop") {
			t.Fatal(w.Body)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
