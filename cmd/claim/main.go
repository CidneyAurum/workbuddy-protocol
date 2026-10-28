// claim 官网控制台赠送额度批量领取：遍历 auths 下账号，逐个
//
//	① /billing/meter/compensation-status 查补偿包 → 未领则 claim-compensation
//	② /billing/meter/check-gift-claimed   查礼品   → 未领则 claim-gift
//
// 每个账号领完再回查一次状态，把上游真实 credit_num 落到表里，不做静默吞错。
// 这两条是 CN 号当前唯一可用的积分增益动作（/billing/ide/trial 是 global 专属，见 trial.go）；
// 补偿包每人限一次、不可撤销，因此默认「先查后领」：claimed=true 时绝不发领取请求。
//
// 401 处理：access token 过期/被吊销时先 RefreshToken 并原子写回 auths/，再重试一次
// （与 cmd/refresh 同一条路径），避免把「token 过期」误报成功能不可用。
//
// 用法（在 services/workbuddy 目录下，需 auths/）：
//
//	go run ./cmd/claim            # 领全部账号
//	go run ./cmd/claim -dry       # 只看不领
//	go run ./cmd/claim 1a2b3c     # 只处理指定 uid 前缀（或 ALL）
//
// 结果逐账号输出：
//
//	uid | nick | realm | comp | gift | detail
//	... | 张三 | CN    | OK(+500) | ALREADY(1500) | -
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/authsel"
	"workbuddy2api/internal/logfmt"
	"workbuddy2api/internal/upstream"
)

// 单账号单通道的归一化结果。
const (
	stOK      = "OK"      // 本次新领成功
	stAlready = "ALREADY" // 上游 claimed=true（含刚领成/早就领过），不发领取请求
	stDry     = "OPEN"    // -dry 模式：查出未领但没下手
	stFail    = "FAIL"
)

type claimRow struct {
	uid    string
	nick   string
	realm  string
	comp   string
	gift   string
	detail string
}

// appendDetail 追加失败原因（两条通道各自报错时串行拼接，不互相覆盖）。
func (r *claimRow) appendDetail(msg string) {
	if r.detail == "" {
		r.detail = msg
		return
	}
	r.detail += " | " + msg
}

// is401 上游 401（access token 过期或被吊销）。
func is401(err error) bool {
	var ue *upstream.Error
	return errors.As(err, &ue) && ue.Status == 401
}

// retryOn401 首次 401 时刷新 token（原子写回 auths/）后重试一次；其余错误原样返回。
func retryOn401(up *upstream.Client, a *auth.Auth, fn func() (string, error)) (string, error) {
	st, err := fn()
	if err == nil || !is401(err) {
		return st, err
	}
	if rerr := up.RefreshToken(a); rerr != nil {
		return st, fmt.Errorf("%w; refresh failed: %v", err, rerr)
	}
	if serr := a.SaveAtomic(); serr != nil {
		return st, fmt.Errorf("%w; refresh done but save failed: %v", err, serr)
	}
	return fn()
}

// alreadyText 已领态展示：带上上游报的额度数（拿不到就只写 ALREADY）。
func alreadyText(credit int) string {
	if credit > 0 {
		return fmt.Sprintf("%s(%d)", stAlready, credit)
	}
	return stAlready
}

// claimCompensationChannel 补偿包通道。
func claimCompensationChannel(up *upstream.Client, a *auth.Auth, dry bool) (string, error) {
	st, err := up.CompensationStatus(a)
	if err != nil {
		return stFail, fmt.Errorf("comp-status: %w", err)
	}
	if st.Claimed != nil && *st.Claimed {
		return alreadyText(st.CreditNum), nil
	}
	if dry {
		return stDry, nil
	}
	claimed, credit, err := up.ClaimCompensation(a)
	switch {
	case err != nil:
		return stFail, fmt.Errorf("comp-claim: %w", err)
	case !claimed:
		return alreadyText(credit), nil
	}
	// 领成功：回查确认，优先用上游回给的 credit_num 展示。
	if after, err := up.CompensationStatus(a); err == nil && after.CreditNum > 0 {
		credit = after.CreditNum
	}
	if credit > 0 {
		return fmt.Sprintf("%s(+%d)", stOK, credit), nil
	}
	return stOK, nil
}

