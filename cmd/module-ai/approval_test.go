package main

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
)

func discard() *bufio.Writer { return bufio.NewWriter(io.Discard) }

const writeCmd = "server run prod-1 systemctl restart nginx"

func TestApproverReadOnlyKhongHoi(t *testing.T) {
	// Kênh rỗng: nếu lệnh đọc mà vẫn đi hỏi thì test sẽ treo tới timeout.
	ap := &approver{mode: approvalAsk, input: make(chan string), wait: time.Second}
	ok, reason := ap.allow(discard(), toolServer, serverWorker, "server list")
	if !ok || reason != "" {
		t.Fatalf("lệnh đọc phải chạy thẳng: %v / %q", ok, reason)
	}
}

func TestApproverAsk(t *testing.T) {
	cases := []struct {
		reply string
		want  bool
	}{
		{"yes", true}, {"y", true}, {"OK", true}, {"có", true},
		{"no", false}, {"n", false}, {"", false},
		// Câu hỏi mới trong lúc chờ duyệt = từ chối, không phải "chạy đi".
		{"pod nào đang crash?", false},
	}
	for _, c := range cases {
		input := make(chan string, 1)
		input <- c.reply
		ap := &approver{mode: approvalAsk, input: input, wait: 2 * time.Second}

		ok, reason := ap.allow(discard(), toolServer, serverWorker, writeCmd)
		if ok != c.want {
			t.Errorf("trả lời %q -> %v, muốn %v (%s)", c.reply, ok, c.want, reason)
		}
		if reason == "" {
			t.Errorf("trả lời %q: thiếu lý do", c.reply)
		}
	}
}

func TestApproverTimeoutLaTuChoi(t *testing.T) {
	ap := &approver{mode: approvalAsk, input: make(chan string), wait: 150 * time.Millisecond}
	start := time.Now()
	ok, reason := ap.allow(discard(), toolServer, serverWorker, writeCmd)
	if ok {
		t.Fatal("hết giờ phải là từ chối")
	}
	if !strings.Contains(reason, "không ai duyệt") {
		t.Errorf("lý do: %q", reason)
	}
	if time.Since(start) > time.Second {
		t.Errorf("chờ quá lâu: %s", time.Since(start))
	}
}

// stdin đóng (supervisor kill, hoặc chạy headless): không được treo.
func TestApproverStdinDong(t *testing.T) {
	input := make(chan string)
	close(input)
	ap := &approver{mode: approvalAsk, input: input, wait: time.Minute}

	ok, reason := ap.allow(discard(), toolServer, serverWorker, writeCmd)
	if ok || !strings.Contains(reason, "stdin đã đóng") {
		t.Fatalf("%v / %q", ok, reason)
	}
}

func TestApproverAutoVaDeny(t *testing.T) {
	// Kênh rỗng: auto/deny không được đi hỏi.
	auto := &approver{mode: approvalAuto, input: make(chan string), wait: time.Second}
	if ok, _ := auto.allow(discard(), toolServer, serverWorker, writeCmd); !ok {
		t.Error("auto phải cho chạy")
	}
	deny := &approver{mode: approvalDeny, input: make(chan string), wait: time.Second}
	if ok, reason := deny.allow(discard(), toolServer, serverWorker, writeCmd); ok {
		t.Errorf("deny phải chặn (%s)", reason)
	}
}

func TestNewApproverMacDinhLaAsk(t *testing.T) {
	for _, v := range []string{"", "linh tinh", "ASK"} {
		t.Setenv(approvalEnvVar, v)
		if got := newApprover(nil).mode; got != approvalAsk {
			t.Errorf("%s=%q -> %q, muốn ask", approvalEnvVar, v, got)
		}
	}
	t.Setenv(approvalEnvVar, "AUTO")
	if got := newApprover(nil).mode; got != approvalAuto {
		t.Errorf("AUTO -> %q", got)
	}
}
