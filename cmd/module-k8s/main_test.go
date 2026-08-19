package main

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func waiting(reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{State: corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: reason},
	}}
}

func terminated(reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Reason: reason},
	}}
}

func running() corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}
}

// Pod CrashLoopBackOff có phase "Running" — cột STATUS phải in lý do container
// chứ không phải phase, nếu không thì câu "pod nào crash?" trả lời sai.
func TestPodStatusCrashLoopKhongPhaiRunning(t *testing.T) {
	p := &corev1.Pod{Status: corev1.PodStatus{
		Phase:             corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{waiting("CrashLoopBackOff")},
	}}
	if got := podStatus(p); got != "CrashLoopBackOff" {
		t.Fatalf("podStatus = %q", got)
	}
}

func TestPodStatusCacTruongHop(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{
			name: "pod bình thường lấy phase",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{running()},
			}},
			want: "Running",
		},
		{
			name: "container chạy xong không coi là lỗi",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:             corev1.PodSucceeded,
				ContainerStatuses: []corev1.ContainerStatus{terminated(reasonCompleted)},
			}},
			want: "Succeeded",
		},
		{
			name: "kéo image lỗi",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:             corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{waiting("ImagePullBackOff")},
			}},
			want: "ImagePullBackOff",
		},
		{
			name: "bị OOM",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{terminated("OOMKilled")},
			}},
			want: "OOMKilled",
		},
		{
			name: "init container lỗi thì gắn tiền tố",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:                 corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{waiting("CrashLoopBackOff")},
				ContainerStatuses:     []corev1.ContainerStatus{waiting("PodInitializing")},
			}},
			want: statusInitPrefix + "CrashLoopBackOff",
		},
		{
			name: "init container xong thì xét container thường",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:                 corev1.PodRunning,
				InitContainerStatuses: []corev1.ContainerStatus{terminated(reasonCompleted)},
				ContainerStatuses:     []corev1.ContainerStatus{running()},
			}},
			want: "Running",
		},
		{
			name: "đang bị xoá",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{}},
				Status: corev1.PodStatus{
					Phase:             corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{running()},
				},
			},
			want: statusTerminating,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := podStatus(c.pod); got != c.want {
				t.Fatalf("podStatus = %q, chờ %q", got, c.want)
			}
		})
	}
}

// Flag lạ phải bị báo lỗi: bỏ qua im lặng thì ai-worker tưởng output đã được lọc.
func TestUnsupportedArgs(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{args: nil, want: nil},
		{args: []string{"-A"}, want: nil},
		{args: []string{"-n", "kube-system"}, want: nil},
		{args: []string{"--namespace", "default"}, want: nil},
		{args: []string{"-o", "wide"}, want: []string{"-o", "wide"}},
		{args: []string{"-A", "--field-selector=status.phase=Failed"},
			want: []string{"--field-selector=status.phase=Failed"}},
		// Tên pod cũng chưa hỗ trợ — phải nói ra chứ không list cả namespace.
		{args: []string{"crashloop-test", "-n", "default"}, want: []string{"crashloop-test"}},
	}
	for _, c := range cases {
		got := unsupportedArgs(c.args)
		if len(got) != len(c.want) {
			t.Fatalf("unsupportedArgs(%v) = %v, chờ %v", c.args, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("unsupportedArgs(%v) = %v, chờ %v", c.args, got, c.want)
			}
		}
	}
}

// Lệnh có flag lạ phải trả lỗi trước khi chạm tới cluster.
func TestGetFlagLaBaoLoiTruocKhiGoiAPI(t *testing.T) {
	w := &worker{}
	lines := w.get("pods", []string{"-A", "-o", "yaml"})
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("không có thông báo lỗi")
	}
	for _, want := range []string{"-o", "yaml"} {
		found := false
		for _, l := range lines {
			if strings.Contains(l, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("thông báo %v không nhắc %q", lines, want)
		}
	}
}
