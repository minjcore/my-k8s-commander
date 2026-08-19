package main

// Bề mặt helm: đủ để "cài Airbyte lên cụm" mà không mở cửa cho lệnh tuỳ ý.
//
// Ba nguyên tắc:
//
//  1. ALLOWLIST verb, không phải passthrough shell. Verb lạ bị chặn.
//  2. Chạy qua exec.Command với args tách rời — không qua shell, nên không có
//     chuyện chèn `;` hay backtick. Vẫn phải chặn riêng vài flag tự nó là RCE
//     (`--post-renderer`) hoặc phá cách chọn cluster (`--kubeconfig`).
//  3. `install`/`upgrade` KHÔNG dùng `--wait`: helm trả về ngay sau khi apply
//     manifest, còn việc chờ pod Running để người gọi tự poll `get pods`. Chờ
//     đồng bộ ở đây sẽ vượt timeout RPC 90s của ai-worker và bị kill giữa chừng.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"my-k8s-commander/pkg/helmpolicy"
)

// helm render chart lớn (Airbyte) có thể mất kha khá, nhưng vẫn phải nằm dưới
// timeout RPC của caller.
const helmTimeout = 60 * time.Second

func helmUsage() []string {
	return []string{
		"lệnh helm (allowlist):",
		"  helm repo add <tên> <url> | helm repo update | helm repo list",
		"  helm search repo <từ khoá> | helm show values <chart>",
		"  helm list [-n <ns>] | helm status <release> [-n <ns>]",
		"  helm install <release> <chart> [-n <ns>] [--create-namespace] [--set k=v] [-f <file>]",
		"  helm upgrade <release> <chart> [...]   | helm uninstall <release> [-n <ns>]",
		"install/upgrade không chờ pod Ready — poll `get pods -n <ns>` để xem tiến độ",
	}
}

func (w *worker) helm(args []string) []string {
	if len(args) == 0 {
		return helmUsage()
	}
	if strings.ToLower(args[0]) == "help" {
		return helmUsage()
	}

	writes, ok := helmpolicy.Writes(args)
	if !ok {
		return append([]string{"helm: verb không được phép: " + strings.Join(args, " ")}, helmUsage()...)
	}
	if banned := helmpolicy.Banned(args); banned != "" {
		return []string{"helm: flag bị chặn: " + banned}
	}

	bin, err := exec.LookPath("helm")
	if err != nil {
		return []string{"helm: chưa cài helm trên máy này (brew install helm)"}
	}

	// Bám đúng context worker đang dùng, thay vì để helm tự đoán current-context.
	full := append([]string{}, args...)
	if ctx := w.currentContext(); ctx != "" {
		full = append(full, "--kube-context", ctx)
	}

	ctx, cancel := context.WithTimeout(context.Background(), helmTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, full...)
	raw, runErr := cmd.CombinedOutput()

	out := []string{"$ helm " + strings.Join(full, " ")}
	out = append(out, table(strings.Split(strings.TrimRight(string(raw), "\n"), "\n"))...)
	switch {
	case ctx.Err() != nil:
		out = append(out, fmt.Sprintf("helm: quá %s, đã dừng — kiểm tra bằng `helm list -n <ns>`", helmTimeout))
	case runErr != nil:
		out = append(out, "helm: "+runErr.Error())
	case writes:
		out = append(out, "đã apply — pod chưa chắc Ready, poll `get pods -n <ns>` để xem")
	}
	return out
}
