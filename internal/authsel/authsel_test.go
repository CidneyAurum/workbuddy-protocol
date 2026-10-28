package authsel

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{"", "  "}, nil},
		{[]string{"ALL"}, nil},
		{[]string{"all"}, nil},
		{[]string{"11111111"}, []string{"11111111"}},
		{[]string{" a ", "ALL"}, nil}, // ALL 与具体号混写 → 全部
		{[]string{"a", "b"}, []string{"a", "b"}},
	}
	for _, c := range cases {
		if got := Parse(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatch(t *testing.T) {
	file := `auths\workbuddy-11111111-2222-4333-8444-555555555555.json`
	cases := []struct {
		selectors []string
		want      bool
	}{
		{nil, true},
		{[]string{"11111111"}, true},
		{[]string{"11111111"}, true}, // 大小写无关
		{[]string{"55555555"}, true},
		{[]string{"e8c413ef"}, false},
		{[]string{"e8c413ef", "11111111"}, true}, // 任一命中即可
	}
	for _, c := range cases {
		if got := Match(file, c.selectors); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.selectors, got, c.want)
		}
	}
}

func TestFilter(t *testing.T) {
	files := []string{"auths/workbuddy-aaaa.json", "auths/workbuddy-bbbb.json", "auths/workbuddy-cccc.json"}
	got := Filter(files, []string{"bbbb"})
	want := []string{"auths/workbuddy-bbbb.json"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Filter = %q, want %q", got, want)
	}
	// 空选择器原样返回（不复制，调用方只读）
	if got := Filter(files, nil); !reflect.DeepEqual(got, files) {
		t.Errorf("Filter(all) = %q, want %q", got, files)
	}
}
