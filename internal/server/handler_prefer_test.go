package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/session"
)

type preferTestResponse struct {
	UIDs         []string        `json:"prefer_uids"`
	Mode         string          `json:"prefer_mode"`
	Exact        bool            `json:"prefer_exact"`
	Enabled      bool            `json:"enabled"`
	Policy       int             `json:"policy_version"`
	Capabilities map[string]bool `json:"capabilities"`
}

func readPreferResponse(t *testing.T, rec *httptest.ResponseRecorder) preferTestResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("prefer returned %d: %s", rec.Code, rec.Body)
	}
	var got preferTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Policy != 2 || !got.Capabilities["sticky_aware"] || !got.Capabilities["strict_only"] || !got.Capabilities["exact_uids"] {
		t.Fatalf("missing reliable selection policy capabilities: %+v", got)
	}
	return got
}

func TestPreferEndpointCapabilitiesAuthAndClear(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u3"})
	h := NewHandler(Config{Pool: p, APIKey: "unit-test-key"})
	call := func(method, body, key string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/prefer", strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if rec := call(method, `{}`, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s /prefer without auth returned %d", method, rec.Code)
		}
	}
	initial := readPreferResponse(t, call(http.MethodGet, "", "unit-test-key"))
	if initial.Enabled || initial.Exact || initial.Mode != "prefer" || len(initial.UIDs) != 0 {
		t.Fatalf("unexpected initial configuration: %+v", initial)
	}
	updated := readPreferResponse(t, call(http.MethodPost, `{"prefer_uids":[" U3 ",""],"prefer_mode":" ONLY ","prefer_exact":true}`, "unit-test-key"))
	if !updated.Enabled || !updated.Exact || updated.Mode != "only" || !reflect.DeepEqual(updated.UIDs, []string{"u3"}) {
		t.Fatalf("unexpected normalized configuration: %+v", updated)
	}
	current := readPreferResponse(t, call(http.MethodGet, "", "unit-test-key"))
	if !reflect.DeepEqual(current, updated) {
		t.Fatalf("GET does not reflect hot configuration: got %+v want %+v", current, updated)
	}
	for _, bad := range []string{`{"prefer_uids":[]`, `{"prefer_uids":[],"prefer_mode":"invalid"}`, `{"prefer_uids":[],"prefer_exact":"true"}`} {
		if rec := call(http.MethodPost, bad, "unit-test-key"); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid request returned %d", rec.Code)
		}
		if uids, only, exact := p.PreferConfigWithExact(); !only || !exact || !reflect.DeepEqual(uids, []string{"u3"}) {
			t.Fatalf("invalid request modified policy: uids=%v only=%v exact=%v", uids, only, exact)
		}
	}
	cleared := readPreferResponse(t, call(http.MethodPost, `{"prefer_uids":[],"prefer_mode":"only","prefer_exact":true}`, "unit-test-key"))
	if cleared.Enabled || !cleared.Exact || cleared.Mode != "prefer" || len(cleared.UIDs) != 0 {
		t.Fatalf("empty list did not disable only mode: %+v", cleared)
	}
	legacy := readPreferResponse(t, call(http.MethodPost, `{"prefer_uids":["u3"],"prefer_mode":"only"}`, "unit-test-key"))
	if legacy.Exact {
		t.Fatalf("omitted exact flag must preserve legacy client semantics: %+v", legacy)
	}
}

