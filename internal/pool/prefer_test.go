package pool

import (
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// 造一个三账号池：u1/u2/u3，积分递减。
func preferPool(t *testing.T) *Pool {
	t.Helper()
	p := New("")
	for _, uid := range []string{"u1", "u2", "u3"} {
		p.Add(&auth.Auth{UID: uid})
	}
	p.SetCredits("u1", 50000)
	p.SetCredits("u2", 300)
	p.SetCredits("u3", 100)
	return p
}

func TestPreferEmptyPoolNoChange(t *testing.T) {
	withNoPickGap(t)
	p := preferPool(t)
	p.SetPrefer(nil, true) // 空名单：即便 only=true 也必须不生效
	if uids, only := p.PreferConfig(); len(uids) != 0 || only {
		t.Fatalf("empty prefer must not enable only: uids=%v only=%v", uids, only)
	}
	seen := map[string]int{}
	for i := 0; i < 300; i++ {
		a := p.Pick("")
		if a == nil {
			t.Fatal("empty prefer must keep normal rotation")
		}
		seen[a.UID]++
	}
	if len(seen) < 2 {
		t.Fatalf("normal rotation should use several accounts, got %v", seen)
	}
}

func TestPreferHitWinsOnlySpecified(t *testing.T) {
	withNoPickGap(t)
	p := preferPool(t)
	p.SetPrefer([]string{"u3"}, false) // 指定 u3（积分最低，正常几乎不会被选中）
	for i := 0; i < 200; i++ {
		if got := p.Pick(""); got == nil || got.UID != "u3" {
			t.Fatalf("prefer mode must always pick u3, got %+v", got)
		}
	}
}

func TestPreferMultipleUidsRoundRobinWithinList(t *testing.T) {
	withNoPickGap(t)
	p := preferPool(t)
	p.SetPrefer([]string{"u2", "u3"}, false)
	seen := map[string]int{}
	for i := 0; i < 300; i++ {
		got := p.Pick("")
		if got == nil {
			t.Fatal("nil pick with healthy preferred accounts")
		}
		if got.UID != "u2" && got.UID != "u3" {
			t.Fatalf("picked outside prefer list: %s", got.UID)
		}
		seen[got.UID]++
	}
	if seen["u2"] == 0 || seen["u3"] == 0 {
		t.Fatalf("both preferred accounts should be used: %v", seen)
	}
}

// 优先号冷却时：prefer 模式必须回落全量（不能 503），only 模式必须拒绝越权。
func TestPreferFallbackWhenPreferredCooling(t *testing.T) {
	p := preferPool(t)
	p.SetPrefer([]string{"u3"}, false)
	p.Cooldown("u3", CoolHard, time.Hour, "test")
	got := p.Pick("")
	if got == nil {
		t.Fatal("prefer mode must fall back to other accounts, not 503")
	}
	if got.UID == "u3" {
		t.Fatalf("cooled preferred account must not be picked: %+v", got)
	}
}

func TestPreferOnlyDoesNotFallBack(t *testing.T) {
	p := preferPool(t)
	p.SetPrefer([]string{"u3"}, true) // 独占
	p.Cooldown("u3", CoolHard, time.Hour, "test")
	if got := p.Pick(""); got != nil {
		t.Fatalf("only mode must not use non-listed account, got %+v", got)
	}
}

// uid 片段匹配（与 authsel.MatchID 同口径）。
func TestPreferSubstringMatch(t *testing.T) {
	p := New("")
	full := "3a004cbea3a83dfe6bb9f67e"
	p.Add(&auth.Auth{UID: full})
	p.Add(&auth.Auth{UID: "other-uid"})
	p.SetPrefer([]string{"3A004CBE"}, false) // 大写前缀也应命中
	if got := p.Pick(""); got == nil || got.UID != full {
		t.Fatalf("substring prefer should match, got %+v", got)
	}
}
