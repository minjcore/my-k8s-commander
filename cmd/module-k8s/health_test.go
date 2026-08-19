package main

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func pod(ns, name string, phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
		Status:     corev1.PodStatus{Phase: phase, ContainerStatuses: statuses},
	}
}

func node(name string, ready corev1.ConditionStatus) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}},
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.33.4+k3s1"},
		},
	}
}

// Cụm ổn thì health KHÔNG in gì: UI gọi lệnh này 30s/lần, in ra là Terminal
// thành rác.
func TestHealthCumOnThiImLang(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("kube-system", "coredns", corev1.PodRunning, running()),
		pod("default", "job-xong", corev1.PodSucceeded, terminated(reasonCompleted)),
		node("colima", corev1.ConditionTrue),
	)
	rows := healthRows(context.Background(), client)
	if len(rows) != 0 {
		t.Fatalf("cụm ổn mà vẫn in: %v", rows)
	}
}

func TestHealthBatPodCrash(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("kube-system", "coredns", corev1.PodRunning, running()),
		pod("default", "crashloop-test", corev1.PodRunning, waiting("CrashLoopBackOff")),
		node("colima", corev1.ConditionTrue),
	)
	rows := healthRows(context.Background(), client)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	// Định dạng phải khớp bảng get pods: parser bên UI đọc STATUS ở cột 4.
	fields := strings.Split(rows[0], "\t")
	if len(fields) != 6 {
		t.Fatalf("có %d cột: %q", len(fields), rows[0])
	}
	if fields[0] != "default" || fields[1] != "crashloop-test" || fields[3] != "CrashLoopBackOff" {
		t.Fatalf("fields = %v", fields)
	}
}

func TestHealthBatNodeNotReady(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("kube-system", "coredns", corev1.PodRunning, running()),
		node("hong", corev1.ConditionFalse),
		node("tot", corev1.ConditionTrue),
	)
	rows := healthRows(context.Background(), client)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	fields := strings.Split(rows[0], "\t")
	if fields[0] != "hong" || fields[1] != "NotReady" {
		t.Fatalf("fields = %v", fields)
	}
}

// Trạng thái lạ (không nằm trong allowlist "bình thường") phải bị báo.
func TestHealthTrangThaiLaCoiLaBatThuong(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("default", "la", corev1.PodRunning, waiting("SomeNewFailureReason")),
	)
	rows := healthRows(context.Background(), client)
	if len(rows) != 1 || !strings.Contains(rows[0], "SomeNewFailureReason") {
		t.Fatalf("rows = %v", rows)
	}
}

// Pod Pending là bình thường (mới tạo, chưa kịp chạy) — không bắn cảnh báo.
func TestHealthPendingKhongPhaiCanhBao(t *testing.T) {
	client := fake.NewSimpleClientset(pod("default", "moi", corev1.PodPending))
	if rows := healthRows(context.Background(), client); len(rows) != 0 {
		t.Fatalf("rows = %v", rows)
	}
}

// Node không có condition Ready nào = coi như NotReady, không im lặng bỏ qua.
func TestHealthNodeThieuConditionLaNotReady(t *testing.T) {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "trong"}}
	if rows := healthRows(context.Background(), fake.NewSimpleClientset(n)); len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
}
