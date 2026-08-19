// Micro-binary AI: mỗi dòng stdin là 1 câu hỏi, in trả lời ra stdout.
// Build ra modules/ai-worker. Giữ history trong tiến trình nên hỏi tiếp được; "reset" để xoá.
//
// Thử ollama local trước (miễn phí — xem ollama.go), không có thì gọi Claude
// Messages API. Credential cloud: SDK tự đọc ANTHROPIC_API_KEY, hoặc profile
// OAuth do `ant auth login` tạo.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"my-k8s-commander/pkg/workerrpc"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

const (
	maxTokens = 16000
	prefix    = "[ai-worker]"

	// Output tool cắt ngắn hơn mặc định của workerrpc: bảng 200 dòng vừa tốn
	// token lúc gửi vào, vừa kéo model viết dài ra. Thiếu thì model hỏi tiếp.
	toolOutputLines = 50

	// Model rẻ không nhận effort; chỉ model mạnh mới set, và set vừa phải.
	strongEffort = anthropic.BetaOutputConfigEffortMedium
	// Giữ 20 lượt gần nhất (user+assistant) để context không phình vô hạn.
	maxHistory  = 40
	callTimeout = 3 * time.Minute

	// Dừng vòng tool sau ngần này lượt để một model đi lạc không quay vòng mãi.
	maxToolRounds = 8

	systemPrompt = `Bạn là trợ lý vận hành Kubernetes trong một app terminal.
Trả lời bằng ngôn ngữ người dùng dùng để hỏi. Ngắn gọn, đi thẳng vào việc.
Output là plain text hiển thị trong terminal: không dùng markdown heading,
không bảng, không code fence. Lệnh shell viết trên dòng riêng, thụt 2 space.
Nếu một lệnh có thể phá huỷ (delete, scale 0, drain), nói rõ rủi ro trước khi đưa lệnh.

Bạn có 2 tool để lấy dữ liệu thật: k8s (cụm Kubernetes) và server (sổ server SSH).
Câu hỏi nào cần trạng thái thật của hệ thống thì gọi tool rồi trả lời theo output,
đừng đoán. Lệnh đọc chạy thẳng; lệnh thay đổi hệ thống phải chờ người dùng duyệt,
nên chỉ gọi khi họ đã yêu cầu rõ ràng. Bị từ chối thì đừng thử lại và đừng tìm
đường vòng — đưa lệnh cho người dùng tự gõ vào Terminal.`
)

func main() {
	client := anthropic.NewClient()
	out := bufio.NewWriter(os.Stdout)
	pool := workerrpc.NewPool(prefix)
	pool.MaxLines = toolOutputLines
	// helm render chart lớn (Airbyte) lâu hơn 90s mặc định; timeout phải rộng hơn
	// helmTimeout bên k8s-worker để lỗi hiện ra từ helm chứ không phải bị kill.
	pool.Timeout = 120 * time.Second
	defer pool.StopAll()

	// Đọc stdin trong goroutine: vòng tool nằm giữa 2 lượt gọi API cũng cần đọc
	// stdin (để hỏi duyệt lệnh ghi), đọc trực tiếp trong vòng lặp sẽ tự khoá.
	input := readStdin()
	ap := newApprover(input)
	base := tier(modelEnvVar, defaultTier)
	strong := tier(strongModelEnvVar, defaultStrongTier)
	probe := newOllamaProbe()
	var history []anthropic.BetaMessageParam

	for line := range input {
		if line == "" {
			continue
		}
		if line == "reset" {
			history = nil
			emit(out, []string{"đã xoá history"})
			continue
		}
		// Trả lời tại chỗ, không tốn một lượt gọi API cho câu hỏi về chính worker.
		if line == "help" {
			emit(out, usage(ap, probe, base, strong))
			continue
		}

		// "!" đầu dòng = câu khó, dùng model mạnh cho riêng lượt này.
		// Không thì thử ollama local trước — miễn phí, và dò lại mỗi lượt nên
		// daemon tắt/mở giữa phiên vẫn theo đúng.
		spec := base
		var oc *ollamaClient
		if strings.HasPrefix(line, "!") {
			spec = strong
			if line = strings.TrimSpace(line[1:]); line == "" {
				continue
			}
		} else if oc = probe.get(time.Now()); oc != nil {
			spec = oc.spec()
		}

		// Đọc lại đồng hồ mỗi lượt: sang tháng thì tự về 0, và biết cả phần
		// tiêu ở tiến trình ai-worker khác (supervisor có thể đã restart worker).
		m := loadMeter(time.Now())
		if m.exceeded() && !spec.local {
			emit(out, []string{m.blockedLine()})
			continue
		}

		// Giữ mốc để lượt hỏi thất bại không để lại rác trong history.
		snapshot := history
		history = append(history, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(line)))
		updated, ok := converse(&client, oc, pool, ap, m, spec, base, out, history)
		m.save()
		if ok {
			history = trimHistory(updated)
		} else {
			history = snapshot
		}
	}
}

