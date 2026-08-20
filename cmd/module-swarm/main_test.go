package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// fakeAPI đóng vai Docker Engine: trả dữ liệu dựng sẵn và ghi lại lệnh ghi đã
// nhận, để test không cần swarm thật.
type fakeAPI struct {
	services []swarm.Service
	nodes    []swarm.Node
	tasks    []swarm.Task
	info     swarm.Info
	err      error

	updatedSpec *swarm.ServiceSpec
	updatedNode *swarm.NodeSpec
	removed     string
}

func (f *fakeAPI) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	if f.err != nil {
		return client.SystemInfoResult{}, f.err
	}
	res := client.SystemInfoResult{}
	res.Info.Swarm = f.info
	return res, nil
}

func (f *fakeAPI) ServiceList(_ context.Context, o client.ServiceListOptions) (client.ServiceListResult, error) {
	if f.err != nil {
		return client.ServiceListResult{}, f.err
	}
	if len(o.Filters) == 0 {
		return client.ServiceListResult{Items: f.services}, nil
	}
	// Chỉ cần lọc theo label để phục vụ `stack services`.
	var out []swarm.Service
	for _, s := range f.services {
		for want := range o.Filters["label"] {
			parts := strings.SplitN(want, "=", 2)
			if len(parts) == 2 && s.Spec.Labels[parts[0]] == parts[1] {
				out = append(out, s)
			}
		}
	}
	return client.ServiceListResult{Items: out}, nil
}

func (f *fakeAPI) ServiceInspect(_ context.Context, id string, _ client.ServiceInspectOptions) (client.ServiceInspectResult, error) {
	for _, s := range f.services {
		if s.Spec.Name == id || s.ID == id {
			return client.ServiceInspectResult{Service: s}, nil
		}
	}
	return client.ServiceInspectResult{}, errors.New("no such service: " + id)
}

func (f *fakeAPI) ServiceUpdate(_ context.Context, _ string, o client.ServiceUpdateOptions) (client.ServiceUpdateResult, error) {
	spec := o.Spec
	f.updatedSpec = &spec
	return client.ServiceUpdateResult{}, f.err
}

func (f *fakeAPI) ServiceRemove(_ context.Context, id string, _ client.ServiceRemoveOptions) (client.ServiceRemoveResult, error) {
	f.removed = id
	return client.ServiceRemoveResult{}, f.err
}

func (f *fakeAPI) NodeList(context.Context, client.NodeListOptions) (client.NodeListResult, error) {
	if f.err != nil {
		return client.NodeListResult{}, f.err
	}
	return client.NodeListResult{Items: f.nodes}, nil
}

func (f *fakeAPI) NodeUpdate(_ context.Context, _ string, o client.NodeUpdateOptions) (client.NodeUpdateResult, error) {
	spec := o.Spec
	f.updatedNode = &spec
	return client.NodeUpdateResult{}, f.err
}

func (f *fakeAPI) NodeRemove(_ context.Context, id string, _ client.NodeRemoveOptions) (client.NodeRemoveResult, error) {
	f.removed = id
	return client.NodeRemoveResult{}, f.err
}

func (f *fakeAPI) TaskList(context.Context, client.TaskListOptions) (client.TaskListResult, error) {
	if f.err != nil {
		return client.TaskListResult{}, f.err
	}
	return client.TaskListResult{Items: f.tasks}, nil
}

func service(name string, running, desired uint64, image string, labels map[string]string) swarm.Service {
	replicas := desired
	return swarm.Service{
		ID: "id-" + name,
		Spec: swarm.ServiceSpec{
			Annotations: swarm.Annotations{Name: name, Labels: labels},
			Mode:        swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}},
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{Image: image},
			},
		},
		ServiceStatus: &swarm.ServiceStatus{RunningTasks: running, DesiredTasks: desired},
	}
}

