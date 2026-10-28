// Package anthropiccompat adds Messages to the existing Chat handler without
// a second server, credential store or account pool. Protocol references:
// https://code.claude.com/docs/en/llm-gateway-protocol
// https://github.com/router-for-me/CLIProxyAPI/tree/main/internal/translator/openai/claude
// This is an independent, standard-library-only implementation.
package anthropiccompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type object = map[string]any

func obj(v any) object  { m, _ := v.(map[string]any); return m }
func list(v any) []any  { a, _ := v.([]any); return a }
func str(v any) string  { s, _ := v.(string); return s }
func integer(v any) int { n, _ := v.(float64); return int(n) }

type request struct {
	body   []byte
	model  string
	stream bool
	names  map[string]bool
}

func textContent(value any) (string, error) {
	if s, ok := value.(string); ok {
		return s, nil
	}
	blocks, ok := value.([]any)
	if !ok {
		return "", errors.New("content must be a string or text blocks")
	}
	parts := make([]string, 0, len(blocks))
	for _, raw := range blocks {
		b := obj(raw)
		if str(b["type"]) != "text" {
			return "", errors.New("only text content is supported by this compatibility endpoint")
		}
		s, ok := b["text"].(string)
		if !ok {
			return "", errors.New("text block is missing text")
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n"), nil
}

func convertRequest(raw []byte) (*request, error) {
	var in object
	if err := json.Unmarshal(raw, &in); err != nil || in == nil {
		return nil, errors.New("invalid JSON request")
	}
	model := str(in["model"])
	max, numeric := in["max_tokens"].(float64)
	if strings.TrimSpace(model) == "" || !numeric || max < 1 || max != float64(int(max)) || max > 131072 || len(list(in["messages"])) == 0 {
		return nil, errors.New("model, messages and an integer max_tokens (1..131072) are required")
	}
	if mode := str(obj(in["thinking"])["type"]); mode != "" && mode != "disabled" {
		return nil, errors.New("signed Anthropic thinking is unsupported here; disable thinking in the Claude Code connection profile")
	}
	if in["output_config"] != nil && obj(in["output_config"])["format"] != nil {
		return nil, errors.New("structured output_config.format is unsupported")
	}
	stream, ok := in["stream"].(bool)
	if in["stream"] != nil && !ok {
		return nil, errors.New("stream must be boolean")
	}
	out := object{"model": model, "max_tokens": int(max), "stream": stream}
	for _, key := range []string{"temperature", "top_p", "metadata"} {
		if v, ok := in[key]; ok {
			out[key] = v
		}
	}
	if v := in["stop_sequences"]; v != nil {
		out["stop"] = v
	}
	if v := str(obj(in["output_config"])["effort"]); v != "" {
		out["reasoning_effort"] = v
	}
	if stream {
		out["stream_options"] = object{"include_usage": true}
	}
	messages := make([]any, 0, len(list(in["messages"]))+1)
	systems := []string{}
	if in["system"] != nil {
		s, err := textContent(in["system"])
		if err != nil {
			return nil, err
		}
		if s != "" {
			systems = append(systems, s)
		}
	}
	pending, seen := map[string]bool{}, map[string]bool{}
	for _, rawMessage := range list(in["messages"]) {
		m := obj(rawMessage)
		role := str(m["role"])
		// Claude Code and some gateway clients append inline system/developer
		// reminders. Preserve their authority by hoisting them, not as user text.
		if role == "system" || role == "developer" {
			s, err := textContent(m["content"])
			if err != nil {
				return nil, err
			}
			if s != "" {
				systems = append(systems, s)
			}
			continue
		}
		if role != "user" && role != "assistant" {
			return nil, errors.New("messages roles must be user or assistant")
		}
		if role == "assistant" && len(pending) != 0 {
			return nil, errors.New("tool_result is missing for a previous tool_use")
		}
		var blocks []any
		if s, ok := m["content"].(string); ok {
			blocks = []any{object{"type": "text", "text": s}}
		} else {
			blocks, ok = m["content"].([]any)
			if !ok {
				return nil, errors.New("message content is required")
			}
		}
		texts, calls := []string{}, []any{}
		for _, rawBlock := range blocks {
			b := obj(rawBlock)
			switch str(b["type"]) {
			case "text":
				s, ok := b["text"].(string)
				if !ok {
					return nil, errors.New("text block is missing text")
				}
				texts = append(texts, s)
			case "tool_use":
				id, name := str(b["id"]), str(b["name"])
				if role != "assistant" || id == "" || name == "" || seen[id] || obj(b["input"]) == nil {
					return nil, errors.New("invalid or duplicate tool_use")
				}
				args, _ := json.Marshal(b["input"])
				calls = append(calls, object{"id": id, "type": "function", "function": object{"name": name, "arguments": string(args)}})
				seen[id], pending[id] = true, true
			case "tool_result":
				id := str(b["tool_use_id"])
				if role != "user" || !pending[id] {
					return nil, errors.New("tool_result must match an outstanding tool_use ID")
				}
				content := b["content"]
				if content == nil {
					content = ""
				}
				s, err := textContent(content)
				if err != nil {
					return nil, err
				}
				if b["is_error"] == true {
					s = "Tool execution failed:\n" + s
				}
				messages = append(messages, object{"role": "tool", "tool_call_id": id, "content": s})
				delete(pending, id)
			case "thinking", "redacted_thinking":
				return nil, errors.New("thinking history is unsupported; start a new session with thinking disabled")
			default:
				return nil, fmt.Errorf("unsupported content block type %q; text and client tool calls only", str(b["type"]))
			}
		}
		if role == "user" && len(pending) != 0 {
			return nil, errors.New("all pending tool results must precede the next user turn")
		}
		if len(texts) > 0 || len(calls) > 0 {
			message := object{"role": role, "content": strings.Join(texts, "\n")}
			if len(calls) > 0 {
				message["tool_calls"] = calls
				if len(texts) == 0 {
					message["content"] = nil
				}
			}
			messages = append(messages, message)
		}
	}
	if len(pending) > 0 {
		return nil, errors.New("tool_use history is missing tool_result")
	}
	if len(messages) == 0 {
		return nil, errors.New("messages is empty")
	}
	if len(systems) > 0 {
		messages = append([]any{object{"role": "system", "content": strings.Join(systems, "\n\n")}}, messages...)
	}
	out["messages"] = messages
	names, tools := map[string]bool{}, []any{}
	if in["tools"] != nil {
		if _, ok := in["tools"].([]any); !ok {
			return nil, errors.New("tools must be an array")
		}
	}
	for _, rawTool := range list(in["tools"]) {
		t := obj(rawTool)
		name, typ := str(t["name"]), str(t["type"])
		if name == "" || len(name) > 128 || names[name] || (typ != "" && typ != "custom") || obj(t["input_schema"]) == nil {
			return nil, errors.New("only uniquely named client tools with input_schema are supported; disable tool search and hosted tools")
		}
		names[name] = true
		fn := object{"name": name, "parameters": t["input_schema"]}
		if t["description"] != nil {
			fn["description"] = t["description"]
		}
		tools = append(tools, object{"type": "function", "function": fn})
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if in["tool_choice"] != nil {
		choice := obj(in["tool_choice"])
		switch str(choice["type"]) {
		case "auto", "none":
			out["tool_choice"] = choice["type"]
		case "any":
			if len(tools) == 0 {
				return nil, errors.New("tool_choice any requires tools")
			}
			out["tool_choice"] = "required"
		case "tool":
			name := str(choice["name"])
			if !names[name] {
				return nil, errors.New("tool_choice names an undefined tool")
			}
			out["tool_choice"] = object{"type": "function", "function": object{"name": name}}
		default:
			return nil, errors.New("invalid tool_choice")
		}
		if v, ok := choice["disable_parallel_tool_use"].(bool); ok {
			out["parallel_tool_calls"] = !v
		}
	}
	body, err := json.Marshal(out)
	return &request{body: body, model: model, stream: stream, names: names}, err
}
