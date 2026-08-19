package main

// `node addr`: địa chỉ node ở định dạng máy đọc được, cho worker khác gọi qua
// workerrpc (server-worker dùng để map node <-> server SSH trong sổ).
//
// Mỗi dòng 1 node, phân tách bằng TAB, thiếu thì "-":
//
//	NAME \t STATUS \t INTERNAL-IP \t EXTERNAL-IP \t HOSTNAME
//
// Cố ý KHÔNG dùng tabwriter: caller tách theo '\t', căn cột bằng space sẽ hỏng.
// Dòng lỗi không có TAB nên caller phân biệt được bằng số field.

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeAddrFields là số cột của 1 dòng `node addr`. server-worker dựa vào đây.
const NodeAddrFields = 5

func (w *worker) nodeAddr() []string {
	client, err := w.clientset()
	if err != nil {
		return []string{"lỗi tạo client: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return []string{"lỗi list nodes: " + err.Error()}
	}
	out := make([]string, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		out = append(out, strings.Join([]string{
			n.Name,
			nodeStatus(n),
			dash(nodeAddress(n, corev1.NodeInternalIP)),
			dash(nodeAddress(n, corev1.NodeExternalIP)),
			dash(nodeAddress(n, corev1.NodeHostName)),
		}, "\t"))
	}
	if len(out) == 0 {
		return []string{"không có node nào"}
	}
	return out
}

func nodeAddress(n *corev1.Node, kind corev1.NodeAddressType) string {
	for _, a := range n.Status.Addresses {
		if a.Type == kind {
			return a.Address
		}
	}
	return ""
}

func nodeStatus(n *corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			return "Ready"
		}
	}
	return "NotReady"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (w *worker) node(args []string) []string {
	if len(args) == 0 {
		return []string{"node: thiếu lệnh con (addr)"}
	}
	switch strings.ToLower(args[0]) {
	case "addr", "addrs", "address":
		return w.nodeAddr()
	default:
		return []string{fmt.Sprintf("node: không hiểu %q (chỉ có: addr)", args[0])}
	}
}
