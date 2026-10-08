package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const zenLikeStream = `data: {"id":"b36e4b20a84549f4af9467921c116b6f","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"role":"assistant","content":"","reasoning_content":null}}]}

data: {"id":"b36e4b20a84549f4af9467921c116b6f","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"reasoning_content":"The"}}]}

data: {"id":"b36e4b20a84549f4af9467921c116b6f","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"reasoning_content":" user"}}]}

data: {"id":"b36e4b20a84549f4af9467921c116b6f","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"content":"Hello"}}]}

data: {"id":"b36e4b20a84549f4af9467921c116b6f","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"finish_reason":null,"logprobs":null,"delta":{"content":" world"}}]}

data: {"id":"b36e4b20a84549f4af9467921c116b6f","object":"chat.completion.chunk","created":1791394469,"model":"fledge-alpha-free","choices":[{"index":0,"finish_reason":"stop","logprobs":null,"delta":{}}],"usage":{"prompt_tokens":169,"completion_tokens":17,"total_tokens":186,"prompt_tokens_details":{}}}

data: [DONE]

`

func TestAggregateOpenAISTreamChunksBasic(t *testing.T) {
	out, err := AggregateOpenAISTreamChunks(strings.NewReader(zenLikeStream), "fallback-model", 1_700_000_000)
	if err != nil {
		t.Fatalf("aggregate failed: %v", err)
	}

	root := gjson.ParseBytes(out)
	if got := root.Get("object").String(); got != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", got)
	}
	if got := root.Get("id").String(); got != "b36e4b20a84549f4af9467921c116b6f" {
		t.Errorf("id = %q, want upstream id", got)
	}
	if got := root.Get("model").String(); got != "fledge-alpha-free" {
		t.Errorf("model = %q, want fledge-alpha-free", got)
	}
	if got := root.Get("created").Int(); got != 1791394469 {
		t.Errorf("created = %d, want upstream value", got)
	}
	if got := root.Get("choices.0.message.content").String(); got != "Hello world" {
		t.Errorf("content = %q, want concatenated deltas", got)
	}
	if got := root.Get("choices.0.message.reasoning_content").String(); got != "The user" {
		t.Errorf("reasoning_content = %q, want concatenated reasoning deltas", got)
	}
	if got := root.Get("choices.0.finish_reason").String(); got != "stop" {
		t.Errorf("finish_reason = %q, want stop", got)
	}
	if got := root.Get("usage.total_tokens").Int(); got != 186 {
		t.Errorf("total_tokens = %d, want 186", got)
	}
}

const toolCallStream = `data: {"id":"tc1","object":"chat.completion.chunk","created":1791394469,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":""}}]}}]}

data: {"id":"tc1","object":"chat.completion.chunk","created":1791394469,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}

data: {"id":"tc1","object":"chat.completion.chunk","created":1791394469,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls -la\"}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"read","arguments":"{\"path\":\"/etc/hosts\"}"}}]}}]}

data: {"id":"tc1","object":"chat.completion.chunk","created":1791394469,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`

func TestAggregateOpenAISTreamChunksToolCalls(t *testing.T) {
	out, err := AggregateOpenAISTreamChunks(strings.NewReader(toolCallStream), "", 42)
	if err != nil {
		t.Fatalf("aggregate failed: %v", err)
	}
	calls := gjson.GetBytes(out, "choices.0.message.tool_calls")
	if got := len(calls.Array()); got != 2 {
		t.Fatalf("tool_calls count = %d, want 2 merged", got)
	}
	bash := calls.Array()[0]
	if got := bash.Get("id").String(); got != "call_1" {
		t.Errorf("bash id = %q", got)
	}
	if got := bash.Get("function.name").String(); got != "bash" {
		t.Errorf("bash name = %q", got)
	}
	if got := bash.Get("function.arguments").String(); got != `{"command":"ls -la"}` {
		t.Errorf("bash arguments = %q, want merged fragments", got)
	}
	read := calls.Array()[1]
	if got := read.Get("id").String(); got != "call_2" {
		t.Errorf("read id = %q", got)
	}
	if got := read.Get("function.arguments").String(); got != `{"path":"/etc/hosts"}` {
		t.Errorf("read arguments = %q", got)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", got)
	}
}

func TestAggregateOpenAISTreamChunksFallbacks(t *testing.T) {
	out, err := AggregateOpenAISTreamChunks(strings.NewReader(toolCallStream), "fallback-model", 42)
	if err != nil {
		t.Fatalf("aggregate failed: %v", err)
	}
	if got := gjson.GetBytes(out, "model").String(); got != "m" {
		t.Errorf("model = %q, want stream-reported model", got)
	}
}

func TestAggregateOpenAISTreamChunksNoChoices(t *testing.T) {
	stream := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[]}\n\ndata: [DONE]\n"
	if _, err := AggregateOpenAISTreamChunks(strings.NewReader(stream), "m", 1); err == nil {
		t.Fatal("expected error for stream without any completion chunks")
	}
}

func TestAggregateOpenAISTreamChunksNoFinishReason(t *testing.T) {
	stream := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n"
	if _, err := AggregateOpenAISTreamChunks(strings.NewReader(stream), "m", 1); err == nil {
		t.Fatal("expected error for stream ended before finish_reason")
	}
}

func TestAggregateOpenAISTreamChunksInvalidJSON(t *testing.T) {
	stream := "data: {not json\n\ndata: [DONE]\n"
	if _, err := AggregateOpenAISTreamChunks(strings.NewReader(stream), "m", 1); err == nil {
		t.Fatal("expected error for invalid JSON frame")
	}
}