func node(host string, state swarm.NodeState, avail swarm.NodeAvailability) swarm.Node {
	return swarm.Node{
		ID:          "nodeid-" + host,
		Spec:        swarm.NodeSpec{Availability: avail, Role: swarm.NodeRoleWorker},
		Description: swarm.NodeDescription{Hostname: host},
		Status:      swarm.NodeStatus{State: state},
	}
}

func runCmd(t *testing.T, api dockerAPI, line string) []string {
	t.Helper()
	return (&worker{api: api}).handle(line)
}

func TestServiceLS(t *testing.T) {
	api := &fakeAPI{services: []swarm.Service{
		service("web", 3, 3, "nginx:1.25@sha256:deadbeef", nil),
		service("api", 1, 2, "api:v1", nil),
	}}
	out := runCmd(t, api, "service ls")
	joined := strings.Join(out, "\n")

	if !strings.Contains(out[0], "2 service") {
		t.Fatalf("dòng đầu = %q", out[0])
	}
	// Sort theo tên: api trước web.
	if strings.Index(joined, "api") > strings.Index(joined, "web") {
		t.Fatalf("chưa sort theo tên:\n%s", joined)
	}
	if !strings.Contains(joined, "1/2") || !strings.Contains(joined, "3/3") {
		t.Fatalf("thiếu cột replicas:\n%s", joined)
	}
	// Digest phải bị cắt cho bảng đọc được.
	if strings.Contains(joined, "sha256") {
		t.Fatalf("digest chưa bị cắt:\n%s", joined)
	}
}

func TestServicePSDoiNodeIDThanhHostname(t *testing.T) {
	api := &fakeAPI{
		nodes: []swarm.Node{node("mgr1", swarm.NodeStateReady, swarm.NodeAvailabilityActive)},
		tasks: []swarm.Task{{
			ID: "task1", Slot: 1, NodeID: "nodeid-mgr1",
			DesiredState: swarm.TaskStateRunning,
			Status:       swarm.TaskStatus{State: swarm.TaskStateFailed, Err: "OOMKilled"},
		}},
	}
	joined := strings.Join(runCmd(t, api, "service ps web"), "\n")
	for _, want := range []string{"web.1", "mgr1", "failed", "OOMKilled"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("thiếu %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "nodeid-") {
		t.Fatalf("vẫn in node id thô:\n%s", joined)
	}
}

func TestNodeLS(t *testing.T) {
	api := &fakeAPI{nodes: []swarm.Node{
		node("w2", swarm.NodeStateDown, swarm.NodeAvailabilityDrain),
		node("w1", swarm.NodeStateReady, swarm.NodeAvailabilityActive),
	}}
	joined := strings.Join(runCmd(t, api, "node ls"), "\n")
	if strings.Index(joined, "w1") > strings.Index(joined, "w2") {
		t.Fatalf("chưa sort theo hostname:\n%s", joined)
	}
	for _, want := range []string{"down", "drain", "ready", "active"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("thiếu %q:\n%s", want, joined)
		}
	}
}

// stack không có API riêng: gom theo label com.docker.stack.namespace.
func TestStackLSGomTheoLabel(t *testing.T) {
	api := &fakeAPI{services: []swarm.Service{
		service("blog_web", 1, 1, "nginx", map[string]string{stackLabel: "blog"}),
		service("blog_db", 1, 1, "pg", map[string]string{stackLabel: "blog"}),
		service("le-loi", 1, 1, "busybox", nil),
	}}
	joined := strings.Join(runCmd(t, api, "stack ls"), "\n")
	if !strings.Contains(joined, "1 stack") || !strings.Contains(joined, "blog") {
		t.Fatalf("out:\n%s", joined)
	}
	// Service không có label thì không thành stack.
	if strings.Contains(joined, "le-loi") {
		t.Fatalf("service lẻ bị tính thành stack:\n%s", joined)
	}
}

