package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// IsZenJevModelID reports whether a model id belongs to the OpenCode Zen "jev"
// structured-evaluation family (TypeSafe AI jev). The anonymous free tier
// serves these models on the /systemone endpoint instead of
// /chat/completions, so the executor needs a cheap pre-translator check.
// Detection is by id prefix on purpose ("jev-1.13-free", "jev-1.13") so new
// jev generations are picked up without code changes; extend here if a future
// generation ever breaks the prefix convention.
func IsZenJevModelID(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), zenJevModelPrefix)
}

const (
	zenJevModelPrefix = "jev-"

	// ZenJevSystemOnePath joins the structured-evaluation endpoint against a
	// zen base URL (the same root the anonymous chat tier uses).
	ZenJevSystemOnePath = "/systemone"

	// zenJevQuestionsKey is the reserved key the client-answered system
	// message must wrap its questions payload in.
	zenJevQuestionsKey = "jev_questions"
)

// zenJevQuestionType enumerates the structured question kinds the systemone
// endpoint validates server-side. Anything outside this whitelist is rejected
// locally with a 400 so unknown types never leave the proxy (anti-injection:
// the questions JSON is client-supplied and must not smuggle extra shapes).
const (
	zenJevTypeNoul   = "noul"
	zenJevTypeChoice = "choice"
	zenJevTypeScore  = "score"
)

// ZenJevQuestionsMaxBytes caps the questions JSON carried inside a chat
// message. It only guards memory/parse cost; it is nowhere near any real
// questionnaire size and is not a semantic limit.
const ZenJevQuestionsMaxBytes = 256 << 10

// ZenJevContractExample is reused verbatim in every 400 body so clients get a
// copy-pastable request shape instead of a bare refusal.
const ZenJevContractExample = `jev models accept standard OpenAI chat requests on this proxy: put the questions JSON in a system message whose content is a JSON object with a single "jev_questions" key, and keep the conversation (which becomes the evaluation state) in the other messages.
Example:
{"model":"jev-1.13-free","messages":[
  {"role":"system","content":"{\"jev_questions\":{\"q1\":{\"type\":\"noul\",\"question\":\"Is the answer correct?\",\"instructions\":\"Answer yes only when it is fully correct.\"}}}"},
  {"role":"user","content":"The sky is ..."},
  {"role":"assistant","content":"blue"}
]}
Question types: "noul" (yes/no, needs criteria map or instructions), "choice" (needs "criteria" as a map of option id to description), "score" (needs "criteria" as an array of score descriptions).`

type zenJevValidationError struct{ msg string }

func (e *zenJevValidationError) Error() string { return e.msg }

// ZenJevValidationError reports whether err is a client-side (HTTP 400)
// contract violation raised while extracting the jev request.
func ZenJevValidationError(err error) bool {
	var target *zenJevValidationError
	return errors.As(err, &target)
}

func zenJevBadRequest(format string, args ...any) error {
	reason := fmt.Sprintf(format, args...)
	return &zenJevValidationError{msg: reason + "\n\n" + ZenJevContractExample}
}

// ParseZenJevRequestParts extracts the structured-evaluation request from an
// OpenAI chat payload. A system message whose content is a JSON object with a
// "jev_questions" key carries the questions map; every remaining message is
// rendered as one "role: content" line per entry to build the evaluation
// state. One questions block is required: without it the request is
// ambiguous, so this fails loudly instead of guessing.
func ParseZenJevRequestParts(payload []byte) (state string, questions []byte, err error) {
	if !gjson.ValidBytes(payload) {
		return "", nil, zenJevBadRequest("request payload must be valid JSON with a messages array.")
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return "", nil, zenJevBadRequest(`no messages array found; the jev questions must ride inside messages.`)
	}

	var questionsRaw string
	seenQuestions := false
	var stateLines []string
	var questionsErr error
	messages.ForEach(func(_, message gjson.Result) bool {
		if questionsErr != nil {
			return false
		}
		role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
		content := zenJevMessageContent(message)
		if role == "system" || role == "developer" {
			if parsed, ok := zenJevTryParseQuestions(content); ok {
				if seenQuestions {
					questionsErr = zenJevBadRequest("multiple system messages carry a `\"jev_questions\"` block; keep exactly one.")
					return false
				}
				seenQuestions = true
				questionsRaw = string(parsed)
				return true
			}
		}
		if trimmed := strings.TrimSpace(content); trimmed != "" {
			displayRole := role
			if displayRole == "" {
				displayRole = strings.TrimSpace(message.Get("role").String())
			}
			stateLines = append(stateLines, displayRole+": "+trimmed)
		}
		return true
	})
	if questionsErr != nil {
		return "", nil, questionsErr
	}
	if !seenQuestions {
		return "", nil, zenJevBadRequest(`no jev questions found: one system message must hold a JSON object starting with {"jev_questions": ...}.`)
	}
	if err = ValidateZenJevQuestions([]byte(questionsRaw)); err != nil {
		return "", nil, err
	}
	state = strings.Join(stateLines, "\n")
	if strings.TrimSpace(state) == "" {
		return "", nil, zenJevBadRequest("the conversation state is empty: keep at least one non-system message (or system message without jev_questions) so jev has something to evaluate.")
	}
	return state, []byte(questionsRaw), nil
}

