package main

// Backend Ollama chạy local: miễn phí nên thử TRƯỚC, hết đường mới gọi Claude API.
//
// Trong nhà chỉ có một dạng history: []anthropic.BetaMessageParam. Ollama nói
// tiếng khác nên file này dịch hai chiều — history -> /api/chat, và response
// -> đúng JSON wire của Anthropic rồi Unmarshal vào *anthropic.BetaMessage.
// Đi đường JSON chứ không dựng struct tay: có vậy `JSON.Input.Raw()` (tools.go
// đọc input tool bằng nó) và `AsAny()` mới có dữ liệu.
//
// Không gửi thinking của model local trở lại: khối thinking của Anthropic cần
// signature, mà giữ lại cũng chỉ phình prompt.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

const (
	ollamaURLEnvVar   = "K8SC_AI_OLLAMA_URL"   // mặc định localhost
	ollamaModelEnvVar = "K8SC_AI_OLLAMA_MODEL" // tên model, trống = model to nhất đang có
	ollamaOffEnvVar   = "K8SC_AI_OLLAMA"       // "off"/"0" = bỏ qua local, đi thẳng API

	defaultOllamaURL = "http://localhost:11434"
	ollamaTagsPath   = "/api/tags"
	ollamaShowPath   = "/api/show"
	ollamaChatPath   = "/api/chat"

	ollamaCapTools    = "tools"
	ollamaCapThinking = "thinking"

	// Dò mỗi lượt hỏi: ollama sống thì /api/tags trên localhost trả về gần như
	// tức thì, nên rẻ. Timeout ngắn để lúc nó chết không giữ người dùng.
	ollamaProbeTimeout = 1500 * time.Millisecond
	// Chết thì nghỉ dò một lúc, khỏi cộng 1.5s vào mỗi câu.
	ollamaRetryEvery = 60 * time.Second
	// Model local chậm nhưng không tính tiền nên chờ được lâu hơn API.
	ollamaChatTimeout = 5 * time.Minute

	ollamaRoleSystem    = "system"
	ollamaRoleUser      = "user"
	ollamaRoleAssistant = "assistant"
	ollamaRoleTool      = "tool"

	// ID tự sinh cho tool_call nào ollama không kèm id.
	ollamaCallIDPrefix = "ollama_call_"

	// Quy đổi hậu tố parameter_size ("8.2B", "596M") ra số tham số để so.
	ollamaSizeBillion = 1e9
	ollamaSizeMillion = 1e6

	// Nhãn hiện trên dòng chi phí.
	ollamaLabelSuffix = " (local)"
)

// ollamaClient: một daemon ollama sống, đã chọn xong model.
type ollamaClient struct {
	url   string
	model string
	hc    *http.Client
	// Model khai báo hỗ trợ tool. Không hỗ trợ thì nó lờ tool đi và trả lời theo
	// trí nhớ — sai mà không báo, nên `help` phải nói ra.
	tools bool
	// Model có thinking: xem thinkOff.
	thinking bool
}

func (o *ollamaClient) spec() modelSpec {
	return modelSpec{id: o.model, local: true}
}

// ollamaProbe giữ trạng thái dò daemon giữa các lượt hỏi.
// Nil receiver = local bị tắt bằng env, mọi lượt đi thẳng API.
type ollamaProbe struct {
	url     string
	model   string
	hc      *http.Client
	nextTry time.Time
}

func newOllamaProbe() *ollamaProbe {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ollamaOffEnvVar))) {
	case "off", "0", "no", "false":
		return nil
	}
	url := strings.TrimRight(os.Getenv(ollamaURLEnvVar), "/")
	if url == "" {
		url = defaultOllamaURL
	}
	return &ollamaProbe{
		url:   url,
		model: strings.TrimSpace(os.Getenv(ollamaModelEnvVar)),
		hc:    &http.Client{Timeout: ollamaChatTimeout},
	}
}

// get trả về client nếu daemon đang sống và có model dùng được, nil nếu không.
// Gọi mỗi lượt: kết quả thành công KHÔNG cache, để ollama chết giữa phiên là
// lượt sau tự về cloud thay vì chờ timeout.
func (p *ollamaProbe) get(now time.Time) *ollamaClient {
	if p == nil || now.Before(p.nextTry) {
		return nil
	}
	model, err := p.pickModel()
	if err != nil {
		p.nextTry = now.Add(ollamaRetryEvery)
		return nil
	}
	oc := &ollamaClient{url: p.url, model: model, hc: p.hc}
	// Đọc được capability thì dùng, không đọc được thì coi như không có gì —
	// mất tool/thinking tốt hơn là mất luôn cả backend local.
	oc.tools, oc.thinking = p.capabilities(model)
	return oc
}