// readStdin đẩy từng dòng (đã trim) vào channel, đóng channel khi hết stdin.
func readStdin() <-chan string {
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			lines <- strings.TrimSpace(sc.Text())
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", prefix, err)
		}
	}()
	return lines
}

func usage(ap *approver, probe *ollamaProbe, base, strong modelSpec) []string {
	m := loadMeter(time.Now())
	engine := "model " + shortModel(base.id) + " (Claude API)"
	warn := ""
	if oc := probe.get(time.Now()); oc != nil {
		engine = "model " + oc.model + ollamaLabelSuffix + ", miễn phí — hỏng thì rơi về " + shortModel(base.id)
		if !oc.tools {
			// Model không có tool sẽ trả lời theo trí nhớ chứ không đọc cụm thật,
			// mà không hề báo — phải nói ra chỗ này.
			warn = "  ⚠ model local này không hỗ trợ tool: nó đoán chứ không đọc cụm thật. " +
				"Đổi bằng " + ollamaModelEnvVar + "=<model có tool>, hoặc tắt local bằng " +
				ollamaOffEnvVar + "=off"
		}
	}
	lines := []string{
		"gõ `ai <câu hỏi>` — " + engine,
	}
	if warn != "" {
		lines = append(lines, warn)
	}
	return append(lines,
		"  ai !<câu hỏi>   dùng model mạnh ("+shortModel(strong.id)+") cho riêng câu đó",
		"  ai reset        xoá history hội thoại",
		"tool: k8s (k8s-worker), server (server-worker)",
		"  "+ap.describe(),
		"tháng này đã tiêu "+money(m.state.Cost)+"/"+money(m.budget)+
			" qua "+strconv.Itoa(m.state.Asks)+" câu ("+m.path+")",
	)
}

// converse chạy vòng model -> tool -> model cho tới khi model trả lời xong.
// In text và log tool ngay khi có, trả về history đã nối thêm lượt của vòng này.
func converse(client *anthropic.Client, oc *ollamaClient, pool *workerrpc.Pool, ap *approver, m *meter,
	spec, fallback modelSpec, out *bufio.Writer,
	history []anthropic.BetaMessageParam) ([]anthropic.BetaMessageParam, bool) {
	var turn turnUsage
	// Chi phí luôn được in ra, kể cả khi lượt hỏi lỗi giữa chừng — tiêu rồi thì
	// phải thấy.
	defer func() {
		// Câu chạy local không tính vào số câu đã tiêu tiền.
		if !spec.local {
			m.state.Asks++
		}
		emit(out, []string{m.line(spec, turn)})
	}()

	for round := 0; round < maxToolRounds; round++ {
		// Trần tháng có thể bị vượt ngay giữa vòng tool của một câu dài.
		if m.exceeded() && !spec.local {
			emit(out, []string{m.blockedLine()})
			return nil, false
		}
		resp, err := ask(client, oc, spec, history)
		// Ollama chết giữa vòng tool: rơi về cloud cho nốt câu này thay vì mất
		// cả lượt (trừ khi đã chạm trần tháng — lúc đó không được tiêu thêm).
		if err != nil && spec.local && !m.exceeded() {
			emit(out, []string{"ollama lỗi: " + err.Error(),
				"chuyển sang " + shortModel(fallback.id)})
			spec = fallback
			resp, err = ask(client, oc, spec, history)
		}
		if err != nil {
			if spec.local {
				emit(out, []string{"lỗi gọi ollama: " + err.Error()})
				return nil, false
			}
			emit(out, []string{"lỗi gọi API: " + apiError(err)})
			return nil, false
		}
		turn.add(m.record(spec, resp.Usage))
		if resp.StopReason == anthropic.BetaStopReasonRefusal {
			emit(out, []string{"model từ chối trả lời câu này (" + string(resp.StopDetails.Category) + ")"})
			return nil, false
		}
		// Nối nguyên lượt assistant (kèm thinking/tool_use) trước khi xử lý tool.
		history = append(history, resp.ToParam())

		var (
			sawText bool
			results []anthropic.BetaContentBlockParamUnion
		)
		for _, block := range resp.Content {
			switch variant := block.AsAny().(type) {
			case anthropic.BetaTextBlock:
				sawText = true
				emit(out, []string{variant.Text})
			case anthropic.BetaToolUseBlock:
				results = append(results, runTool(pool, ap, out, variant))
			}
		}

		if len(results) == 0 {
			if !sawText {
				emit(out, []string{"model trả về rỗng (stop_reason: " + string(resp.StopReason) + ")"})
				return nil, false
			}
			return history, true
		}
		// Mọi tool_result của một lượt phải nằm chung 1 message user.
		history = append(history, anthropic.NewBetaUserMessage(results...))
	}
	emit(out, []string{fmt.Sprintf("dừng sau %d vòng tool — hỏi lại cụ thể hơn", maxToolRounds)})
	return history, true
}