// claimGiftChannel 礼品通道。check 的 Claimed 为 nil（字段缺失/未定稿）时仍尝试领取，
// 让上游用幂等码回答——比直接判「未知」有用，重复领由上游兜底。
func claimGiftChannel(up *upstream.Client, a *auth.Auth, dry bool) (string, error) {
	st, err := up.CheckGiftClaimed(a)
	if err != nil {
		return stFail, fmt.Errorf("gift-check: %w", err)
	}
	if st.Claimed != nil && *st.Claimed {
		return alreadyText(st.CreditNum()), nil
	}
	if dry {
		return stDry, nil
	}
	claimed, err := up.ClaimGift(a)
	switch {
	case err != nil:
		return stFail, fmt.Errorf("gift-claim: %w", err)
	case !claimed:
		return stAlready, nil
	}
	if after, err := up.CheckGiftClaimed(a); err == nil && after.CreditNum() > 0 {
		return fmt.Sprintf("%s(+%d)", stOK, after.CreditNum()), nil
	}
	return stOK, nil
}

func main() {
	authDir := "auths"
	var dry bool
	var selectors []string
	args := os.Args[1:]
	if len(args) > 0 {
		// 兼容旧的 `claim <auths目录>` 调用：首个参数是存在的目录时按目录解释。
		if st, err := os.Stat(args[0]); err == nil && st.IsDir() {
			authDir, args = args[0], args[1:]
		}
	}
	for _, a := range args {
		if a == "-dry" || a == "--dry" {
			dry = true
			continue
		}
		selectors = append(selectors, a)
	}

	files, err := auth.LoadAuthFiles(authDir)
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no auth files in %s\n", authDir)
		os.Exit(1)
	}
	files = authsel.Filter(files, authsel.Parse(selectors))
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no auth file matches %v in %s\n", selectors, authDir)
		os.Exit(1)
	}

	up := upstream.New()
	up.GlobalEnabled = true // 按 realm 路由 billingBase；global 号别误落 CN base

	// 撞到 APISIX 401 时刷新一次 token 并写回，让后续账号也有新 token。
	fmt.Printf("mode=%s accounts=%d\n", modeName(dry), len(files))
	var rows []claimRow
	for _, f := range files {
		r := claimRow{uid: filepath.Base(f), comp: "-", gift: "-"}
		raw, err := os.ReadFile(f)
		if err != nil {
			r.comp, r.gift, r.detail = stFail, stFail, "load: "+err.Error()
			rows = append(rows, r)
			continue
		}
		a, err := auth.Parse(raw)
		if err != nil {
			r.comp, r.gift, r.detail = stFail, stFail, "parse: "+err.Error()
			rows = append(rows, r)
			continue
		}
		a.FilePath = f
		r.uid, r.nick, r.realm = a.UID, a.Nickname, strings.ToUpper(a.Realm())

		var err1, err2 error
		r.comp, err1 = retryOn401(up, a, func() (string, error) {
			return claimCompensationChannel(up, a, dry)
		})
		if err1 != nil {
			r.appendDetail(err1.Error())
		}
		r.gift, err2 = retryOn401(up, a, func() (string, error) {
			return claimGiftChannel(up, a, dry)
		})
		if err2 != nil {
			r.appendDetail(err2.Error())
		}
		rows = append(rows, r)
	}

	fmt.Printf("\n%-24s | %-11s | %-6s | %-12s | %-14s | detail\n", "uid", "nick", "realm", "comp", "gift")
	fmt.Printf("%s\n", strings.Repeat("-", 110))
	var okN, alN, dryN, failN int
	for _, r := range rows {
		d := r.detail
		if d == "" {
			d = "-"
		}
		fmt.Printf("%-24s | %-11s | %-6s | %-12s | %-14s | %s\n",
			logfmt.Truncate(r.uid, 24), logfmt.Truncate(r.nick, 11), r.realm,
			r.comp, r.gift, logfmt.Truncate(oneLine(d), 240))
		if r.comp == stFail || r.gift == stFail {
			failN++
			continue
		}
		if strings.HasPrefix(r.comp, stOK) || strings.HasPrefix(r.gift, stOK) {
			okN++
		}
		if strings.HasPrefix(r.comp, stAlready) || strings.HasPrefix(r.gift, stAlready) {
			alN++
		}
		if r.comp == stDry || r.gift == stDry {
			dryN++
		}
	}
	fmt.Printf("\ntotal=%d ok=%d already=%d open=%d fail=%d\n", len(rows), okN, alN, dryN, failN)
	if failN > 0 {
		os.Exit(1)
	}
}

// oneLine 把多行错误（APISIX 的 HTML 401 页）压成单行，保证表格不散架。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func modeName(dry bool) string {
	if dry {
		return "dry-run"
	}
	return "claim"
}
