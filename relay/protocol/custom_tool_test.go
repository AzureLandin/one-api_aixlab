package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/sse"
)

// A Responses custom (freeform) tool has no chat equivalent, so it goes upstream
// as a function taking one string argument — and the request has to remember
// that, because the response side is what puts the call back into its own shape.
func TestParseResponses_CustomToolBecomesFunction(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hi","tools":[
		{"type":"custom","name":"apply_patch","description":"Edit files.",
		 "format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("a custom tool must be accepted, got %+v", perr)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(req.Tools))
	}
	tool := req.Tools[0]
	if tool.Type != "function" || tool.Function.Name != "apply_patch" {
		t.Fatalf("a custom tool goes upstream as a function: %+v", tool)
	}
	schema, ok := tool.Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v", tool.Function.Parameters)
	}
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != customToolInputArgument {
		t.Fatalf("required = %#v, want [input]", schema["required"])
	}
	properties, _ := schema["properties"].(map[string]any)
	input, _ := properties[customToolInputArgument].(map[string]any)
	if input["type"] != "string" {
		t.Fatalf("the one argument must be a string: %#v", properties)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties must be false: %#v", schema["additionalProperties"])
	}
	// The schema can only say "a string", so the contract and the client's
	// grammar have to survive as prose.
	for _, want := range []string{"Edit files.", "freeform", "Lark", "start: /.+/"} {
		if !strings.Contains(tool.Function.Description, want) {
			t.Fatalf("description %q is missing %q", tool.Function.Description, want)
		}
	}
	if !req.CustomTools.Has("apply_patch") {
		t.Fatal("the name must be remembered for the response side")
	}
	if len(req.Hosted.Definitions) != 0 {
		t.Fatalf("a custom tool is not a hosted declaration: %+v", req.Hosted.Definitions)
	}
}

func TestParseResponses_CustomToolWithoutNameIsRefused(t *testing.T) {
	_, perr := ParseResponses([]byte(`{"model":"gpt-4o","input":"hi","tools":[{"type":"custom"}]}`))
	if perr == nil {
		t.Fatal("a custom tool with no name cannot be sent; it must be refused, not dropped")
	}
	if !strings.Contains(perr.Param, "name") {
		t.Fatalf("the refusal must name the missing field: %+v", perr)
	}
}

// Forcing a custom tool is the same shape as forcing a function, because that is
// what it became upstream.
func TestParseResponses_CustomToolChoiceBecomesFunctionChoice(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hi",
		"tools":[{"type":"custom","name":"apply_patch"}],
		"tool_choice":{"type":"custom","name":"apply_patch"}}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	choice, ok := req.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("a sent custom tool must be forcible, got %#v", req.ToolChoice)
	}
	if choice["type"] != "function" {
		t.Fatalf("choice type = %v", choice["type"])
	}
	fn, _ := choice["function"].(map[string]any)
	if fn["name"] != "apply_patch" {
		t.Fatalf("choice names %v", fn["name"])
	}
	if req.Hosted.ForcedChoice != nil {
		t.Fatalf("a tool we did send is not a forced hosted choice: %+v", req.Hosted.ForcedChoice)
	}
}

// A custom tool shares its name with another tool: the target protocol tells
// their calls apart by name alone, so one of them would be unreachable. The
// custom one loses, and the loss is named at error severity.
func TestParseResponses_CustomToolNameConflictIsDropped(t *testing.T) {
	// The function is declared AFTER the custom tool: order must not decide the
	// winner, so the check cannot run inside the walk.
	body := `{"model":"gpt-4o","input":"hi","tools":[
		{"type":"custom","name":"exec"},
		{"type":"function","name":"exec","parameters":{"type":"object"}}]}`
	req, perr := ParseResponses([]byte(body))
	if perr != nil {
		t.Fatalf("a name conflict is a loss, not a refusal: %+v", perr)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("want only the function to survive, got %+v", req.Tools)
	}
	if req.Tools[0].Function.Name != "exec" || req.Tools[0].Function.Parameters == nil {
		t.Fatalf("the function must win: %+v", req.Tools[0])
	}
	if req.CustomTools.Has("exec") {
		t.Fatal("a dropped custom tool must not be remembered")
	}
	conflicts := 0
	for _, d := range req.Hosted.LossOf() {
		if d.Code == relaymodel.DiagCustomToolNameConflict {
			conflicts++
			if d.Severity != relaymodel.DiagnosticError {
				t.Fatalf("a conflict changes what the model can do, so error severity: %s", d.Severity)
			}
		}
	}
	if conflicts != 1 {
		t.Fatalf("want exactly 1 conflict diagnostic, got %d in %+v", conflicts, req.Hosted.LossOf())
	}
}

