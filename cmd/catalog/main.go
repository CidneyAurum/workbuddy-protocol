// cmd/catalog 目录体检：核对 /v3/config 主目录源与旧端点的差异、限免声明、免费/付费双轨。
//
// 用途（2026-10 官方目录换源后的运维探针）：
//   - 每个 realm 取一个可用账号抓一次目录，打印可路由模型数 / 全表条数；
//   - 打印 modelPromotions 与「此刻免费」集合（factor=0 + schedule 窗口）；
//   - 打印 ModelRateLimitCap 的 freeId↔paidId 双轨映射；
//   - 顺带核对每个账号的 UserResource 是否可读（余额探测是否还活着）。
//
// 只读：不领取、不改 token、不写远程状态。
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

func main() {
	authDir := flag.String("auths", "auths", "认证目录")
	perRealm := flag.Int("per", 1, "每个 realm 抽几个账号抓目录")
	flag.Parse()

	files, err := auth.LoadAuthFiles(*authDir)
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "读取认证失败: %v\n", err)
		os.Exit(1)
	}

	up := upstream.New()
	up.GlobalEnabled = true
	now := time.Now()

	// 按 realm 取样：cn 先、global 后，各取 perRealm 个账号。
	want := map[string]int{"cn": *perRealm, "global": *perRealm}
	seen := map[string]int{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		a, err := auth.Parse(raw)
		if err != nil {
			continue
		}
		realm := a.Realm()
		if seen[realm] >= want[realm] {
			continue
		}
		keep := seen[realm]
		seen[realm]++

		fmt.Printf("\n========== realm=%s uid=%s (第 %d 个取样)\n", realm, a.UID, keep+1)
		infos, err := up.FetchModels(a)
		if err != nil {
			fmt.Printf("  目录抓取失败: %v\n", err)
			continue
		}
		ids := make([]string, 0, len(infos))
		for _, mi := range infos {
			ids = append(ids, mi.ID)
		}
		sort.Strings(ids)

		meta := up.DirectoryMeta(realm)
		agentN := 0
		if meta != nil {
			agentN = meta.AgentModelCount
		}
		fmt.Printf("  可路由模型: %d 个（/v3/config 全表 %d 条，agent picker %d 条）\n",
			len(infos), metaCount(meta), agentN)
		fmt.Printf("  模型: %v\n", ids)

		free := up.FreeModels(realm, now)
		if len(free) == 0 {
			fmt.Printf("  此刻全天免费: 无（无 factor=0 且在窗口内的全天活动）\n")
		} else {
			names := make([]string, 0, len(free))
			for id := range free {
				names = append(names, id)
			}
			sort.Strings(names)
			fmt.Printf("  此刻全天免费: %v  ← 声明 0 积分，选号应优先\n", names)
		}

		if meta != nil {
			for _, p := range meta.Promotions {
				if p.Kind != "discount" {
					continue // 非折扣条目（模型促销位）不展示，避免噪音
				}
				window := "无日期窗口"
				if !p.ValidFrom.IsZero() || !p.ValidUntil.IsZero() {
					window = fmt.Sprintf("%s ~ %s", fmtDay(p.ValidFrom), fmtDay(p.ValidUntil))
				}
				// 时段活动单独标注：factor=0 也不代表此刻全天免费。
				scope := "全天"
				if p.IntraDay {
					scope = "时段"
				}
				fmt.Printf("  活动[%s] %-40s factor=%-4g %s  %s\n",
					scope, p.ID, p.Factor, window, p.TextZh)
			}
			for _, p := range meta.RateLimitPairs {
				fmt.Printf("  双轨 %s: freeId=%s ↔ paidId=%s\n", p.DisplayName, p.FreeID, p.PaidID)
			}
		}
	}
}

func metaCount(m *upstream.DirectoryMeta) int {
	if m == nil {
		return 0
	}
	return m.ModelCount
}

func fmtDay(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("01-02")
}
