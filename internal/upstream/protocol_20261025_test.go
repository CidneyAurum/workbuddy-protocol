package upstream

import (
	"net/http"
	"testing"

	"workbuddy2api/internal/auth"
)

func TestProtocolGlobalIssuerHostsAndHeaders(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	c := New()
	c.GlobalEnabled = true
	c.ChatBaseGlobal, c.BillingBaseGlobal = "", ""
	for _, tc := range []struct{ domain, base string }{
		{"www.codebuddy.ai", "https://www.codebuddy.ai"},
		{"codebuddy.ai", "https://www.codebuddy.ai"},
		{"www.workbuddy.ai", "https://www.workbuddy.ai"},
		{"workbuddy.ai", "https://www.workbuddy.ai"},
	} {
		a := &auth.Auth{UID: "fixture", Domain: tc.domain, AccessToken: "fixture"}
		if a.Realm() != "global" || c.chatBase(a) != tc.base || c.billingBase(a) != tc.base {
			t.Fatalf("issuer host or realm mismatch for %s", tc.domain)
		}
		r, _ := http.NewRequest("POST", tc.base+"/console/chat/completions", nil)
		c.ChatHeaders(r, a, "", ChatMeta{})
		if r.Header.Get("Origin") != tc.base || r.Header.Get("Referer") != tc.base+"/" {
			t.Fatalf("cross-site origin for %s", tc.domain)
		}
		if want := tc.base[len("https://"):]; r.Header.Get("X-Domain") != want {
			t.Fatalf("wrong X-Domain for %s", tc.domain)
		}
	}
	// Old manual --realm=global credentials without an issuer domain keep their
	// legacy default. Explicit new intl credentials route by codebuddy.ai above.
	manual, err := auth.Parse([]byte(`{"accessToken":"fixture","realm":"global"}`))
	if err != nil || c.chatBase(manual) != "https://www.workbuddy.ai" || c.billingBase(manual) != "https://www.workbuddy.ai" || originRefererFor(manual) != "https://www.workbuddy.ai" {
		t.Fatal("blank-domain manual global credential changed sites")
	}
	c.ChatBaseGlobal, c.BillingBaseGlobal = "https://chat.override", "https://billing.override"
	a := &auth.Auth{Domain: "www.codebuddy.ai"}
	if c.chatBase(a) != c.ChatBaseGlobal || c.billingBase(a) != c.BillingBaseGlobal {
		t.Fatal("explicit deployment endpoint override lost")
	}
}

func TestProtocolQueueCodesAreChannelCapacity(t *testing.T) {
	for _, body := range []string{
		`{"code":6020,"msg":"queue.waiting.title"}`,
		`{"code" : "6021", "msg":"queue.full.title: quota exceeded"}`,
	} {
		for _, status := range []int{429, 500} {
			if got := Classify(status, body); got != ErrUpstreamBusy {
				t.Fatalf("queue misclassified: %s status=%d kind=%s", body, status, got)
			}
		}
	}
	for _, body := range []string{`{"requestId":"6020","msg":"rate limit"}`, `{"code":60201,"msg":"rate limit"}`} {
		if got := Classify(429, body); got != ErrSoftRate {
			t.Fatalf("incidental queue code matched: %s kind=%s", body, got)
		}
	}
}

func TestProtocol6000RequiresModelSwitchHint(t *testing.T) {
	if !IsModelRateLimit(`{"code":"6000","msg":"您也可以切换其他模型继续使用"}`) {
		t.Fatal("explicit model-specific 6000 ignored")
	}
	for _, body := range []string{
		`{"code":6000,"msg":"账号并发超限"}`,
		`{"code":60001,"msg":"切换其他模型"}`,
		`{"requestId":"6000","msg":"切换其他模型"}`,
	} {
		if IsModelRateLimit(body) {
			t.Fatalf("account or incidental limit incorrectly broadened: %s", body)
		}
	}
}
