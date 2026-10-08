package helps

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// OpenAIChatCompletionAggregator folds an OpenAI SSE stream
// (chat.completion.chunk frames) into one non-stream chat.completion JSON.
// It exists for upstreams that only speak SSE (the opencode Zen anonymous
// tier): the executor still asks for stream=true, then answers the client's
// non-stream request with the assembled body.
type OpenAIChatCompletionAggregator struct {
	id       string
	model    string
	created  int64
	content  strings.Builder
	reasons  map[int]string
	finish   map[int]string
	tools    map[int]*openAIToolCallAcc
	usageRaw []byte
}

type openAIToolCallAcc struct {
	id   string
	name string
	args strings.Builder
}

var openAIStreamFrameMarker = []byte("data:")

// ObserveOpenAISTreamLine consumes one raw SSE line (with or without the
// "data: " prefix). Invalid payloads abort the aggregation: a partial body
// must never be presented as a complete non-stream response.
func (a *OpenAIChatCompletionAggregator) ObserveOpenAISTreamLine(line []byte) error {
	if a == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte(":")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("id:")) || bytes.HasPrefix(trimmed, []byte("retry:")) {
		return nil
	}
	if !bytes.HasPrefix(trimmed, openAIStreamFrameMarker) {
		return nil
	}
	data := bytes.TrimSpace(trimmed[len(openAIStreamFrameMarker):])
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}
	if !json.Valid(data) {
		return fmt.Errorf("openai stream aggregate: invalid JSON frame (%d bytes)", len(data))
	}
	a.observeChunk(gjson.ParseBytes(data), bytes.Clone(data))
	return nil
}

func (a *OpenAIChatCompletionAggregator) observeChunk(chunk gjson.Result, raw []byte) {
	if a.id == "" {
		a.id = chunk.Get("id").String()
	}
	if a.model == "" {
		a.model = chunk.Get("model").String()
	}
	if created := chunk.Get("created").Int(); created > 0 {
		a.created = created
	}
	if usage := chunk.Get("usage"); usage.Exists() && usage.IsObject() && len(usage.Raw) > 0 {
		// The final usage-carrying chunk wins; the executor sets
		// stream_options.include_usage for these requests.
		a.usageRaw = bytes.Clone([]byte(usage.Raw))
	}
	for _, choice := range chunk.Get("choices").Array() {
		index := int(choice.Get("index").Int())
		delta := choice.Get("delta")
		if content := delta.Get("content").String(); content != "" {
			a.content.WriteString(content)
		}
		// DeepSeek-style reasoning_content (and kimi-style "reasoning") flows
		// through the aggregate unchanged so downstream reasoning handling
		// sees the standard reasoning_content field.
		reasoning := delta.Get("reasoning_content").String()
		if reasoning == "" {
			reasoning = delta.Get("reasoning").String()
		}
		if reasoning != "" {
			if a.reasons == nil {
				a.reasons = make(map[int]string)
			}
			a.reasons[index] += reasoning
		}
		for _, tc := range delta.Get("tool_calls").Array() {
			accIndex := int(tc.Get("index").Int())
			if a.tools == nil {
				a.tools = make(map[int]*openAIToolCallAcc)
			}
			acc, ok := a.tools[accIndex]
			if !ok {
				acc = &openAIToolCallAcc{}
				a.tools[accIndex] = acc
			}
			if id := tc.Get("id").String(); id != "" {
				acc.id = id
			}
			if name := tc.Get("function.name").String(); name != "" {
				acc.name = name
			}
			acc.args.WriteString(tc.Get("function.arguments").String())
		}
		if finish := choice.Get("finish_reason"); finish.Exists() && finish.Type == gjson.String && finish.String() != "" {
			if a.finish == nil {
				a.finish = make(map[int]string)
			}
			a.finish[index] = finish.String()
		}
	}
}