func TestStackServicesLocTheoLabel(t *testing.T) {
	api := &fakeAPI{services: []swarm.Service{
		service("blog_web", 1, 1, "nginx", map[string]string{stackLabel: "blog"}),
		service("shop_web", 1, 1, "nginx", map[string]string{stackLabel: "shop"}),
	}}
	joined := strings.Join(runCmd(t, api, "stack services blog"), "\n")
	if !strings.Contains(joined, "blog_web") || strings.Contains(joined, "shop_web") {
		t.Fatalf("lọc sai:\n%s", joined)
	}
}

// scale phải gửi kèm Version của spec vừa đọc, và không chờ task Running.
func TestServiceScale(t *testing.T) {
	api := &fakeAPI{services: []swarm.Service{service("web", 1, 1, "nginx", nil)}}
	out := strings.Join(runCmd(t, api, "service scale web 5"), "\n")
	if api.updatedSpec == nil {
		t.Fatal("không gọi ServiceUpdate")
	}
	if got := *api.updatedSpec.Mode.Replicated.Replicas; got != 5 {
		t.Fatalf("replicas = %d", got)
	}
	if !strings.Contains(out, "poll") {
		t.Fatalf("không nhắc poll: %q", out)
	}
}

func TestServiceScaleSoSai(t *testing.T) {
	api := &fakeAPI{services: []swarm.Service{service("web", 1, 1, "nginx", nil)}}
	if out := strings.Join(runCmd(t, api, "service scale web nhieu"), "\n"); !strings.Contains(out, "không phải số") {
		t.Fatalf("out = %q", out)
	}
	if api.updatedSpec != nil {
		t.Fatal("số sai mà vẫn gọi update")
	}
}

func TestServiceScaleModeGlobal(t *testing.T) {
	s := service("agent", 3, 3, "busybox", nil)
	s.Spec.Mode = swarm.ServiceMode{Global: &swarm.GlobalService{}}
	api := &fakeAPI{services: []swarm.Service{s}}
	if out := strings.Join(runCmd(t, api, "service scale agent 5"), "\n"); !strings.Contains(out, "global") {
		t.Fatalf("out = %q", out)
	}
	if api.updatedSpec != nil {
		t.Fatal("mode global mà vẫn scale")
	}
}

func TestNodeUpdateAvailability(t *testing.T) {
	api := &fakeAPI{nodes: []swarm.Node{node("w1", swarm.NodeStateReady, swarm.NodeAvailabilityActive)}}
	out := strings.Join(runCmd(t, api, "node update w1 --availability drain"), "\n")
	if api.updatedNode == nil {
		t.Fatal("không gọi NodeUpdate")
	}
	if api.updatedNode.Availability != swarm.NodeAvailabilityDrain {
		t.Fatalf("availability = %q", api.updatedNode.Availability)
	}
	if !strings.Contains(out, "drain") {
		t.Fatalf("out = %q", out)
	}
}

func TestNodeUpdateFlagLa(t *testing.T) {
	api := &fakeAPI{nodes: []swarm.Node{node("w1", swarm.NodeStateReady, swarm.NodeAvailabilityActive)}}
	if out := strings.Join(runCmd(t, api, "node update w1 --label-add a=b"), "\n"); !strings.Contains(out, "chỉ hỗ trợ") {
		t.Fatalf("out = %q", out)
	}
	if api.updatedNode != nil {
		t.Fatal("flag lạ mà vẫn update")
	}
}

// Lệnh ngoài allowlist phải bị chặn TRƯỚC khi gọi Docker.
func TestLenhNgoaiAllowlistBiChan(t *testing.T) {
	for _, line := range []string{
		"swarm leave --force",
		"service create --name x nginx",
		"stack deploy -c docker-compose.yml blog",
		"container ls",
		"secret rm token",
	} {
		api := &fakeAPI{}
		out := strings.Join(runCmd(t, api, line), "\n")
		if !strings.Contains(out, "không được phép") {
			t.Fatalf("%q không bị chặn: %s", line, out)
		}
	}
}

