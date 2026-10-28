// activity 一次性触发器：只跑一次 RunActivityNow（5 连发 + 领猫联动），部署后验证活跃上报闭环用，不常驻。
//
// 用法（部署后手动触发）：
//
//	# 容器内：先 cp 进去再 exec
//	docker cp activity-run workbuddy2api:/tmp/activity-run
//	docker exec -w /app workbuddy2api /tmp/activity-run
//
//	# 本地：在项目根目录（需 config.json + auths/ + data/）直接 run
//	go run ./cmd/activity
//
// 读取工作目录的 config.json（auth_dir / state_file / schedule / upstream.timeout_seconds），
// 加载 auths 后构建 pool + upstream，调用 scheduler.RunActivityNow 立即执行一次。
//
// schedule 段复用 internal/config 的同一份 Schedule 结构 + 默认值（issue #49）：
// 与 cmd/server 共用，缺省 activity_report_count=5 不再各自复制漂移。
package main

import (
	"encoding/json"
	"log"
	"os"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/authsel"
	"workbuddy2api/internal/config"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/scheduler"
	"workbuddy2api/internal/upstream"
)

// cfgFile 只取本工具需要的字段；Schedule 段直接用 internal/config.Schedule
// （与 cmd/server 同源），其余段保持精简内联。
type cfgFile struct {
	AuthDir   string            `json:"auth_dir"`
	StateFile string            `json:"state_file"`
	Schedule  config.Schedule   `json:"schedule"`
	Upstream  struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	} `json:"upstream"`
}

func main() {
	// 位置参数 = 账号选择器（uid 前缀或 ALL）：只对这些号做活跃上报。
	// 池里仍然装载全部账号（SyncToDir 会剔除未列出的号并写 state.json，
	// 过滤后传进去会连带清掉其余账号的积分/冷却状态），范围收窄交给
	// scheduler.Config.ActivityScope。
	selectors := authsel.Parse(os.Args[1:])
	raw, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	// 先置默认值再 Unmarshal：键缺席（或为 null）时字段原样保留默认，
	// 与 cmd/server 的 Load 同路——缺省 activity_report_count=5 而非 Go 零值 0。
	c := cfgFile{Schedule: config.DefaultSchedule()}
	if err := json.Unmarshal(raw, &c); err != nil {
		log.Fatalf("parse config: %v", err)
	}
	if err := c.Schedule.Normalize(); err != nil {
		log.Fatalf("normalize schedule: %v", err)
	}
	if c.AuthDir == "" {
		c.AuthDir = "./auths"
	}
	if c.StateFile == "" {
		c.StateFile = "data/state.json"
	}
	log.Printf("activity count=%d", c.Schedule.ActivityReportCount)

	auths, err := auth.LoadDir(c.AuthDir)
	if err != nil {
		log.Fatalf("load auths: %v", err)
	}
	log.Printf("loaded %d account(s)", len(auths))

	p := pool.New(c.StateFile)
	p.SyncToDir(auths)

	var scope map[string]bool
	if len(selectors) > 0 {
		scope = map[string]bool{}
		for _, a := range auths {
			if authsel.MatchID(a.UID, selectors) {
				scope[a.UID] = true
			}
		}
		if len(scope) == 0 {
			log.Fatalf("no account matches %v", os.Args[1:])
		}
		log.Printf("scope: %d account(s) selected", len(scope))
	}

	sch := scheduler.New(scheduler.Config{
		Pool:                p,
		Upstream:            newUpstream(&c),
		CheckinHours:        c.Schedule.CheckinHours,
		TravelHours:         c.Schedule.TravelHours,
		ActivityHours:       c.Schedule.ActivityHours,
		KeepaliveHours:      c.Schedule.KeepaliveHours,
		ActivityReportCount: c.Schedule.ActivityReportCount,
		ActivityScope:       scope,
	})
	sch.RunActivityNow()
	log.Printf("activity run complete")
}

// newUpstream 构造上游客户端并显式接线 global realm 路由。
// activity 是 CN 任务中心的上报工具（global 账号已被 scheduler 的 IsGlobal 门控跳过），
// 但 GlobalEnabled=false 会把工具自造的 global 账号请求路由到 CN base（codebuddy.cn）而必然失败。
// 与 cmd/signin、cmd/credit、cmd/trial 同风格：显式接线，避免 Producer 误读成「未适配」。
func newUpstream(c *cfgFile) *upstream.Client {
	up := upstream.New()
	up.GlobalEnabled = true
	if c.Upstream.TimeoutSeconds > 0 {
		up.HTTP.Timeout = time.Duration(c.Upstream.TimeoutSeconds) * time.Second
	}
	return up
}
