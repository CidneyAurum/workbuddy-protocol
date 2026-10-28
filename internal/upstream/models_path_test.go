package upstream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// modelsBodyCLI 构造 /enterprises/personal/models 形态响应（models + cli agent），
// 含 maxInputTokens/maxOutputTokens（供 modelList 输出真实 context_length/max_output_tokens）。
func modelsBodyCLI(ids ...string) string {
	var sb strings.Builder
	sb.WriteString(`{"code":0,"data":{"agents":[{"name":"cli","models":[`)
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"` + id + `"`)
	}
	sb.WriteString(`]}],"models":[`)
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":"` + id + `","name":"` + id + `","maxInputTokens":65536,"maxOutputTokens":8192,"disabled":false}`)
	}
	sb.WriteString(`]}}`)
	return sb.String()
}

// TestModelsPathCNV3First 2026-10 目录换源：CN 首选 /v3/config（models+agents 同形，
// 且目录名单已比 /console 新——CN 多出付费档 hy3-x）。
func TestModelsPathCNV3First(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	var gotPath string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		return jsonResp(200, modelsBodyCLI("glm-5.2")), nil
	})
	cn := &auth.Auth{AccessToken: "at", UID: "cn1", Domain: "www.codebuddy.cn"}

	if _, err := c.FetchModels(cn); err != nil {
		t.Fatalf("cn fetch models: %v", err)
	}
	if gotPath != "/v3/config" {
		t.Errorf("cn FetchModels path=%q want /v3/config（2026-10 起主目录源）", gotPath)
	}
}

// TestModelsPathCNConsoleFallback /v3/config 不可用（旧部署/灰度回滚）时回落
// /console/enterprises/personal/models，保证可用性只增不减。
func TestModelsPathCNConsoleFallback(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	var calls []string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/v3/config" {
			return jsonResp(500, `{"code":500,"msg":"not deployed"}`), nil
		}
		return jsonResp(200, modelsBodyCLI("glm-5.2")), nil
	})
	cn := &auth.Auth{AccessToken: "at", UID: "cn1", Domain: "www.codebuddy.cn"}

	if _, err := c.FetchModels(cn); err != nil {
		t.Fatalf("cn fetch models: %v", err)
	}
	want := []string{"/v3/config", "/console/enterprises/personal/models"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("calls=%v want %v", calls, want)
	}
}

func TestModelsPathGlobalV2AfterV3(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v3/config" {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"code":500,"msg":"boom"}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(modelsBodyCLI("gpt-5.4")))
	}))
	defer srv.Close()

	c := &Client{
		HTTP:           http.DefaultClient,
		ChatBaseGlobal: strings.TrimSuffix(srv.URL, "/"),
		GlobalEnabled:  true,
	}
	if _, err := c.FetchModels(globalAcct()); err != nil {
		t.Fatalf("global fetch models: %v", err)
	}
	// global：/v3/config 首选，失败后仍走 /v2 家族（PR #20 实测 /console 500 → /v2 200）。
	if len(calls) != 2 || calls[0] != "/v3/config" || calls[1] != "/v2/enterprises/personal/models" {
		t.Fatalf("global calls=%v want [/v3/config /v2/enterprises/personal/models]", calls)
	}
}

func TestGlobalModelsProbeV2First(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	// /v3/config 404 → /v2 首选返回 200，/console 也应可达（fallback），断言打到 /v2 即止。
	var calls []string
	srv := globalModelsSrv(t, &calls, nil, func(path string) (int, string) {
		switch path {
		case "/v2/enterprises/personal/models":
			return 200, modelsResp("gpt-5.4", "probe-x")
		case "/console/enterprises/personal/models":
			return 500, `{"code":500,"msg":"boom"}`
		}
		return 404, `{"code":404,"msg":"nope"}`
	})
	defer srv.Close()

	got := globalModelsClient(t, srv).FetchGlobalModels(globalAcct())

	// 2026-10 起 /v3/config 是主目录源（先探一次，404 则回落），故首跳是 /v3/config。
	if len(calls) != 2 || calls[0] != "/v3/config" || calls[1] != "/v2/enterprises/personal/models" {
		t.Fatalf("probe calls=%v want [/v3/config /v2/enterprises/personal/models] (v3 优先, v2 兜底)", calls)
	}
	counts := map[string]int{}
	for _, id := range got {
		counts[id]++
	}
	if counts["probe-x"] != 1 || counts["gpt-5.4"] != 1 {
		t.Fatalf("probe names not merged: %v", counts)
	}
}

func TestGlobalModelsProbeConsoleFallback(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	// /v2 失败 → fallback /console 成功（探活顺序正确：新路径优先，旧路径兜底）。
	var calls []string
	srv := globalModelsSrv(t, &calls, nil, func(path string) (int, string) {
		switch path {
		case "/v2/enterprises/personal/models":
			return 500, `{"code":500,"msg":"boom"}`
		case "/console/enterprises/personal/models":
			return 200, modelsResp("legacy-only")
		}
		return 404, `{"code":404,"msg":"nope"}`
	})
	defer srv.Close()

	got := globalModelsClient(t, srv).FetchGlobalModels(globalAcct())

	if len(calls) != 3 ||
		calls[0] != "/v3/config" ||
		calls[1] != "/v2/enterprises/personal/models" ||
		calls[2] != "/console/enterprises/personal/models" {
		t.Fatalf("fallback calls=%v want [/v3/config, /v2/..., /console/...]", calls)
	}
	counts := map[string]int{}
	for _, id := range got {
		counts[id]++
	}
	if counts["legacy-only"] != 1 {
		t.Errorf("console fallback name not merged: %v", counts)
	}
}

// TestFetchGlobalModelsProbeSoundNoPanic probePaths 顺序测试（探测成功后调用）为
// /console 旧路径取消的响应；本测试占位保 probe 代码继续编译（无函数体引用被删）。
func TestGlobalModelsProbePathsOrderStable(t *testing.T) {
	if len(globalModelsProbePaths) != 2 {
		t.Fatalf("globalModelsProbePaths=%v want 2 entries (v2 + console fallback)", globalModelsProbePaths)
	}
	if globalModelsProbePaths[0] != "/v2/enterprises/personal/models" {
		t.Errorf("probePaths[0]=%q want /v2/enterprises/personal/models (newest first)", globalModelsProbePaths[0])
	}
	if globalModelsProbePaths[1] != "/console/enterprises/personal/models" {
		t.Errorf("probePaths[1]=%q want /console/enterprises/personal/models (fallback)", globalModelsProbePaths[1])
	}
}