func ask(client *anthropic.Client, oc *ollamaClient, spec modelSpec, history []anthropic.BetaMessageParam) (*anthropic.BetaMessage, error) {
	if spec.local {
		return oc.chat(history)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	// Haiku 4.5 không nhận output_config.effort — gửi lên là 400.
	var outputConfig anthropic.BetaOutputConfigParam
	if spec.supportsEffort {
		outputConfig.Effort = strongEffort
	}

	return client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(spec.id),
		MaxTokens:    maxTokens,
		OutputConfig: outputConfig,
		System: []anthropic.BetaTextBlockParam{{
			Text: systemPrompt,
		}},
		Messages: history,
		Tools:    toolDefs(),
		// Classifier của Opus 5 có thể từ chối. "default" để server tự chọn model
		// fallback theo loại refusal, khỏi phải pin tay rồi migrate về sau.
		Fallbacks: anthropic.BetaFallbacksParamUnion{
			OfDefault: constant.ValueOf[constant.Default](),
		},
		Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
}

// apiError bóc *anthropic.Error để nói rõ 401/429 thay vì dump cả body.
func apiError(err error) string {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return err.Error()
	}
	switch apiErr.StatusCode {
	case 401:
		return "401 chưa có credential — set ANTHROPIC_API_KEY hoặc chạy `ant auth login`"
	case 429:
		return "429 rate limit, thử lại sau"
	default:
		return fmt.Sprintf("%d %s", apiErr.StatusCode, apiErr.Error())
	}
}

func emit(out *bufio.Writer, lines []string) {
	for _, l := range lines {
		// Tách tiếp theo '\n': message lỗi của SDK là multi-line, mỗi dòng phải
		// có prefix riêng để supervisor log ra đúng.
		for _, part := range strings.Split(strings.TrimRight(l, "\n"), "\n") {
			fmt.Fprintf(out, "%s %s\n", prefix, part)
		}
	}
	// Flush ngay: supervisor đọc theo dòng, không chờ buffer đầy.
	_ = out.Flush()
}

// trimHistory giữ maxHistory message cuối, luôn bắt đầu bằng một lượt user
// "sạch". Không được bắt đầu bằng user chứa tool_result: khối tool_use tương
// ứng đã bị cắt mất, API sẽ trả 400.
func trimHistory(history []anthropic.BetaMessageParam) []anthropic.BetaMessageParam {
	if len(history) <= maxHistory {
		return history
	}
	trimmed := history[len(history)-maxHistory:]
	for len(trimmed) > 0 && !startsTurn(trimmed[0]) {
		trimmed = trimmed[1:]
	}
	return trimmed
}

func startsTurn(m anthropic.BetaMessageParam) bool {
	if m.Role != anthropic.BetaMessageParamRoleUser {
		return false
	}
	for _, block := range m.Content {
		if block.OfToolResult != nil {
			return false
		}
	}
	return true
}
