// compensation.go 官网控制台「赠送额度」领取：补偿包 + 礼品包（CN 域实测可用）。
//
// 端点（WorkBuddy.md §五，2026-10-16 CN 号实领取证 +500 credits）：
//
//	POST {billingBase}/billing/meter/compensation-status   查补偿包状态
//	POST {billingBase}/billing/meter/claim-compensation    领补偿包（每人限一次，不可撤销）
//	POST {billingBase}/billing/meter/check-gift-claimed    查礼品包
//	POST {billingBase}/billing/meter/claim-gift            领礼品包
//
// 全部无请求体、POST + JSON、且带 X-Client-Platform: web —— 这是官网 web 控制台的
// 口径；网关既有的 BillingHeaders 不含该头，故这四个端点单独走 consoleJSON 注入。
//
// 与 trial.go 的分工：/billing/ide/trial 是 global 专属（CN 无此端点），而
// compensation/gift 是 CN 号当前唯一可用的积分增益动作（PLAN 里 13 个号全是 CN）。
package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"workbuddy2api/internal/auth"
)

// 官网控制台赠送额度端点路径（均挂在 billingBase 下）。
const (
	compensationStatusPath = "/billing/meter/compensation-status"
	claimCompensationPath  = "/billing/meter/claim-compensation"
	checkGiftClaimedPath   = "/billing/meter/check-gift-claimed"
	claimGiftPath          = "/billing/meter/claim-gift"
)

// alreadyClaimedMarkers 「已领过」幂等语义的两种指纹：
//   - 业务码非 0 且带中文原文（实测 400 {"code":10001,"msg":"每人限领一次，您已领取过无法重复领取"}）；
//   - HTTP ≥400 时 doJSON 把原始 body 塞进 Msg，故按子串匹配中英文两种措辞。
//
// 刻意不复用 trialAlreadyMarkers：trial 的幂等码是 14051，本族是文案型，混淆会误判。
var alreadyClaimedMarkers = []string{
	"每人限领一次", "已领取过", "无法重复领取", "已经领取", "已领取",
	"already claimed", "already received", "already been claimed", "have claimed",
}

// alreadyClaimedErr 判定错误 Msg 是否表达「已领取过」（幂等，不算失败）。
func alreadyClaimedErr(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range alreadyClaimedMarkers {
		if strings.Contains(m, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// CompensationStatus 补偿包/礼品状态。形状取自 2026-10-17 实测原始 data：
//
//	compensation-status → {"claimed":false,"active":true,"credit_num":500,
//	                       "validity_period":1,"end_time":"2026-10-22 23:59:59"}
//	check-gift-claimed  → {"claimed":true,"claimed_at":"2026-10-07 11:05:34","active":true,
//	                       "credit_num":1500,"validity_period":1,
//	                       "start_time":"2026-04-01 00:00:00","end_time":"2026-10-31 23:59:59"}
//
// 注意 validity_period 是**数字**（不是文档 §五 写的字符串），单位为天。
// 指针字段用于区分「上游明确 false」与「字段缺失」。
type CompensationStatus struct {
	Claimed        *bool           `json:"claimed"`
	Active         *bool           `json:"active"`
	CreditNum      int             `json:"credit_num"`
	ValidityPeriod int             `json:"validity_period"`
	StartTime      string          `json:"start_time"`
	EndTime        string          `json:"end_time"`
	ClaimedAt      string          `json:"claimed_at"`
	Raw            json.RawMessage `json:"-"` // 原始 data，供排查上游形状漂移
}

// CompensationStatus 查询当前账号补偿包状态。data 为 null（从未发过）时返回空结构，
// 不报错——领没领过靠 Claimed 指针是否为 nil 区分。
func (c *Client) CompensationStatus(a *auth.Auth) (*CompensationStatus, error) {
	raw, err := c.consoleJSON(a, http.MethodPost, compensationStatusPath, nil)
	if err != nil {
		return nil, err
	}
	st := &CompensationStatus{Raw: raw}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, st); err != nil {
			return nil, fmt.Errorf("decode compensation-status: %w (body: %s)", err, truncate(string(raw), 120))
		}
	}
	return st, nil
}

// ClaimCompensation 领取一次性补偿包。返回 (claimed, creditNum, err)：
//   - claimed=true：本次新领成功（creditNum 为响应里能解析到的发放数，未给则 0）；
//   - claimed=false, err=nil：已领取过（幂等，不算失败）；
//   - err != nil：真实失败（网络/鉴权/其他业务码）。
func (c *Client) ClaimCompensation(a *auth.Auth) (claimed bool, creditNum int, err error) {
	raw, err := c.consoleJSON(a, http.MethodPost, claimCompensationPath, nil)
	if err != nil {
		var ue *Error
		if errors.As(err, &ue) && alreadyClaimedErr(ue.Msg) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, creditFromRaw(raw), nil
}

// GiftStatus 礼品包查询结果。§五 只给了端点、没给定稿字段，故按常见 claimed
// 键名探测；Raw 一并保留，上游换键名时可据此定位（而不是静默判成「未领」）。
type GiftStatus struct {
	Claimed *bool           // nil = 响应里没有可识别的 claimed 布尔字段
	Raw     json.RawMessage // 原始 data
}

// CheckGiftClaimed 查询礼品包是否已领。
func (c *Client) CheckGiftClaimed(a *auth.Auth) (*GiftStatus, error) {
	raw, err := c.consoleJSON(a, http.MethodPost, checkGiftClaimedPath, nil)
	if err != nil {
		return nil, err
	}
	return &GiftStatus{Claimed: claimedFromRaw(raw), Raw: raw}, nil
}

// CreditNum 从原始 data 里取礼品额度数（礼品状态未定稿，不建专用结构体）。
func (g *GiftStatus) CreditNum() int {
	if g == nil {
		return 0
	}
	return creditFromRaw(g.Raw)
}

// ClaimGift 领取礼品包。语义同 ClaimCompensation：claimed=false + err=nil = 已领过。
func (c *Client) ClaimGift(a *auth.Auth) (claimed bool, err error) {
	raw, err := c.consoleJSON(a, http.MethodPost, claimGiftPath, nil)
	if err != nil {
		var ue *Error
		if errors.As(err, &ue) && alreadyClaimedErr(ue.Msg) {
			return false, nil
		}
		return false, err
	}
	// 部分端点成功也回 code=0 + 空 data；只要信封 code=0 即视为发放成功。
	_ = raw
	return true, nil
}

// creditFromRaw 从领取响应的 data 里尽力取发放 credits 数（未给则 0，仅作展示）。
func creditFromRaw(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return 0
	}
	for _, k := range []string{"credit_num", "creditNum", "credits", "granted"} {
		if n, ok := m[k].(float64); ok {
			return int(n)
		}
	}
	return 0
}

// claimedFromRaw 在 data 里按候选键名探测 claimed 布尔值；识别不到返回 nil。
func claimedFromRaw(raw json.RawMessage) *bool {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	for _, k := range []string{
		"claimed", "is_claimed", "isClaimed", "gift_claimed", "giftClaimed",
		"received", "is_received", "isReceived",
	} {
		if v, ok := m[k].(bool); ok {
			return &v
		}
	}
	return nil
}