// zenJevMessageContent renders an OpenAI message content value as plain text:
// string content passes through; array content concatenates its text parts
// (multimodal parts are skipped: jev evaluates text state only).
func zenJevMessageContent(message gjson.Result) string {
	content := message.Get("content")
	switch {
	case content.Type == gjson.String:
		return content.String()
	case content.IsArray():
		var sb strings.Builder
		content.ForEach(func(_, part gjson.Result) bool {
			text := ""
			if part.Type == gjson.String {
				text = part.String()
			} else if part.IsObject() {
				text = part.Get("text").String()
			}
			sb.WriteString(text)
			return true
		})
		return sb.String()
	default:
		return ""
	}
}

// zenJevTryParseQuestions unwraps the `{"jev_questions": {...}}` envelope from
// a system message. The second return value reports whether this message is
// the reserved block at all; malformed envelopes stay conversation text on
// purpose (a system prompt that merely starts with a brace is not ours).
// A valid envelope without an object at jev_questions is treated as absent.
func zenJevTryParseQuestions(content string) ([]byte, bool) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, `{"jev_questions":`) && !strings.HasPrefix(trimmed, `{ "jev_questions":`) {
		return nil, false
	}
	if !gjson.Valid(trimmed) {
		return nil, false
	}
	questions := gjson.Get(trimmed, zenJevQuestionsKey)
	if !questions.Exists() || !questions.IsObject() {
		return nil, false
	}
	return []byte(questions.Raw), true
}

// ValidateZenJevQuestions enforces the client-supplied questions contract
// before anything is forwarded upstream: a JSON object keyed by question id,
// each entry an object with a whitelisted type and the shape that type needs.
// Unknown types or broken criteria are rejected here so a hostile or sloppy
// client cannot relay arbitrary structured payloads through this path.
func ValidateZenJevQuestions(questions []byte) error {
	if len(questions) > ZenJevQuestionsMaxBytes {
		return zenJevBadRequest("jev questions payload exceeds %d bytes.", ZenJevQuestionsMaxBytes)
	}
	if !gjson.ValidBytes(questions) {
		return zenJevBadRequest("jev_questions value must be a JSON object: got invalid JSON.")
	}
	root := gjson.ParseBytes(questions)
	if !root.IsObject() {
		return zenJevBadRequest("jev_questions must be an object keyed by question id.")
	}
	count := 0
	var validationErr error
	root.ForEach(func(id, entry gjson.Result) bool {
		count++
		if validationErr != nil {
			return false
		}
		name := id.String()
		if !entry.IsObject() {
			validationErr = zenJevBadRequest("question %q must be an object with a type field.", name)
			return false
		}
		qtype := strings.ToLower(strings.TrimSpace(entry.Get("type").String()))
		if strings.TrimSpace(entry.Get("question").String()) == "" {
			validationErr = zenJevBadRequest("question %q is missing its \"question\" text.", name)
			return false
		}
		switch qtype {
		case zenJevTypeNoul:
			criteria := entry.Get("criteria")
			hasCriteria := criteria.IsObject() && len(criteria.Map()) > 0
			if !hasCriteria && strings.TrimSpace(entry.Get("instructions").String()) == "" {
				validationErr = zenJevBadRequest("noul question %q needs either a non-empty \"criteria\" object or \"instructions\" text.", name)
				return false
			}
		case zenJevTypeChoice:
			criteria := entry.Get("criteria")
			if !criteria.IsObject() || len(criteria.Map()) == 0 {
				validationErr = zenJevBadRequest("choice question %q needs \"criteria\" as a non-empty object of option id to description.", name)
				return false
			}
		case zenJevTypeScore:
			criteria := entry.Get("criteria")
			if !criteria.IsArray() || len(criteria.Array()) == 0 {
				validationErr = zenJevBadRequest("score question %q needs \"criteria\" as a non-empty array of score descriptions.", name)
				return false
			}
		default:
			validationErr = zenJevBadRequest("question %q has unsupported type %q; allowed types: noul, choice, score.", name, qtype)
			return false
		}
		return true
	})
	if validationErr != nil {
		return validationErr
	}
	if count == 0 {
		return zenJevBadRequest("jev_questions must contain at least one question entry.")
	}
	return nil
}

// BuildZenJevSystemOnePayload renders the upstream body after validation:
// exactly model/state/questions, nothing else. User payload rules must never
// run over this body (see the executor call site), so the fields are pinned
// here from validated inputs only.
func BuildZenJevSystemOnePayload(model, state string, questions []byte) ([]byte, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("zen jev: model must not be empty")
	}
	if err := ValidateZenJevQuestions(questions); err != nil {
		return nil, err
	}
	body, err := sjson.SetBytes(nil, "model", strings.TrimSpace(model))
	if err != nil {
		return nil, fmt.Errorf("zen jev: set model: %w", err)
	}
	if body, err = sjson.SetBytes(body, "state", state); err != nil {
		return nil, fmt.Errorf("zen jev: set state: %w", err)
	}
	if body, err = sjson.SetRawBytes(body, "questions", questions); err != nil {
		return nil, fmt.Errorf("zen jev: set questions: %w", err)
	}
	return body, nil
}

