package anthropiccompat

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const bodyLimit = 8 << 20

func errorType(status int) string {
	switch status {
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 503:
		return "overloaded_error"
	}
	if status >= 500 {
		return "api_error"
	}
	return "invalid_request_error"
}
func errorBody(status int, message string) object {
	return object{"type": "error", "error": object{"type": errorType(status), "message": message}}
}
func replyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody(status, message))
}

// Wrap is installed only on /v1/messages. Existing OpenAI routes are unchanged.
// Authentication/account selection/retries/usage continue through the old handler.
func Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, bearer := r.Header.Get("x-api-key"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key != "" && bearer != "" && subtle.ConstantTimeCompare([]byte(key), []byte(bearer)) != 1 {
			replyError(w, 401, "conflicting credentials")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
		if err != nil {
			replyError(w, 400, "could not read request")
			return
		}
		if len(raw) > bodyLimit {
			replyError(w, 413, "request exceeds 8 MiB")
			return
		}
		prepared, err := convertRequest(raw)
		if err != nil {
			replyError(w, 400, err.Error())
			return
		}
		if len(prepared.body) > bodyLimit {
			replyError(w, 413, "converted request exceeds 8 MiB")
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		forward := r.Clone(ctx)
		forward.Header = r.Header.Clone()
		if bearer == "" && key != "" {
			forward.Header.Set("Authorization", "Bearer "+key)
		}
		forward.Header.Set("Content-Type", "application/json")
		forward.Header.Del("Content-Length")
		forward.Body = io.NopCloser(bytes.NewReader(prepared.body))
		forward.ContentLength = int64(len(prepared.body))
		a := &writer{w: w, header: make(http.Header), req: prepared, cancel: cancel, textIndex: -1, tools: map[int]*toolState{}, usage: object{"input_tokens": 0, "output_tokens": 0}}
		done := make(chan struct{})
		if prepared.stream {
			go func() {
				ticker := time.NewTicker(10 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-done:
						return
					case <-ctx.Done():
						return
					case <-ticker.C:
						a.ping()
					}
				}
			}()
		}
		defer close(done)
		next(a, forward)
		a.finish()
	}
}

type toolState struct {
	id, name, arguments string
	block               int
	sent                int
	opened              bool
}
type writer struct {
	w                             http.ResponseWriter
	header                        http.Header
	req                           *request
	cancel                        context.CancelFunc
	mu                            sync.Mutex
	status                        int
	buffer, line, data            bytes.Buffer
	started, terminal, finishSeen bool
	err                           error
	id, reason                    string
	textIndex, nextIndex          int
	tools                         map[int]*toolState
	usage                         object
}

