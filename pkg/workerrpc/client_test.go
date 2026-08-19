package workerrpc

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-k8s-commander/pkg/common"
)

// TestMain kiêm luôn vai worker giả: khi K8SC_FAKE_WORKER=1 thì binary test
// chạy vòng lặp stdin/stdout đúng protocol thay vì chạy test.
func TestMain(m *testing.M) {
	if os.Getenv("K8SC_FAKE_WORKER") == "1" {
		fakeWorker()
		return
	}
	os.Exit(m.Run())
}

func fakeWorker() {
	out := bufio.NewWriter(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		switch line := strings.TrimSpace(sc.Text()); line {
		case "slow":
			time.Sleep(30 * time.Second) // không bao giờ tới sentinel
		case "die":
			os.Exit(1)
		case "flood":
			for i := 0; i < DefaultMaxLines+50; i++ {
				fmt.Fprintf(out, "[k8s-worker] dòng %d\n", i)
			}
		default:
			fmt.Fprintf(out, "[k8s-worker] echo: %s\n", line)
		}
		fmt.Fprintln(out, common.RPCDone)
		_ = out.Flush()
	}
}

// fakeModules dựng thư mục modules/ giả có k8s-worker trỏ về binary test.
func fakeModules(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(dir, "k8s-worker")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("K8SC_MODULES_DIR", dir)
	t.Setenv("K8SC_FAKE_WORKER", "1") // worker con thừa kế env của test
}

func TestCall(t *testing.T) {
	fakeModules(t)
	p := NewPool("[test]")
	defer p.StopAll()

	// Prefix "[k8s-worker] " phải được bóc trước khi trả cho caller.
	lines, err := p.Call("k8s-worker", "get pods")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(lines) != 1 || lines[0] != "echo: get pods" {
		t.Fatalf("nhận %q", lines)
	}

	// Lệnh thứ hai dùng lại đúng tiến trình đó, không spawn thêm.
	if lines, err = p.Call("k8s-worker", "get nodes"); err != nil || lines[0] != "echo: get nodes" {
		t.Fatalf("call lần 2: %v / %q", err, lines)
	}
}

func TestCallTruncatesOutput(t *testing.T) {
	fakeModules(t)
	p := NewPool("[test]")
	defer p.StopAll()

	lines, err := p.Call("k8s-worker", "flood")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(lines) != DefaultMaxLines+1 {
		t.Fatalf("muốn %d dòng + 1 ghi chú, nhận %d", DefaultMaxLines, len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "cắt bớt") {
		t.Fatalf("thiếu ghi chú cắt bớt: %q", lines[len(lines)-1])
	}
}

func TestCallTimeoutThenRecovers(t *testing.T) {
	fakeModules(t)
	p := NewPool("[test]")
	defer p.StopAll()
	p.Timeout = 300 * time.Millisecond

	if _, err := p.Call("k8s-worker", "slow"); err == nil {
		t.Fatal("lệnh treo phải trả lỗi timeout")
	}
	// Sau timeout worker bị kill; lệnh sau phải spawn lại và chạy sạch.
	p.Timeout = DefaultTimeout
	lines, err := p.Call("k8s-worker", "get pods")
	if err != nil {
		t.Fatalf("call sau timeout: %v", err)
	}
	if len(lines) != 1 || lines[0] != "echo: get pods" {
		t.Fatalf("output còn rác từ lệnh trước: %q", lines)
	}
}

func TestCallWorkerDies(t *testing.T) {
	fakeModules(t)
	p := NewPool("[test]")
	defer p.StopAll()

	if _, err := p.Call("k8s-worker", "die"); err == nil {
		t.Fatal("worker thoát giữa chừng phải trả lỗi")
	}
	if lines, err := p.Call("k8s-worker", "get pods"); err != nil || lines[0] != "echo: get pods" {
		t.Fatalf("không tự spawn lại: %v / %q", err, lines)
	}
}

func TestCallMissingBinary(t *testing.T) {
	t.Setenv("K8SC_MODULES_DIR", t.TempDir())
	p := NewPool("[test]")
	defer p.StopAll()

	_, err := p.Call("server-worker", "list")
	if err == nil || !strings.Contains(err.Error(), "không thấy server-worker") {
		t.Fatalf("muốn lỗi thiếu binary, nhận %v", err)
	}
}
