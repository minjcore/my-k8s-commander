package main

// Duyệt lệnh ghi.
//
// Lệnh đọc chạy thẳng. Lệnh thay đổi hệ thống (`server run`, `cluster add/rm`,
// `cluster use --persist`, `server add/rm/trust`) phải được người dùng gõ `ai yes`
// mới chạy — thay vì một biến môi trường mở toang tất cả.
//
// Người dùng gõ gì khác (kể cả câu hỏi mới) = từ chối; nói rõ trong lời nhắc.
// Hết giờ cũng là từ chối, để chạy headless không treo mãi.

import (
	"bufio"
	"os"
	"strings"
	"time"
)

const approvalEnvVar = "K8SC_AI_APPROVAL"

// Chế độ duyệt.
const (
	approvalAsk  = "ask"  // mặc định: hỏi từng lệnh
	approvalAuto = "auto" // duyệt sẵn tất cả — chỉ dùng khi biết mình làm gì
	approvalDeny = "deny" // chặn hẳn, không hỏi (headless/cron)
)

const approvalWait = 2 * time.Minute

type approver struct {
	mode  string
	input <-chan string // chung kênh với vòng đọc stdin của main
	wait  time.Duration
}

func newApprover(input <-chan string) *approver {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(approvalEnvVar)))
	switch mode {
	case approvalAuto, approvalDeny, approvalAsk:
	default:
		mode = approvalAsk
	}
	return &approver{mode: mode, input: input, wait: approvalWait}
}

func (a *approver) describe() string {
	switch a.mode {
	case approvalAuto:
		return "lệnh ghi: duyệt sẵn (" + approvalEnvVar + "=auto)"
	case approvalDeny:
		return "lệnh ghi: chặn (" + approvalEnvVar + "=deny)"
	default:
		return "lệnh ghi: hỏi trước, gõ `ai yes` để duyệt"
	}
}

// allow trả về (cho chạy, lý do để in/gửi lại cho model).
func (a *approver) allow(out *bufio.Writer, toolName, worker, command string) (bool, string) {
	if readOnly(toolName, command) {
		return true, ""
	}
	switch a.mode {
	case approvalAuto:
		return true, "lệnh ghi, chạy vì " + approvalEnvVar + "=auto"
	case approvalDeny:
		return false, "lệnh ghi bị chặn (" + approvalEnvVar + "=deny)"
	}

	emit(out, []string{
		"  ⚠ lệnh này thay đổi hệ thống: " + worker + " " + command,
		"    gõ `ai yes` để chạy — gõ gì khác (hoặc để im " + a.wait.String() + ") là bỏ qua",
	})
	select {
	case line, ok := <-a.input:
		if !ok {
			return false, "stdin đã đóng, coi như từ chối"
		}
		if isYes(line) {
			return true, "người dùng đã duyệt"
		}
		return false, "người dùng từ chối (gõ: " + strings.TrimSpace(line) + ")"
	case <-time.After(a.wait):
		return false, "hết " + a.wait.String() + " không ai duyệt"
	}
}

func isYes(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "yes", "y", "ok", "duyệt", "co", "có":
		return true
	}
	return false
}
