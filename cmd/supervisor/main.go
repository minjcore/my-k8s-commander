// Orchestrator: entry cho FFI. Export StartCore(paths), GetUpdate (pointer buffer DiffStream), GetLogs (bytes log đẩy lên Terminal Flutter).
package main

/*
#cgo CFLAGS: -O2
#include <stdlib.h>
*/
import "C"

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"unsafe"

	"my-k8s-commander/internal/supervisor"
	"my-k8s-commander/pkg/common"
)

var (
	sup           *supervisor.Supervisor
	diffStreamBuf bytes.Buffer
	logBuf        bytes.Buffer
	bufMu         sync.Mutex

	updateSnapshot  [1 << 20]byte
	updateLen       int
	logSnapshot     [1 << 19]byte
	logLen          int
	modulesSnapshot [4096]byte
)

const consoleWorkerName = "console-worker"

type orchestratorLogWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
	sup *supervisor.Supervisor
}

func (w *orchestratorLogWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err = w.buf.Write(p)
	if w.sup != nil {
		_ = w.sup.WriteToModule(consoleWorkerName, p)
	}
	return n, err
}

//export StartCore
func StartCore(paths *C.char) C.int {
	path := "./modules"
	if paths != nil {
		path = C.GoString(paths)
		if path == "" {
			path = "./modules"
		}
	}
	logWriter := &orchestratorLogWriter{mu: &bufMu, buf: &logBuf, sup: nil}
	sup = supervisor.New(path, logWriter)
	sup.SetEchoSink(consoleWorkerName)
	logWriter.sup = sup
	if err := sup.StartAll(); err != nil {
		bufMu.Lock()
		logBuf.WriteString(common.LogPrefix + " -> [Supervisor]: start failed: " + err.Error() + "\n")
		bufMu.Unlock()
		return -1
	}
	return 0
}

//export SendToModule
// SendToModule đẩy 1 dòng lệnh vào stdin của module. Trả 0 nếu ghi được,
// -1 nếu core chưa start / tham số nil, -2 nếu module không chạy.
func SendToModule(name *C.char, line *C.char) C.int {
	if sup == nil || name == nil || line == nil {
		return -1
	}
	moduleName := C.GoString(name)
	if err := sup.WriteToModule(moduleName, []byte(C.GoString(line)+"\n")); err != nil {
		if errors.Is(err, supervisor.ErrModuleNotRunning) {
			return -2
		}
		return -1
	}
	return 0
}

//export ListModules
// ListModules trả về tên các module đang chạy, phân cách bằng ','. Buffer tĩnh,
// hợp lệ tới lần gọi tiếp theo; Dart phải copy ra String ngay.
func ListModules() *C.char {
	if sup == nil {
		return nil
	}
	names := strings.Join(sup.ModuleNames(), ",")
	n := copy(modulesSnapshot[:len(modulesSnapshot)-1], names)
	modulesSnapshot[n] = 0
	return (*C.char)(unsafe.Pointer(&modulesSnapshot[0]))
}

//export GetUpdate
func GetUpdate() *C.uchar {
	bufMu.Lock()
	defer bufMu.Unlock()
	data := diffStreamBuf.Bytes()
	n := len(data)
	if n > len(updateSnapshot) {
		n = len(updateSnapshot)
	}
	if n > 0 {
		copy(updateSnapshot[:], data[:n])
	}
	updateLen = n
	if n == 0 {
		return nil
	}
	return (*C.uchar)(unsafe.Pointer(&updateSnapshot[0]))
}

//export GetUpdateSize
func GetUpdateSize() C.int {
	bufMu.Lock()
	defer bufMu.Unlock()
	return C.int(updateLen)
}

//export AppendDiffStream
func AppendDiffStream(ptr *C.uchar, n C.int) {
	if ptr == nil || n <= 0 {
		return
	}
	bufMu.Lock()
	defer bufMu.Unlock()
	data := C.GoBytes(unsafe.Pointer(ptr), n)
	diffStreamBuf.Write(data)
}

//export GetLogs
func GetLogs() *C.uchar {
	bufMu.Lock()
	defer bufMu.Unlock()
	n := logBuf.Len()
	if n > len(logSnapshot) {
		n = len(logSnapshot)
	}
	if n > 0 {
		// Next(n) rút đúng n byte đã copy — phần dư ở lại cho lần poll sau.
		// (Reset() cả buffer sẽ làm mất log khi vượt 512KB.)
		copy(logSnapshot[:], logBuf.Next(n))
	}
	logLen = n
	if n == 0 {
		return nil
	}
	return (*C.uchar)(unsafe.Pointer(&logSnapshot[0]))
}

//export GetLogsSize
func GetLogsSize() C.int {
	bufMu.Lock()
	defer bufMu.Unlock()
	return C.int(logLen)
}

//export StopCore
func StopCore() {
	if sup != nil {
		sup.StopAll()
		sup = nil
	}
}

func main() {}
