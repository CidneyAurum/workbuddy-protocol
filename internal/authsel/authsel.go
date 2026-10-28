// Package authsel 解析命令行里的「账号选择器」（uid 前缀或 ALL）并过滤 auth 文件清单。
//
// 各一次性工具（signin / credit / activity / trial / refresh）原本只能全量跑，
// 工作台要支持「只对某一个号执行」，于是把选择器语义收敛到这一处，避免四个
// main 各写一份前缀匹配、慢慢长歪。
//
// 语义：
//   - 无选择器，或含 "ALL"（大小写无关）→ 全部账号；
//   - 否则任一选择器大小写无关地出现在文件名里即命中（auth 文件名为
//     workbuddy-<uid>.json，所以 uid 前缀、uid 片段都能选中）。
package authsel

import (
	"path/filepath"
	"strings"
)

// Parse 归一化选择器：去空白、丢弃空项；出现 ALL 直接返回 nil（= 全部）。
func Parse(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.EqualFold(a, "all") {
			return nil
		}
		out = append(out, strings.ToLower(a))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Match 判断某个 auth 文件是否命中；selectors 为空（=全部）时恒真。
func Match(file string, selectors []string) bool {
	if len(selectors) == 0 {
		return true
	}
	name := strings.ToLower(filepath.Base(file))
	for _, s := range selectors {
		// 再 lower 一次：调用方可能直接传原始大小写（Parse 已归一，但不能依赖）
		if strings.Contains(name, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// Filter 按选择器过滤文件清单（保持原顺序）。
func Filter(files []string, selectors []string) []string {
	if len(selectors) == 0 {
		return files
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if Match(f, selectors) {
			out = append(out, f)
		}
	}
	return out
}

// MatchID 直接对 uid（而不是文件名）做判断，供"池子里已有全量账号、
// 只想按选择器限制本轮动作范围"的场景使用（如活跃上报）。
func MatchID(uid string, selectors []string) bool {
	if len(selectors) == 0 {
		return true
	}
	low := strings.ToLower(uid)
	for _, s := range selectors {
		if strings.Contains(low, strings.ToLower(s)) {
			return true
		}
	}
	return false
}
