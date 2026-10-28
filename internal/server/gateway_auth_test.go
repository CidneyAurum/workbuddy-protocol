package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func gatewayAuthFixture(t *testing.T, refreshStatus int, refreshBody string) (*Handler, *auth.Auth, *int) {
	t.Helper()
	a := &auth.Auth{UID: "fixture-account", AccessToken: "fixture-old", RefreshToken: "fixture-refresh",
		ExpiresAt: 9999999999, FilePath: filepath.Join(t.TempDir(), "auth.json")}
	refreshCalls := new(int)
	up := &upstream.Client{ChatBaseCN: "https://fake.example", HTTP: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			status, body := 401, "<html>401 Authorization Required</html>"
			if strings.HasSuffix(r.URL.Path, "/auth/token/refresh") {
				*refreshCalls++
				status, body = refreshStatus, refreshBody
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	}}
	h := NewHandler(Config{Pool: testPoolWith(a), Upstream: up})
	t.Cleanup(func() { h.cfg.Pool.Close() })
	return h, a, refreshCalls
}

func TestGateway401RefreshPersistsAndIsBounded(t *testing.T) {
	h, a, calls := gatewayAuthFixture(t, 200, `{"code":0,"data":{"accessToken":"fixture-new","refreshToken":"fixture-new-refresh","expiresIn":3600}}`)
	h.cfg.Pool.NoteSessionDead(a.UID)
	h.cfg.Pool.NoteSessionDead(a.UID)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"cn:deepseek-v4.1-flash","messages":[{"role":"user","content":"test"}]}`)))
		if rec.Code != 503 || strings.Contains(rec.Body.String(), "fixture-") {
			t.Fatalf("unexpected or credential-leaking response: code=%d", rec.Code)
		}
	}
	if *calls != 1 {
		t.Fatalf("refresh repeated during cooldown: got %d calls", *calls)
	}
	raw, readErr := os.ReadFile(a.FilePath)
	if readErr != nil {
		t.Fatal("persisted auth file missing")
	}
	got, err := auth.Parse(raw)
	if err != nil || got.AccessToken != "fixture-new" || got.RefreshToken != "fixture-new-refresh" {
		t.Fatal("successful refresh was not persisted")
	}
	if h.cfg.Pool.NoteSessionDead(a.UID) {
		t.Fatal("successful refresh failed to clear prior session-dead evidence")
	}
	if st, _ := h.cfg.Pool.Status(a.UID); st.Disabled {
		t.Fatal("edge 401 disabled a refreshable account")
	}
}

func TestGateway401OnlyExplicitRefreshVerdictCounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		dead   bool
	}{
		{"edge401", 401, "<html>401 Authorization Required</html>", false},
		{"edge403", 403, "<html>Forbidden</html>", false},
		{"rate", 429, `{"code":10001,"msg":"rate limited"}`, false},
		{"server", 550, "", false},
		{"invalid_grant", 400, `{"code":400,"msg":"invalid_grant"}`, true},
		{"offline", 401, `{"code":12153,"msg":"Offline user session not found"}`, true},
		{"offline_http200", 200, `{"code":12153,"msg":"Offline user session not found"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, a, calls := gatewayAuthFixture(t, tc.status, tc.body)
			for i := 0; i < pool.SessionDeadThreshold(); i++ {
				h.cfg.Pool.ReenableIfCredits(a.UID, 1)
				if !h.recoverGatewayAuth(a, 401, "<html>Authorization Required</html>") {
					t.Fatal("edge 401 not handled")
				}
			}
			st, _ := h.cfg.Pool.Status(a.UID)
			if st.Disabled != tc.dead {
				t.Fatalf("disabled=%v want %v after %d checks", st.Disabled, tc.dead, *calls)
			}
		})
	}
}

func TestGateway401ConcurrentRecoveryIsSingleFlight(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"data":{"accessToken":"fixture-new","refreshToken":"fixture-new-refresh","expiresIn":3600}}`,
		`{"code":12153,"msg":"Offline user session not found"}`,
	} {
		t.Run(body[:9], func(t *testing.T) {
			a := &auth.Auth{UID: "concurrent", AccessToken: "fixture-old", RefreshToken: "fixture-refresh", ExpiresAt: 9999999999, FilePath: filepath.Join(t.TempDir(), "auth.json")}
			started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			up := &upstream.Client{ChatBaseCN: "https://fake.example", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					close(started)
				}
				<-release
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			h := NewHandler(Config{Pool: testPoolWith(a), Upstream: up})
			go func() { defer close(finished); h.recoverGatewayAuth(a, 401, "unauthorized") }()
			<-started
			var followers sync.WaitGroup
			for i := 0; i < 10; i++ {
				followers.Add(1)
				go func() { defer followers.Done(); h.recoverGatewayAuth(a, 401, "unauthorized") }()
			}
			followers.Wait()
			close(release)
			<-finished
			if calls.Load() != 1 {
				t.Fatalf("concurrent refresh calls=%d want 1", calls.Load())
			}
			if st, _ := h.cfg.Pool.Status(a.UID); st.Disabled {
				t.Fatal("one concurrent incident exhausted session-dead threshold")
			}
		})
	}
}

func TestGateway401DoesNotDisableStaticCredentials(t *testing.T) {
	h, a, calls := gatewayAuthFixture(t, 400, "")
	a.RefreshToken = ""
	for i := 0; i < pool.SessionDeadThreshold()+1; i++ {
		h.cfg.Pool.ReenableIfCredits(a.UID, 1)
		h.recoverGatewayAuth(a, 401, "unauthorized")
	}
	if st, _ := h.cfg.Pool.Status(a.UID); st.Disabled || *calls != 0 {
		t.Fatal(fmt.Sprintf("static credential misclassified, disabled=%v calls=%d", st.Disabled, *calls))
	}
}