// ParseZenJevUsage reads the jev token counters. input/output are zero (and
// ok false) when the upstream omitted usage.
func ParseZenJevUsage(body []byte) (inputTokens, outputTokens int64, ok bool) {
	usage := gjson.GetBytes(body, "usage")
	if !usage.IsObject() {
		return 0, 0, false
	}
	inputTokens = usage.Get("input_tokens").Int()
	outputTokens = usage.Get("output_tokens").Int()
	return inputTokens, outputTokens, true
}

// ZenJevAnswersText renders the structured answers object for the assistant
// message. Keys are sorted so the text content is deterministic across calls
// (Go maps iterate randomly; clients diffing evaluations rely on stability).
// Answers that are not a JSON object are returned verbatim.
func ZenJevAnswersText(body []byte) string {
	answers := gjson.GetBytes(body, "answers")
	if !answers.Exists() {
		answers = gjson.Result{Type: gjson.JSON, Raw: "{}"}
	}
	if !answers.IsObject() {
		return answers.Raw
	}
	var keys []string
	raws := make(map[string]string)
	answers.ForEach(func(key, value gjson.Result) bool {
		keys = append(keys, key.String())
		raws[key.String()] = value.Raw
		return true
	})
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(key)
		sb.Write(keyJSON)
		sb.WriteByte(':')
		sb.WriteString(raws[key])
	}
	sb.WriteByte('}')
	return sb.String()
}

// zenJevServedModel prefers the model echo from the upstream body and falls
// back to the requested model id.
func zenJevServedModel(body []byte, requested string) string {
	if served := strings.TrimSpace(gjson.GetBytes(body, "model").String()); served != "" {
		return served
	}
	return strings.TrimSpace(requested)
}

// BuildZenJevChatCompletion renders the standard chat.completion envelope the
// executor reports back to clients: the structured answers become the
// assistant message text, token counters map input/output onto
// prompt/completion, and the finish reason is fixed to stop.
func BuildZenJevChatCompletion(body []byte, requestedModel string) ([]byte, error) {
	answersText := ZenJevAnswersText(body)
	inputTokens, outputTokens, hasUsage := ParseZenJevUsage(body)
	completion := map[string]any{
		"id":      "chatcmpl-" + uuid.NewString()[:24],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   zenJevServedModel(body, requestedModel),
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": answersText},
				"finish_reason": "stop",
			},
		},
	}
	if hasUsage {
		completion["usage"] = map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		}
	}
	out, err := json.Marshal(completion)
	if err != nil {
		return nil, fmt.Errorf("zen jev: render chat.completion: %w", err)
	}
	return out, nil
}

// zenJevChunkFromCompletion renders one chat.completion.chunk envelope from
// the assembled completion body, reusing its id/created/model. The delta (or
// finish-choices fragment) is spliced in as-is so callers decide between
// role-only, content, and finish frames.
func zenJevChunkFromCompletion(completion []byte, deltaOrFinish string) []byte {
	chunk := fmt.Sprintf(
		`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,%s}]}`,
		gjson.GetBytes(completion, "id").String(),
		gjson.GetBytes(completion, "created").Int(),
		gjson.GetBytes(completion, "model").String(),
		deltaOrFinish,
	)
	return []byte(chunk)
}

// BuildZenJevStreamSequence renders the complete SSE wire body for a jev
// stream request: role frame, one content frame, finish frame with usage,
// then the [DONE] marker. The anonymous systemone tier answers in one shot,
// so a single-frame sequence is the honest streaming view of it.
func BuildZenJevStreamSequence(completion []byte) []byte {
	answersText := gjson.GetBytes(completion, "choices.0.message.content").String()
	contentJSON, _ := json.Marshal(map[string]any{"content": answersText, "role": "assistant"})
	usageJSON := gjson.GetBytes(completion, "usage")

	var sb bytes.Buffer
	sb.WriteString("data: ")
	sb.Write(zenJevChunkFromCompletion(completion, `"delta":{"role":"assistant"}`))
	sb.WriteString("\n\n")
	sb.WriteString("data: ")
	sb.Write(zenJevChunkFromCompletion(completion, `"delta":`+string(contentJSON)))
	sb.WriteString("\n\n")

	if usageJSON.Exists() {
		frame := fmt.Sprintf(
			`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":%s}`,
			gjson.GetBytes(completion, "id").String(),
			gjson.GetBytes(completion, "created").Int(),
			gjson.GetBytes(completion, "model").String(),
			usageJSON.Raw,
		)
		sb.WriteString("data: ")
		sb.WriteString(frame)
	} else {
		sb.WriteString("data: ")
		sb.Write(zenJevChunkFromCompletion(completion, `"delta":{},"finish_reason":"stop"`))
	}
	sb.WriteString("\n\n")
	sb.WriteString("data: [DONE]\n\n")
	return sb.Bytes()
}
