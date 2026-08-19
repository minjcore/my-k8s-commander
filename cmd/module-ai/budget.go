package main

// Đếm token, quy ra tiền, chặn khi chạm trần tháng.
//
// Công cụ này chạy trên ngân sách cố định nên chi phí phải nhìn thấy được: mỗi
// câu trả lời kèm 1 dòng token + số đã tiêu trong tháng. Chạm trần thì ngừng gọi
// API chứ không âm thầm tiêu tiếp.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// modelSpec: giá USD trên 1 triệu token, theo bảng giá Claude API.
// Đọc cache rẻ ~0.1x giá input, ghi cache đắt 1.25x.
type modelSpec struct {
	id       string
	inPrice  float64
	outPrice float64
	// Haiku 4.5 KHÔNG nhận output_config.effort — gửi lên là lỗi. Chỉ set effort
	// cho model nào thật sự hỗ trợ.
	supportsEffort bool
	// Model chạy trên ollama local: giá 0, không đụng vào trần tháng.
	local bool
}

var modelTiers = map[string]modelSpec{
	"haiku":  {id: "claude-haiku-4-5", inPrice: 1, outPrice: 5},
	"sonnet": {id: "claude-sonnet-5", inPrice: 3, outPrice: 15, supportsEffort: true},
	"opus":   {id: "claude-opus-5", inPrice: 5, outPrice: 25, supportsEffort: true},
}

const (
	modelEnvVar       = "K8SC_AI_MODEL"        // tier mặc định
	strongModelEnvVar = "K8SC_AI_MODEL_STRONG" // tier khi gõ `!`
	budgetEnvVar      = "K8SC_AI_BUDGET"       // USD/tháng, 0 = không chặn

	defaultTier       = "haiku"
	defaultStrongTier = "opus"
	defaultBudget     = 5.0
)

func tier(envVar, fallback string) modelSpec {
	name := os.Getenv(envVar)
	if spec, ok := modelTiers[name]; ok {
		return spec
	}
	return modelTiers[fallback]
}

// turnUsage: chi phí của 1 câu hỏi (cộng dồn qua các vòng tool).
type turnUsage struct {
	in, out, cacheRead int64
	cost               float64
}

func (t *turnUsage) add(o turnUsage) {
	t.in += o.in
	t.out += o.out
	t.cacheRead += o.cacheRead
	t.cost += o.cost
}

type monthState struct {
	Month string  `json:"month"` // "2026-08"
	In    int64   `json:"input_tokens"`
	Out   int64   `json:"output_tokens"`
	Cache int64   `json:"cache_read_tokens"`
	Cost  float64 `json:"cost_usd"`
	Asks  int     `json:"asks"`
}

type meter struct {
	path   string
	budget float64
	state  monthState
}

func meterPath() string {
	if p := os.Getenv("K8SC_AI_USAGE_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "usage.json"
	}
	return filepath.Join(home, ".k8s-commander", "usage.json")
}

func loadMeter(now time.Time) *meter {
	m := &meter{path: meterPath(), budget: defaultBudget}
	if raw := os.Getenv(budgetEnvVar); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			m.budget = v
		}
	}
	if data, err := os.ReadFile(m.path); err == nil {
		_ = json.Unmarshal(data, &m.state)
	}
	// Sang tháng thì bắt đầu lại từ 0.
	if month := now.Format("2006-01"); m.state.Month != month {
		m.state = monthState{Month: month}
	}
	return m
}

// record cộng usage của 1 lần gọi API vào tháng hiện tại, trả về chi phí lần đó.
// Model local không tốn tiền nên chỉ trả về token, không ghi vào sổ tháng.
func (m *meter) record(spec modelSpec, u anthropic.BetaUsage) turnUsage {
	if spec.local {
		return turnUsage{in: u.InputTokens, out: u.OutputTokens}
	}
	t := turnUsage{in: u.InputTokens, out: u.OutputTokens, cacheRead: u.CacheReadInputTokens}
	t.cost = float64(u.InputTokens)/1e6*spec.inPrice +
		float64(u.OutputTokens)/1e6*spec.outPrice +
		float64(u.CacheReadInputTokens)/1e6*spec.inPrice*0.1 +
		float64(u.CacheCreationInputTokens)/1e6*spec.inPrice*1.25

	m.state.In += t.in
	m.state.Out += t.out
	m.state.Cache += t.cacheRead
	m.state.Cost += t.cost
	return t
}

func (m *meter) exceeded() bool { return m.budget > 0 && m.state.Cost >= m.budget }

func (m *meter) save() {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, m.path)
}

// line: dòng tổng kết in sau mỗi câu trả lời.
func (m *meter) line(spec modelSpec, t turnUsage) string {
	if spec.local {
		return fmt.Sprintf("%s%s · %s in · %s out · miễn phí",
			spec.id, ollamaLabelSuffix, tokens(t.in), tokens(t.out))
	}
	s := fmt.Sprintf("%s · %s in", shortModel(spec.id), tokens(t.in))
	if t.cacheRead > 0 {
		s += fmt.Sprintf(" (%s cache)", tokens(t.cacheRead))
	}
	s += fmt.Sprintf(" · %s out · %s", tokens(t.out), money(t.cost))
	if m.budget > 0 {
		s += fmt.Sprintf(" · tháng này %s/%s", money(m.state.Cost), money(m.budget))
	} else {
		s += fmt.Sprintf(" · tháng này %s", money(m.state.Cost))
	}
	return s
}

func (m *meter) blockedLine() string {
	return fmt.Sprintf("đã tiêu %s / trần %s tháng này — không gọi API nữa. "+
		"Nâng trần bằng %s=<usd> (0 = bỏ chặn), hoặc chờ sang tháng.",
		money(m.state.Cost), money(m.budget), budgetEnvVar)
}

func shortModel(id string) string {
	switch id {
	case "claude-haiku-4-5":
		return "haiku"
	case "claude-sonnet-5":
		return "sonnet"
	case "claude-opus-5":
		return "opus"
	}
	return id
}

func tokens(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// money: dưới 1 cent vẫn phải thấy được, nếu không mọi câu đều hiện $0.00.
func money(v float64) string {
	if v > 0 && v < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", v)
}