// Finish renders the assembled chat.completion. It returns an error when the
// stream carried no choices at all (an empty or aborted upstream stream).
func (a *OpenAIChatCompletionAggregator) Finish(fallbackModel string, fallbackCreated int64) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("openai stream aggregate: aggregator is nil")
	}
	if a.finish == nil && a.content.Len() == 0 && len(a.tools) == 0 && len(a.usageRaw) == 0 {
		return nil, fmt.Errorf("openai stream aggregate: stream carried no completion chunks")
	}
	if len(a.finish) == 0 {
		return nil, fmt.Errorf("openai stream aggregate: upstream stream ended before any finish_reason")
	}

	model := strings.TrimSpace(a.model)
	if model == "" {
		model = strings.TrimSpace(fallbackModel)
	}
	created := a.created
	if created <= 0 {
		created = fallbackCreated
	}
	id := strings.TrimSpace(a.id)
	if id == "" {
		id = fmt.Sprintf("chatcmpl-agg-%x", created)
	}

	indexes := make([]int, 0, len(a.finish))
	for index := range a.finish {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	var b strings.Builder
	b.WriteString(`{"id":`)
	b.WriteString(strconvQuote(id))
	b.WriteString(`,"object":"chat.completion","created":`)
	fmt.Fprintf(&b, "%d", created)
	b.WriteString(`,"model":`)
	b.WriteString(strconvQuote(model))
	b.WriteString(`,"choices":[`)
	for i, index := range indexes {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"index":`)
		fmt.Fprintf(&b, "%d", index)
		b.WriteString(`,"message":{"role":"assistant","content":`)
		if i == 0 {
			b.WriteString(strconvQuote(a.content.String()))
		} else {
			// Only the first choice is ever observed in practice; keep
			// additional choices structurally valid with empty content.
			b.WriteString(`""`)
		}
		if i == 0 && len(a.tools) > 0 {
			b.WriteString(`,"tool_calls":[`)
			toolIdx := make([]int, 0, len(a.tools))
			for ti := range a.tools {
				toolIdx = append(toolIdx, ti)
			}
			sort.Ints(toolIdx)
			for j, ti := range toolIdx {
				if j > 0 {
					b.WriteByte(',')
				}
				acc := a.tools[ti]
				b.WriteString(`{"index":`)
				fmt.Fprintf(&b, "%d", ti)
				b.WriteString(`,"id":`)
				b.WriteString(strconvQuote(acc.id))
				b.WriteString(`,"type":"function","function":{"name":`)
				b.WriteString(strconvQuote(acc.name))
				b.WriteString(`,"arguments":`)
				b.WriteString(strconvQuote(acc.args.String()))
				b.WriteString(`}}`)
			}
			b.WriteByte(']')
		}
		if i == 0 {
			if reasoning := strings.TrimSpace(a.reasons[index]); reasoning != "" {
				b.WriteString(`,"reasoning_content":`)
				b.WriteString(strconvQuote(a.reasons[index]))
			}
		}
		b.WriteString(`},"finish_reason":`)
		b.WriteString(strconvQuote(a.finish[index]))
		b.WriteByte('}')
	}
	b.WriteByte(']')
	if len(a.usageRaw) > 0 {
		b.WriteString(`,"usage":`)
		b.Write(a.usageRaw)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// AggregateOpenAISTreamChunks consumes a full SSE body and returns the
// assembled non-stream chat.completion body. It tolerates trailing junk after
// [DONE] (comment pings) but rejects malformed frames.
func AggregateOpenAISTreamChunks(body io.Reader, fallbackModel string, fallbackCreated int64) ([]byte, error) {
	agg := &OpenAIChatCompletionAggregator{}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 52_428_800)
	for scanner.Scan() {
		if err := agg.ObserveOpenAISTreamLine(scanner.Bytes()); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("openai stream aggregate: read stream: %w", err)
	}
	return agg.Finish(fallbackModel, fallbackCreated)
}

func strconvQuote(s string) string {
	// json.Marshal keeps the escaping rules identical to the rest of the
	// codebase without pulling in another quoting helper.
	out, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(out)
}
