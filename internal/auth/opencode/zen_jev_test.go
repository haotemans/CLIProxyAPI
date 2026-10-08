package opencode

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIsZenJevModelID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"jev-1.13-free", true},
		{"jev-1.13", true},
		{"JEV-2.0-free", true},
		{" jev-1.13-free ", true},
		{"fledge-alpha-free", false},
		{"gpt-5.1", false},
		{"marc-jev-1", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsZenJevModelID(tc.id); got != tc.want {
			t.Errorf("IsZenJevModelID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func jevChatPayload(t *testing.T, messages string) []byte {
	t.Helper()
	if !gjson.Valid(messages) {
		t.Fatalf("messages fixture invalid: %s", messages)
	}
	return []byte(`{"model":"jev-1.13-free","messages":` + messages + `}`)
}

func TestParseZenJevRequestParts(t *testing.T) {
	questions := `{"alive":{"type":"noul","question":"Is it alive?","instructions":"Answer yes if reachable."}}`
	escaped := strings.ReplaceAll(questions, `"`, `\"`)
	payload := jevChatPayload(t, `[
		{"role":"system","content":"{\"jev_questions\":`+escaped+`}"},
		{"role":"user","content":"ping"},
		{"role":"assistant","content":"pong"}
	]`)
	state, rawQuestions, err := ParseZenJevRequestParts(payload)
	if err != nil {
		t.Fatalf("ParseZenJevRequestParts error: %v", err)
	}
	if state != "user: ping\nassistant: pong" {
		t.Errorf("state = %q, want conversation lines", state)
	}
	if !gjson.ValidBytes(rawQuestions) {
		t.Fatalf("questions invalid JSON: %s", rawQuestions)
	}
	if got := gjson.GetBytes(rawQuestions, "alive.type").String(); got != "noul" {
		t.Errorf("questions alive.type = %q, want noul", got)
	}
}

func TestParseZenJevRequestPartsMultipleBlocks(t *testing.T) {
	payload := jevChatPayload(t, `[
		{"role":"system","content":"{\"jev_questions\":{\"a\":{\"type\":\"noul\",\"question\":\"q?\",\"instructions\":\"i\"}}}"},
		{"role":"system","content":"{\"jev_questions\":{\"b\":{\"type\":\"noul\",\"question\":\"q?\",\"instructions\":\"i\"}}}"},
		{"role":"user","content":"ping"}
	]`)
	if _, _, err := ParseZenJevRequestParts(payload); err == nil || !ZenJevValidationError(err) {
		t.Fatalf("want validation error for duplicate jev_questions, got %v", err)
	}
}

func TestParseZenJevRequestPartsArrayContent(t *testing.T) {
	questions := `{"q":{"type":"score","question":"Rate.","criteria":["bad","good"]}}`
	escaped := strings.ReplaceAll(questions, `"`, `\"`)
	payload := jevChatPayload(t, `[
		{"role":"system","content":[{"type":"text","text":"{\"jev_questions\":`+escaped+`}"}]},
		{"role":"user","content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}
	]`)
	state, _, err := ParseZenJevRequestParts(payload)
	if err != nil {
		t.Fatalf("ParseZenJevRequestParts error: %v", err)
	}
	if state != "user: hello world" {
		t.Errorf("state = %q, want concatenated text parts", state)
	}
}

func TestParseZenJevRequestPartsErrors(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"no_questions", `{"model":"jev-1.13-free","messages":[{"role":"user","content":"hi"}]}`},
		{"invalid_payload", `not json`},
		{"questions_envelope_not_json", `{"model":"jev-1.13-free","messages":[{"role":"system","content":"{\"jev_questions\": not json}"},{"role":"user","content":"hi"}]}`},
		{"empty_state", `{"model":"jev-1.13-free","messages":[{"role":"system","content":"{\"jev_questions\":{\"q\":{\"type\":\"noul\",\"question\":\"q?\",\"instructions\":\"i\"}}}"}]}`},
		{"no_messages", `{"model":"jev-1.13-free"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseZenJevRequestParts([]byte(tc.payload))
			if err == nil {
				t.Fatalf("want error for %s", tc.name)
			}
			if !ZenJevValidationError(err) {
				t.Errorf("error should classify as validation (400): %v", err)
			}
			if !strings.Contains(err.Error(), ZenJevContractExample) {
				t.Errorf("error must carry the contract example")
			}
		})
	}
}

func TestValidateZenJevQuestionsTypes(t *testing.T) {
	cases := []struct {
		name      string
		questions string
		wantErr   bool
	}{
		{"noul_instructions", `{"a":{"type":"noul","question":"q?","instructions":"be strict"}}`, false},
		{"noul_criteria", `{"a":{"type":"noul","question":"q?","criteria":{"yes":"good","no":"bad"}}}`, false},
		{"noul_missing_both", `{"a":{"type":"noul","question":"q?"}}`, true},
		{"choice_ok", `{"a":{"type":"choice","question":"pick","criteria":{"A":"one","B":"two"}}}`, false},
		{"choice_criteria_array", `{"a":{"type":"choice","question":"pick","criteria":["one","two"]}}`, true},
		{"choice_empty_map", `{"a":{"type":"choice","question":"pick","criteria":{}}}`, true},
		{"score_ok", `{"a":{"type":"score","question":"rate","criteria":["low","high"]}}`, false},
		{"score_criteria_map", `{"a":{"type":"score","question":"rate","criteria":{"0":"low"}}}`, true},
		{"unknown_type", `{"a":{"type":"rank","question":"q?"}}`, true},
		{"missing_question_text", `{"a":{"type":"noul","instructions":"i"}}`, true},
		{"entry_not_object", `{"a":"noul"}`, true},
		{"empty_questions", `{}`, true},
		{"not_an_object", `["a"]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateZenJevQuestions([]byte(tc.questions))
			if tc.wantErr && err == nil {
				t.Fatalf("want error for %s", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
			if tc.wantErr && err != nil && !ZenJevValidationError(err) {
				t.Errorf("want validation error classification: %v", err)
			}
		})
	}
}

func TestValidateZenJevQuestionsOversize(t *testing.T) {
	oversized := strings.Repeat("x", ZenJevQuestionsMaxBytes+1)
	if err := ValidateZenJevQuestions([]byte(oversized)); err == nil {
		t.Fatal("want error for oversized questions payload")
	}
}

func TestBuildZenJevSystemOnePayload(t *testing.T) {
	questions := []byte(`{"q":{"type":"noul","question":"q?","instructions":"i"}}`)
	payload, err := BuildZenJevSystemOnePayload("jev-1.13-free", "user: ping", questions)
	if err != nil {
		t.Fatalf("BuildZenJevSystemOnePayload error: %v", err)
	}
	root := gjson.ParseBytes(payload)
	if got := root.Get("model").String(); got != "jev-1.13-free" {
		t.Errorf("model = %q", got)
	}
	if got := root.Get("state").String(); got != "user: ping" {
		t.Errorf("state = %q", got)
	}
	if got := root.Get("questions.q.type").String(); got != "noul" {
		t.Errorf("questions.q.type = %q", got)
	}
	if _, err := BuildZenJevSystemOnePayload("", "state", questions); err == nil {
		t.Error("want error for empty model")
	}
}

func TestZenJevAnswersTextSorted(t *testing.T) {
	body := []byte(`{"model":"jev-1.13-free","answers":{"zeta":{"type":"noul","noul":0.5},"alpha":{"type":"choice","choice":"B","confidence":0.9}}}`)
	text := ZenJevAnswersText(body)
	if !strings.HasPrefix(text, `{"alpha"`) {
		t.Errorf("answers keys not sorted: %s", text)
	}
	if !strings.Contains(text, `"zeta":{"type":"noul","noul":0.5}`) {
		t.Errorf("zeta answer mangled: %s", text)
	}
	if got := ZenJevAnswersText([]byte(`{}`)); got != "{}" {
		t.Errorf("empty answers = %q, want {}", got)
	}
}

func TestBuildZenJevChatCompletion(t *testing.T) {
	body := []byte(`{"model":"jev-1.13-free","answers":{"alive":{"type":"noul","noul":0.75},"quality":{"type":"score","score":0.99,"confidence":0.98}},"usage":{"input_tokens":310,"output_tokens":33},"cost":"0"}`)
	completion, err := BuildZenJevChatCompletion(body, "jev-1.13-free")
	if err != nil {
		t.Fatalf("BuildZenJevChatCompletion error: %v", err)
	}
	root := gjson.ParseBytes(completion)
	if got := root.Get("object").String(); got != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", got)
	}
	if !strings.HasPrefix(root.Get("id").String(), "chatcmpl-") {
		t.Errorf("id = %q, want chatcmpl- prefix", root.Get("id").String())
	}
	if got := root.Get("model").String(); got != "jev-1.13-free" {
		t.Errorf("model = %q", got)
	}
	if got := root.Get("choices.0.message.role").String(); got != "assistant" {
		t.Errorf("role = %q", got)
	}
	content := root.Get("choices.0.message.content").String()
	if !gjson.Valid(content) {
		t.Errorf("content must be valid answers JSON: %s", content)
	}
	if got := gjson.Get(content, "alive.noul").Float(); got != 0.75 {
		t.Errorf("answers alive.noul = %v", got)
	}
	if got := root.Get("choices.0.finish_reason").String(); got != "stop" {
		t.Errorf("finish_reason = %q", got)
	}
	if got := root.Get("usage.prompt_tokens").Int(); got != 310 {
		t.Errorf("prompt_tokens = %d, want 310", got)
	}
	if got := root.Get("usage.completion_tokens").Int(); got != 33 {
		t.Errorf("completion_tokens = %d, want 33", got)
	}
	if got := root.Get("usage.total_tokens").Int(); got != 343 {
		t.Errorf("total_tokens = %d, want 343", got)
	}
}

