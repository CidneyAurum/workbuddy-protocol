package upstream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// consoleStub 模拟官网控制台端点，记录最后一次请求的路径/头/方法。
type consoleStub struct {
	path     string
	body     string
	status   int
	hits     int
	lastPath string
	lastPlat string
	lastAuth string
}

func (s *consoleStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits++
		s.lastPath = r.URL.Path
		s.lastPlat = r.Header.Get("X-Client-Platform")
		s.lastAuth = r.Header.Get("Authorization")
		if s.path != "" && r.URL.Path != s.path {
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404,"msg":"not found"}`))
			return
		}
		if s.status >= 400 {
			w.WriteHeader(s.status)
		}
		w.Write([]byte(s.body))
	})
}

// newConsoleClient 建一个指向 stub 的客户端（CN 域，13 个真实号全是 CN）。
func newConsoleClient(t *testing.T, stub *consoleStub) (*Client, *auth.Auth) {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	c := &Client{
		HTTP:              srv.Client(),
		BillingBaseCN:     srv.URL,
		BillingBaseGlobal: srv.URL,
	}
	return c, &auth.Auth{UID: "cn1", AccessToken: "at", Domain: "www.codebuddy.cn"}
}

// TestConsoleJSONInjectsWebPlatform 控制台端点必须带 X-Client-Platform: web
// （§四/§五 实测口径）——这是与 billingJSON 的唯一差别，漂移了就必须能红。
func TestConsoleJSONInjectsWebPlatform(t *testing.T) {
	stub := &consoleStub{path: compensationStatusPath, body: `{"code":0,"msg":"OK","data":{"claimed":true}}`}
	c, a := newConsoleClient(t, stub)

	if _, err := c.CompensationStatus(a); err != nil {
		t.Fatalf("err=%v", err)
	}
	if stub.lastPlat != "web" {
		t.Errorf("X-Client-Platform=%q want web", stub.lastPlat)
	}
	if stub.lastPath != compensationStatusPath {
		t.Errorf("path=%q want %s", stub.lastPath, compensationStatusPath)
	}
	if stub.lastAuth != "Bearer at" {
		t.Errorf("auth=%q want Bearer at", stub.lastAuth)
	}
}

// TestCompensationStatusDecoded 解实测形状 {claimed,active,credit_num,
// validity_period(数字/天),end_time}。
func TestCompensationStatusDecoded(t *testing.T) {
	stub := &consoleStub{body: `{"code":0,"msg":"OK","data":{"claimed":false,"active":true,"credit_num":500,"validity_period":1,"end_time":"2026-10-22 23:59:59"}}`}
	c, a := newConsoleClient(t, stub)

	st, err := c.CompensationStatus(a)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if st.Claimed == nil || *st.Claimed {
		t.Errorf("claimed=%v want 明确 false", st.Claimed)
	}
	if st.CreditNum != 500 || st.EndTime == "" {
		t.Errorf("credit_num=%d end_time=%q 未解析", st.CreditNum, st.EndTime)
	}
}

// TestCompensationStatusNullData data 为 null（从未发过）不得报错，Claimed 保持 nil。
func TestCompensationStatusNullData(t *testing.T) {
	stub := &consoleStub{body: `{"code":0,"msg":"OK"}`}
	c, a := newConsoleClient(t, stub)

	st, err := c.CompensationStatus(a)
	if err != nil {
		t.Fatalf("err=%v（空 data 不应失败）", err)
	}
	if st.Claimed != nil {
		t.Errorf("claimed=%v want nil（上游未给字段）", st.Claimed)
	}
}

// TestClaimCompensationSuccess 200 + code=0 → claimed=true，并解析发放数。
func TestClaimCompensationSuccess(t *testing.T) {
	stub := &consoleStub{body: `{"code":0,"msg":"OK","requestId":"e0ca4be4","data":{"credit_num":500}}`}
	c, a := newConsoleClient(t, stub)

	claimed, credit, err := c.ClaimCompensation(a)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !claimed {
		t.Error("claimed=false want true")
	}
	if credit != 500 {
		t.Errorf("credit=%d want 500", credit)
	}
	if stub.lastPath != claimCompensationPath {
		t.Errorf("path=%q want %s", stub.lastPath, claimCompensationPath)
	}
}

// TestClaimCompensationAlreadyHTTP400 实测幂等形状：400 + code=10001 +
// 「每人限领一次，您已领取过无法重复领取」→ (false,nil)，调用方输出 ALREADY。
func TestClaimCompensationAlreadyHTTP400(t *testing.T) {
	stub := &consoleStub{
		status: 400,
		body:   `{"code":10001,"msg":"每人限领一次，您已领取过无法重复领取"}`,
	}
	c, a := newConsoleClient(t, stub)

	claimed, _, err := c.ClaimCompensation(a)
	if err != nil {
		t.Fatalf("err=%v（已领取过应判幂等）", err)
	}
	if claimed {
		t.Error("claimed=true want false")
	}
}

// TestClaimCompensationAlreadyEnvelope200 同文案走 200 + code!=0 通道也要判幂等。
func TestClaimCompensationAlreadyEnvelope200(t *testing.T) {
	stub := &consoleStub{body: `{"code":10001,"msg":"每人限领一次，您已领取过无法重复领取"}`}
	c, a := newConsoleClient(t, stub)

	claimed, _, err := c.ClaimCompensation(a)
	if err != nil {
		t.Fatalf("err=%v（信封幂等码应判已领）", err)
	}
	if claimed {
		t.Error("claimed=true want false")
	}
}

// TestClaimCompensationRealError 其他业务码不得被当成幂等吞掉。
func TestClaimCompensationRealError(t *testing.T) {
	stub := &consoleStub{status: 500, body: `{"code":50000,"msg":"internal error"}`}
	c, a := newConsoleClient(t, stub)

	claimed, _, err := c.ClaimCompensation(a)
	if err == nil {
		t.Fatal("err=nil want error")
	}
	if claimed {
		t.Error("claimed=true want false")
	}
	if !strings.Contains(err.Error(), "50000") {
		t.Errorf("err=%v 应含业务码 50000", err)
	}
}

// TestClaimGiftAlready / success 两条主路径。
func TestClaimGiftSuccessAndAlready(t *testing.T) {
	stub := &consoleStub{body: `{"code":0,"msg":"OK"}`}
	c, a := newConsoleClient(t, stub)
	if claimed, err := c.ClaimGift(a); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v want true/nil", claimed, err)
	}

	stub2 := &consoleStub{status: 400, body: `{"code":10001,"msg":"已领取过"}`}
	c2, a2 := newConsoleClient(t, stub2)
	if claimed, err := c2.ClaimGift(a2); err != nil || claimed {
		t.Fatalf("claimed=%v err=%v want false/nil", claimed, err)
	}
}

// TestCheckGiftClaimedParsesVariants 礼品字段未定稿：多种候选键名都要能识别。
func TestCheckGiftClaimedParsesVariants(t *testing.T) {
	cases := []struct {
		body string
		want *bool
	}{
		{`{"code":0,"data":{"claimed":true}}`, boolPtr(true)},
		{`{"code":0,"data":{"isClaimed":false}}`, boolPtr(false)},
		{`{"code":0,"data":{"gift_claimed":true}}`, boolPtr(true)},
		{`{"code":0,"data":{"foo":1}}`, nil},
		{`{"code":0,"msg":"OK"}`, nil},
	}
	for _, tc := range cases {
		stub := &consoleStub{body: tc.body}
		c, a := newConsoleClient(t, stub)
		st, err := c.CheckGiftClaimed(a)
		if err != nil {
			t.Fatalf("body=%s err=%v", tc.body, err)
		}
		switch {
		case tc.want == nil && st.Claimed != nil:
			t.Errorf("body=%s claimed=%v want nil", tc.body, *st.Claimed)
		case tc.want != nil && (st.Claimed == nil || *st.Claimed != *tc.want):
			t.Errorf("body=%s claimed=%v want %v", tc.body, st.Claimed, *tc.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
