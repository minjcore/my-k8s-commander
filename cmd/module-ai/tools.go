package main

// Định nghĩa tool cho model + hàng rào an toàn.
//
// Lệnh ĐỌC chạy thẳng. `server run` là thực thi lệnh tuỳ ý trên máy từ xa và
// `cluster add/rm` sửa kubeconfig thật — những lệnh đó phải qua approver (xem
// approval.go). Mọi lệnh đều log ra Terminal để người dùng thấy AI vừa làm gì.

import (
	"bufio"
	"encoding/json"
	"strings"

	"my-k8s-commander/pkg/helmpolicy"
	"my-k8s-commander/pkg/workerrpc"

	"github.com/anthropics/anthropic-sdk-go"
)

const (
	toolK8s    = "k8s"
	toolServer = "server"

	k8sWorker    = "k8s-worker"
	serverWorker = "server-worker"
)

func toolDefs() []anthropic.BetaToolUnionParam {
	commandSchema := anthropic.BetaToolInputSchemaParam{
		Properties: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Lệnh gửi cho worker, đúng cú pháp worker hiểu. Mỗi lần 1 lệnh.",
			},
		},
		Required: []string{"command"},
	}

	k8s := anthropic.BetaToolParam{
		Name:        toolK8s,
		InputSchema: commandSchema,
		Description: anthropic.String(`Chạy 1 lệnh trên cụm Kubernetes qua k8s-worker (client-go, đọc ~/.kube/config).
Gọi tool này khi câu hỏi cần dữ liệu thật của cụm — pod nào đang chạy, node có Ready không,
đang ở context nào, endpoint của cluster là gì — thay vì trả lời theo trí nhớ.
Lệnh đọc dùng được: "get pods [-n <ns> | -A]", "get nodes", "get ns", "ctx",
"cluster list", "cluster info [tên]", "cluster test [tên|all]", "use <context>".
"health" quét cả cụm và CHỈ in pod/node bất thường — hỏi "cụm có vấn đề gì
không?" thì dùng lệnh này, rẻ hơn đọc cả bảng "get pods -A"; không in gì = ổn.
Cài/nâng cấp chart bằng helm: "helm repo add <tên> <url>", "helm repo update",
"helm search repo <từ khoá>", "helm show values <chart>", "helm list -n <ns>",
"helm install <release> <chart> -n <ns> --create-namespace [--set k=v]",
"helm upgrade", "helm uninstall <release> -n <ns>".
helm install KHÔNG chờ pod Ready — sau khi cài xong hãy gọi "get pods -n <ns>"
vài lần để theo dõi tới khi Running.
Lệnh ghi ("cluster add", "cluster rm", "cluster use --persist", "helm install",
"helm upgrade", "helm uninstall") phải chờ người dùng duyệt.
Worker CHỈ hiểu "-n <ns>" và "-A": không có -o/--output, --field-selector,
--selector, lọc theo tên pod, hay pipe/grep. Gửi flag lạ là bị trả lỗi.
Kết quả trả về là output thô của worker, đã căn cột sẵn — tự lọc trên đó.
Cột STATUS đã là trạng thái thật (CrashLoopBackOff, ImagePullBackOff, OOMKilled...).`),
	}

	server := anthropic.BetaToolParam{
		Name:        toolServer,
		InputSchema: commandSchema,
		Description: anthropic.String(`Chạy 1 lệnh trên sổ server SSH qua server-worker.
Gọi tool này khi cần biết người dùng có những server nào, server còn sống không,
hoặc node nào của cluster ứng với server nào ("server nodes" — ghép node K8s với
entry trong sổ theo IP).
Lệnh đọc dùng được: "server list", "server nodes", "server test <selector>".
Selector: <tên> | @tag | all | node/<tên node> | node/all.
Lệnh đổi trạng thái ("server add/rm/trust") và "server run" (thực thi lệnh từ xa)
phải chờ người dùng duyệt — chỉ gọi khi họ đã yêu cầu rõ ràng.`),
	}

	alert := anthropic.BetaToolParam{
		Name:        toolAlert,
		InputSchema: commandSchema,
		Description: anthropic.String(`Sửa danh sách pattern cảnh báo mà status bar của app dùng để soi log.
Mỗi pattern là 1 regex; dòng log nào khớp thì status bar bật cảnh báo đỏ.
Lệnh: "alert list" (đọc), "alert add <tên> <regex>", "alert rm <tên>".
Regex có capture group thì group 1 hiện làm tên đối tượng, ví dụ:
  alert add disk-full no space left on device.*pod=(\S+)
add/rm ghi vào file cấu hình thật nên phải chờ người dùng duyệt.
Chỉ gọi khi người dùng yêu cầu rõ ràng là muốn thêm/bớt cảnh báo.`),
	}

	return []anthropic.BetaToolUnionParam{
		{OfTool: &k8s},
		{OfTool: &server},
		{OfTool: &alert},
	}
}

