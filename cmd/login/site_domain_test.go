package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workbuddy2api/internal/loginsite"
)

func TestGlobalLoginRetainsSelectedIssuerIfDomainOmitted(t *testing.T) {
	for _, siteID := range []string{loginsite.SiteIntl, loginsite.SiteWorkBuddy} {
		for _, supplied := range []string{"", "issuer.workbuddy.ai"} {
			t.Run(siteID+"/"+supplied, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "/auth/token") {
						fmt.Fprintf(w, `{"code":0,"data":{"accessToken":"fixture","refreshToken":"fixture","expiresIn":3600,"domain":%q}}`, supplied)
					} else {
						fmt.Fprint(w, `{"code":0,"data":{"uid":"fixture"}}`)
					}
				}))
				defer srv.Close()
				site := loginsite.For(siteID)
				want := strings.TrimPrefix(site.API, "https://")
				if supplied != "" {
					want = supplied
				}
				site.API = srv.URL
				statePath := filepath.Join(t.TempDir(), "state.json")
				state, _ := json.Marshal(loginState{State: "fixture", Realm: realmGlobal, Site: siteID})
				if err := os.WriteFile(statePath, state, 0o600); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				runPollForSite(site, statePath, srv.Client(), &out)
				var result map[string]any
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal("invalid login JSON")
				}
				if result["domain"] != want || result["realm"] != realmGlobal {
					t.Fatal("selected issuer domain lost or overwritten")
				}
			})
		}
	}
}