// The downgrade is presentation severity: the tool is still callable, it just
// travels in a different shape. That is what lets "safe" keep accepting it.
func TestParseResponses_CustomToolDowngradeIsPresentationSeverity(t *testing.T) {
	req, perr := ParseResponses([]byte(`{"model":"gpt-4o","input":"hi","tools":[{"type":"custom","name":"apply_patch"}]}`))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	seen := 0
	for _, d := range req.Hosted.LossOf() {
		if d.Code == relaymodel.DiagCustomToolAsFunction {
			seen++
			if d.Severity != relaymodel.DiagnosticWarning {
				t.Fatalf("downgrade severity = %s, want warning", d.Severity)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("want exactly 1 downgrade diagnostic, got %d", seen)
	}
}

func customTextResponse(arguments string) *openai.TextResponse {
	return &openai.TextResponse{
		Id: "chatcmpl-1",
		Choices: []openai.TextResponseChoice{{
			Message: relaymodel.Message{
				ToolCalls: []relaymodel.Tool{{
					Id:       "call_a",
					Type:     "function",
					Function: relaymodel.Function{Name: "apply_patch", Arguments: arguments},
				}},
			},
		}},
		CustomTools: relaymodel.ResponsesCustomToolNames{"apply_patch": {}},
	}
}

func TestRenderResponses_CustomToolCallCarriesRawInput(t *testing.T) {
	env := RenderResponses(customTextResponse(`{"input":"*** Begin Patch"}`), "gpt-4o")
	output := env["output"].([]map[string]any)
	var item map[string]any
	for _, it := range output {
		if it["type"] == "custom_tool_call" {
			item = it
		}
	}
	if item == nil {
		t.Fatalf("no custom_tool_call in %+v", output)
	}
	if item["input"] != "*** Begin Patch" {
		t.Fatalf("input = %v, want the raw text", item["input"])
	}
	if _, ok := item["arguments"]; ok {
		t.Fatalf("a custom item must not carry arguments: %+v", item)
	}
	if item["call_id"] != "call_a" || item["name"] != "apply_patch" || item["status"] != "completed" {
		t.Fatalf("item = %+v", item)
	}
}

// A model that answered in the wrong shape still answered: hand the client what
// it produced rather than an empty input.
func TestRenderResponses_CustomToolInputFallsBackToRawArguments(t *testing.T) {
	env := RenderResponses(customTextResponse(`{"other":1}`), "gpt-4o")
	for _, it := range env["output"].([]map[string]any) {
		if it["type"] == "custom_tool_call" {
			if it["input"] != `{"other":1}` {
				t.Fatalf("input = %v, want the arguments verbatim", it["input"])
			}
			return
		}
	}
	t.Fatalf("no custom_tool_call in %+v", env["output"])
}

// The same call without the client having declared it custom stays a
// function_call, arguments and all.
func TestRenderResponses_OrdinaryToolIsStillAFunctionCall(t *testing.T) {
	up := customTextResponse(`{"input":"x"}`)
	up.CustomTools = nil
	for _, it := range RenderResponses(up, "gpt-4o")["output"].([]map[string]any) {
		if it["type"] == "function_call" {
			if it["arguments"] != `{"input":"x"}` {
				t.Fatalf("arguments = %v", it["arguments"])
			}
			return
		}
	}
	t.Fatal("an undeclared tool must stay a function_call")
}

func customToolStreamChunks() []string {
	return []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"apply_patch","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"*** Begin"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" Patch\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[]},"finish_reason":"tool_calls"}]}`,
	}
}

// A fragment of a JSON object cannot be unwrapped on its own, so a custom tool's
// input is not streamed piecewise: it is delivered whole, once, when the item
// closes — and never on the function-call events.
func TestResponsesStream_CustomToolCallUsesInputEvents(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_1", "gpt-4o",
		relaymodel.ResponsesCustomToolNames{"apply_patch": {}})
	for _, raw := range customToolStreamChunks() {
		if err := s.Chunk(mustParse(t, raw)); err != nil {
			t.Fatalf("chunk: %s", err)
		}
	}
	if err := s.Finish("tool_calls", nil); err != nil {
		t.Fatalf("finish: %s", err)
	}

	events := parseSSE(t, c.buf.String())
	if got := events["response.function_call_arguments.delta"]; len(got) != 0 {
		t.Fatalf("custom input must not ride the function-call delta: %+v", got)
	}
	if got := events["response.function_call_arguments.done"]; len(got) != 0 {
		t.Fatalf("custom input must not ride the function-call done: %+v", got)
	}
	deltas := events["response.custom_tool_call_input.delta"]
	if len(deltas) != 1 {
		t.Fatalf("want exactly 1 input delta, got %d", len(deltas))
	}
	if deltas[0]["delta"] != "*** Begin Patch" {
		t.Fatalf("delta = %v, want the unwrapped input", deltas[0]["delta"])
	}
	dones := events["response.custom_tool_call_input.done"]
	if len(dones) != 1 {
		t.Fatalf("want 1 input done, got %d", len(dones))
	}
	if dones[0]["input"] != "*** Begin Patch" {
		t.Fatalf("done input = %v", dones[0]["input"])
	}

	added := events["response.output_item.added"]
	if len(added) != 1 {
		t.Fatalf("want 1 output_item.added, got %d", len(added))
	}
	if idx := int(added[0]["output_index"].(float64)); idx != 0 {
		t.Fatalf("output_index = %d, want the item numbering to stay dense", idx)
	}
	item := added[0]["item"].(map[string]any)
	if item["type"] != "custom_tool_call" || item["input"] != "" || item["status"] != "in_progress" {
		t.Fatalf("announced item = %+v", item)
	}
	if _, ok := item["arguments"]; ok {
		t.Fatalf("a custom item must not carry arguments: %+v", item)
	}
	closed := events["response.output_item.done"][0]["item"].(map[string]any)
	if closed["type"] != "custom_tool_call" || closed["status"] != "completed" || closed["input"] != "*** Begin Patch" {
		t.Fatalf("closed item = %+v", closed)
	}
	output := events["response.completed"][0]["response"].(map[string]any)["output"].([]any)
	last := output[len(output)-1].(map[string]any)
	if last["type"] != "custom_tool_call" || last["input"] != "*** Begin Patch" {
		t.Fatalf("terminal output = %+v", last)
	}
	if _, err := json.Marshal(last); err != nil {
		t.Fatalf("terminal item must be encodable: %s", err)
	}
}

// A custom call that streamed no arguments closes with an empty input and no
// delta — there is nothing to unwrap, and an empty payload must not be announced
// twice.
func TestResponsesStream_CustomToolCallWithoutInputSkipsDelta(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_1", "gpt-4o",
		relaymodel.ResponsesCustomToolNames{"apply_patch": {}})
	_ = s.Chunk(mustParse(t, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"apply_patch"}}]}}]}`))
	_ = s.Finish("tool_calls", nil)

	events := parseSSE(t, c.buf.String())
	if got := events["response.custom_tool_call_input.delta"]; len(got) != 0 {
		t.Fatalf("nothing to say, so no delta: %+v", got)
	}
	dones := events["response.custom_tool_call_input.done"]
	if len(dones) != 1 || dones[0]["input"] != "" {
		t.Fatalf("want one done with an empty input: %+v", dones)
	}
}

