package pool

import (
	"reflect"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

func TestPreferOnlyFallbackNeverLeavesList(t *testing.T) {
	p := preferPool(t)
	p.SetPrefer([]string{"u3"}, true)
	p.Cooldown("u3", CoolHard, time.Hour, "selected account has no credit")
	p.Cooldown("u1", CoolSoft, time.Minute, "outside account is probeable")
	p.Disable("u2", "outside account disabled")
	if got := p.Pick(""); got != nil {
		t.Fatalf("only fallback escaped selection: got %s, want nil", got.UID)
	}
}

func TestPreferOnlyFallbackKeepsListedProbesAndHonorsTried(t *testing.T) {
	p := preferPool(t)
	p.SetPrefer([]string{"u2", "u3"}, true)
	p.Cooldown("u1", CoolSoft, time.Minute, "outside account expires first")
	p.Cooldown("u2", CoolSoft, 10*time.Minute, "selected probe")
	p.Cooldown("u3", CoolSoft, 20*time.Minute, "selected probe")
	if got := p.PickExcludingForRealm(map[string]bool{"u2": true}, "model", "cn"); got == nil || got.UID != "u3" {
		t.Fatalf("only fallback must probe an untried selected account: %v", got)
	}
	if got := p.PickExcludingForRealm(map[string]bool{"u2": true, "u3": true}, "model", "cn"); got != nil {
		t.Fatalf("exhausted only selection escaped to %s", got.UID)
	}
}

func TestPreferUnknownAndTriedFallBack(t *testing.T) {
	p := preferPool(t)
	p.SetPrefer([]string{"missing-account"}, false)
	if got := p.PickExcludingForRealm(nil, "model", "cn"); got == nil || got.Realm() != "cn" {
		t.Fatalf("unknown prefer selector must retain normal selection: %v", got)
	}
	p.SetPrefer([]string{"u3"}, false)
	if got := p.PickExcludingForRealm(map[string]bool{"u3": true}, "model", "cn"); got == nil || got.UID == "u3" {
		t.Fatalf("tried preferred account must allow fallback: %v", got)
	}
	p.SetPrefer([]string{"u3"}, true)
	if got := p.PickExcludingForRealm(map[string]bool{"u3": true}, "model", "cn"); got != nil {
		t.Fatalf("tried only account must not allow fallback: %v", got)
	}
}

func TestPreferAmbiguousSubstringRetainsConfiguredMatchSemantics(t *testing.T) {
	p := New("")
	for _, uid := range []string{"match-one", "match-two", "outside"} {
		p.Add(&auth.Auth{UID: uid})
	}
	p.SetPrefer([]string{" MATCH- "}, true)
	if got := p.AvailableUIDs(); !reflect.DeepEqual(got, []string{"match-one", "match-two"}) {
		t.Fatalf("legacy substring selector must expose all its matches: %v", got)
	}
	if got := p.PickExcludingForRealm(map[string]bool{"match-one": true}, "model", "cn"); got == nil || got.UID != "match-two" {
		t.Fatalf("second substring match must remain selectable: %v", got)
	}
	if got := p.PickByUIDForModel("outside", "model"); got != nil {
		t.Fatal("ambiguous selector must not allow a nonmatching account")
	}
}

func TestPreferExactUIDWinsOverSubstringCollision(t *testing.T) {
	for _, exactUID := range []string{"u1", "U1"} {
		for _, only := range []bool{false, true} {
			name := exactUID + "/prefer"
			if only {
				name = exactUID + "/only"
			}
			t.Run(name, func(t *testing.T) {
				p := New("")
				p.Add(&auth.Auth{UID: exactUID})
				p.Add(&auth.Auth{UID: "u12"})
				p.SetCredits("u12", 50000)
				p.SetPrefer([]string{" u1 "}, only)
				if got := p.AvailableUIDsForModelRealm("model", "cn"); !reflect.DeepEqual(got, []string{exactUID}) {
					t.Fatalf("exact UID selection expanded into a substring collision: %v", got)
				}
				if got := p.PickExcludingForRealm(nil, "model", "cn"); got == nil || got.UID != exactUID {
					t.Fatalf("exact UID did not win selection: %v", got)
				}
				if got := p.PickByUIDForModel(exactUID, "model"); got == nil {
					t.Fatal("exact selected UID unavailable to direct sticky pick")
				}
				if only {
					if got := p.PickByUIDForModel("u12", "model"); got != nil {
						t.Fatal("only direct pick accepted substring collision")
					}
				}
			})
		}
	}
}

func TestPreferExactOnlyDoesNotExpandAfterAccountRemoval(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "U1"})
	other := &auth.Auth{UID: "u12"}
	p.Add(other)
	p.SetPrefer([]string{"u1"}, true, true)
	p.SyncToDir([]*auth.Auth{other}) // Simulate selected auth file removal, without touching any files.
	if uids, only, exact := p.PreferConfigWithExact(); !only || !exact || !reflect.DeepEqual(uids, []string{"u1"}) {
		t.Fatalf("account removal changed exact selection: uids=%v only=%v exact=%v", uids, only, exact)
	}
	for _, cooled := range []bool{false, true} {
		if cooled {
			p.Cooldown("u12", CoolSoft, time.Minute, "outside probe candidate")
		}
		if got := p.PickExcludingForRealm(nil, "model", "cn"); got != nil {
			t.Fatalf("removed exact UID was reinterpreted as substring: %s", got.UID)
		}
		if got := p.PickByUIDForModel("u12", "model"); got != nil {
			t.Fatal("removed exact UID allowed a different sticky account")
		}
		if got := p.AvailableUIDs(); len(got) != 0 || p.ServableNow() {
			t.Fatalf("removed exact selection still looks available: %v", got)
		}
	}
	p.Add(&auth.Auth{UID: "U1"})
	if got := p.PickExcludingForRealm(nil, "model", "cn"); got == nil || got.UID != "U1" {
		t.Fatalf("reimported exact account did not become usable: %v", got)
	}
}

