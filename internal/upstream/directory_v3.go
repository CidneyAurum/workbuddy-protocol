// directory_v3.go 官方 `/v3/config` 目录端点的解析与元数据（2026-10-21 实测定稿）。
//
// 官方风向变化（对照参考仓库 maiphucgiang / wb330 2026-10 文档）：
//   - `/v3/config` 已成**主目录源**：一次响应同时给出 `data.models`（全量模型表，
//     CN 实测 62 条 / global 28 条）与 `data.agents[].models`（agent picker 子集，
//     即 CLI 真正可路由名单，CN 20 条 / global 28 条）。
//   - 旧端点 `/console/enterprises/personal/models`（CN）与
//     `/v2/enterprises/personal/models`（global）仍在，但**名单已经落后**：
//     CN 缺 `hy3-x`（付费档双轨），global 缺 `gpt-6-astra` / `gpt-6-sol` /
//     `gpt-6-luna` / `gpt-6.1-sol` / `grok-4.7` / `gemini-3.8-flash` /
//     `deepseek-v4.1-flash-sg` / `minimax-m3.1-flash` / `glm-5.3-flash` /
//     `kimi-k2.8-preview` / `step-5-preview` 共 11 个。
//   - `/v3/config` 独有**限免/折扣声明** `data.modelPromotions[]`：
//     `kind=discount` + `discount.factor==0` + schedule 窗口 = 此刻真正 0 积分。
//     这是「声明免费」，与网关上「实测扣费」口径互补（参考仓库把两者分开记）。
//   - `/v3/config` 独有**免费/付费双轨** `data.productFeaturesConfig
//     .ModelRateLimitCap.lines[]`：`{freeId:"hy3", paidId:"hy3-x"}`。
//
// 本文件只做解析与查询，不做网络决策；目录抓取入口仍是 client.go 的 FetchModels
// （/v3/config 优先，失败回落旧端点，保证可用性不回退）。
package upstream

import (
	"encoding/json"
	"strings"
	"time"
)

// v3ConfigPath 官方主目录端点（chatBase 下）。
const v3ConfigPath = "/v3/config"

type v3ModelTier struct {
	Enabled  bool     `json:"enabled"`
	Priority int      `json:"priority"`
	ModelIDs []string `json:"modelIds"`
	Badge    *struct {
		Label string `json:"label"`
	} `json:"badge"`
}

// applyModelTiers carries the official wording. CN priority and global Pro-only
// access differ, so a badge never removes a model from the shared pool catalog.
func applyModelTiers(models []ModelInfo, tiers []v3ModelTier) {
	best := map[string]int{}
	labels := map[string]string{}
	for _, tier := range tiers {
		if !tier.Enabled || tier.Badge == nil || tier.Badge.Label == "" {
			continue
		}
		for _, id := range tier.ModelIDs {
			if priority, seen := best[id]; !seen || tier.Priority > priority {
				best[id], labels[id] = tier.Priority, tier.Badge.Label
			}
		}
	}
	for i := range models {
		if label := labels[models[i].ID]; label != "" {
			models[i].Tier = label
		} else if models[i].ID == "primary-model" && models[i].Tier == "" {
			// Upstream omitted primary-model from modelTiers; Oct 23 maintenance fix.
			models[i].Tier = "Pro Only"
		}
	}
}

func (c *Client) storeModelInfos(realm string, models []ModelInfo) {
	byID := make(map[string]ModelInfo, len(models))
	for _, model := range models {
		model.Efforts = append([]string(nil), model.Efforts...)
		byID[model.ID] = model
	}
	c.dirMu.Lock()
	defer c.dirMu.Unlock()
	if c.modelInfos == nil {
		c.modelInfos = map[string]map[string]ModelInfo{}
	}
	c.modelInfos[realmKey(realm)] = byID
}

// ModelInfoSnapshot retains remote limits even when a caller only needs names.
func (c *Client) ModelInfoSnapshot(realm string) map[string]ModelInfo {
	c.dirMu.RLock()
	defer c.dirMu.RUnlock()
	out := make(map[string]ModelInfo, len(c.modelInfos[realmKey(realm)]))
	for id, model := range c.modelInfos[realmKey(realm)] {
		model.Efforts = append([]string(nil), model.Efforts...)
		out[id] = model
	}
	return out
}

