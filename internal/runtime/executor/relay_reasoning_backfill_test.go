package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestMapReasoningBackfill_DetailsToContent(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"role":"assistant","reasoning_details":[{"type":"reasoning.text","text":"think"}],"content":null}}]}`)
	out, changed := mapReasoningBackfill(body)
	if !changed {
		t.Fatal("expected body to change")
	}
	if got := gjson.GetBytes(out, "choices.0.delta.reasoning_content").String(); got != "think" {
		t.Fatalf("reasoning_content = %q, want %q", got, "think")
	}
}

func TestMapReasoningBackfill_StringToContent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","reasoning":"ponder","content":"answer"}}]}`)
	out, changed := mapReasoningBackfill(body)
	if !changed {
		t.Fatal("expected body to change")
	}
	if got := gjson.GetBytes(out, "choices.0.message.reasoning_content").String(); got != "ponder" {
		t.Fatalf("reasoning_content = %q, want %q", got, "ponder")
	}
}

func TestMapReasoningBackfill_ExistingContentPreserved(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"reasoning":"new","reasoning_content":"original"}}]}`)
	out, changed := mapReasoningBackfill(body)
	if changed {
		t.Fatal("expected body to stay unchanged when reasoning_content is set")
	}
	if got := gjson.GetBytes(out, "choices.0.delta.reasoning_content").String(); got != "original" {
		t.Fatalf("reasoning_content = %q, want %q", got, "original")
	}
}

func TestMapReasoningBackfill_DetailsWinOverString(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"reasoning":"plain","reasoning_details":[{"text":"rich"}]}}]}`)
	out, changed := mapReasoningBackfill(body)
	if !changed {
		t.Fatal("expected body to change")
	}
	if got := gjson.GetBytes(out, "choices.0.delta.reasoning_content").String(); got != "rich" {
		t.Fatalf("reasoning_content = %q, want %q", got, "rich")
	}
}

func TestMapReasoningBackfill_NoChoices(t *testing.T) {
	body := []byte(`{"id":"chunk-1","object":"chat.completion.chunk"}`)
	if _, changed := mapReasoningBackfill(body); changed {
		t.Fatal("expected body without choices to stay unchanged")
	}
}

func TestReasoningBackfillForProvider_Guard(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"reasoning":"x"}}]}`)
	for _, provider := range []string{"commandcode", "opencode-go", "CommandCode", " OpenCode-Go "} {
		out := reasoningBackfillForProvider(provider, body)
		if got := gjson.GetBytes(out, "choices.0.delta.reasoning_content").String(); got != "x" {
			t.Fatalf("provider %q: reasoning_content = %q, want %q", provider, got, "x")
		}
	}
	for _, provider := range []string{"openai", "mirasim", "claude", ""} {
		out := reasoningBackfillForProvider(provider, body)
		if gjson.GetBytes(out, "choices.0.delta.reasoning_content").Exists() {
			t.Fatalf("provider %q: backfill must not apply", provider)
		}
	}
}
