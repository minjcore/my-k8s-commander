package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// capsTools/capsFull: body /api/show đủ dùng cho test.
const (
	capsTools = `{"capabilities":["completion","tools"]}`
	capsFull  = `{"capabilities":["completion","tools","thinking"]}`
)

// fakeOllama đóng vai daemon: /api/tags trả danh sách model, /api/show trả
// capabilities, /api/chat trả nguyên chuỗi JSON đã dựng sẵn. chatBody giữ lại
// request cuối gửi vào /api/chat.
type fakeOllama struct {
	srv      *httptest.Server
	chatBody []byte
}

func newFakeOllama(t *testing.T, tags, show, chat string) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case ollamaTagsPath:
			io.WriteString(w, tags)
		case ollamaShowPath:
			io.WriteString(w, show)
		case ollamaChatPath:
			f.chatBody, _ = io.ReadAll(r.Body)
			io.WriteString(w, chat)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// thinkField đọc field "think" trong request cuối: nil = không gửi.
func (f *fakeOllama) thinkField(t *testing.T) *bool {
	t.Helper()
	var body struct {
		Think *bool `json:"think"`
	}
	if err := json.Unmarshal(f.chatBody, &body); err != nil {
		t.Fatalf("chat body không đọc được: %v", err)
	}
	return body.Think
}

func probeFor(t *testing.T, url string) *ollamaProbe {
	t.Helper()
	t.Setenv(ollamaURLEnvVar, url)
	t.Setenv(ollamaModelEnvVar, "")
	t.Setenv(ollamaOffEnvVar, "")
	return newOllamaProbe()
}

// Không chỉ định model thì lấy model to nhất, không phải model đầu danh sách.
func TestProbeChonModelToNhat(t *testing.T) {
	tags := `{"models":[
	  {"model":"gemma4:e2b","details":{"parameter_size":"5.1B"}},
	  {"model":"qwen3:8b","details":{"parameter_size":"8.2B"}},
	  {"model":"qwen3:0.6b","details":{"parameter_size":"596M"}}]}`
	f := newFakeOllama(t, tags, capsFull, "")
	oc := probeFor(t, f.srv.URL).get(time.Now())
	if oc == nil {
		t.Fatal("probe không thấy daemon")
	}
	if oc.model != "qwen3:8b" {
		t.Fatalf("model = %q", oc.model)
	}
	if !oc.spec().local {
		t.Fatal("spec phải là local")
	}
}

// Model thiếu parameter_size vẫn dùng được khi không có lựa chọn nào khác.
func TestProbeThieuMetadataVanDung(t *testing.T) {
	f := newFakeOllama(t, `{"models":[{"model":"la:latest"}]}`, capsFull, "")
	if oc := probeFor(t, f.srv.URL).get(time.Now()); oc == nil || oc.model != "la:latest" {
		t.Fatalf("oc = %+v", oc)
	}
}

func TestParseParamSize(t *testing.T) {
	cases := map[string]float64{
		"9.0B": 9 * ollamaSizeBillion,
		"30B":  30 * ollamaSizeBillion,
		"596M": 596 * ollamaSizeMillion,
		"":     0,
		"abc":  0,
	}
	for raw, want := range cases {
		if got := parseParamSize(raw); got != want {
			t.Fatalf("parseParamSize(%q) = %v, chờ %v", raw, got, want)
		}
	}
	if parseParamSize("8.2B") <= parseParamSize("5.1B") {
		t.Fatal("8.2B phải lớn hơn 5.1B")
	}
}

func TestProbeUuTienModelTheoEnv(t *testing.T) {
	f := newFakeOllama(t, `{"models":[{"model":"gemma4:e2b"},{"model":"qwen3:4b"}]}`, capsFull, "")
	p := probeFor(t, f.srv.URL)
	p.model = "qwen3:4b"
	if oc := p.get(time.Now()); oc == nil || oc.model != "qwen3:4b" {
		t.Fatalf("oc = %+v", oc)
	}

	p.model = "khong-co"
	if oc := p.get(time.Now()); oc != nil {
		t.Fatalf("model không tồn tại vẫn trả về %+v", oc)
	}
}

func TestProbeTatBangEnv(t *testing.T) {
	t.Setenv(ollamaOffEnvVar, "off")
	if p := newOllamaProbe(); p != nil {
		t.Fatal("K8SC_AI_OLLAMA=off vẫn dựng probe")
	}
	// Nil receiver phải im lặng trả nil, không panic.
	var p *ollamaProbe
	if p.get(time.Now()) != nil {
		t.Fatal("nil probe trả về client")
	}
}