func (a *writer) Header() http.Header { return a.header }
func (a *writer) WriteHeader(code int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status == 0 {
		a.status = code
	}
}
func (a *writer) Flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started && a.err == nil {
		if f, ok := a.w.(http.Flusher); ok {
			f.Flush()
		}
	}
}
func (a *writer) ping() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started && !a.terminal && a.err == nil {
		a.event("ping", object{"type": "ping"})
	}
}
func (a *writer) copyHeaders() {
	// Keep diagnostic routing metadata, never blindly copy upstream auth headers.
	for _, key := range []string{"X-Request-ID", "Request-ID", "Retry-After", "X-Qoder-Upstream-Model-Key"} {
		if v := a.header.Get(key); v != "" {
			a.w.Header().Set(key, v)
		}
	}
	a.w.Header().Set("Cache-Control", "no-cache, no-transform")
	a.w.Header().Set("X-Accel-Buffering", "no")
	a.w.Header().Set("X-LocalAPI-Protocol", "anthropic-messages-v1")
}
func (a *writer) start() {
	if a.started {
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	a.id = "msg_" + hex.EncodeToString(b)
	a.copyHeaders()
	a.w.Header().Set("Content-Type", "text/event-stream")
	a.w.WriteHeader(200)
	a.started = true
	a.event("message_start", object{"type": "message_start", "message": object{"id": a.id, "type": "message", "role": "assistant", "model": a.req.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": a.usage}})
}
func (a *writer) event(name string, value object) {
	if a.err != nil {
		return
	}
	raw, _ := json.Marshal(value)
	_, a.err = fmt.Fprintf(a.w, "event: %s\ndata: %s\n\n", name, raw)
	if a.err != nil {
		a.cancel()
		return
	}
	if f, ok := a.w.(http.Flusher); ok {
		f.Flush()
	}
}
func (a *writer) fail(message string) {
	if a.terminal {
		return
	}
	a.terminal = true
	if a.started {
		a.event("error", errorBody(502, message))
	} else {
		a.copyHeaders()
		replyError(a.w, 502, message)
		a.started = true
	}
	a.cancel()
}
func (a *writer) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return 0, a.err
	}
	if a.status == 0 {
		a.status = 200
	}
	if a.status >= 400 || !a.req.stream || !strings.Contains(a.header.Get("Content-Type"), "text/event-stream") {
		if a.buffer.Len()+len(p) > 32<<20 {
			a.fail("upstream response exceeds 32 MiB")
			return 0, errors.New("response too large")
		}
		return a.buffer.Write(p)
	}
	for _, c := range p {
		if c != '\n' {
			a.line.WriteByte(c)
			if a.line.Len() > bodyLimit {
				a.fail("upstream SSE line too large")
				return 0, errors.New("SSE line too large")
			}
			continue
		}
		line := strings.TrimSuffix(a.line.String(), "\r")
		a.line.Reset()
		if line == "" {
			if a.data.Len() > 0 {
				a.chunk(strings.TrimSuffix(a.data.String(), "\n"))
				a.data.Reset()
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			a.data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			a.data.WriteByte('\n')
		}
	}
	return len(p), a.err
}
func (a *writer) mergeUsage(value any) {
	u := obj(value)
	if u == nil {
		return
	}
	if n, ok := u["prompt_tokens"].(float64); ok {
		cached := integer(obj(u["prompt_tokens_details"])["cached_tokens"])
		if cached < 0 || cached > int(n) {
			cached = 0
		}
		a.usage["input_tokens"] = int(n) - cached
		a.usage["cache_read_input_tokens"] = cached
	}
	if n, ok := u["completion_tokens"].(float64); ok {
		a.usage["output_tokens"] = int(n)
	}
}
func (a *writer) chunk(payload string) {
	if a.terminal || a.err != nil {
		return
	}
	if payload == "[DONE]" {
		a.complete()
		return
	}
	var chunk object
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		a.fail("invalid upstream SSE JSON")
		return
	}
	if chunk["error"] != nil {
		a.fail("upstream returned a streaming error; inspect gateway logs")
		return
	}
	a.mergeUsage(chunk["usage"])
	a.start()
	for _, raw := range list(chunk["choices"]) {
		c := obj(raw)
		if integer(c["index"]) != 0 {
			a.fail("multiple completion choices are unsupported")
			return
		}
		delta := obj(c["delta"])
		if text := str(delta["content"]); text != "" {
			if a.textIndex < 0 {
				a.textIndex = a.nextIndex
				a.nextIndex++
				a.event("content_block_start", object{"type": "content_block_start", "index": a.textIndex, "content_block": object{"type": "text", "text": ""}})
			}
			a.event("content_block_delta", object{"type": "content_block_delta", "index": a.textIndex, "delta": object{"type": "text_delta", "text": text}})
		}
		for _, rawTool := range list(delta["tool_calls"]) {
			t := obj(rawTool)
			index := integer(t["index"])
			if index < 0 || index > 255 {
				a.fail("invalid tool call index")
				return
			}
			state := a.tools[index]
			if state == nil {
				state = &toolState{}
				a.tools[index] = state
			}
			if id := str(t["id"]); id != "" {
				if state.id != "" && state.id != id {
					a.fail("tool call ID changed mid-stream")
					return
				}
				state.id = id
			}
			fn := obj(t["function"])
			if name := str(fn["name"]); name != "" {
				if state.opened && name != state.name {
					a.fail("tool name changed mid-stream")
					return
				}
				if !state.opened {
					state.name += name
				}
			}
			state.arguments += str(fn["arguments"])
			if len(state.arguments) > bodyLimit {
				a.fail("tool arguments too large")
				return
			}
			a.emitTool(state)
		}
		if reason := str(c["finish_reason"]); reason != "" {
			a.finishSeen = true
			a.reason = reason
		}
	}
}
func (a *writer) emitTool(t *toolState) {
	if !t.opened && t.id != "" && a.req.names[t.name] && t.arguments != "" {
		t.block = a.nextIndex
		a.nextIndex++
		t.opened = true
		a.event("content_block_start", object{"type": "content_block_start", "index": t.block, "content_block": object{"type": "tool_use", "id": t.id, "name": t.name, "input": object{}}})
	}
	if t.opened && len(t.arguments) > t.sent {
		a.event("content_block_delta", object{"type": "content_block_delta", "index": t.block, "delta": object{"type": "input_json_delta", "partial_json": t.arguments[t.sent:]}})
		t.sent = len(t.arguments)
	}
}
func stopReason(reason string, tools bool) string {
	if reason == "length" {
		return "max_tokens"
	}
	if reason == "content_filter" {
		return "refusal"
	}
	if tools || reason == "tool_calls" || reason == "function_call" {
		return "tool_use"
	}
	return "end_turn"
}
func (a *writer) complete() {
	if a.terminal {
		return
	}
	if !a.finishSeen {
		a.fail("upstream stream ended without finish_reason; response is incomplete")
		return
	}
	ids := map[string]bool{}
	for _, t := range a.tools {
		if t.arguments == "" {
			t.arguments = "{}"
		}
		var args object
		if t.id == "" || ids[t.id] || !a.req.names[t.name] || json.Unmarshal([]byte(t.arguments), &args) != nil || args == nil {
			a.fail("upstream returned incomplete or invalid tool calls; no successful tool stop emitted")
			return
		}
		ids[t.id] = true
	}
	a.start()
	indexes := make([]int, 0, len(a.tools))
	for i := range a.tools {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		a.emitTool(a.tools[i])
	}
	if a.nextIndex == 0 {
		a.fail("upstream returned no text or tool call")
		return
	}
	for i := 0; i < a.nextIndex; i++ {
		a.event("content_block_stop", object{"type": "content_block_stop", "index": i})
	}
	a.event("message_delta", object{"type": "message_delta", "delta": object{"stop_reason": stopReason(a.reason, len(a.tools) > 0), "stop_sequence": nil}, "usage": a.usage})
	a.event("message_stop", object{"type": "message_stop"})
	a.terminal = true
}
func (a *writer) finish() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminal || a.err != nil {
		return
	}
	if a.buffer.Len() == 0 {
		a.complete()
		return
	}
	var raw object
	if json.Unmarshal(a.buffer.Bytes(), &raw) != nil {
		a.fail("invalid upstream response JSON")
		return
	}
	if a.status >= 400 || raw["error"] != nil {
		status := a.status
		if status < 400 {
			status = 502
		}
		msg := str(obj(raw["error"])["message"])
		if msg == "" {
			msg = "gateway request failed"
		}
		a.copyHeaders()
		replyError(a.w, status, msg)
		a.terminal = true
		return
	}
	choices := list(raw["choices"])
	if len(choices) != 1 {
		a.fail("upstream response has no unique completion")
		return
	}
	c := obj(choices[0])
	message := obj(c["message"])
	reason := str(c["finish_reason"])
	if reason == "" {
		a.fail("upstream response has no finish_reason")
		return
	}
	content := []any{}
	if s := str(message["content"]); s != "" {
		content = append(content, object{"type": "text", "text": s})
	}
	ids := map[string]bool{}
	for _, v := range list(message["tool_calls"]) {
		t := obj(v)
		id := str(t["id"])
		fn := obj(t["function"])
		name := str(fn["name"])
		args := object{}
		arg := str(fn["arguments"])
		if arg == "" {
			arg = "{}"
		}
		if id == "" || ids[id] || !a.req.names[name] || json.Unmarshal([]byte(arg), &args) != nil || args == nil {
			a.fail("invalid upstream tool call")
			return
		}
		ids[id] = true
		content = append(content, object{"type": "tool_use", "id": id, "name": name, "input": args})
	}
	if len(content) == 0 {
		a.fail("upstream returned no text or tool call")
		return
	}
	a.mergeUsage(raw["usage"])
	if a.req.stream {
		a.fail("upstream did not provide an event stream")
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	a.copyHeaders()
	a.w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(a.w).Encode(object{"id": "msg_" + hex.EncodeToString(b), "type": "message", "role": "assistant", "model": a.req.model, "content": content, "stop_reason": stopReason(reason, len(ids) > 0), "stop_sequence": nil, "usage": a.usage})
	a.terminal = true
}