// Promotion 一条活动/促销声明（`data.modelPromotions[]` 元素）。
//
// 实测样例（global，2026-10-21）：
//
//	{"id":"step-5-discount-daytime-badge-202610","kind":"discount","enabled":true,
//	 "modelIds":["step-5-preview"],"priority":50,
//	 "discount":{"discountedCredits":"0.00x","displayMode":"strikethrough","factor":0},
//	 "badge":{"color":"#FF0000","display":"activeOnly","label":"Free now"},
//	 "hover":{"action":{"labelZh":"Use now","type":"selectModel"},
//	          "textZh":"Oct 12 - Oct 25: Free daily use with unlimited credits through exclusive official partnership."},
//	 "schedule":{"timezone":"Asia/Shanghai","validFrom":"2026-10-12T00:00:00+08:00",
//	             "validUntil":"2026-10-26T00:00:00+08:00"}}
//
// 注意 `data.modelPromotions` 里**混着非折扣条目**（如 `{"id":"hy-image-3.5",
// "name":"HY-Image-3.5","tags":["text-to-image"]}` 这种「模型促销位」），
// 所以判定免费必须同时看 Kind=="discount" 与 Factor==0，不能只看存在性。
type Promotion struct {
	ID         string
	Kind       string
	Enabled    bool
	ModelIDs   []string
	Priority   int
	Factor     float64
	Label      string
	TextZh     string
	ValidFrom  time.Time
	ValidUntil time.Time
	// IntraDay 该活动是「按时段」生效（每晚/非高峰/具体钟点），而非全天。
	//
	// 这是 2026-10-21 实测踩到的坑：官方把时段写在**中文文案**里而不是 schedule 里。
	// 例（CN）：`glm-52-night-discount-daytime-badge-202607` factor=0 且无 schedule，
	// 文案「每晚 23:00—次日 8:00 积分限时立减」；`ds-discount-daytime-badge-202608`
	// factor=0 无 schedule，文案「周一至周五 09:00–12:00、14:00–18:00 属高峰原价，
	// 非高峰期积分5折」。若只看 factor==0 会把它们当「全天免费」，严重误导选号。
	// 因此 FreeModels 保守排除 IntraDay 条目，只在可真判定全天 0 积分时才声称免费。
	IntraDay bool
}

// RateLimitPair 免费/付费双轨映射（`ModelRateLimitCap.lines[]` 元素）。
// 实测 CN：`{displayName:"Hy3", freeId:"hy3", paidId:"hy3-x"}`；
// global：`{displayName:"Deepseek-V4.1-Flash", freeId:"deepseek-v4.1-flash",
// paidId:"deepseek-v4.1-flash-sg"}`。
// 语义：免费额度耗尽后客户端会切到 paidId，网关侧据此可做同族降级/避让。
type RateLimitPair struct {
	DisplayName string
	FreeID      string
	PaidID      string
}

// DirectoryMeta `/v3/config` 独有的目录元数据（旧端点不返回这些）。
type DirectoryMeta struct {
	// Endpoint 该快照来自哪个 base（如 https://www.codebuddy.ai），排查用。
	Endpoint string
	// Promotions 原样保留的活动声明（含非折扣条目），查询免费请看 FreeModels。
	Promotions []Promotion
	// RateLimitPairs 免费/付费双轨映射。
	RateLimitPairs []RateLimitPair
	// ModelCount 本次 models 全表条数（CN 62 / global 28），用于观测目录漂移。
	ModelCount int
	// AgentModelCount agent picker 名单条数（可路由上限）。
	AgentModelCount int
	// FetchedAt 抓取时间。
	FetchedAt time.Time
}

