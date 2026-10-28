// refresh 一次性凭证刷新工具：只刷新 token，不做签到/上报等副作用。
//
// 用途：工作台「账号」页的「刷新凭证」按钮。平时 token 由网关在发请求前自动续期
// （internal/server.handler 的 RefreshSkew），这个命令把同一套刷新逻辑（client.go
// 的 RefreshToken）单独暴露出来，让用户能挑单个账号立刻续期、马上看到新的到期时间。
//
// 用法：
//
//	refresh                     # 全部账号，只刷新"快过期"的（2h 内）
//	refresh -force              # 全部账号，无论剩余多久都换一张新 token
//	refresh 96a7d2a7            # 单个账号（uid 前缀）
//	refresh 96a7d2a7 e8c413ef   # 多个账号
//	refresh -dir ./auths -pretty
//
// 输出（stdout，JSON；-pretty 缩进）：逐个账号一条记录，失败不中断其余账号。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

type accountResult struct {
	UID       string `json:"uid"`
	Nickname  string `json:"nickname,omitempty"`
	Realm     string `json:"realm,omitempty"`
	OK        bool   `json:"ok"`
	Skipped   bool   `json:"skipped,omitempty"` // 未到刷新窗口（正常，不是失败）
	ExpiresAt int64  `json:"expires_at,omitempty"`
	ExpiresIn string `json:"expires_in,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type report struct {
	Service   string          `json:"service"`
	TS        int64           `json:"ts"`
	Total     int             `json:"total"`
	Refreshed int             `json:"refreshed"`
	Failed    int             `json:"failed"`
	Accounts  []accountResult `json:"accounts"`
}

func main() {
	pretty := flag.Bool("pretty", false, "缩进输出 JSON")
	force := flag.Bool("force", false, "无论剩余有效期多长都刷新")
	dir := flag.String("dir", "", "auths 目录（缺省 $WB2A_AUTH_DIR 或 ./auths）")
	flag.Parse()
	selectors := flag.Args()

	authDir := strings.TrimSpace(*dir)
	if authDir == "" {
		authDir = strings.TrimSpace(os.Getenv("WB2A_AUTH_DIR"))
	}
	if authDir == "" {
		authDir = "./auths"
	}

	rep := report{Service: "workbuddy", TS: time.Now().Unix(), Accounts: []accountResult{}}
	files, err := auth.LoadAuthFiles(authDir)
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no auth files in %s\n", authDir)
		os.Exit(1)
	}
	up := upstream.New()
	up.GlobalEnabled = true // realm 路由：global 账号的 refresh 打 workbuddy.ai

	for _, f := range files {
		base := filepath.Base(f)
		if !matchesAny(base, selectors) {
			continue
		}
		rep.Total++
		res := refreshOne(up, f, *force)
		if res.OK {
			if res.Skipped {
				// 未到窗口，不计入 refreshed
			} else {
				rep.Refreshed++
			}
		} else {
			rep.Failed++
		}
		rep.Accounts = append(rep.Accounts, res)
	}

	enc := json.NewEncoder(os.Stdout)
	if *pretty {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(rep)
}

// refreshOne 刷新单个 auth 文件。skip 情况（未到窗口）返回 OK+Skipped。
func refreshOne(up *upstream.Client, file string, force bool) accountResult {
	res := accountResult{UID: strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))}
	raw, err := os.ReadFile(file)
	if err != nil {
		res.Detail = "读取失败: " + err.Error()
		return res
	}
	a, err := auth.Parse(raw)
	if err != nil {
		res.Detail = "解析失败: " + err.Error()
		return res
	}
	a.FilePath = file
	if a.UID != "" {
		res.UID = a.UID
	}
	res.Nickname = a.Nickname
	res.Realm = a.Realm()

	// 阈值 2h 与 cmd/signin 一致：网关发请求前的窗口是 10m，这里放宽到 2h，
	// 让「手动点一下」也能覆盖到"今天之内就要过期"的账号。
	if !force && !a.NeedsRefresh(2*time.Hour) {
		res.OK = true
		res.Skipped = true
		res.Detail = "未到刷新窗口（剩余 " + humanLeft(a) + "）"
		fillExpiry(&res, a)
		return res
	}
	if err := up.RefreshToken(a); err != nil {
		res.Detail = short(err.Error())
		fillExpiry(&res, a)
		return res
	}
	// 刷新成功必须落盘：不落盘则重启回旧 token（与 cmd/signin 同口径，失败要暴露）。
	a.BackfillRealm()
	if err := a.SaveAtomic(); err != nil {
		res.Detail = "刷新成功但落盘失败: " + err.Error()
		fillExpiry(&res, a)
		return res
	}
	res.OK = true
	res.Detail = "已换新凭证"
	fillExpiry(&res, a)
	return res
}

func fillExpiry(res *accountResult, a *auth.Auth) {
	if a.ExpiresAt <= 0 {
		return
	}
	res.ExpiresAt = a.ExpiresAt
	res.ExpiresIn = humanLeft(a)
}

func humanLeft(a *auth.Auth) string {
	if a.ExpiresAt <= 0 {
		return "未知"
	}
	d := time.Until(time.Unix(a.ExpiresAt, 0))
	switch {
	case d <= 0:
		return "已过期"
	case d >= 24*time.Hour:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	}
}

// matchesAny 判断 auth 文件名是否命中选择器（uid 前缀，大小写无关；无选择器=全部）。
func matchesAny(base string, selectors []string) bool {
	if len(selectors) == 0 {
		return true
	}
	low := strings.ToLower(base)
	for _, s := range selectors {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if s == "all" || strings.Contains(low, s) {
			return true
		}
	}
	return false
}

func short(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
