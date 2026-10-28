package anthropiccompat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const initial = `{"model":"test-model","max_tokens":1024,"system":[{"type":"text","text":"system one"},{"type":"text","text":"system two"}],"messages":[{"role":"user","content":"run"}],"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}`

func body(t *testing.T, stream bool, fields object) []byte {
	t.Helper()
	var value object
	_ = json.Unmarshal([]byte(initial), &value)
	value["stream"] = stream
	for k, v := range fields {
		value[k] = v
	}
	b, _ := json.Marshal(value)
	return b
}
func streamEvents(t *testing.T, text string) []object {
	t.Helper()
	out := []object{}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "data: ") {
			var v object
			if err := json.Unmarshal([]byte(line[6:]), &v); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
	}
	return out
}
func eventHas(events []object, kind string) bool {
	for _, e := range events {
		if e["type"] == kind {
			return true
		}
	}
	return false
}
func execute(t *testing.T, stream bool, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(string(body(t, stream, nil))))
	req.Header.Set("x-api-key", "test-secret")
	Wrap(handler).ServeHTTP(rec, req)
	return rec
}
func TestRequestConversionAndHistory(t *testing.T) {
	raw := body(t, false, object{"messages": []any{
		object{"role": "user", "content": "read"},
		object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "toolu_a", "name": "read_file", "input": object{"path": "a"}}}},
		object{"role": "user", "content": []any{object{"type": "text", "text": "try again"}, object{"type": "tool_result", "tool_use_id": "toolu_a", "content": []any{object{"type": "text", "text": "not found"}}, "is_error": true}}},
	}, "tool_choice": object{"type": "tool", "name": "read_file", "disable_parallel_tool_use": true}})
	p, err := convertRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got object
	_ = json.Unmarshal(p.body, &got)
	ms := list(got["messages"])
	if obj(ms[0])["content"] != "system one\nsystem two" || obj(ms[3])["role"] != "tool" || obj(ms[4])["content"] != "try again" {
		t.Fatal(got)
	}
	if got["parallel_tool_calls"] != false || !strings.Contains(str(obj(ms[3])["content"]), "failed") {
		t.Fatal(got)
	}
}
func TestInvalidRequestsDoNotCallGateway(t *testing.T) {
	cases := []object{
		{"max_tokens": 0}, {"messages": []any{}}, {"thinking": object{"type": "adaptive"}},
		{"tools": []any{object{"type": "web_search_20250305", "name": "web_search"}}},
		{"tool_choice": object{"type": "tool", "name": "missing"}},
		{"messages": []any{object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "lost", "content": "x"}}}}},
		{"messages": []any{object{"role": "user", "content": []any{object{"type": "image", "source": object{}}}}}},
	}
	for i, fields := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rec := httptest.NewRecorder()
			Wrap(func(http.ResponseWriter, *http.Request) { t.Fatal("must not forward invalid request") }).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(body(t, false, fields)))))
			if rec.Code != 400 {
				t.Fatal(rec.Code)
			}
		})
	}
}
func TestAuthAndNonStreamingTool(t *testing.T) {
	rec := execute(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Fatal("key not translated")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"finish_reason":"tool_calls","message":{"tool_calls":[{"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]}}],"usage":{"prompt_tokens":120,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":100}}}`)
	})
	var result object
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if rec.Code != 200 || result["stop_reason"] != "tool_use" || obj(result["usage"])["input_tokens"] != float64(20) || len(list(result["content"])) != 1 {
		t.Fatal(rec.Code, result)
	}
}
func TestStreamingFragmentsAndTerminalUsage(t *testing.T) {
	rec := execute(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		wire := `data: {"choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\r\n\r\n" +
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"中文.txt\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
			`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":17}}` + "\n\ndata: [DONE]\n\n"
		for i := 0; i < len(wire); i++ {
			if _, err := w.Write([]byte(wire[i : i+1])); err != nil {
				t.Fatal(err)
			}
		}
	})
	events := streamEvents(t, rec.Body.String())
	if !eventHas(events, "message_stop") || eventHas(events, "error") {
		t.Fatal(rec.Body.String())
	}
	args := ""
	var last object
	for _, e := range events {
		if e["type"] == "content_block_delta" {
			args += str(obj(e["delta"])["partial_json"])
		}
		if e["type"] == "message_delta" {
			last = e
		}
	}
	if args != `{"path":"中文.txt"}` || obj(last["usage"])["output_tokens"] != float64(17) {
		t.Fatal(args, last)
	}
}
func TestTruncationAndInvalidToolNeverSucceed(t *testing.T) {
	for _, payload := range []string{
		`{"choices":[{"delta":{"content":"partial"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"bad","function":{"name":"read_file","arguments":"{"}}]},"finish_reason":"tool_calls"}]}`,
		`{"error":{"message":"broken"}}`,
	} {
		rec := execute(t, true, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
		})
		if strings.Contains(rec.Body.String(), "message_stop") || !strings.Contains(rec.Body.String(), "error") {
			t.Fatal(rec.Body.String())
		}
	}
}
func TestAuthenticationErrorStaysHTTP401(t *testing.T) {
	rec := execute(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	})
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "authentication_error") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
func TestConflictingCredentialsRejected(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(initial))
	r.Header.Set("Authorization", "Bearer a")
	r.Header.Set("x-api-key", "b")
	Wrap(func(http.ResponseWriter, *http.Request) { t.Fatal("must not forward") }).ServeHTTP(rec, r)
	if rec.Code != 401 {
		t.Fatal(rec.Code)
	}
}

