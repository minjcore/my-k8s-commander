package main

// Lệnh `health`: quét service + node, in RA CHỈ những dòng bất thường.
//
// Cùng hợp đồng với `health` của k8s-worker: IM LẶNG khi mọi thứ ổn, để UI gọi
// định kỳ mà không làm rác Terminal. Định dạng cột trùng `service ls` / `node ls`
// để chỗ nào cần parse thì dùng lại được.

import (
	"context"
	"strings"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// Node bình thường: đang ready VÀ availability active. pause/drain là người vận
// hành cố ý, nhưng vẫn nên hiện ra — cụm chạy thiếu node là điều cần biết.
func nodeHealthy(n *swarm.Node) bool {
	return n.Status.State == swarm.NodeStateReady &&
		n.Spec.Availability == swarm.NodeAvailabilityActive
}

// serviceHealthy: đủ task đang chạy so với số muốn chạy. Thiếu ServiceStatus
// (list không bật Status) thì không kết luận được — coi như ổn, đừng báo bừa.
func serviceHealthy(s *swarm.Service) bool {
	if s.ServiceStatus == nil {
		return true
	}
	return s.ServiceStatus.RunningTasks >= s.ServiceStatus.DesiredTasks
}

func (w *worker) health(ctx context.Context) []string {
	var rows []string

	services, err := w.api.ServiceList(ctx, client.ServiceListOptions{Status: true})
	if err != nil {
		rows = append(rows, "lỗi list service: "+err.Error())
	} else {
		items := sortedServices(services.Items)
		for i := range items {
			s := &items[i]
			if serviceHealthy(s) {
				continue
			}
			rows = append(rows, strings.Join([]string{
				s.Spec.Name, serviceMode(s), replicas(s), shortImage(s),
			}, "\t"))
		}
	}

	nodes, err := w.api.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		rows = append(rows, "lỗi list node: "+err.Error())
	} else {
		for i := range nodes.Items {
			n := &nodes.Items[i]
			if nodeHealthy(n) {
				continue
			}
			rows = append(rows, strings.Join([]string{
				n.Description.Hostname, string(n.Status.State),
				string(n.Spec.Availability), nodeRole(n),
			}, "\t"))
		}
	}

	if len(rows) == 0 {
		return nil
	}
	return table(rows)
}
