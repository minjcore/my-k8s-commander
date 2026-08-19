// Module Console: Logger/Terminal nội bộ. Nhận log từ Supervisor qua Stdin (in màu ANSI), gửi lệnh người dùng qua Stdout (MsgConsoleInput).
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"my-k8s-commander/pkg/common"
)

// ANSI màu theo nguồn log (đơn giản: prefix [module] -> màu)
const (
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiCyan   = "\033[36m"
	ansiReset  = "\033[0m"
)

func colorForSource(source string) string {
	switch {
	case strings.Contains(source, "ai-worker"), strings.Contains(source, "AI"):
		return ansiCyan
	case strings.Contains(source, "k8s-worker"), strings.Contains(source, "K8S"):
		return ansiGreen
	case strings.Contains(source, "Supervisor"):
		return ansiYellow
	case strings.Contains(source, "stderr"), strings.Contains(source, "ERROR"):
		return ansiRed
	default:
		return ansiGreen
	}
}

func main() {
	// Stdin: log từ Supervisor (format [Supervisor] -> [module]: message)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		source := "log"
		if idx := strings.Index(line, "-> ["); idx >= 0 {
			if end := strings.Index(line[idx+4:], "]"); end >= 0 {
				source = line[idx+4 : idx+4+end]
			}
		}
		clr := colorForSource(source)
		fmt.Fprintf(os.Stdout, "%s%s%s\n", clr, line, ansiReset)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "[console-worker] %v\n", err)
	}
}

// sendUIAction gửi lệnh người dùng lên Supervisor qua Stdout (Packet Type = MsgUIAction).
// Gọi khi có input từ bàn phím (standalone TTY) hoặc từ Flutter.
func sendUIAction(payload []byte) {
	p := &common.Packet{Type: common.MsgUIAction, Payload: payload}
	_, _ = os.Stdout.Write(p.Encode())
}