func TestClaudeCodeInlineSystemMessages(t *testing.T) {
	raw := body(t, false, object{"messages": []any{
		object{"role": "user", "content": "hi"},
		object{"role": "system", "content": "session reminder"},
		object{"role": "developer", "content": []any{object{"type": "text", "text": "developer instruction"}}},
	}})
	r, err := convertRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got object
	_ = json.Unmarshal(r.body, &got)
	ms := list(got["messages"])
	if len(ms) != 2 || obj(ms[0])["role"] != "system" || obj(ms[0])["content"] != "system one\nsystem two\n\nsession reminder\n\ndeveloper instruction" || obj(ms[1])["role"] != "user" {
		t.Fatal(got)
	}
}

func TestRouteMetadataHeadersDoNotLeakCredentials(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			rec := execute(t, streaming, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Qoder-Upstream-Model-Key", "test-upstream-route")
				w.Header().Set("X-Upstream-Token", "must-not-leak")
				w.Header().Set("Set-Cookie", "private=must-not-leak")
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
			})
			if rec.Code != http.StatusOK || rec.Header().Get("X-Qoder-Upstream-Model-Key") != "test-upstream-route" {
				t.Fatal("routing diagnostic missing", rec.Code, rec.Header())
			}
			if rec.Header().Get("X-Upstream-Token") != "" || rec.Header().Get("Set-Cookie") != "" {
				t.Fatal("private upstream headers leaked")
			}
		})
	}
}

func TestNoSystemInventedAndToolTextPreserved(t *testing.T) {
	const literal = "Keep exact bytes: 11128, Main branch (you will usually use this for PRs)."
	raw := body(t, false, object{"system": nil, "messages": []any{
		object{"role": "user", "content": literal},
		object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "toolu_literal", "name": "read_file", "input": object{"path": literal}}}},
		object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "toolu_literal", "content": literal}}},
	}})
	prepared, err := convertRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got object
	_ = json.Unmarshal(prepared.body, &got)
	messages := list(got["messages"])
	if len(messages) != 3 || obj(messages[0])["role"] != "user" || obj(messages[0])["content"] != literal || obj(messages[2])["content"] != literal {
		t.Fatal("adapter injected system or changed text", got)
	}
	call := obj(list(obj(messages[1])["tool_calls"])[0])
	var args object
	if err := json.Unmarshal([]byte(str(obj(call["function"])["arguments"])), &args); err != nil || args["path"] != literal {
		t.Fatal("tool input changed", args, err)
	}
}
