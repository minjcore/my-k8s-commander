package main

import (
	"encoding/json"
	"testing"
)

func TestReadOnly(t *testing.T) {
	allowed := map[string][]string{
		toolK8s: {
			"get pods -A", "get nodes", "ctx", "cluster", "cluster list",
			"cluster info dev", "cluster test all", "kubectl get pods", "use dev",
			"node addr", "help",
			"helm list -n airbyte", "helm status airbyte", "helm version",
			"helm repo add airbyte https://airbytehq.github.io/helm-charts",
			"helm repo update", "helm search repo airbyte", "helm show values airbyte/airbyte",
		},
		toolServer: {
			"server list", "list", "server test prod-1", "test @prod",
			"server nodes", "nodes", "help",
		},
	}
	blocked := map[string][]string{
		toolK8s: {
			"cluster add staging --server https://x", "cluster rm dev --yes",
			"cluster use dev --persist", "kubectl cluster rm dev --yes", "delete pod x", "",
			// helm đổi cụm -> phải qua duyệt, không được chạy thẳng.
			"helm install airbyte airbyte/airbyte -n airbyte --create-namespace",
			"helm upgrade airbyte airbyte/airbyte", "helm uninstall airbyte -n airbyte",
			"helm rollback airbyte 1", "helm repo remove airbyte",
			// verb lạ -> mặc định coi là ghi.
			"helm template x y", "helm plugin install evil", "helm",
		},
		toolServer: {
			"server run all rm -rf /", "run prod-1 uptime", "run node/gke-1 uptime",
			"server add x u@h", "server rm x", "server trust x", "srv run x whoami",
		},
	}

	for tool, cmds := range allowed {
		for _, c := range cmds {
			if !readOnly(tool, c) {
				t.Errorf("%s: %q phải được phép", tool, c)
			}
		}
	}
	for tool, cmds := range blocked {
		for _, c := range cmds {
			if readOnly(tool, c) {
				t.Errorf("%s: %q phải bị chặn", tool, c)
			}
		}
	}
}

// TestToolDefsJSON kiểm hình dạng JSON gửi lên API. Sai input_schema thì API
// trả 400 lúc chạy thật, mà lỗi đó chỉ lộ ra khi có credential — nên chốt ở đây.
func TestToolDefsJSON(t *testing.T) {
	raw, err := json.Marshal(toolDefs())
	if err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			Type       string `json:"type"`
			Required   []string
			Properties map[string]struct {
				Type        string `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
		} `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if len(tools) != 3 {
		t.Fatalf("muốn 3 tool, nhận %d", len(tools))
	}
	for _, tool := range tools {
		if tool.Name != toolK8s && tool.Name != toolServer && tool.Name != toolAlert {
			t.Errorf("tên tool lạ: %q", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s: thiếu description", tool.Name)
		}
		if tool.InputSchema.Type != "object" {
			t.Errorf("%s: input_schema.type = %q, muốn object", tool.Name, tool.InputSchema.Type)
		}
		if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "command" {
			t.Errorf("%s: required = %v", tool.Name, tool.InputSchema.Required)
		}
		cmd, ok := tool.InputSchema.Properties["command"]
		if !ok {
			t.Errorf("%s: thiếu property command", tool.Name)
			continue
		}
		if cmd.Type != "string" || cmd.Description == "" {
			t.Errorf("%s: command = %+v", tool.Name, cmd)
		}
	}
}

// Model local vẫn chèn code fence dù system prompt cấm; Terminal là plain text
// nên dòng ``` phải bị bỏ, nội dung bên trong giữ nguyên.
func TestStripFences(t *testing.T) {
	cases := []struct{ in, want string }{
		{"không có fence", "không có fence"},
		{"thêm bằng lệnh:\n```\nserver add prod-1 ubuntu@10.0.0.5\n```",
			"thêm bằng lệnh:\nserver add prod-1 ubuntu@10.0.0.5"},
		{"```bash\nkubectl get pods\n```", "kubectl get pods"},
		// Fence thụt lề vẫn là fence.
		{"  ```\nx\n  ```", "x"},
		// Fence không đóng cũng phải bỏ.
		{"lệnh:\n```\nget pods", "lệnh:\nget pods"},
		// Backtick lẻ trong câu không phải fence, giữ nguyên.
		{"gõ `ai yes` để duyệt", "gõ `ai yes` để duyệt"},
	}
	for _, c := range cases {
		if got := stripFences(c.in); got != c.want {
			t.Fatalf("stripFences(%q) = %q, chờ %q", c.in, got, c.want)
		}
	}
}