// Daemon chết: get() nghỉ dò trong ollamaRetryEvery để không cộng timeout vào
// mọi câu hỏi tiếp theo.
func TestProbeChetThiNghiDo(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := probeFor(t, srv.URL)
	now := time.Now()
	if p.get(now) != nil {
		t.Fatal("daemon lỗi vẫn trả client")
	}
	if p.get(now.Add(ollamaRetryEvery/2)) != nil {
		t.Fatal("chưa tới hạn đã dò lại")
	}
	if hits != 1 {
		t.Fatalf("dò %d lần, chờ 1", hits)
	}
	if p.get(now.Add(ollamaRetryEvery+time.Second)) != nil {
		t.Fatal("daemon vẫn lỗi mà trả client")
	}
	if hits != 2 {
		t.Fatalf("hết hạn nghỉ mà dò %d lần", hits)
	}
}

func TestChatDoiToolCallThanhToolUseBlock(t *testing.T) {
	chat := `{"message":{"role":"assistant","content":"để xem",
	  "thinking":"nghĩ dài dòng",
	  "tool_calls":[{"id":"call_abc","function":{"name":"k8s","arguments":{"command":"get pods -A"}}}]},
	  "prompt_eval_count":105,"eval_count":278}`
	f := newFakeOllama(t, `{"models":[{"model":"gemma4:e2b"}]}`, capsFull, chat)
	oc := probeFor(t, f.srv.URL).get(time.Now())

	history := []anthropic.BetaMessageParam{
		anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("cụm có pod nào?")),
	}
	resp, err := oc.chat(history)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != anthropic.BetaStopReasonToolUse {
		t.Fatalf("stop_reason = %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 105 || resp.Usage.OutputTokens != 278 {
		t.Fatalf("usage = %+v", resp.Usage)
	}

	var sawText, sawUse bool
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			sawText = true
			if v.Text != "để xem" {
				t.Fatalf("text = %q", v.Text)
			}
		case anthropic.BetaToolUseBlock:
			sawUse = true
			if v.ID != "call_abc" || v.Name != toolK8s {
				t.Fatalf("tool_use = %+v", v)
			}
			// tools.go đọc input qua JSON.Input.Raw(); không có là runTool vô dụng.
			var in struct{ Command string }
			if err := json.Unmarshal([]byte(v.JSON.Input.Raw()), &in); err != nil {
				t.Fatalf("raw input không đọc được: %v (raw=%q)", err, v.JSON.Input.Raw())
			}
			if in.Command != "get pods -A" {
				t.Fatalf("command = %q", in.Command)
			}
		}
	}
	if !sawText || !sawUse {
		t.Fatalf("thiếu block: text=%v tool_use=%v", sawText, sawUse)
	}

	// Lượt assistant phải nối lại được vào history mà không mất tool_use.
	param := resp.ToParam()
	if len(param.Content) != 2 || param.Content[1].OfToolUse == nil {
		t.Fatalf("ToParam mất tool_use: %+v", param.Content)
	}
}

func TestChatTraLoiRong(t *testing.T) {
	chat := `{"message":{"role":"assistant","content":""},"prompt_eval_count":10,"eval_count":0}`
	f := newFakeOllama(t, `{"models":[{"model":"gemma4:e2b"}]}`, capsFull, chat)
	oc := probeFor(t, f.srv.URL).get(time.Now())

	resp, err := oc.chat(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 0 {
		t.Fatalf("content = %+v", resp.Content)
	}
	if resp.StopReason != anthropic.BetaStopReasonEndTurn {
		t.Fatalf("stop_reason = %q", resp.StopReason)
	}
}

func TestChatBaoLoiCuaDaemon(t *testing.T) {
	f := newFakeOllama(t, `{"models":[{"model":"gemma4:e2b"}]}`, capsFull, `{"error":"model requires more system memory"}`)
	oc := probeFor(t, f.srv.URL).get(time.Now())
	if _, err := oc.chat(nil); err == nil {
		t.Fatal("lỗi daemon bị bỏ qua")
	}
}

// History có tool_result phải ra message role=tool kèm tool_name, đúng thứ tự
// sau assistant gọi tool.
func TestOllamaMessagesDichHistory(t *testing.T) {
	toolUse := anthropic.BetaToolUseBlockParam{
		ID:    "call_abc",
		Name:  toolK8s,
		Input: map[string]any{"command": "get pods -A"},
	}
	history := []anthropic.BetaMessageParam{
		anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("pod nào?")),
		{
			Role: anthropic.BetaMessageParamRoleAssistant,
			Content: []anthropic.BetaContentBlockParamUnion{
				anthropic.NewBetaTextBlock("để xem"),
				{OfToolUse: &toolUse},
			},
		},
		anthropic.NewBetaUserMessage(anthropic.NewBetaToolResultBlock("call_abc", "nginx Running", false)),
	}

	msgs := ollamaMessages("SYS", history)
	if len(msgs) != 4 {
		t.Fatalf("có %d message: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != ollamaRoleSystem || msgs[0].Content != "SYS" {
		t.Fatalf("system = %+v", msgs[0])
	}
	if msgs[1].Role != ollamaRoleUser || msgs[1].Content != "pod nào?" {
		t.Fatalf("user = %+v", msgs[1])
	}
	if msgs[2].Role != ollamaRoleAssistant || msgs[2].Content != "để xem" || len(msgs[2].ToolCalls) != 1 {
		t.Fatalf("assistant = %+v", msgs[2])
	}
	if msgs[2].ToolCalls[0].Function.Name != toolK8s {
		t.Fatalf("tool_call = %+v", msgs[2].ToolCalls[0])
	}
	if msgs[3].Role != ollamaRoleTool || msgs[3].ToolName != toolK8s || msgs[3].Content != "nginx Running" {
		t.Fatalf("tool = %+v", msgs[3])
	}
}

func TestOllamaToolsGiuNguyenSchema(t *testing.T) {
	tools := ollamaTools()
	if len(tools) != len(toolDefs()) {
		t.Fatalf("có %d tool, chờ %d", len(tools), len(toolDefs()))
	}
	first, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool[0] = %T", tools[0])
	}
	fn := first["function"].(map[string]any)
	if fn["name"] != toolK8s {
		t.Fatalf("name = %v", fn["name"])
	}
	if fn["description"] == "" {
		t.Fatal("description rỗng")
	}
	params := fn["parameters"].(map[string]any)
	if params["properties"] == nil {
		t.Fatal("thiếu properties")
	}
	req, _ := params["required"].([]string)
	if len(req) != 1 || req[0] != "command" {
		t.Fatalf("required = %v", params["required"])
	}
}