// capabilities đọc /api/show để biết model có tool và thinking hay không.
func (p *ollamaProbe) capabilities(model string) (tools, thinking bool) {
	ctx, cancel := context.WithTimeout(context.Background(), ollamaProbeTimeout)
	defer cancel()

	body, err := json.Marshal(struct {
		Model string `json:"model"`
	}{Model: model})
	if err != nil {
		return false, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url+ollamaShowPath, bytes.NewReader(body))
	if err != nil {
		return false, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&show); err != nil {
		return false, false
	}
	for _, c := range show.Capabilities {
		switch c {
		case ollamaCapTools:
			tools = true
		case ollamaCapThinking:
			thinking = true
		}
	}
	return tools, thinking
}

// pickModel: model do env chỉ định (nếu daemon có), không thì model NHIỀU THAM SỐ
// NHẤT. Chạy local không tốn tiền nên to là tốt; lấy model đầu danh sách thì cài
// thêm một model nhỏ là tự nhiên tụt chất lượng.
func (p *ollamaProbe) pickModel() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ollamaProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url+ollamaTagsPath, nil)
	if err != nil {
		return "", err
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s trả %d", ollamaTagsPath, resp.StatusCode)
	}

	var tags struct {
		Models []struct {
			Model   string `json:"model"`
			Name    string `json:"name"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return "", err
	}
	best, bestSize := "", float64(-1)
	for _, m := range tags.Models {
		name := m.Model
		if name == "" {
			name = m.Name
		}
		if name == "" {
			continue
		}
		if name == p.model {
			return name, nil
		}
		if size := parseParamSize(m.Details.ParameterSize); size > bestSize {
			best, bestSize = name, size
		}
	}
	if p.model != "" {
		return "", fmt.Errorf("không có model %q", p.model)
	}
	if best == "" {
		return "", errors.New("ollama chưa pull model nào")
	}
	return best, nil
}

// parseParamSize đọc "8.2B"/"596M" ra số tham số. Không đọc được thì 0 — model
// thiếu metadata vẫn dùng được, chỉ là xếp cuối.
func parseParamSize(raw string) float64 {
	raw = strings.TrimSpace(strings.ToUpper(raw))
	if raw == "" {
		return 0
	}
	unit := float64(1)
	switch raw[len(raw)-1] {
	case 'B':
		unit = ollamaSizeBillion
		raw = raw[:len(raw)-1]
	case 'M':
		unit = ollamaSizeMillion
		raw = raw[:len(raw)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return n * unit
}

// ollamaThinkOff: địa chỉ để trỏ vào, request cần *bool để phân biệt "không gửi
// field think" với "gửi think=false".
var ollamaThinkOff = false

// thinkOff tắt thinking của model local. Model thinking hay xả hết vào field
// `thinking` rồi dừng, `content` rỗng — người dùng không thấy câu trả lời nào.
// Suy nghĩ ra ngoài cũng chỉ làm chậm: máy người dùng chạy vài chục token/s.
// Chỉ gửi field này khi model khai báo có thinking; model không có mà gửi thì
// ollama báo lỗi.
func thinkOff(supported bool) *bool {
	if !supported {
		return nil
	}
	return &ollamaThinkOff
}

type ollamaToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolName  string           `json:"tool_name,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

// chat gọi /api/chat một lượt (không stream) và trả về response đã mang hình
// dạng của Anthropic, để converse xử lý y như lượt gọi API thật.
func (o *ollamaClient) chat(history []anthropic.BetaMessageParam) (*anthropic.BetaMessage, error) {
	body, err := json.Marshal(struct {
		Model    string          `json:"model"`
		Stream   bool            `json:"stream"`
		Think    *bool           `json:"think,omitempty"`
		Messages []ollamaMessage `json:"messages"`
		Tools    []any           `json:"tools"`
	}{
		Model:    o.model,
		Think:    thinkOff(o.thinking),
		Messages: ollamaMessages(systemPrompt, history),
		Tools:    ollamaTools(),
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), ollamaChatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+ollamaChatPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []ollamaToolCall `json:"tool_calls"`
		} `json:"message"`
		PromptEvalCount int64  `json:"prompt_eval_count"`
		EvalCount       int64  `json:"eval_count"`
		Error           string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s trả %d, body không đọc được: %w", ollamaChatPath, resp.StatusCode, err)
	}
	if out.Error != "" {
		return nil, errors.New(out.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s trả %d", ollamaChatPath, resp.StatusCode)
	}

	return toAnthropicMessage(o.model, out.Message.Content, out.Message.ToolCalls,
		out.PromptEvalCount, out.EvalCount)
}

// ollamaMessages dịch history sang chuỗi message của ollama. Tool_result thành
// message role=tool riêng, giữ đúng thứ tự với tool_call sinh ra nó.
func ollamaMessages(system string, history []anthropic.BetaMessageParam) []ollamaMessage {
	names := toolUseNames(history)
	msgs := []ollamaMessage{{Role: ollamaRoleSystem, Content: system}}

	for _, m := range history {
		role := ollamaRoleUser
		if m.Role == anthropic.BetaMessageParamRoleAssistant {
			role = ollamaRoleAssistant
		}
		cur := ollamaMessage{Role: role}
		var texts []string
		flush := func() {
			cur.Content = strings.Join(texts, "\n")
			if cur.Content != "" || len(cur.ToolCalls) > 0 {
				msgs = append(msgs, cur)
			}
			cur = ollamaMessage{Role: role}
			texts = nil
		}

		for _, block := range m.Content {
			switch {
			case block.OfText != nil:
				texts = append(texts, block.OfText.Text)
			case block.OfToolUse != nil:
				tc := ollamaToolCall{ID: block.OfToolUse.ID}
				tc.Function.Name = block.OfToolUse.Name
				tc.Function.Arguments = block.OfToolUse.Input
				cur.ToolCalls = append(cur.ToolCalls, tc)
			case block.OfToolResult != nil:
				flush()
				msgs = append(msgs, ollamaMessage{
					Role:     ollamaRoleTool,
					ToolName: names[block.OfToolResult.ToolUseID],
					Content:  toolResultText(block.OfToolResult),
				})
			}
		}
		flush()
	}
	return msgs
}

// toolUseNames map tool_use id -> tên tool: ollama nhận diện tool_result theo
// tên chứ không theo id.
func toolUseNames(history []anthropic.BetaMessageParam) map[string]string {
	names := map[string]string{}
	for _, m := range history {
		for _, block := range m.Content {
			if block.OfToolUse != nil {
				names[block.OfToolUse.ID] = block.OfToolUse.Name
			}
		}
	}
	return names
}

func toolResultText(r *anthropic.BetaToolResultBlockParam) string {
	var parts []string
	for _, c := range r.Content {
		if c.OfText != nil {
			parts = append(parts, c.OfText.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ollamaTools dịch toolDefs() sang schema function-calling của ollama. Một
// nguồn định nghĩa tool duy nhất cho cả hai backend.
func ollamaTools() []any {
	defs := toolDefs()
	tools := make([]any, 0, len(defs))
	for _, d := range defs {
		t := d.OfTool
		if t == nil {
			continue
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description.Or(""),
				"parameters": map[string]any{
					"type":       "object",
					"properties": t.InputSchema.Properties,
					"required":   t.InputSchema.Required,
				},
			},
		})
	}
	return tools
}

// toAnthropicMessage dựng JSON wire của Anthropic rồi Unmarshal, để mọi field
// raw mà SDK dùng nội bộ đều có dữ liệu.
func toAnthropicMessage(model, text string, calls []ollamaToolCall, in, out int64) (*anthropic.BetaMessage, error) {
	type block struct {
		Type  string `json:"type"`
		Text  string `json:"text,omitempty"`
		ID    string `json:"id,omitempty"`
		Name  string `json:"name,omitempty"`
		Input any    `json:"input,omitempty"`
	}
	var blocks []block
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, block{Type: "text", Text: text})
	}
	for i, c := range calls {
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("%s%d", ollamaCallIDPrefix, i)
		}
		args := c.Function.Arguments
		if args == nil {
			args = map[string]any{}
		}
		blocks = append(blocks, block{Type: "tool_use", ID: id, Name: c.Function.Name, Input: args})
	}

	stop := string(anthropic.BetaStopReasonEndTurn)
	if len(calls) > 0 {
		stop = string(anthropic.BetaStopReasonToolUse)
	}

	raw, err := json.Marshal(struct {
		ID         string  `json:"id"`
		Type       string  `json:"type"`
		Role       string  `json:"role"`
		Model      string  `json:"model"`
		StopReason string  `json:"stop_reason"`
		Content    []block `json:"content"`
		Usage      struct {
			In  int64 `json:"input_tokens"`
			Out int64 `json:"output_tokens"`
		} `json:"usage"`
	}{
		ID:         ollamaCallIDPrefix + model,
		Type:       "message",
		Role:       string(anthropic.BetaMessageParamRoleAssistant),
		Model:      model,
		StopReason: stop,
		Content:    blocks,
		Usage: struct {
			In  int64 `json:"input_tokens"`
			Out int64 `json:"output_tokens"`
		}{In: in, Out: out},
	})
	if err != nil {
		return nil, err
	}

	msg := &anthropic.BetaMessage{}
	if err := json.Unmarshal(raw, msg); err != nil {
		return nil, err
	}
	return msg, nil
}