// All upstream calls below use an in-memory RoundTripper. No real account,
// network generation, configuration file, or running gateway is involved.
func TestPreferChatHotSwitchRebindsExistingAndNewSticky(t *testing.T) {
	for _, mode := range []string{"prefer", "only"} {
		t.Run(mode, func(t *testing.T) {
			p := testPoolWith(
				&auth.Auth{UID: "u1", AccessToken: "at-u1", ExpiresAt: 9999999999},
				&auth.Auth{UID: "u3", AccessToken: "at-u3", ExpiresAt: 9999999999},
			)
			sess := session.New(session.Config{
				Available: p.AvailableUIDs,
				AvailableForModel: func(model string) []string {
					return p.AvailableUIDsForModelRealm(model, "cn")
				},
			})
			sess.Bind("existing-conversation", "u1")
			var used []string
			up := newFakeUpstream(t, func(authz string) (int, string, bool) {
				used = append(used, authz)
				return http.StatusOK, sseOK, true
			})
			h := NewHandler(Config{Pool: p, Upstream: up, Session: sess})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/prefer",
				strings.NewReader(fmt.Sprintf(`{"prefer_uids":["u3"],"prefer_mode":%q,"prefer_exact":true}`, mode))))
			readPreferResponse(t, rec)
			for _, key := range []string{"existing-conversation", "new-conversation"} {
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
					strings.NewReader(fmt.Sprintf(`{"model":"glm-5.2","conversation_id":%q,"messages":[]}`, key)))
				// This legacy workbench header is not a gateway selection API.
				// In particular it cannot bypass an active only policy.
				req.Header.Set("X-WorkBuddy-Account", "u1")
				chat := httptest.NewRecorder()
				h.ServeHTTP(chat, req)
				if chat.Code != http.StatusOK {
					t.Fatalf("%s returned %d: %s", key, chat.Code, chat.Body)
				}
				if uid, ok := sess.ResolveForModel(key, "glm-5.2"); !ok || uid != "u3" {
					t.Fatalf("%s did not rebind to selected account: uid=%s ok=%v", key, uid, ok)
				}
			}
			if !reflect.DeepEqual(used, []string{"Bearer at-u3", "Bearer at-u3"}) {
				t.Fatalf("sticky path did not follow updated %s policy: %v", mode, used)
			}
		})
	}
}

func TestPreferOnlyChatRejectsUnavailableSelection(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "selected", AccessToken: "at-selected", ExpiresAt: 9999999999},
		&auth.Auth{UID: "outside", AccessToken: "at-outside", ExpiresAt: 9999999999},
	)
	p.SetPrefer([]string{"selected"}, true)
	p.Cooldown("selected", pool.CoolHard, time.Hour, "test")
	p.Cooldown("outside", pool.CoolSoft, time.Minute, "test")
	sess := session.New(session.Config{Available: p.AvailableUIDs})
	sess.Bind("existing-conversation", "outside")
	called := false
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		called = true
		return http.StatusOK, sseOK, true
	})
	h := NewHandler(Config{Pool: p, Upstream: up, Session: sess})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","conversation_id":"existing-conversation","messages":[]}`))
	req.Header.Set("X-WorkBuddy-Account", "outside")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("only selection escaped: code=%d upstreamCalled=%v body=%s", rec.Code, called, rec.Body)
	}
}

func TestPreferChatRejectsStickyAccountFromDifferentRealm(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := testPoolWith(
		&auth.Auth{UID: "cn1", AccessToken: "at-cn1", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"},
		&auth.Auth{UID: "global1", AccessToken: "at-global1", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"},
	)
	p.SetPrefer([]string{"cn1", "global1"}, true)
	// Simulate a stale/wrong-realm sticky candidate. The handler must defend
	// its realm boundary even if an earlier availability snapshot is stale.
	sess := session.New(session.Config{Available: func() []string { return []string{"global1"} }})
	sess.Bind("realm-conversation", "global1")
	var used []string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		used = append(used, authz)
		return http.StatusOK, sseOK, true
	})
	up.GlobalEnabled = true
	h := NewHandler(Config{Pool: p, Upstream: up, Session: sess})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","conversation_id":"realm-conversation","messages":[]}`)))
	if rec.Code != http.StatusOK || !reflect.DeepEqual(used, []string{"Bearer at-cn1"}) {
		t.Fatalf("wrong-realm sticky account was not discarded: code=%d used=%v body=%s", rec.Code, used, rec.Body)
	}
}
