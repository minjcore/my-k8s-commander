package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func alertFileFor(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), alertFileName)
	t.Setenv(alertFileEnvVar, path)
	return path
}

func readPatterns(t *testing.T, path string) []alertPattern {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f alertFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("file ghi ra không phải JSON hợp lệ: %v\n%s", err, data)
	}
	return f.Patterns
}

func TestAlertAddRoiList(t *testing.T) {
	path := alertFileFor(t)

	out := strings.Join(runAlert("alert add oom OOMKilled"), "\n")
	if !strings.Contains(out, "đã thêm") || !strings.Contains(out, path) {
		t.Fatalf("output = %q", out)
	}
	got := readPatterns(t, path)
	if len(got) != 1 || got[0].Name != "oom" || got[0].Regex != "OOMKilled" {
		t.Fatalf("patterns = %+v", got)
	}

	list := strings.Join(runAlert("list"), "\n")
	if !strings.Contains(list, "oom -> OOMKilled") {
		t.Fatalf("list = %q", list)
	}
}

// Regex có dấu cách phải giữ nguyên, không bị cắt ở field đầu.
func TestAlertRegexCoDauCach(t *testing.T) {
	path := alertFileFor(t)
	runAlert(`alert add disk-full no space left on device.*pod=(\S+)`)
	got := readPatterns(t, path)
	if len(got) != 1 || got[0].Regex != `no space left on device.*pod=(\S+)` {
		t.Fatalf("patterns = %+v", got)
	}
}

func TestAlertAddTrungTenLaCapNhat(t *testing.T) {
	path := alertFileFor(t)
	runAlert("alert add oom OOMKilled")
	out := strings.Join(runAlert("alert add oom Killed.*memory"), "\n")
	if !strings.Contains(out, "đã cập nhật") {
		t.Fatalf("output = %q", out)
	}
	got := readPatterns(t, path)
	if len(got) != 1 || got[0].Regex != "Killed.*memory" {
		t.Fatalf("patterns = %+v", got)
	}
}

func TestAlertRegexSaiKhongGhi(t *testing.T) {
	path := alertFileFor(t)
	out := strings.Join(runAlert("alert add xau ([unclosed"), "\n")
	if !strings.Contains(out, "regex sai") {
		t.Fatalf("output = %q", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("regex sai mà vẫn tạo file")
	}
}

func TestAlertRegexQuaDai(t *testing.T) {
	alertFileFor(t)
	long := strings.Repeat("a", maxAlertRegexLen+1)
	if out := strings.Join(runAlert("alert add dai "+long), "\n"); !strings.Contains(out, "trần") {
		t.Fatalf("output = %q", out)
	}
}

func TestAlertTranSoPattern(t *testing.T) {
	alertFileFor(t)
	for i := 0; i < maxAlertPatterns; i++ {
		runAlert("alert add p" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + " x")
	}
	out := strings.Join(runAlert("alert add themnua x"), "\n")
	if !strings.Contains(out, "trần") {
		t.Fatalf("output = %q", out)
	}
}

func TestAlertRm(t *testing.T) {
	path := alertFileFor(t)
	runAlert("alert add oom OOMKilled")
	runAlert("alert add crash CrashLoopBackOff")

	if out := strings.Join(runAlert("alert rm oom"), "\n"); !strings.Contains(out, "đã xoá") {
		t.Fatalf("output = %q", out)
	}
	got := readPatterns(t, path)
	if len(got) != 1 || got[0].Name != "crash" {
		t.Fatalf("patterns = %+v", got)
	}
	if out := strings.Join(runAlert("alert rm khong-co"), "\n"); !strings.Contains(out, "không có pattern") {
		t.Fatalf("output = %q", out)
	}
}

func TestAlertListKhiChuaCoFile(t *testing.T) {
	alertFileFor(t)
	if out := strings.Join(runAlert("alert list"), "\n"); !strings.Contains(out, "chưa có pattern") {
		t.Fatalf("output = %q", out)
	}
}

func TestAlertFileHongBaoLoi(t *testing.T) {
	path := alertFileFor(t)
	if err := os.WriteFile(path, []byte("{khong-phai-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := strings.Join(runAlert("alert list"), "\n"); !strings.Contains(out, "không phải JSON") {
		t.Fatalf("output = %q", out)
	}
}

// list là lệnh đọc; add/rm phải qua approver.
func TestAlertReadOnly(t *testing.T) {
	cases := map[string]bool{
		"alert list":            true,
		"list":                  true,
		"alert help":            true,
		"alert add oom OOMKill": false,
		"alert rm oom":          false,
		"alert":                 false,
	}
	for cmd, want := range cases {
		if got := readOnly(toolAlert, cmd); got != want {
			t.Fatalf("readOnly(%q) = %v, chờ %v", cmd, got, want)
		}
	}
}

// Model gửi kèm chữ "alert" hay không thì kết quả phải như nhau.
func TestAlertBoTienToTrungLap(t *testing.T) {
	path := alertFileFor(t)
	runAlert("alert add a X")
	runAlert("add b Y")
	got := readPatterns(t, path)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("patterns = %+v", got)
	}
}
