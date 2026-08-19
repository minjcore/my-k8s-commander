package main

// Nối hai mảng lại: node của cluster <-> server SSH trong sổ.
//
// server-worker không biết gì về Kubernetes, nên nó hỏi k8s-worker qua
// workerrpc (`node addr`, TSV) rồi ghép địa chỉ node với Host của các entry
// trong sổ. Cố ý KHÔNG tự SSH vào node lạ: `node/<tên>` chỉ là cách gọi tắt
// entry đã có sẵn (đã có user/key/known_hosts), không mở đường xác thực mới.

import (
	"fmt"
	"strings"

	"my-k8s-commander/pkg/workerrpc"
)

const (
	k8sWorker = "k8s-worker"
	// Số cột của 1 dòng `node addr` bên k8s-worker.
	nodeAddrFields = 5
	nodeSelector   = "node/"
)

// node là 1 dòng `node addr` đã tách.
type node struct {
	Name     string
	Status   string
	Internal string
	External string
	Hostname string
}

// addresses trả về các địa chỉ dùng để ghép với Host của entry trong sổ.
func (n node) addresses() []string {
	var out []string
	for _, a := range []string{n.Internal, n.External, n.Hostname, n.Name} {
		if a != "" && a != "-" {
			out = append(out, a)
		}
	}
	return out
}

// listNodes hỏi k8s-worker. pool là biến gói: server-worker chỉ spawn k8s-worker
// khi thật sự có lệnh cần tới nó.
var pool = workerrpc.NewPool(prefix)

func listNodes() ([]node, error) {
	lines, err := pool.Call(k8sWorker, "node addr")
	if err != nil {
		return nil, err
	}
	nodes := make([]node, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != nodeAddrFields {
			// k8s-worker trả lỗi dạng câu chữ (không có TAB) — chuyển nguyên văn.
			return nil, fmt.Errorf("%s: %s", k8sWorker, line)
		}
		nodes = append(nodes, node{
			Name: fields[0], Status: fields[1],
			Internal: fields[2], External: fields[3], Hostname: fields[4],
		})
	}
	return nodes, nil
}

// matchServer tìm entry trong sổ có Host trùng một địa chỉ của node.
func matchServer(st *store, n node) (*Server, bool) {
	for _, addr := range n.addresses() {
		for i := range st.servers {
			if strings.EqualFold(st.servers[i].Host, addr) {
				return &st.servers[i], true
			}
		}
	}
	return nil, false
}

// nodesTable: `server nodes` — node của cluster hiện tại, kèm entry khớp.
func nodesTable() []string {
	st, err := loadStore()
	if err != nil {
		return []string{"lỗi đọc sổ server: " + err.Error()}
	}
	nodes, err := listNodes()
	if err != nil {
		return []string{"không lấy được node: " + err.Error()}
	}

	rows := []string{"NODE\tSTATUS\tINTERNAL-IP\tEXTERNAL-IP\tSERVER"}
	matched := 0
	for _, n := range nodes {
		name := "-"
		if s, ok := matchServer(st, n); ok {
			name = s.Name
			matched++
		}
		rows = append(rows, strings.Join([]string{
			n.Name, n.Status, n.Internal, n.External, name,
		}, "\t"))
	}
	head := fmt.Sprintf("%d node, %d khớp với sổ server", len(nodes), matched)
	out := append([]string{head}, table(rows)...)
	if matched < len(nodes) {
		out = append(out, "node chưa khớp: thêm bằng `server add <tên> <user>@<ip>` với IP ở trên")
	}
	return out
}

// resolveNode đổi "node/<tên>" hoặc "node/all" thành các entry trong sổ.
func resolveNode(st *store, selector string) ([]Server, error) {
	want := strings.TrimPrefix(selector, nodeSelector)
	if want == "" {
		return nil, fmt.Errorf("thiếu tên node sau %q", nodeSelector)
	}
	nodes, err := listNodes()
	if err != nil {
		return nil, fmt.Errorf("không lấy được node: %w", err)
	}

	all := want == "all" || want == "*"
	var (
		out     []Server
		unnamed []string
		found   bool
	)
	for _, n := range nodes {
		if !all && !strings.EqualFold(n.Name, want) {
			continue
		}
		found = true
		if s, ok := matchServer(st, n); ok {
			out = append(out, *s)
		} else {
			unnamed = append(unnamed, n.Name)
		}
	}
	if !found {
		return nil, fmt.Errorf("cluster không có node %q (xem `server nodes`)", want)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("node %s chưa có entry trong sổ — `server add <tên> <user>@<ip>` trước "+
			"(xem `server nodes` để lấy IP)", strings.Join(unnamed, ", "))
	}
	return out, nil
}