func TestPreferExactModePreservesLegacyOptIn(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u12"})
	p.SetPrefer([]string{"u1"}, true, true)
	if got := p.Pick(""); got != nil {
		t.Fatal("unknown exact UID must not become a fragment match")
	}
	p.SetPrefer([]string{"u1"}, true) // Existing callers/configuration retain legacy fragments.
	if _, _, exact := p.PreferConfigWithExact(); exact {
		t.Fatal("legacy SetPrefer call must not retain exact mode implicitly")
	}
	if got := p.Pick(""); got == nil || got.UID != "u12" {
		t.Fatalf("legacy fragment configuration stopped matching: %v", got)
	}
}

func TestPreferOnlyUnknownSelectorFailsClosed(t *testing.T) {
	p := preferPool(t)
	p.SetPrefer([]string{"missing-account"}, true)
	p.Cooldown("u1", CoolSoft, time.Minute, "outside account is probeable")
	if got := p.Pick(""); got != nil {
		t.Fatalf("unmatched only selector escaped selection: got %s", got.UID)
	}
	if got := p.AvailableUIDs(); len(got) != 0 {
		t.Fatalf("unmatched only selector exposed sticky candidates: %v", got)
	}
	if p.ServableNow() {
		t.Fatal("unmatched only selector must not report servable")
	}
}

func TestPreferAvailabilityUsesSelectionPolicy(t *testing.T) {
	cases := []struct {
		name    string
		only    bool
		prepare func(*Pool)
		want    []string
	}{
		{name: "prefer healthy", want: []string{"u3"}},
		{name: "only healthy", only: true, want: []string{"u3"}},
		{name: "prefer cooling fallback", prepare: func(p *Pool) { p.Cooldown("u3", CoolHard, time.Hour, "test") }, want: []string{"u1", "u2"}},
		{name: "only cooling no fallback", only: true, prepare: func(p *Pool) { p.Cooldown("u3", CoolHard, time.Hour, "test") }, want: []string{}},
		{name: "prefer disabled fallback", prepare: func(p *Pool) { p.Disable("u3", "test") }, want: []string{"u1", "u2"}},
		{name: "only disabled no fallback", only: true, prepare: func(p *Pool) { p.Disable("u3", "test") }, want: []string{}},
		{name: "prefer full fallback", prepare: func(p *Pool) { p.SetMaxInFlight(1); p.Acquire("u3") }, want: []string{"u1", "u2"}},
		{name: "only full no fallback", only: true, prepare: func(p *Pool) { p.SetMaxInFlight(1); p.Acquire("u3") }, want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := preferPool(t)
			p.SetPrefer([]string{"u3"}, tc.only)
			if tc.prepare != nil {
				tc.prepare(p)
			}
			views := map[string][]string{
				"all":         p.AvailableUIDs(),
				"model":       p.AvailableUIDsForModel("test-model"),
				"realm":       p.AvailableUIDsForRealm("cn"),
				"model realm": p.AvailableUIDsForModelRealm("test-model", "cn"),
			}
			for name, got := range views {
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s candidates=%v, want %v", name, got, tc.want)
				}
			}
		})
	}
}