func TestGoQuenTayCoTienTo(t *testing.T) {
	api := &fakeAPI{services: []swarm.Service{service("web", 1, 1, "nginx", nil)}}
	for _, line := range []string{"service ls", "swarm service ls", "docker service ls"} {
		if out := strings.Join(runCmd(t, api, line), "\n"); !strings.Contains(out, "web") {
			t.Fatalf("%q -> %s", line, out)
		}
	}
}

func TestInfoChuaVaoSwarm(t *testing.T) {
	api := &fakeAPI{info: swarm.Info{LocalNodeState: swarm.LocalNodeStateInactive}}
	if out := strings.Join(runCmd(t, api, "info"), "\n"); !strings.Contains(out, "chưa vào swarm") {
		t.Fatalf("out = %q", out)
	}
}

func TestKhongNoiDuocDockerThiBaoChuKhongCrash(t *testing.T) {
	w := &worker{initErr: errors.New("cannot connect to the Docker daemon")}
	out := strings.Join(w.handle("service ls"), "\n")
	if !strings.Contains(out, "không nối được Docker") {
		t.Fatalf("out = %q", out)
	}
}

func TestLoiAPIBaoRaChuKhongPanic(t *testing.T) {
	api := &fakeAPI{err: errors.New("permission denied")}
	if out := strings.Join(runCmd(t, api, "service ls"), "\n"); !strings.Contains(out, "permission denied") {
		t.Fatalf("out = %q", out)
	}
}

func TestHealthCumOnThiImLang(t *testing.T) {
	api := &fakeAPI{
		services: []swarm.Service{service("web", 3, 3, "nginx", nil)},
		nodes:    []swarm.Node{node("w1", swarm.NodeStateReady, swarm.NodeAvailabilityActive)},
	}
	if out := runCmd(t, api, "health"); len(out) != 0 {
		t.Fatalf("cụm ổn mà vẫn in: %v", out)
	}
}

func TestHealthBatServiceThieuReplica(t *testing.T) {
	api := &fakeAPI{
		services: []swarm.Service{
			service("web", 3, 3, "nginx", nil),
			service("api", 0, 2, "api:v1", nil),
		},
		nodes: []swarm.Node{node("w1", swarm.NodeStateReady, swarm.NodeAvailabilityActive)},
	}
	out := runCmd(t, api, "health")
	if len(out) != 1 {
		t.Fatalf("out = %v", out)
	}
	if !strings.Contains(out[0], "api") || !strings.Contains(out[0], "0/2") {
		t.Fatalf("out = %q", out[0])
	}
}

func TestHealthBatNodeKhongReadyVaDrain(t *testing.T) {
	api := &fakeAPI{nodes: []swarm.Node{
		node("ok", swarm.NodeStateReady, swarm.NodeAvailabilityActive),
		node("chet", swarm.NodeStateDown, swarm.NodeAvailabilityActive),
		node("drain", swarm.NodeStateReady, swarm.NodeAvailabilityDrain),
	}}
	out := strings.Join(runCmd(t, api, "health"), "\n")
	for _, want := range []string{"chet", "drain"} {
		if !strings.Contains(out, want) {
			t.Fatalf("thiếu %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ok ") {
		t.Fatalf("node ổn bị báo:\n%s", out)
	}
}

// Thiếu ServiceStatus (list không bật Status) thì đừng kết luận bừa là hỏng.
func TestHealthThieuServiceStatusThiCoiLaOn(t *testing.T) {
	s := service("web", 0, 0, "nginx", nil)
	s.ServiceStatus = nil
	api := &fakeAPI{services: []swarm.Service{s}}
	if out := runCmd(t, api, "health"); len(out) != 0 {
		t.Fatalf("out = %v", out)
	}
}
