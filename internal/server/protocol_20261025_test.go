package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

func TestProtocolQueueDoesNotRotatePenalizeOrLeak(t *testing.T) {
	for _, code := range []int{6020, 6021} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				calls++
				return 429, fmt.Sprintf(`{"code":%d,"msg":"queue waiting fixture-secret"}`, code), false
			})
			p := testPoolWith(
				&auth.Auth{UID: "fixture-a", AccessToken: "fixture-a", ExpiresAt: 9999999999},
				&auth.Auth{UID: "fixture-b", AccessToken: "fixture-b", ExpiresAt: 9999999999})
			h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 3})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"cn:hy3","messages":[]}`)))
			if rec.Code != 503 || calls != 1 || strings.Contains(rec.Body.String(), "fixture-") || strings.Contains(rec.Body.String(), fmt.Sprint(code)) {
				t.Fatalf("queue response rotated or leaked internal data: status=%d calls=%d", rec.Code, calls)
			}
			for _, uid := range []string{"fixture-a", "fixture-b"} {
				st, _ := p.Status(uid)
				if st.Cooling || st.Disabled || st.ErrTotal != 0 || st.BreakerFails != 0 || st.InFlight != 0 {
					t.Fatalf("queue penalized account: %+v", st)
				}
			}
		})
	}
}

func TestProtocol6000KeepsFullModelResetAndOtherModels(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "fixture", AccessToken: "fixture", ExpiresAt: 9999999999})
	p.SetSoftRateMax(10 * time.Minute)
	h := NewHandler(Config{Pool: p})
	reset := time.Now().Add(6 * time.Hour).In(upstream.SoftRateResetLoc()).Truncate(time.Second)
	body := fmt.Sprintf(`{"code":6000,"msg":"您的使用量已超出频率限制，将在 %s UTC+8 重置，您也可以切换其他模型继续使用。"}`, reset.Format("2006-01-02 15:04:05"))
	h.applyErrorPolicy("fixture", upstream.Classify(429, body), body, "kimi-k3-1")
	if p.PickByUIDForModel("fixture", "kimi-k3-1") != nil || p.PickByUIDForModel("fixture", "hy3") == nil {
		t.Fatal("model-specific limit did not preserve other models")
	}
	st, _ := p.Status("fixture")
	if !st.Until.IsZero() || len(st.RateLimitedModels) != 1 || !st.RateLimitedModels[0].Until.Equal(reset) {
		t.Fatalf("model reset was capped or promoted to account cooldown: %+v", st)
	}
}
