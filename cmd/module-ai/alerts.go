package main

// Tool `alert`: thêm/xoá/liệt kê pattern cảnh báo mà status bar của UI dùng để
// soi stream log.
//
// Khác 2 tool kia, tool này KHÔNG gọi worker nào — nó sửa file cấu hình ngay
// trong tiến trình ai-worker. File dùng chung với UI (lib/src/status_config.dart)
// nên định dạng là hợp đồng: {"patterns":[{"name":..,"regex":..}]}.
//
// `alert add`/`alert rm` là lệnh GHI: phải qua approver như mọi lệnh ghi khác.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	toolAlert = "alert"

	alertFileEnvVar = "K8SC_ALERT_PATTERNS" // đè đường dẫn, dùng chung với UI
	alertDirName    = ".k8s-commander"
	alertFileName   = "alert-patterns.json"

	// Trùng maxCustomPatterns bên Dart: UI match từng regex trên mọi dòng log.
	maxAlertPatterns = 32

	// Regex quá dài thường là dấu hiệu model dán cả output vào; chặn sớm.
	maxAlertRegexLen = 200

	alertVerbList = "list"
	alertVerbAdd  = "add"
	alertVerbRm   = "rm"
)

type alertPattern struct {
	Name  string `json:"name"`
	Regex string `json:"regex"`
}

type alertFile struct {
	Patterns []alertPattern `json:"patterns"`
}

func alertPath() string {
	if p := os.Getenv(alertFileEnvVar); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return alertFileName
	}
	return filepath.Join(home, alertDirName, alertFileName)
}

// loadAlertFile đọc file. Chưa có file = chưa có pattern nào, không phải lỗi.
func loadAlertFile(path string) (alertFile, error) {
	var f alertFile
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("file %s không phải JSON hợp lệ: %w", path, err)
	}
	return f, nil
}

func saveAlertFile(path string, f alertFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runAlert xử lý 1 lệnh alert, trả về các dòng in cho model và người dùng.
func runAlert(command string) []string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return []string{alertUsage()}
	}
	// Cho phép cả "alert list" và "list".
	if strings.EqualFold(fields[0], toolAlert) {
		fields = fields[1:]
		if len(fields) == 0 {
			return []string{alertUsage()}
		}
	}

	path := alertPath()
	switch strings.ToLower(fields[0]) {
	case alertVerbList:
		return alertList(path)
	case alertVerbAdd:
		// add <name> <regex...>: regex có thể chứa dấu cách nên gom phần còn lại.
		if len(fields) < 3 {
			return []string{"alert add: cần <tên> <regex>"}
		}
		return alertAdd(path, fields[1], strings.Join(fields[2:], " "))
	case alertVerbRm:
		if len(fields) < 2 {
			return []string{"alert rm: cần <tên>"}
		}
		return alertRm(path, fields[1])
	default:
		return []string{"alert: không hiểu " + fields[0], alertUsage()}
	}
}

func alertUsage() string {
	return "alert list | alert add <tên> <regex> | alert rm <tên>"
}

func alertList(path string) []string {
	f, err := loadAlertFile(path)
	if err != nil {
		return []string{"lỗi đọc " + path + ": " + err.Error()}
	}
	if len(f.Patterns) == 0 {
		return []string{"chưa có pattern cảnh báo nào (" + path + ")"}
	}
	out := []string{fmt.Sprintf("%d pattern (%s)", len(f.Patterns), path)}
	for _, p := range f.Patterns {
		out = append(out, "  "+p.Name+" -> "+p.Regex)
	}
	return out
}

func alertAdd(path, name, regex string) []string {
	if len(regex) > maxAlertRegexLen {
		return []string{fmt.Sprintf("regex dài %d ký tự, trần %d", len(regex), maxAlertRegexLen)}
	}
	// Biên dịch thử: pattern sai cú pháp mà ghi vào là UI phải tự loại lúc nạp.
	if _, err := regexp.Compile(regex); err != nil {
		return []string{"regex sai: " + err.Error()}
	}

	f, err := loadAlertFile(path)
	if err != nil {
		return []string{"lỗi đọc " + path + ": " + err.Error()}
	}
	for i, p := range f.Patterns {
		if p.Name == name {
			f.Patterns[i].Regex = regex
			if err := saveAlertFile(path, f); err != nil {
				return []string{"lỗi ghi " + path + ": " + err.Error()}
			}
			return []string{"đã cập nhật pattern " + name + " -> " + regex, "ghi vào " + path}
		}
	}
	if len(f.Patterns) >= maxAlertPatterns {
		return []string{fmt.Sprintf("đã có %d pattern, trần %d — xoá bớt bằng `alert rm`",
			len(f.Patterns), maxAlertPatterns)}
	}
	f.Patterns = append(f.Patterns, alertPattern{Name: name, Regex: regex})
	sort.Slice(f.Patterns, func(i, j int) bool { return f.Patterns[i].Name < f.Patterns[j].Name })
	if err := saveAlertFile(path, f); err != nil {
		return []string{"lỗi ghi " + path + ": " + err.Error()}
	}
	return []string{"đã thêm pattern " + name + " -> " + regex, "ghi vào " + path}
}

func alertRm(path, name string) []string {
	f, err := loadAlertFile(path)
	if err != nil {
		return []string{"lỗi đọc " + path + ": " + err.Error()}
	}
	kept := make([]alertPattern, 0, len(f.Patterns))
	for _, p := range f.Patterns {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(f.Patterns) {
		return []string{"không có pattern tên " + name}
	}
	f.Patterns = kept
	if err := saveAlertFile(path, f); err != nil {
		return []string{"lỗi ghi " + path + ": " + err.Error()}
	}
	return []string{"đã xoá pattern " + name, "ghi vào " + path}
}

// alertReadOnly: chỉ `list`/`help` là lệnh đọc; còn lại sửa file nên phải duyệt.
func alertReadOnly(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	if fields[0] == toolAlert {
		return len(fields) > 1 && alertReadOnly(fields[1:])
	}
	switch fields[0] {
	case alertVerbList, "ls", "help":
		return true
	}
	return false
}
