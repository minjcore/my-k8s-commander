package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-k8s-commander/pkg/common"
)

// TestMain kiêm vai k8s-worker giả: K8SC_FAKE_K8S=ok|err thì binary test chạy
// vòng lặp stdin/stdout đúng protocol thay vì chạy test.
func TestMain(m *testing.M) {
	switch os.Getenv("K8SC_FAKE_K8S") {
	case "ok":
		fakeK8s(fakeNodeLines)
		return
	case "err":
		fakeK8s([]string{"[k8s-worker] lỗi list nodes: connection refused"})
		return
	}
	os.Exit(m.Run())
}

var fakeNodeLines = []string{
	"[k8s-worker] gke-a\tReady\t10.0.0.5\t34.1.2.3\tgke-a.internal",
	"[k8s-worker] gke-b\tReady\t10.0.0.6\t-\tgke-b.internal",
	"[k8s-worker] gke-c\tNotReady\t10.0.0.7\t-\t-",
}

func fakeK8s(reply []string) {
	out := bufio.NewWriter(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		for _, l := range reply {
			fmt.Fprintln(out, l)
		}
		fmt.Fprintln(out, common.RPCDone)
		_ = out.Flush()
	}
}

// withFakeK8s trỏ workerrpc vào binary test và dựng sổ server 2 entry:
// web khớp internal IP của gke-a, db khớp gke-b, gke-c không khớp gì.
func withFakeK8s(t *testing.T, mode string) {
	t.Helper()
	pool.StopAll() // pool là biến gói: bỏ tiến trình của test trước
	t.Cleanup(pool.StopAll)

	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(dir, k8sWorker)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("K8SC_MODULES_DIR", dir)
	t.Setenv("K8SC_FAKE_K8S", mode)

	servers := filepath.Join(dir, "servers.json")
	body := `[{"name":"web","host":"10.0.0.5","port":22,"user":"ubuntu"},
	          {"name":"db","host":"10.0.0.6","port":22,"user":"ubuntu"}]`
	if err := os.WriteFile(servers, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("K8SC_SERVERS_FILE", servers)
}

func TestNodesTable(t *testing.T) {
	withFakeK8s(t, "ok")
	out := strings.Join(nodesTable(), "\n")

	if !strings.Contains(out, "3 node, 2 khớp") {
		t.Errorf("header sai:\n%s", out)
	}
	for _, want := range []string{"gke-a", "web", "gke-b", "db", "gke-c"} {
		if !strings.Contains(out, want) {
			t.Errorf("thiếu %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "node chưa khớp") {
		t.Errorf("phải gợi ý cho node chưa khớp:\n%s", out)
	}
}

func TestResolveNode(t *testing.T) {
	withFakeK8s(t, "ok")
	st, err := loadStore()
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.resolve("node/gke-a")
	if err != nil || len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("node/gke-a -> %v / %v", got, err)
	}
	// Khớp cả qua external IP.
	if got, err = st.resolve("node/gke-b"); err != nil || got[0].Name != "db" {
		t.Fatalf("node/gke-b -> %v / %v", got, err)
	}
	if got, err = st.resolve("node/all"); err != nil || len(got) != 2 {
		t.Fatalf("node/all -> %v / %v", got, err)
	}

	// Node có thật nhưng chưa có entry: phải chỉ cách thêm, không im lặng bỏ qua.
	_, err = st.resolve("node/gke-c")
	if err == nil || !strings.Contains(err.Error(), "chưa có entry") {
		t.Fatalf("node/gke-c -> %v", err)
	}
	// Node không có trong cluster.
	_, err = st.resolve("node/nope")
	if err == nil || !strings.Contains(err.Error(), "không có node") {
		t.Fatalf("node/nope -> %v", err)
	}
	if _, err = st.resolve("node/"); err == nil {
		t.Fatal("node/ rỗng phải báo lỗi")
	}
}

// Lỗi của k8s-worker (dòng không có TAB) phải nổi lên nguyên văn, không bị
// hiểu nhầm thành một node tên "lỗi list nodes:...".
func TestNodeErrorPassthrough(t *testing.T) {
	withFakeK8s(t, "err")

	if _, err := listNodes(); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("muốn lỗi từ k8s-worker, nhận %v", err)
	}
	out := strings.Join(nodesTable(), "\n")
	if !strings.Contains(out, "không lấy được node") {
		t.Errorf("nodesTable phải báo lỗi:\n%s", out)
	}
}
