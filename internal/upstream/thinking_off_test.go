package upstream

import (
	"encoding/json"
	"testing"
)

func TestThinkingHonorsEffortOff(t *testing.T) {
	for _, body := range []string{
		`{"model":"deepseek-v4.1-flash","reasoning_effort":"off","messages":[]}`,
		`{"model":"deepseek-v4.1-flash","reasoningEffort":"none","messages":[]}`,
		`{"model":"deepseek-v4.1-flash","reasoning_effort":"disabled","thinking":{"type":"enabled"},"messages":[]}`,
		`{"model":"deepseek-v4.1-flash","reasoning_effort":" OFF ","messages":[]}`,
	} {
		out := PrepareBodyOptWithEffortsAndDefault([]byte(body), false,
			map[string][]string{"deepseek-v4.1-flash": {"low", "high", "max"}}, nil)
		if typ, _ := getThinkingType(t, out); typ != "disabled" {
			t.Fatalf("thinking.type=%q, want disabled: %s", typ, out)
		}
		var obj map[string]any
		_ = json.Unmarshal(out, &obj)
		if obj["reasoning_effort"] != nil || obj["reasoningEffort"] != nil {
			t.Fatalf("disabled thinking retained effort: %s", out)
		}
	}
}

func TestThinkingOffDoesNotEnableOtherModels(t *testing.T) {
	for _, efforts := range []map[string][]string{nil, {"glm-5.3": {"low", "high", "max"}}} {
		out := PrepareBodyOptWithEffortsAndDefault(
			[]byte(`{"model":"glm-5.3","reasoning_effort":"off","messages":[]}`), false, efforts, nil)
		var obj map[string]any
		_ = json.Unmarshal(out, &obj)
		if obj["reasoning_effort"] != nil || obj["thinking"] != nil {
			t.Fatalf("off enabled reasoning or leaked an unsupported effort: %s", out)
		}
	}
}
