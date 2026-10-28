// 指定账号优先：preferUIDs 命中的账号在选号时优先参与，preferOnly 时只允许这些账号。
// 空名单 = 完全不改变原行为（零回归）。
package pool

import "strings"

// SetPrefer 注入优先名单。uids 为空表示不启用优先（恢复普通轮换）。
// only=true 时只允许名单内账号被选中（硬独占）；false 时优先用名单内账号，
// 名单内全部不可用（冷却/熔断/在途占满）时自动回落全量轮换，不会把服务打死。
// exact=true 始终按完整 UID 匹配；省略/false 保持旧手写片段配置的兼容语义。
func (p *Pool) SetPrefer(uids []string, only bool, exact ...bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	clean := make([]string, 0, len(uids))
	for _, u := range uids {
		u = strings.ToLower(strings.TrimSpace(u))
		if u != "" {
			clean = append(clean, u)
		}
	}
	p.preferUIDs = clean
	p.preferOnly = only && len(clean) > 0
	p.preferExact = len(exact) > 0 && exact[0]
}

// PreferConfig 返回当前优先名单配置（状态透出用）。
func (p *Pool) PreferConfig() ([]string, bool) {
	uids, only, _ := p.PreferConfigWithExact()
	return uids, only
}

// PreferConfigWithExact 原子返回名单、独占模式与完整 UID 匹配标记。
// 原有 PreferConfig 保留双返回值，兼容已有调用者。
func (p *Pool) PreferConfigWithExact() ([]string, bool, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.preferUIDs...), p.preferOnly, p.preferExact
}

// preferMatch 报告 uid 是否命中优先名单。空名单恒 false。
// 完整 UID 优先精确匹配（大小写无关）：选择 u1 不能顺带选中 u12。
// preferExact=true 时，不存在/删除的完整 UID 也不会退化成片段。
// 仅 legacy 模式且池内不存在对应完整 UID 时，保留手写片段的子串匹配语义。
// 调用方必须持有 p.mu；账号同步后，匹配基于当前池内的完整 UID。
func (p *Pool) preferMatch(uid string) bool {
	if len(p.preferUIDs) == 0 {
		return false
	}
	low := strings.ToLower(uid)
	for _, s := range p.preferUIDs {
		if low == s || (!p.preferExact && strings.Contains(low, s) && !p.hasExactPreferUID(s)) {
			return true
		}
	}
	return false
}

// hasExactPreferUID 判断选择器是否是池内的完整 UID。常规小写 UID 走 map
// 快查；保留大小写不同的历史 UID 兼容。调用方必须持有 p.mu。
func (p *Pool) hasExactPreferUID(selector string) bool {
	if _, ok := p.byUID[selector]; ok {
		return true
	}
	for uid := range p.byUID {
		if strings.EqualFold(uid, selector) {
			return true
		}
	}
	return false
}