// v3Envelope /v3/config 响应体里只需要用到的字段。
// 其余 40+ 字段（productFeatures/config/telemetry/marketplaces/tokenUsageThresholds…）
// 与网关路由无关，刻意不解析：官方随时增删，少解析就少坏一次。
type v3Envelope struct {
	Code int `json:"code"`
	Msg  string
	Data struct {
		Endpoint string `json:"endpoint"`
		Models   []struct {
			ID              string   `json:"id"`
			Name            string   `json:"name"`
			MaxInputTokens  int64    `json:"maxInputTokens"`
			MaxOutputTokens int64    `json:"maxOutputTokens"`
			Disabled        bool     `json:"disabled"`
			SupportsImages  bool     `json:"supportsImages"`
			Tags            []string `json:"tags"`
			Reasoning       struct {
				Effort           string   `json:"effort"`
				DefaultEffort    string   `json:"defaultEffort"`
				SupportedEfforts []string `json:"supportedEfforts"`
			} `json:"reasoning"`
		} `json:"models"`
		Agents []struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
		} `json:"agents"`
		ModelPromotions []struct {
			ID       string   `json:"id"`
			Kind     string   `json:"kind"`
			Enabled  bool     `json:"enabled"`
			ModelIDs []string `json:"modelIds"`
			Priority int      `json:"priority"`
			Discount struct {
				Factor float64 `json:"factor"`
			} `json:"discount"`
			Badge struct {
				Label string `json:"label"`
			} `json:"badge"`
			Hover struct {
				TextZh string `json:"textZh"`
			} `json:"hover"`
			Schedule struct {
				Timezone   string `json:"timezone"`
				ValidFrom  string `json:"validFrom"`
				ValidUntil string `json:"validUntil"`
			} `json:"schedule"`
		} `json:"modelPromotions"`
		ProductFeaturesConfig struct {
			ModelRateLimitCap struct {
				Lines []struct {
					DisplayName string `json:"displayName"`
					FreeID      string `json:"freeId"`
					PaidID      string `json:"paidId"`
				} `json:"lines"`
			} `json:"ModelRateLimitCap"`
		} `json:"productFeaturesConfig"`
	} `json:"data"`
}

// parseV3Meta 从 /v3/config 原始响应里抽取目录元数据（promotions / 双轨 / 计数）。
// 不返回 error：元数据缺失只是少信息，绝不能让目录抓取失败。
func parseV3Meta(raw []byte, endpoint string, now time.Time) *DirectoryMeta {
	var env v3Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	meta := &DirectoryMeta{
		Endpoint:        endpoint,
		ModelCount:      len(env.Data.Models),
		AgentModelCount: 0,
		FetchedAt:       now,
	}
	for _, ag := range env.Data.Agents {
		if ag.Name == "cli" {
			meta.AgentModelCount = len(ag.Models)
			break
		}
	}
	for _, p := range env.Data.ModelPromotions {
		promo := Promotion{
			ID:         p.ID,
			Kind:       p.Kind,
			Enabled:    p.Enabled,
			ModelIDs:   p.ModelIDs,
			Priority:   p.Priority,
			Factor:     p.Discount.Factor,
			Label:      p.Badge.Label,
			TextZh:     p.Hover.TextZh,
			ValidFrom:  parsePromoTime(p.Schedule.ValidFrom),
			ValidUntil: parsePromoTime(p.Schedule.ValidUntil),
			IntraDay:   hasIntraDayWindow(p.Hover.TextZh),
		}
		meta.Promotions = append(meta.Promotions, promo)
	}
	for _, l := range env.Data.ProductFeaturesConfig.ModelRateLimitCap.Lines {
		if l.FreeID == "" && l.PaidID == "" {
			continue
		}
		meta.RateLimitPairs = append(meta.RateLimitPairs, RateLimitPair{
			DisplayName: l.DisplayName, FreeID: l.FreeID, PaidID: l.PaidID,
		})
	}
	return meta
}

