package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

func tempMeter(t *testing.T, now time.Time) *meter {
	t.Helper()
	t.Setenv("K8SC_AI_USAGE_FILE", filepath.Join(t.TempDir(), "usage.json"))
	return loadMeter(now)
}

func TestMeterCostMath(t *testing.T) {
	m := tempMeter(t, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC))
	haiku := modelTiers["haiku"] // $1 in / $5 out trên 1M token

	// 1M in + 1M out = $1 + $5. Cache đọc 1M = 0.1 * $1.
	got := m.record(haiku, anthropic.BetaUsage{
		InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadInputTokens: 1_000_000,
	})
	if want := 6.1; got.cost < want-1e-9 || got.cost > want+1e-9 {
		t.Errorf("cost = %v, muốn %v", got.cost, want)
	}
	if m.state.Cost != got.cost {
		t.Errorf("tháng chưa cộng dồn: %v vs %v", m.state.Cost, got.cost)
	}

	// Ghi cache đắt 1.25x giá input.
	m2 := tempMeter(t, time.Now())
	w := m2.record(haiku, anthropic.BetaUsage{CacheCreationInputTokens: 1_000_000})
	if want := 1.25; w.cost < want-1e-9 || w.cost > want+1e-9 {
		t.Errorf("cache write = %v, muốn %v", w.cost, want)
	}
}

func TestMeterSangThangThiVeKhong(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	t.Setenv("K8SC_AI_USAGE_FILE", path)

	thang8 := time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC)
	m := loadMeter(thang8)
	m.record(modelTiers["opus"], anthropic.BetaUsage{InputTokens: 500_000})
	m.save()

	// Cùng tháng: đọc lại phải thấy số cũ.
	if again := loadMeter(thang8); again.state.Cost == 0 {
		t.Fatal("cùng tháng mà mất số đã tiêu")
	}
	// Sang tháng: về 0.
	thang9 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	next := loadMeter(thang9)
	if next.state.Cost != 0 || next.state.Month != "2026-09" {
		t.Fatalf("sang tháng phải reset, nhận %+v", next.state)
	}
}

func TestMeterChanKhiChamTran(t *testing.T) {
	t.Setenv("K8SC_AI_BUDGET", "0.10")
	m := tempMeter(t, time.Now())
	if m.exceeded() {
		t.Fatal("mới bắt đầu chưa được chặn")
	}
	// $5/MTok output * 40k = $0.20 > trần 0.10
	m.record(modelTiers["haiku"], anthropic.BetaUsage{OutputTokens: 40_000})
	if !m.exceeded() {
		t.Fatalf("phải chặn khi vượt trần, cost = %v", m.state.Cost)
	}
	if !strings.Contains(m.blockedLine(), budgetEnvVar) {
		t.Error("thông báo chặn phải chỉ cách nâng trần")
	}

	// budget = 0 nghĩa là không chặn.
	t.Setenv("K8SC_AI_BUDGET", "0")
	free := tempMeter(t, time.Now())
	free.record(modelTiers["opus"], anthropic.BetaUsage{OutputTokens: 10_000_000})
	if free.exceeded() {
		t.Error("budget=0 thì không được chặn")
	}
}

func TestTierFallback(t *testing.T) {
	t.Setenv(modelEnvVar, "khong-co-tier-nay")
	if got := tier(modelEnvVar, defaultTier); got.id != "claude-haiku-4-5" {
		t.Errorf("tier lạ phải rơi về mặc định, nhận %q", got.id)
	}
	t.Setenv(modelEnvVar, "sonnet")
	if got := tier(modelEnvVar, defaultTier); got.id != "claude-sonnet-5" {
		t.Errorf("nhận %q", got.id)
	}
}

// Haiku 4.5 trả 400 nếu gửi output_config.effort — bảng giá phải phản ánh đúng.
func TestChiModelManhMoiCoEffort(t *testing.T) {
	if modelTiers["haiku"].supportsEffort {
		t.Error("haiku không hỗ trợ effort")
	}
	for _, name := range []string{"sonnet", "opus"} {
		if !modelTiers[name].supportsEffort {
			t.Errorf("%s phải hỗ trợ effort", name)
		}
	}
}

func TestDinhDangHienThi(t *testing.T) {
	if got := tokens(999); got != "999" {
		t.Errorf("tokens(999) = %q", got)
	}
	if got := tokens(1234); got != "1.2k" {
		t.Errorf("tokens(1234) = %q", got)
	}
	// Câu rẻ vẫn phải thấy được, không phải $0.00.
	if got := money(0.004); got != "<$0.01" {
		t.Errorf("money(0.004) = %q", got)
	}
	if got := money(0); got != "$0.00" {
		t.Errorf("money(0) = %q", got)
	}

	m := tempMeter(t, time.Now())
	t.Setenv("K8SC_AI_BUDGET", "5")
	line := m.line(modelTiers["haiku"], turnUsage{in: 1200, out: 340, cost: 0.0026})
	for _, want := range []string{"haiku", "1.2k in", "340 out", "<$0.01"} {
		if !strings.Contains(line, want) {
			t.Errorf("dòng %q thiếu %q", line, want)
		}
	}
}
