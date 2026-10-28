package upstream

import (
	"net/http"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// v3Fixture 裁剪自 2026-10-21 实测 /v3/config 响应（只留网关用到的字段）。
// 含三类条目，正好覆盖「必须同时看 kind 与 factor」这个坑：
//   - discount + factor=0 + 窗口内   → hy3 此刻免费
//   - discount + factor=0 + 窗口已过 → step-5-preview 不算免费
//   - 无 kind/无 discount 的模型促销位（hy-image-3.5）→ 不算免费
const v3Fixture = `{"code":0,"msg":"OK","data":{
 "endpoint":"https://www.codebuddy.ai",
 "models":[
  {"id":"default-model","name":"Auto","maxInputTokens":176000,"maxOutputTokens":24000,"supportsImages":true},
  {"id":"hy3","name":"Hy3","maxInputTokens":176000,"maxOutputTokens":24000,"reasoning":{"effort":"high"}},
  {"id":"step-5-preview","name":"Step-5","maxInputTokens":128000,"maxOutputTokens":16000}
 ],
 "agents":[{"name":"cli","models":["default-model","hy3","step-5-preview"]}],
 "modelPromotions":[
  {"id":"hy3-free-trial-202608","kind":"discount","enabled":true,"modelIds":["hy3"],"priority":200,
   "discount":{"discountedCredits":"0x","displayMode":"replace","factor":0},
   "badge":{"label":"Free now"},
   "hover":{"textZh":"Aug 6 – Oct 31: Free daily use with unlimited credits."},
   "schedule":{"timezone":"Asia/Shanghai","validFrom":"2026-07-06T00:00:00+08:00","validUntil":"2026-11-01T00:00:00+08:00"}},
  {"id":"step-5-discount-daytime-badge-202610","kind":"discount","enabled":true,"modelIds":["step-5-preview"],"priority":50,
   "discount":{"discountedCredits":"0.00x","displayMode":"strikethrough","factor":0},
   "schedule":{"timezone":"Asia/Shanghai","validFrom":"2026-10-12T00:00:00+08:00","validUntil":"2026-10-26T00:00:00+08:00"}},
  {"id":"glm-52-night-discount-202607","kind":"discount","enabled":true,"modelIds":["glm-5.2"],"priority":10,
   "discount":{"discountedCredits":"0x","displayMode":"replace","factor":0},
   "hover":{"textZh":"每晚 23:00—次日 8:00 积分限时立减，错峰用更省"}},
  {"id":"hy-image-3.5","name":"HY-Image-3.5","tags":["text-to-image"]}
 ],
 "productFeaturesConfig":{"ModelRateLimitCap":{"lines":[
   {"displayName":"Deepseek-V4.1-Flash","freeId":"deepseek-v4.1-flash","paidId":"deepseek-v4.1-flash-sg"}
 ]}}
}}`

var (
	// 窗口内时刻：2026-10-21（hy3 免费、step-5-preview 也免费——它在 10-12~10-26 内）。
	inWindow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	// 窗口外时刻：2026-11-10（hy3 已过 11-01；step-5 已过 10-26）。
	outWindow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
)

// TestHasIntraDayWindow 时段活动的三种实测文案必须都被识别，全天免费文案不得误判。
func TestHasIntraDayWindow(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"每晚 23:00—次日 8:00 积分限时立减，错峰用更省", true},
		{"9月11日–10月31日，每晚 23:00—次日 8:00 享免费额度。", true},
		{"周一至周五 09:00–12:00、14:00–18:00 属高峰原价，非高峰期积分5折", true},
		{"7月6日–10月31日，每日赠送免费额度。", false},
		{"Aug 6 – Oct 31: Free daily use with unlimited credits.", false},
		{"10月2日-10月7日，享限时折扣。", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hasIntraDayWindow(c.text); got != c.want {
			t.Errorf("hasIntraDayWindow(%q)=%v want %v", c.text, got, c.want)
		}
	}
}