// parsePromoTime 解析 ISO8601 带时区的时间；失败返回零值（当作无界）。
func parsePromoTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// hasIntraDayWindow 判断活动文案是否声明了「按时段」生效。
//
// 官方把时段塞进中文文案（`hover.textZh`），schedule 只有日期范围，实测三类写法：
//   - 「每晚 23:00—次日 8:00 积分限时立减」（CN glm-5.2 夜间折扣）
//   - 「周一至周五 09:00–12:00、14:00–18:00 属高峰原价，非高峰期积分5折」（CN deepseek）
//   - 「9月11日–10月31日，每晚 23:00—次日 8:00 享免费额度」（CN hy4）
//
// 判定：命中时段关键词，或文案里出现 `H:MM` 形式钟点。宁可漏判（少数全天免费被
// 当成时段活动，少赚一点）也不能误判（把打折当免费，选号逻辑会持续踩坑）。
func hasIntraDayWindow(text string) bool {
	if text == "" {
		return false
	}
	for _, kw := range [...]string{"每晚", "夜间", "凌晨", "非高峰", "错峰"} {
		if strings.Contains(text, kw) {
			return true
		}
	}
	// 扫描 "H:MM" / "HH:MM"：冒号前一位、后两位都是数字。
	for i := 1; i+2 < len(text); i++ {
		if text[i] != ':' {
			continue
		}
		if !isASCIIDigit(text[i-1]) || !isASCIIDigit(text[i+1]) || !isASCIIDigit(text[i+2]) {
			continue
		}
		return true
	}
	return false
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// FreeModels 返回「此刻全天声明为 0 积分」的模型集合（modelID → true）。
//
// 判定：Kind=="discount" && Enabled && Factor==0 && now ∈ [ValidFrom, ValidUntil)
// 且**不是时段活动**（IntraDay=false，见 Promotion.IntraDay 的实测说明）。
//
// 这个口径刻意保守：`ds-discount-daytime`（非高峰 5 折）与 `glm-52-night-discount`
// （每晚 23:00-08:00 免费）都 factor=0，但它们并非此刻全天免费，混进来会让
// 「免费优先」选号在白天选中实际要扣费的模型。
func (d *DirectoryMeta) FreeModels(now time.Time) map[string]bool {
	out := map[string]bool{}
	if d == nil {
		return out
	}
	for _, p := range d.Promotions {
		if p.Kind != "discount" || !p.Enabled || p.Factor != 0 || p.IntraDay {
			continue
		}
		if !p.ValidFrom.IsZero() && now.Before(p.ValidFrom) {
			continue
		}
		if !p.ValidUntil.IsZero() && !now.Before(p.ValidUntil) {
			continue
		}
		for _, id := range p.ModelIDs {
			if id != "" {
				out[id] = true
			}
		}
	}
	return out
}

// PaidTwin 返回某模型的付费双轨对端 id（无映射返回空串）。
// 例：CN `hy3` → `hy3-x`；global `deepseek-v4.1-flash` → `deepseek-v4.1-flash-sg`。
// 双向查：传 freeId 得 paidId，传 paidId 得 freeId。
func (d *DirectoryMeta) PaidTwin(model string) string {
	if d == nil {
		return ""
	}
	for _, p := range d.RateLimitPairs {
		switch model {
		case p.FreeID:
			return p.PaidID
		case p.PaidID:
			return p.FreeID
		}
	}
	return ""
}

// storeDirectoryMeta 落盘某 realm 的目录元数据（FetchModels 成功抓 /v3/config 时调用）。
func (c *Client) storeDirectoryMeta(realm string, meta *DirectoryMeta) {
	if meta == nil {
		return
	}
	key := realmKey(realm)
	c.dirMu.Lock()
	defer c.dirMu.Unlock()
	if c.dirMeta == nil {
		c.dirMeta = make(map[string]*DirectoryMeta, 2)
	}
	c.dirMeta[key] = meta
}

// DirectoryMeta 返回某 realm 最近一次抓到的目录元数据（无则 nil）。
// 供运维工具（cmd/catalog）与调用方查询限免/双轨，不参与路由判定。
func (c *Client) DirectoryMeta(realm string) *DirectoryMeta {
	c.dirMu.RLock()
	defer c.dirMu.RUnlock()
	return c.dirMeta[realmKey(realm)]
}

// FreeModels 返回某 realm 此刻声明为 0 积分的模型集合（无元数据 → 空集，不 panic）。
func (c *Client) FreeModels(realm string, now time.Time) map[string]bool {
	return c.DirectoryMeta(realm).FreeModels(now)
}