func TestPreferAvailabilityKeepsRealmAndModelBoundaries(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := New("")
	p.Add(&auth.Auth{UID: "cn1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn2", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "global1", Domain: "www.workbuddy.ai"})
	p.SetPrefer([]string{"global1"}, false)
	if got := p.AvailableUIDsForModelRealm("model", "cn"); !reflect.DeepEqual(got, []string{"cn1", "cn2"}) {
		t.Fatalf("prefer must fall back within requested realm: %v", got)
	}
	if got := p.PickExcludingForRealm(nil, "model", "cn"); got == nil || got.Realm() != "cn" {
		t.Fatalf("prefer pick crossed requested realm: %v", got)
	}
	p.SetPrefer([]string{"global1"}, true)
	if got := p.AvailableUIDsForModelRealm("model", "cn"); len(got) != 0 {
		t.Fatalf("only must not select outside requested realm: %v", got)
	}
	if p.ServableForRealm("cn") || !p.ServableForRealm("global") {
		t.Fatal("only realm health must follow selected realm")
	}
	if got := p.PickExcludingForRealm(nil, "model", "cn"); got != nil {
		t.Fatalf("only pick crossed requested realm: %v", got)
	}
	p.SetPrefer([]string{"cn1"}, true)
	p.BlockModelBackoff("cn1", "blocked-model", "test")
	if got := p.AvailableUIDsForModelRealm("blocked-model", "cn"); len(got) != 0 {
		t.Fatalf("only must not bypass selected account's model cooldown: %v", got)
	}
	if got := p.PickExcludingForRealm(nil, "blocked-model", "cn"); got != nil {
		t.Fatalf("only pick bypassed selected account's model cooldown: %v", got)
	}
	if got := p.AvailableUIDsForModelRealm("other-model", "cn"); !reflect.DeepEqual(got, []string{"cn1"}) {
		t.Fatalf("other model should remain available on selected account: %v", got)
	}
}

func TestPreferOnlyDirectPickAndClear(t *testing.T) {
	p := preferPool(t)
	p.SetPrefer([]string{"u3"}, true)
	if got := p.PickByUIDForModel("u1", "model"); got != nil {
		t.Fatalf("direct sticky pick bypassed only: %s", got.UID)
	}
	if got := p.PickByUIDForModel("u3", "model"); got == nil || got.UID != "u3" {
		t.Fatalf("selected sticky account unavailable: %v", got)
	}
	p.SetPrefer(nil, true)
	if got := p.PickByUIDForModel("u1", "model"); got == nil {
		t.Fatal("clearing selection must restore direct picks")
	}
	if got := p.AvailableUIDs(); !reflect.DeepEqual(got, []string{"u1", "u2", "u3"}) {
		t.Fatalf("clearing selection must restore all candidates: %v", got)
	}
}

func TestPreferHotChangePreservesInFlightLease(t *testing.T) {
	p := preferPool(t)
	p.SetMaxInFlight(1)
	if !p.Acquire("u1") {
		t.Fatal("failed to acquire existing request lease")
	}
	p.SetPrefer([]string{"u3"}, true, true)
	st, _ := p.Status("u1")
	if st.InFlight != 1 {
		t.Fatalf("policy change modified active lease: %d", st.InFlight)
	}
	if got := p.PickByUIDForModel("u1", "model"); got != nil {
		t.Fatal("new direct picks must follow updated only policy")
	}
	p.Release("u1")
	st, _ = p.Status("u1")
	if st.InFlight != 0 {
		t.Fatalf("existing request could not release lease: %d", st.InFlight)
	}
}

func TestPreferOnlyHealthDoesNotCountOutsideAccounts(t *testing.T) {
	p := preferPool(t)
	p.SetPrefer([]string{"u3"}, true)
	p.Cooldown("u3", CoolHard, time.Hour, "test")
	if p.ServableNow() || p.ServableForRealm("cn") {
		t.Fatal("healthy outside accounts must not make only policy servable")
	}
	p.SetPrefer(nil, false)
	if !p.ServableNow() {
		t.Fatal("clearing selection must restore normal health reporting")
	}
}
