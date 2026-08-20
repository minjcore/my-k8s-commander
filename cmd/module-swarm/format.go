package main

// Hàm định dạng thuần — không gọi Docker, test được trực tiếp.

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/moby/moby/api/types/swarm"
)

const (
	// Cắt id cho vừa bảng, đúng độ dài docker CLI vẫn dùng.
	shortIDLen = 12

	availabilityFlag = "--availability"

	nanoCPUsPerCPU = 1e9
	bytesPerMB     = 1 << 20
)

// table căn cột bằng tabwriter rồi trả về từng dòng (supervisor đọc theo dòng).
func table(rows []string) []string {
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, r)
	}
	_ = tw.Flush()
	return strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
}

func shortID(id string) string {
	if len(id) <= shortIDLen {
		return id
	}
	return id[:shortIDLen]
}

func serviceMode(s *swarm.Service) string {
	switch {
	case s.Spec.Mode.Replicated != nil:
		return modeReplicated
	case s.Spec.Mode.Global != nil:
		return modeGlobal
	}
	return modeUnknown
}

// replicas in "chạy/muốn" như docker CLI. ServiceStatus chỉ có khi list với
// Status=true; thiếu thì rơi về số replica trong spec.
func replicas(s *swarm.Service) string {
	if s.ServiceStatus != nil {
		return fmt.Sprintf("%d/%d", s.ServiceStatus.RunningTasks, s.ServiceStatus.DesiredTasks)
	}
	if s.Spec.Mode.Replicated != nil && s.Spec.Mode.Replicated.Replicas != nil {
		return fmt.Sprintf("?/%d", *s.Spec.Mode.Replicated.Replicas)
	}
	return modeUnknown
}

// shortImage bỏ digest: "nginx:1.25@sha256:abc..." dài loà cả bảng.
func shortImage(s *swarm.Service) string {
	img := s.Spec.TaskTemplate.ContainerSpec.Image
	if i := strings.Index(img, digestSeparator); i > 0 {
		return img[:i]
	}
	return img
}

func nodeRole(n *swarm.Node) string {
	role := string(n.Spec.Role)
	if n.ManagerStatus != nil && n.ManagerStatus.Leader {
		return role + " (leader)"
	}
	return role
}

// sortedServices trả về bản sao đã sort theo tên, không đụng slice gốc.
func sortedServices(items []swarm.Service) []swarm.Service {
	out := make([]swarm.Service, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
	return out
}