func TestBuildZenJevChatCompletionModelFallback(t *testing.T) {
	body := []byte(`{"answers":{}}`)
	completion, err := BuildZenJevChatCompletion(body, "jev-1.13-free")
	if err != nil {
		t.Fatalf("BuildZenJevChatCompletion error: %v", err)
	}
	if got := gjson.GetBytes(completion, "model").String(); got != "jev-1.13-free" {
		t.Errorf("model fallback = %q", got)
	}
	if gjson.GetBytes(completion, "usage").Exists() {
		t.Errorf("usage must be omitted when upstream omitted it: %s", completion)
	}
}

func TestBuildZenJevStreamSequence(t *testing.T) {
	completion := []byte(`{"id":"chatcmpl-x","object":"chat.completion","created":1791394469,"model":"jev-1.13-free","choices":[{"index":0,"message":{"role":"assistant","content":"{\"alive\":{\"type\":\"noul\",\"noul\":0.75}}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":310,"completion_tokens":33,"total_tokens":343}}`)
	wire := BuildZenJevStreamSequence(completion)
	var frames []string
	for _, line := range strings.Split(string(wire), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			frames = append(frames, trimmed)
		}
	}
	if len(frames) != 4 {
		t.Fatalf("frame count = %d, want 4 (role, content, finish+usage, [DONE]); wire=%s", len(frames), wire)
	}
	for i, frame := range frames {
		if !strings.HasPrefix(frame, "data: ") {
			t.Fatalf("frame %d missing data prefix: %q", i, frame)
		}
	}
	if frames[3] != "data: [DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", frames[3])
	}
	role := gjson.Get(strings.TrimPrefix(frames[0], "data: "), "choices.0.delta.role").String()
	if role != "assistant" {
		t.Errorf("role frame role = %q", role)
	}
	contentPayload := strings.TrimPrefix(frames[1], "data: ")
	content := gjson.Get(contentPayload, "choices.0.delta.content").String()
	if !gjson.Valid(content) {
		t.Errorf("content frame must carry valid answers JSON: %s", content)
	}
	finishPayload := strings.TrimPrefix(frames[2], "data: ")
	if got := gjson.Get(finishPayload, "choices.0.finish_reason").String(); got != "stop" {
		t.Errorf("finish_reason = %q", got)
	}
	if got := gjson.Get(finishPayload, "usage.total_tokens").Int(); got != 343 {
		t.Errorf("usage.total_tokens = %d, want 343", got)
	}
}

func TestParseZenJevUsage(t *testing.T) {
	in, out, ok := ParseZenJevUsage([]byte(`{"usage":{"input_tokens":310,"output_tokens":33}}`))
	if !ok || in != 310 || out != 33 {
		t.Errorf("ParseZenJevUsage = %d, %d, %v", in, out, ok)
	}
	if _, _, ok := ParseZenJevUsage([]byte(`{}`)); ok {
		t.Error("missing usage must report ok=false")
	}
}