// runTool thực thi 1 tool_use block và trả về tool_result tương ứng.
func runTool(pool *workerrpc.Pool, ap *approver, out *bufio.Writer, use anthropic.BetaToolUseBlock) anthropic.BetaContentBlockParamUnion {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(use.JSON.Input.Raw()), &in); err != nil {
		return anthropic.NewBetaToolResultBlock(use.ID, "input không đọc được: "+err.Error(), true)
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return anthropic.NewBetaToolResultBlock(use.ID, "thiếu command", true)
	}

	// Tool alert không gọi worker nào — nó sửa file cấu hình tại chỗ.
	if use.Name == toolAlert {
		return runAlertTool(ap, out, use.ID, command)
	}

	worker, ok := workerFor(use.Name)
	if !ok {
		return anthropic.NewBetaToolResultBlock(use.ID, "không có tool "+use.Name, true)
	}

	// Log ra Terminal trước khi chạy: người dùng thấy AI định làm gì.
	emit(out, []string{"→ " + worker + ": " + command})

	ok, reason := ap.allow(out, use.Name, worker, command)
	if reason != "" {
		emit(out, []string{"  (" + reason + ")"})
	}
	if !ok {
		return anthropic.NewBetaToolResultBlock(use.ID,
			"Không chạy: "+reason+". Đây là lệnh thay đổi hệ thống. Đừng thử lại — "+
				"hãy đưa lệnh này cho người dùng tự gõ vào Terminal.", true)
	}

	lines, err := pool.Call(worker, command)
	for _, l := range lines {
		emit(out, []string{"  " + l})
	}
	if err != nil {
		emit(out, []string{"  lỗi: " + err.Error()})
		body := err.Error()
		if len(lines) > 0 {
			body = strings.Join(lines, "\n") + "\n" + body
		}
		return anthropic.NewBetaToolResultBlock(use.ID, body, true)
	}
	if len(lines) == 0 {
		return anthropic.NewBetaToolResultBlock(use.ID, "(không có output)", false)
	}
	return anthropic.NewBetaToolResultBlock(use.ID, strings.Join(lines, "\n"), false)
}

// runAlertTool chạy lệnh alert sau khi qua approver. Vẫn log ra Terminal như
// tool worker để người dùng thấy AI vừa sửa gì.
func runAlertTool(ap *approver, out *bufio.Writer, useID, command string) anthropic.BetaContentBlockParamUnion {
	// Model hay gửi kèm cả chữ "alert" ("alert add oom ..."); bỏ đi để dòng hỏi
	// duyệt không thành "alert alert add oom ...".
	command = strings.TrimSpace(strings.TrimPrefix(command, toolAlert+" "))
	emit(out, []string{"→ " + toolAlert + ": " + command})

	ok, reason := ap.allow(out, toolAlert, toolAlert, command)
	if reason != "" {
		emit(out, []string{"  (" + reason + ")"})
	}
	if !ok {
		return anthropic.NewBetaToolResultBlock(useID,
			"Không chạy: "+reason+". Đây là lệnh sửa file cấu hình. Đừng thử lại.", true)
	}

	lines := runAlert(command)
	for _, l := range lines {
		emit(out, []string{"  " + l})
	}
	return anthropic.NewBetaToolResultBlock(useID, strings.Join(lines, "\n"), false)
}

func workerFor(toolName string) (string, bool) {
	switch toolName {
	case toolK8s:
		return k8sWorker, true
	case toolServer:
		return serverWorker, true
	default:
		return "", false
	}
}

// readOnly: lệnh chỉ đọc thì true. Allowlist chứ không blocklist — lệnh lạ mặc
// định bị coi là ghi.
func readOnly(toolName, command string) bool {
	fields := strings.Fields(strings.ToLower(command))
	if len(fields) == 0 {
		return false
	}
	switch toolName {
	case toolK8s:
		return k8sReadOnly(fields)
	case toolServer:
		return serverReadOnly(fields)
	case toolAlert:
		return alertReadOnly(fields)
	default:
		return false
	}
}

func k8sReadOnly(fields []string) bool {
	switch fields[0] {
	case "kubectl":
		return len(fields) > 1 && k8sReadOnly(fields[1:])
	case "get", "ctx", "contexts", "help", "health":
		return true
	case "node":
		return len(fields) > 1 && fields[1] == "addr"
	case "helm":
		// Cùng một bảng phân loại với k8s-worker (pkg/helmpolicy).
		// Verb lạ -> coi như ghi -> phải duyệt.
		writes, ok := helmpolicy.Writes(fields[1:])
		return ok && !writes
	case "use":
		// Chỉ đổi context trong tiến trình worker con của ai-worker, không ghi file.
		return true
	case "cluster", "clusters":
		if len(fields) == 1 {
			return true
		}
		switch fields[1] {
		case "list", "ls", "info", "test", "ping", "help":
			// `cluster use ... --persist` ghi kubeconfig nên không nằm ở đây.
			return true
		}
		return false
	}
	return false
}

func serverReadOnly(fields []string) bool {
	if fields[0] == "server" || fields[0] == "srv" {
		return len(fields) > 1 && serverReadOnly(fields[1:])
	}
	switch fields[0] {
	case "list", "ls", "nodes", "node", "test", "ping", "help":
		return true
	}
	return false
}