func TestParseV3MetaPromotions(t *testing.T) {
	meta := parseV3Meta([]byte(v3Fixture), "https://www.codebuddy.ai", inWindow)
	if meta == nil {
		t.Fatal("meta nil")
	}
	if meta.ModelCount != 3 || meta.AgentModelCount != 3 {
		t.Errorf("counts model=%d agent=%d want 3/3", meta.ModelCount, meta.AgentModelCount)
	}
	if meta.Endpoint != "https://www.codebuddy.ai" {
		t.Errorf("endpoint=%q", meta.Endpoint)
	}
	// 促销位（无 kind）不得混入免费集合。
	if len(meta.Promotions) != 4 {
		t.Fatalf("promotions=%d want 4 (含非折扣条目与时段活动)", len(meta.Promotions))
	}

	free := meta.FreeModels(inWindow)
	if !free["hy3"] || !free["step-5-preview"] {
		t.Errorf("窗口内 free=%v want hy3+step-5-preview", free)
	}
	// 时段活动（每晚 23:00-08:00，factor=0 且无 schedule）不得被当成全天免费。
	if free["glm-5.2"] {
		t.Errorf("时段活动被误判为全天免费: %v", free)
	}
	if free["hy-image-3.5"] || free["default-model"] {
		t.Errorf("非折扣条目被误判免费: %v", free)
	}

	// 窗口外：两条 discount 都过期 → 空集（不能因为 factor=0 就永久当免费）。
	if late := meta.FreeModels(outWindow); len(late) != 0 {
		t.Errorf("窗口外 free=%v want 空集", late)
	}
}

func TestDirectoryMetaPaidTwin(t *testing.T) {
	meta := parseV3Meta([]byte(v3Fixture), "", inWindow)
	if got := meta.PaidTwin("deepseek-v4.1-flash"); got != "deepseek-v4.1-flash-sg" {
		t.Errorf("free→paid=%q", got)
	}
	// 双向往返。
	if got := meta.PaidTwin("deepseek-v4.1-flash-sg"); got != "deepseek-v4.1-flash" {
		t.Errorf("paid→free=%q", got)
	}
	if got := meta.PaidTwin("hy3"); got != "" {
		t.Errorf("无映射应空, got=%q", got)
	}
	// nil 接收者不 panic。
	var nilMeta *DirectoryMeta
	if got := nilMeta.PaidTwin("hy3"); got != "" {
		t.Errorf("nil PaidTwin=%q", got)
	}
	if free := nilMeta.FreeModels(inWindow); len(free) != 0 {
		t.Errorf("nil FreeModels=%v", free)
	}
}

// TestFetchModelsV3StoresMeta /v3/config 成功时目录与元数据都要落：模型列表来自
// agent picker，限免/双轨可从 Client 查到。
func TestFetchModelsV3StoresMeta(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v3/config" {
			t.Errorf("path=%q want /v3/config", r.URL.Path)
		}
		return jsonResp(200, v3Fixture), nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "cn1", Domain: "www.codebuddy.cn"}

	infos, err := c.FetchModels(a)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(infos) != 3 || infos[1].ID != "hy3" {
		t.Fatalf("infos=%+v", infos)
	}
	if infos[0].ContextWindow != 176000 || infos[0].MaxTokens != 24000 {
		t.Errorf("上下文/输出未解析: %+v", infos[0])
	}

	meta := c.DirectoryMeta("cn")
	if meta == nil {
		t.Fatal("meta 未落盘")
	}
	if meta.ModelCount != 3 {
		t.Errorf("meta.ModelCount=%d", meta.ModelCount)
	}
	free := c.FreeModels("cn", inWindow)
	if !free["hy3"] {
		t.Errorf("FreeModels(cn)=%v want 含 hy3", free)
	}
	// 未知 realm 返回 nil，不 panic。
	if c.DirectoryMeta("intl") != nil {
		t.Errorf("未知 realm 应 nil")
	}
}
