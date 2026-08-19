package main

// Lệnh `health`: quét pod + node, in RA CHỈ những dòng bất thường.
//
// UI gọi lệnh này định kỳ để status bar biết cụm có vấn đề mà không cần người
// dùng gõ gì. Vì thế nó phải IM LẶNG khi mọi thứ bình thường — nếu in cả bảng
// pod mỗi 30s thì Terminal thành rác, không ai đọc được nữa.
//
// Định dạng dòng in ra phải TRÙNG bảng của `get pods` / `get nodes`: parser bên
// UI (lib/src/status.dart) tách theo cột, STATUS của pod ở index 3, của node ở
// index 1. Đổi định dạng ở đây là phải sửa parser bên đó.

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Trạng thái pod bình thường — không phải cảnh báo. Mọi thứ khác coi là bất
// thường: allowlist chứ không blocklist, giống chỗ khác trong project.
var podHealthyStatuses = map[string]bool{
	string(corev1.PodRunning):   true,
	string(corev1.PodSucceeded): true,
	string(corev1.PodPending):   true, // pod mới tạo, chưa kịp chạy
	statusTerminating:           true, // đang bị xoá có chủ đích
}

// healthRows trả về các dòng cần báo. Rỗng = cụm ổn, worker không in gì.
//
// Nhận kubernetes.Interface (không phải *Clientset) để test được bằng fake
// clientset, không cần cụm thật.
func healthRows(ctx context.Context, client kubernetes.Interface) []string {
	var rows []string

	pods, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		rows = append(rows, "lỗi list pods: "+err.Error())
	} else {
		for i := range pods.Items {
			p := &pods.Items[i]
			status := podStatus(p)
			if podHealthyStatuses[status] {
				continue
			}
			ready, restarts := containerStats(p)
			rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s",
				p.Namespace, p.Name, ready, status, restarts, age(p.CreationTimestamp)))
		}
	}

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		rows = append(rows, "lỗi list nodes: "+err.Error())
	} else {
		for i := range nodes.Items {
			n := &nodes.Items[i]
			if nodeReady(n) {
				continue
			}
			rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s",
				n.Name, "NotReady", n.Status.NodeInfo.KubeletVersion, age(n.CreationTimestamp)))
		}
	}
	return rows
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (w *worker) health() []string {
	client, err := w.clientset()
	if err != nil {
		return []string{"lỗi tạo client: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	rows := healthRows(ctx, client)
	if len(rows) == 0 {
		return nil
	}
	return table(rows)
}
