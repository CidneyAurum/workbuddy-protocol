package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

func TestCatalogRetainsRemoteLimitsAndTierByRealm(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	resetModelsCache()
	t.Cleanup(resetModelsCache)
	up := &upstream.Client{GlobalEnabled: true, ChatBaseCN: "https://cn.example", ChatBaseGlobal: "https://global.example"}
	up.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{"models":[{"id":"primary-model","maxInputTokens":777777,"maxOutputTokens":22222,"supportsImages":true,"reasoning":{"effort":"high"}}],"agents":[{"name":"cli","models":["primary-model"]}]}}`
		if r.URL.Host == "cn.example" {
			body = `{"code":0,"data":{"models":[{"id":"primary-model","maxInputTokens":555555,"maxOutputTokens":11111}],"agents":[{"name":"cli","models":["primary-model"]}],"modelTiers":[{"enabled":true,"priority":10,"modelIds":["primary-model"],"badge":{"label":"订阅优先"}}]}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p := testPoolWith(
		&auth.Auth{UID: "cn", AccessToken: "fixture-cn", Domain: "www.codebuddy.cn", ExpiresAt: 9999999999},
		&auth.Auth{UID: "global", AccessToken: "fixture-global", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var result struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]any{}
	for _, m := range result.Data {
		seen[m["id"].(string)] = m
	}
	for _, tc := range []struct {
		id, tier        string
		context, output float64
	}{
		{"cn:primary-model", "订阅优先", 555555, 11111},
		{"global:primary-model", "Pro Only", 777777, 22222},
	} {
		m := seen[tc.id]
		if m["context_length"] != tc.context || m["max_output_tokens"] != tc.output || m["tier"] != tc.tier {
			t.Fatalf("wrong capability metadata for %s: %v", tc.id, m)
		}
	}
	if seen["global:primary-model"]["supports_images"] != true || seen["global:primary-model"]["reasoning_default_effort"] != "high" {
		t.Fatal("global image/default-effort metadata lost")
	}
}
