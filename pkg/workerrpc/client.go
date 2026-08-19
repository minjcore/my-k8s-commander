// Package workerrpc: gọi một worker khác như một hàm.
//
// Các worker chỉ nối stdin/stdout với supervisor, không có kênh ngang nào giữa
// chúng. Package này cho một worker tự spawn bản sao worker khác (lazy) và nói
// chuyện qua pipe, dùng sentinel common.RPCDone để biết câu trả lời đã hết —
// thay vì đoán bằng timeout.
//
// Đánh đổi: worker con là tiến trình riêng, tách khỏi worker mà UI đang dùng,
// nên trạng thái trong tiến trình (vd `use <context>` của k8s-worker) không
// dùng chung. Không cần dọn khi tiến trình cha bị kill: cha giữ đầu ghi duy
// nhất của pipe stdin, cha chết thì con thấy EOF và tự thoát.
package workerrpc

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"my-k8s-commander/pkg/common"
)

const (
	// k8s qua exec credential plugin (GKE/EKS) và SSH đều có thể chậm.
	DefaultTimeout = 90 * time.Second
	// Cắt output: một lệnh lỡ tay không được nhấn chìm caller.
	DefaultMaxLines = 200
)

// Pool giữ các Client theo tên worker.
type Pool struct {
	// Timeout cho 1 lệnh; MaxLines là số dòng giữ lại. Đổi trước khi gọi Call.
	Timeout   time.Duration
	MaxLines  int
	LogPrefix string // prefix khi forward stderr của worker con

	mu     sync.Mutex
	byName map[string]*Client
}

func NewPool(logPrefix string) *Pool {
	return &Pool{
		Timeout:   DefaultTimeout,
		MaxLines:  DefaultMaxLines,
		LogPrefix: logPrefix,
		byName:    make(map[string]*Client),
	}
}

func (p *Pool) Get(name string) *Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.byName[name]; ok {
		return c
	}
	c := &Client{name: name, pool: p}
	p.byName[name] = c
	return c
}

// Call là lối tắt cho Get(name).Call(command).
func (p *Pool) Call(name, command string) ([]string, error) { return p.Get(name).Call(command) }

func (p *Pool) StopAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.byName {
		c.mu.Lock()
		c.killLocked()
		c.mu.Unlock()
	}
}

type Client struct {
	name string
	pool *Pool

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
}

// Call gửi 1 lệnh và đọc tới sentinel. Tuần tự hoá theo từng worker: 1 lệnh
// xong mới tới lệnh sau, nên không cần correlation id.
func (c *Client) Call(command string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLocked(); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(c.stdin, command+"\n"); err != nil {
		c.killLocked()
		return nil, fmt.Errorf("ghi vào %s lỗi: %w", c.name, err)
	}

	lines := c.lines
	deadline := time.After(c.pool.Timeout)
	linePrefix := "[" + c.name + "] "
	var out []string
	truncated := 0
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				c.killLocked() // worker chết giữa chừng: dọn để lần sau spawn lại
				return out, fmt.Errorf("%s đã thoát giữa chừng", c.name)
			}
			if line == common.RPCDone {
				if truncated > 0 {
					out = append(out, fmt.Sprintf("... (cắt bớt %d dòng)", truncated))
				}
				return out, nil
			}
			if len(out) >= c.pool.MaxLines {
				truncated++ // vẫn phải đọc hết tới sentinel, chỉ không giữ lại
				continue
			}
			out = append(out, strings.TrimPrefix(line, linePrefix))
		case <-deadline:
			// Kill luôn: output dở dang còn trong pipe sẽ làm bẩn lệnh kế tiếp.
			c.killLocked()
			return out, fmt.Errorf("%s không trả lời sau %s", c.name, c.pool.Timeout)
		}
	}
}

func (c *Client) ensureLocked() error {
	if c.stdin != nil {
		return nil
	}
	path, err := WorkerPath(c.name)
	if err != nil {
		return err
	}
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = append(os.Environ(), common.RPCEnvVar+"=1")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("chạy %s lỗi: %w", path, err)
	}

	c.cmd, c.stdin = cmd, stdin
	c.lines = make(chan string, 256)
	go readLines(stdout, c.lines)
	go forwardStderr(c.pool.LogPrefix, c.name, stderr)
	go func() { _ = cmd.Wait() }() // reap; Call phát hiện chết qua channel đóng
	return nil
}

func (c *Client) killLocked() {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.cmd, c.stdin, c.lines = nil, nil, nil
}

func readLines(r io.Reader, out chan<- string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		out <- sc.Text()
	}
	close(out)
}

// forwardStderr đẩy stderr của worker con lên stderr của mình — supervisor sẽ
// log lại, nên lỗi của worker con không biến mất.
func forwardStderr(logPrefix, name string, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fmt.Fprintf(os.Stderr, "%s [%s] %s\n", logPrefix, name, sc.Text())
	}
}

// WorkerPath tìm binary anh em cùng thư mục modules/. $K8SC_MODULES_DIR ghi đè —
// dùng khi chạy worker ngoài modules/ và trong test.
func WorkerPath(name string) (string, error) {
	dir := os.Getenv("K8SC_MODULES_DIR")
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		dir = filepath.Dir(exe)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("không thấy %s trong %s — chạy `make build`", name, dir)
	}
	return path, nil
}