// Without a custom set the renderer must behave exactly as it did before: the
// function-call events and the `{}` fallback are what every other client sees.
func TestResponsesStream_OrdinaryToolIsUnchanged(t *testing.T) {
	c := &capture{}
	s := NewResponsesStream(sse.NewWriter(c), "resp_1", "gpt-4o", nil)
	for _, raw := range customToolStreamChunks() {
		if err := s.Chunk(mustParse(t, raw)); err != nil {
			t.Fatalf("chunk: %s", err)
		}
	}
	if err := s.Finish("tool_calls", nil); err != nil {
		t.Fatalf("finish: %s", err)
	}

	events := parseSSE(t, c.buf.String())
	if got := events["response.custom_tool_call_input.delta"]; len(got) != 0 {
		t.Fatalf("an ordinary function must not use custom events: %+v", got)
	}
	if len(events["response.function_call_arguments.delta"]) == 0 {
		t.Fatal("an ordinary function still streams its arguments")
	}
	closed := events["response.output_item.done"][0]["item"].(map[string]any)
	if closed["type"] != "function_call" {
		t.Fatalf("item type = %v", closed["type"])
	}
	if _, err := json.Marshal(closed["arguments"]); err != nil {
		t.Fatalf("arguments = %v", closed["arguments"])
	}
}
