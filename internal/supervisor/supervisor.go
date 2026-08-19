// Package supervisor: quét modules/, chạy binary con, giao tiếp qua Pipe, self-healing.
package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"my-k8s-commander/pkg/common"
)

const restartDelay = 2 * time.Second

type Supervisor struct {
	modulesDir string
	logSink    io.Writer
	echoSink   string
	mu         sync.Mutex
	procs      map[string]*procState
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

type procState struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc
}

func New(modulesDir string, logSink io.Writer) *Supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{
		modulesDir: modulesDir,
		logSink:    logSink,
		procs:      make(map[string]*procState),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// SetEchoSink đánh dấu module chỉ render lại log (vd console-worker): stdout của nó
// KHÔNG được đưa vào logSink. Nếu đưa vào sẽ thành vòng lặp vô hạn —
// logSink ghi -> console-worker in ra stdout -> forwardLines log lại -> logSink ghi...
func (s *Supervisor) SetEchoSink(moduleName string) {
	s.mu.Lock()
	s.echoSink = moduleName
	s.mu.Unlock()
}

func (s *Supervisor) isEchoSink(moduleName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.echoSink != "" && s.echoSink == moduleName
}

func (s *Supervisor) log(moduleName, msg string) {
	common.Log(moduleName, msg)
	if s.logSink != nil {
		_, _ = s.logSink.Write([]byte(common.LogPrefix + " -> [" + moduleName + "]: " + msg + "\n"))
	}
}
func isExecutable(name string, mode os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return filepath.Ext(name) == ".exe"
	}
	return mode.IsRegular() && (mode&0111 != 0)
}

func (s *Supervisor) Discover() ([]string, error) {
	var out []string
	entries, err := os.ReadDir(s.modulesDir)
	if err != nil {
		if os.IsNotExist(err) {
			s.log("Supervisor", "dir "+s.modulesDir+" not found")
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, _ := e.Info()
		if info == nil || !isExecutable(e.Name(), info.Mode()) {
			continue
		}
		path, _ := filepath.Abs(filepath.Join(s.modulesDir, e.Name()))
		out = append(out, path)
	}
	return out, nil
}

func (s *Supervisor) StartAll() error {
	paths, err := s.Discover()
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		s.log("Supervisor", "no executable module found in "+s.modulesDir+" (run `make build`)")
		return nil
	}
	for _, path := range paths {
		name := filepath.Base(path)
		s.wg.Add(1)
		go s.run(name, path)
	}
	return nil
}

func (s *Supervisor) run(name, path string) {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		procCtx, procCancel := context.WithCancel(s.ctx)
		cmd := exec.CommandContext(procCtx, path)
		cmd.Dir = filepath.Dir(path)
		cmd.Env = os.Environ()

		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			s.log(name, "start: "+err.Error())
			procCancel() // không rò context mỗi vòng retry
			time.Sleep(restartDelay)
			continue
		}

		s.mu.Lock()
		s.procs[name] = &procState{name: name, cmd: cmd, stdin: stdin, cancel: procCancel}
		s.mu.Unlock()

		s.log(name, fmt.Sprintf("started (PID %d)", cmd.Process.Pid))
		go s.forwardLines(name, stdout, true)
		go s.forwardLines(name, stderr, false)

		_ = cmd.Wait()
		s.mu.Lock()
		delete(s.procs, name)
		s.mu.Unlock()
		s.log(name, "exited, restarting in "+restartDelay.String())
		procCancel()
		time.Sleep(restartDelay)
	}
}

func (s *Supervisor) forwardLines(moduleName string, r io.Reader, stdout bool) {
	echo := stdout && s.isEchoSink(moduleName)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if echo {
			// Vẫn phải đọc hết pipe để module không bị block, nhưng không log lại.
			continue
		}
		msg := sc.Text()
		if stdout {
			s.log(moduleName, msg)
		} else {
			s.log(moduleName, "stderr: "+msg)
		}
	}
}

func (s *Supervisor) StopAll() {
	s.cancel()
	s.mu.Lock()
	for _, p := range s.procs {
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		if p.cancel != nil {
			p.cancel()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.log("Supervisor", "all children stopped")
}

// ErrModuleNotRunning: module không tồn tại hoặc chưa chạy. Trả lỗi thay vì im lặng
// để caller (UI) không báo "đã gửi" khi thực ra không ai nhận.
var ErrModuleNotRunning = errors.New("module not running")

func (s *Supervisor) WriteToModule(moduleName string, data []byte) error {
	s.mu.Lock()
	p := s.procs[moduleName]
	s.mu.Unlock()
	if p == nil || p.stdin == nil {
		return fmt.Errorf("%s: %w", moduleName, ErrModuleNotRunning)
	}
	_, err := p.stdin.Write(data)
	return err
}

// ModuleNames trả về tên các module đang chạy (đã sort) để UI biết gửi lệnh cho ai.
func (s *Supervisor) ModuleNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.procs))
	for name := range s.procs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
