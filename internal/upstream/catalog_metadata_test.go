package upstream

import (
	"net/http"
	"testing"

	"workbuddy2api/internal/auth"
)

func TestLegacyGlobalCatalogRetainsMetadata(t *testing.T) {
	var calls []string
	srv := globalModelsSrv(t, &calls, nil, func(path string) (int, string) {
		if path == v3ConfigPath {
			return http.StatusNotFound, ""
		}
		return 200, `{"code":0,"data":{"models":[{"id":"primary-model","maxInputTokens":900000,"maxOutputTokens":64000,"supportsImages":true}]}}`
	})
	defer srv.Close()
	c := globalModelsClient(t, srv)
	c.FetchGlobalModels(&auth.Auth{UID: "fixture", Domain: "www.workbuddy.ai", AccessToken: "fixture"})
	info := c.ModelInfoSnapshot("global")["primary-model"]
	if info.ContextWindow != 900000 || info.MaxTokens != 64000 || !info.SupportsImages || info.Tier != "Pro Only" {
		t.Fatalf("legacy metadata lost: %+v", info)
	}
}

func TestModelTierPrefersHighestOfficialLabel(t *testing.T) {
	models := []ModelInfo{{ID: "primary-model"}, {ID: "m"}}
	applyModelTiers(models, []v3ModelTier{
		{Enabled: true, Priority: 1, ModelIDs: []string{"primary-model", "m"}, Badge: &struct {
			Label string `json:"label"`
		}{"low"}},
		{Enabled: true, Priority: 20, ModelIDs: []string{"primary-model"}, Badge: &struct {
			Label string `json:"label"`
		}{"official"}},
	})
	if models[0].Tier != "official" || models[1].Tier != "low" {
		t.Fatalf("bad tiers: %+v", models)
	}
}
