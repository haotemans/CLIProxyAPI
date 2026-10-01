package executor

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// reasoningBackfillForProvider applies the reasoning-details backfill used by
// OpenAI-shaped relay providers (Commandcode, OpenCode Go): upstreams return
// thinking text under message/delta "reasoning" (string) and "reasoning_details"
// (array of {text,...}) but the openai translators only read the standard
// "reasoning_content" field. Derived from
// github.com/ahoo/cpa-plugin-commandcode (MIT), reasoning.go; parity hook only
// for those two providers so all other upstreams pass through untouched.
//
// Priority: reasoning_details[].text (authoritative per-token text) first,
// then plain "reasoning". When both carry the same token (observed: details
// mirror the string), details-only avoids duplicating content.
func reasoningBackfillForProvider(provider string, body []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "commandcode", "opencode-go":
		out, changed := mapReasoningBackfill(body)
		if changed {
			return out
		}
		return body
	default:
		return body
	}
}

// mapReasoningBackfill walks choices[].delta/message and backfills
// reasoning_content. Returns the rewritten body and whether it changed.
func mapReasoningBackfill(body []byte) ([]byte, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false
	}
	if !strings.Contains(string(body), "reasoning") {
		return body, false
	}
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body, false
	}
	out := body
	changed := false
	choiceIdx := -1
	choices.ForEach(func(_, choice gjson.Result) bool {
		choiceIdx++
		for _, field := range []string{"delta", "message"} {
			msg := choice.Get(field)
			if !msg.Exists() || !msg.IsObject() {
				continue
			}
			text, ok := reasoningBackfillText(msg)
			if !ok || text == "" {
				continue
			}
			if existing := msg.Get("reasoning_content"); existing.Exists() {
				if strings.TrimSpace(existing.String()) != "" {
					continue
				}
			}
			updated, err := sjson.SetBytes(out, "choices."+intToString(choiceIdx)+"."+field+".reasoning_content", text)
			if err != nil {
				continue
			}
			out = updated
			changed = true
		}
		return true
	})
	return out, changed
}

// reasoningBackfillText extracts thinking text from one delta/message object.
func reasoningBackfillText(msg gjson.Result) (string, bool) {
	if details := msg.Get("reasoning_details"); details.IsArray() {
		var parts []string
		for _, d := range details.Array() {
			if t := d.Get("text").String(); strings.TrimSpace(t) != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ""), true
		}
	}
	if r := msg.Get("reasoning"); r.Type == gjson.String {
		if t := strings.TrimSpace(r.String()); t != "" {
			return r.String(), true
		}
	}
	return "", false
}

func intToString(i int) string { return strconv.Itoa(i) }