// Model local không được ghi gì vào sổ chi tiêu, và dòng tổng kết phải nói rõ
// là chạy local + miễn phí.
func TestMeterKhongTinhTienModelLocal(t *testing.T) {
	m := &meter{budget: defaultBudget}
	spec := modelSpec{id: "gemma4:e2b", local: true}
	t2 := m.record(spec, anthropic.BetaUsage{InputTokens: 105, OutputTokens: 278})

	if t2.cost != 0 || m.state.Cost != 0 || m.state.In != 0 || m.state.Out != 0 {
		t.Fatalf("local vẫn ghi vào sổ: turn=%+v state=%+v", t2, m.state)
	}
	if t2.in != 105 || t2.out != 278 {
		t.Fatalf("mất token: %+v", t2)
	}
	line := m.line(spec, t2)
	for _, want := range []string{"gemma4:e2b", "(local)", "miễn phí"} {
		if !strings.Contains(line, want) {
			t.Fatalf("dòng %q thiếu %q", line, want)
		}
	}
}

// Model có thinking phải bị tắt thinking: bật thì nó xả hết vào field thinking
// rồi dừng, content rỗng.
func TestChatTatThinkingKhiModelHoTro(t *testing.T) {
	chat := `{"message":{"role":"assistant","content":"ok"},"prompt_eval_count":1,"eval_count":1}`
	f := newFakeOllama(t, `{"models":[{"model":"qwen3.5:9b"}]}`, capsFull, chat)
	oc := probeFor(t, f.srv.URL).get(time.Now())
	if !oc.thinking || !oc.tools {
		t.Fatalf("capability đọc sai: tools=%v thinking=%v", oc.tools, oc.thinking)
	}
	if _, err := oc.chat(nil); err != nil {
		t.Fatal(err)
	}
	think := f.thinkField(t)
	if think == nil || *think {
		t.Fatalf("think = %v, chờ false", think)
	}
}

// Model không có thinking thì KHÔNG được gửi field think — ollama báo lỗi.
func TestChatKhongGuiThinkKhiModelKhongHoTro(t *testing.T) {
	chat := `{"message":{"role":"assistant","content":"ok"},"prompt_eval_count":1,"eval_count":1}`
	f := newFakeOllama(t, `{"models":[{"model":"co-tool:latest"}]}`, capsTools, chat)
	oc := probeFor(t, f.srv.URL).get(time.Now())
	if oc.thinking {
		t.Fatal("model không khai thinking mà đọc ra có")
	}
	if _, err := oc.chat(nil); err != nil {
		t.Fatal(err)
	}
	if think := f.thinkField(t); think != nil {
		t.Fatalf("think = %v, chờ không gửi", *think)
	}
}

// /api/show hỏng thì vẫn dùng được model, chỉ là coi như không có capability.
func TestCapabilityDocKhongDuocVanChay(t *testing.T) {
	chat := `{"message":{"role":"assistant","content":"ok"},"prompt_eval_count":1,"eval_count":1}`
	f := newFakeOllama(t, `{"models":[{"model":"x:latest"}]}`, `khong-phai-json`, chat)
	oc := probeFor(t, f.srv.URL).get(time.Now())
	if oc == nil {
		t.Fatal("show lỗi mà mất luôn backend local")
	}
	if oc.tools || oc.thinking {
		t.Fatalf("capability = tools:%v thinking:%v", oc.tools, oc.thinking)
	}
	if _, err := oc.chat(nil); err != nil {
		t.Fatal(err)
	}
	if think := f.thinkField(t); think != nil {
		t.Fatalf("think = %v, chờ không gửi", *think)
	}
}
