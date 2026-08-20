// Micro-binary Swarm: đọc 1 dòng stdin = 1 lệnh, in kết quả ra stdout.
// Build ra modules/swarm-worker.
//
// Nói trực tiếp với Docker Engine API qua SDK (github.com/moby/moby/client),
// không shell ra CLI. Daemon chọn bằng DOCKER_HOST — kể cả ssh://user@host
// (xem docker.go).
//
// ALLOWLIST theo cặp (đối tượng, verb) ở pkg/swarmpolicy, không phải passthrough.
// Cặp lạ bị chặn, và ai-worker coi cặp lạ là lệnh ghi nên phải người dùng duyệt.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"my-k8s-commander/pkg/common"
	"my-k8s-commander/pkg/swarmpolicy"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

const (
	prefix = "[swarm-worker]"

	// Lệnh nào cũng phải nằm dưới timeout RPC của ai-worker.
	dockerTimeout = 30 * time.Second

	// Label docker CLI dùng để gom service thành "stack". Engine API không có
	// khái niệm stack, chỉ có label này.
	stackLabel = "com.docker.stack.namespace"

	// Cột bảng.
	headServiceLS = "NAME\tMODE\tREPLICAS\tIMAGE"
	headServicePS = "TASK\tNODE\tDESIRED\tCURRENT\tERROR"
	headNodeLS    = "HOSTNAME\tSTATUS\tAVAILABILITY\tROLE"
	headStackLS   = "STACK\tSERVICES"

	modeReplicated = "replicated"
	modeGlobal     = "global"
	modeUnknown    = "-"

	// Image in kèm digest dài loà cả bảng; cắt ở dấu @.
	digestSeparator = "@"
)

type worker struct {
	api dockerAPI
	// Lỗi dựng client giữ lại để mỗi lệnh in ra hướng dẫn, thay vì crash và để
	// supervisor restart vô tận.
	initErr error
}

func main() {
	w := &worker{}
	if api, err := newDockerClient(); err != nil {
		w.initErr = err
	} else {
		w.api = api
	}

	out := bufio.NewWriter(os.Stdout)
	rpc := common.RPCMode()

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		for _, l := range w.handle(line) {
			fmt.Fprintf(out, "%s %s\n", prefix, l)
		}
		if rpc {
			fmt.Fprintln(out, common.RPCDone)
		}
		// Phải flush ngay: supervisor đọc theo dòng, không chờ buffer đầy.
		_ = out.Flush()
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", prefix, err)
	}
}

func (w *worker) handle(line string) []string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return usage()
	}
	// Cho phép gõ quen tay "docker service ls" / "swarm service ls".
	if head := strings.ToLower(fields[0]); head == "docker" || head == "swarm" {
		fields = fields[1:]
		if len(fields) == 0 {
			return usage()
		}
	}

	if _, ok := swarmpolicy.Writes(fields); !ok {
		return append([]string{"swarm: lệnh không được phép: " + strings.Join(fields, " ")}, usage()...)
	}
	if strings.EqualFold(fields[0], "help") {
		return usage()
	}
	if w.api == nil {
		return []string{"swarm: không nối được Docker: " + w.initErr.Error(),
			"daemon: " + dockerHost()}
	}

	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()

	switch strings.ToLower(fields[0]) {
	case "info":
		return w.info(ctx)
	case "health":
		return w.health(ctx)
	case "service":
		return w.service(ctx, fields[1:])
	case "node":
		return w.node(ctx, fields[1:])
	case "stack":
		return w.stack(ctx, fields[1:])
	}
	return append([]string{"swarm: chưa hỗ trợ: " + strings.Join(fields, " ")}, usage()...)
}

func usage() []string {
	return []string{
		"lệnh swarm (allowlist):",
		"  service ls | service ps <tên> | service inspect <tên>",
		"  node ls | node inspect <tên> | stack ls | stack services <tên> | info",
		"  health     # chỉ in service/node bất thường, ổn thì không in gì",
		"lệnh ghi (AI gọi thì phải người dùng duyệt):",
		"  service scale <tên> <n> | service rm <tên> | node update <tên> --availability <active|pause|drain>",
		"daemon chọn bằng " + dockerHostEnvVar + " (unix://..., ssh://user@host, tcp://...)",
		"`swarm init|join|leave` và `stack deploy` KHÔNG đi qua tool này — làm bằng docker CLI",
	}
}

func (w *worker) info(ctx context.Context) []string {
	res, err := w.api.Info(ctx, client.InfoOptions{})
	if err != nil {
		return []string{"lỗi đọc info: " + err.Error()}
	}
	s := res.Info.Swarm
	out := []string{
		"daemon: " + dockerHost(),
		"swarm: " + string(s.LocalNodeState),
	}
	if s.LocalNodeState != swarm.LocalNodeStateActive {
		return append(out, "máy này chưa vào swarm — `docker swarm init` trên node manager")
	}
	return append(out,
		fmt.Sprintf("node: %d (manager %d)", s.Nodes, s.Managers),
		"node id: "+s.NodeID)
}

func (w *worker) service(ctx context.Context, args []string) []string {
	switch strings.ToLower(args[0]) {
	case "ls", "list":
		return w.serviceLS(ctx, nil)
	case "ps":
		if len(args) < 2 {
			return []string{"service ps: cần tên service"}
		}
		return w.servicePS(ctx, args[1])
	case "inspect":
		if len(args) < 2 {
			return []string{"service inspect: cần tên service"}
		}
		return w.serviceInspect(ctx, args[1])
	case "scale":
		if len(args) < 3 {
			return []string{"service scale: cần <tên> <số replica>"}
		}
		return w.serviceScale(ctx, args[1], args[2])
	case "rm":
		if len(args) < 2 {
			return []string{"service rm: cần tên service"}
		}
		return w.serviceRM(ctx, args[1])
	}
	return usage()
}

// serviceLS in bảng service. filter != nil để dùng lại cho `stack services`.
func (w *worker) serviceLS(ctx context.Context, filters client.Filters) []string {
	res, err := w.api.ServiceList(ctx, client.ServiceListOptions{Status: true, Filters: filters})
	if err != nil {
		return []string{"lỗi list service: " + err.Error()}
	}
	if len(res.Items) == 0 {
		return []string{"không có service nào"}
	}
	items := sortedServices(res.Items)
	rows := []string{headServiceLS}
	for i := range items {
		s := &items[i]
		rows = append(rows, strings.Join([]string{
			s.Spec.Name, serviceMode(s), replicas(s), shortImage(s),
		}, "\t"))
	}
	return append([]string{fmt.Sprintf("%d service", len(items))}, table(rows)...)
}

func (w *worker) servicePS(ctx context.Context, name string) []string {
	tasks, err := w.api.TaskList(ctx, client.TaskListOptions{
		Filters: client.Filters{}.Add("service", name),
	})
	if err != nil {
		return []string{"lỗi list task: " + err.Error()}
	}
	if len(tasks.Items) == 0 {
		return []string{"service " + name + " không có task nào"}
	}
	hosts := w.nodeHostnames(ctx)
	sort.Slice(tasks.Items, func(i, j int) bool { return tasks.Items[i].ID < tasks.Items[j].ID })

	rows := []string{headServicePS}
	for i := range tasks.Items {
		t := &tasks.Items[i]
		node := hosts[t.NodeID]
		if node == "" {
			node = shortID(t.NodeID)
		}
		rows = append(rows, strings.Join([]string{
			name + "." + strconv.Itoa(t.Slot), node,
			string(t.DesiredState), string(t.Status.State), t.Status.Err,
		}, "\t"))
	}
	return table(rows)
}

func (w *worker) serviceInspect(ctx context.Context, name string) []string {
	res, err := w.api.ServiceInspect(ctx, name, client.ServiceInspectOptions{})
	if err != nil {
		return []string{"lỗi inspect: " + err.Error()}
	}
	s := &res.Service
	out := []string{
		"name: " + s.Spec.Name,
		"id: " + shortID(s.ID),
		"mode: " + serviceMode(s),
		"replicas: " + replicas(s),
		"image: " + s.Spec.TaskTemplate.ContainerSpec.Image,
	}
	if stack := s.Spec.Labels[stackLabel]; stack != "" {
		out = append(out, "stack: "+stack)
	}
	if s.UpdateStatus != nil && s.UpdateStatus.State != "" {
		out = append(out, "update: "+string(s.UpdateStatus.State)+" — "+s.UpdateStatus.Message)
	}
	return out
}

// serviceScale đọc spec rồi ghi lại với số replica mới. Phải gửi kèm Version của
// spec vừa đọc: Docker từ chối update nếu ai đó đã sửa service ở giữa.
func (w *worker) serviceScale(ctx context.Context, name, want string) []string {
	n, err := strconv.ParseUint(want, 10, 32)
	if err != nil {
		return []string{"service scale: " + want + " không phải số"}
	}
	res, err := w.api.ServiceInspect(ctx, name, client.ServiceInspectOptions{})
	if err != nil {
		return []string{"lỗi inspect: " + err.Error()}
	}
	s := res.Service
	if s.Spec.Mode.Replicated == nil {
		return []string{"service " + name + " chạy mode " + serviceMode(&s) + ", không scale được"}
	}
	s.Spec.Mode.Replicated.Replicas = &n

	if _, err := w.api.ServiceUpdate(ctx, s.ID, client.ServiceUpdateOptions{
		Version: s.Version,
		Spec:    s.Spec,
	}); err != nil {
		return []string{"lỗi scale: " + err.Error()}
	}
	// Trả về ngay, không chờ task Running — chờ đồng bộ ở đây sẽ vượt timeout RPC
	// của ai-worker rồi bị kill. Poll `service ls` để xem tiến độ.
	return []string{fmt.Sprintf("đã đặt %s về %d replica — poll `service ls` để xem tiến độ", name, n)}
}

func (w *worker) serviceRM(ctx context.Context, name string) []string {
	if _, err := w.api.ServiceRemove(ctx, name, client.ServiceRemoveOptions{}); err != nil {
		return []string{"lỗi xoá service: " + err.Error()}
	}
	return []string{"đã xoá service " + name}
}

func (w *worker) node(ctx context.Context, args []string) []string {
	switch strings.ToLower(args[0]) {
	case "ls", "list":
		return w.nodeLS(ctx)
	case "inspect":
		if len(args) < 2 {
			return []string{"node inspect: cần tên node"}
		}
		return w.nodeInspect(ctx, args[1])
	case "update":
		return w.nodeUpdate(ctx, args[1:])
	case "rm":
		if len(args) < 2 {
			return []string{"node rm: cần tên node"}
		}
		if _, err := w.api.NodeRemove(ctx, args[1], client.NodeRemoveOptions{}); err != nil {
			return []string{"lỗi xoá node: " + err.Error()}
		}
		return []string{"đã xoá node " + args[1]}
	}
	return usage()
}

func (w *worker) nodeLS(ctx context.Context) []string {
	res, err := w.api.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return []string{"lỗi list node: " + err.Error()}
	}
	if len(res.Items) == 0 {
		return []string{"không có node nào"}
	}
	items := res.Items
	sort.Slice(items, func(i, j int) bool {
		return items[i].Description.Hostname < items[j].Description.Hostname
	})
	rows := []string{headNodeLS}
	for i := range items {
		n := &items[i]
		rows = append(rows, strings.Join([]string{
			n.Description.Hostname, string(n.Status.State),
			string(n.Spec.Availability), nodeRole(n),
		}, "\t"))
	}
	return append([]string{fmt.Sprintf("%d node", len(items))}, table(rows)...)
}

func (w *worker) nodeInspect(ctx context.Context, name string) []string {
	res, err := w.api.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return []string{"lỗi list node: " + err.Error()}
	}
	for i := range res.Items {
		n := &res.Items[i]
		if n.Description.Hostname != name && !strings.HasPrefix(n.ID, name) {
			continue
		}
		return []string{
			"hostname: " + n.Description.Hostname,
			"id: " + shortID(n.ID),
			"status: " + string(n.Status.State) + " (" + n.Status.Addr + ")",
			"availability: " + string(n.Spec.Availability),
			"role: " + nodeRole(n),
			"engine: " + n.Description.Engine.EngineVersion,
			fmt.Sprintf("tài nguyên: %d CPU, %d MB RAM",
				n.Description.Resources.NanoCPUs/nanoCPUsPerCPU,
				n.Description.Resources.MemoryBytes/bytesPerMB),
		}
	}
	return []string{"không có node tên " + name}
}

// nodeUpdate chỉ mở đúng --availability: drain/pause/active là việc vận hành hay
// dùng, còn label/role thì để CLI làm.
func (w *worker) nodeUpdate(ctx context.Context, args []string) []string {
	if len(args) < 3 || args[1] != availabilityFlag {
		return []string{"node update: chỉ hỗ trợ `node update <tên> " + availabilityFlag + " <active|pause|drain>`"}
	}
	want := swarm.NodeAvailability(strings.ToLower(args[2]))
	switch want {
	case swarm.NodeAvailabilityActive, swarm.NodeAvailabilityPause, swarm.NodeAvailabilityDrain:
	default:
		return []string{"availability lạ: " + args[2]}
	}

	res, err := w.api.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return []string{"lỗi list node: " + err.Error()}
	}
	for i := range res.Items {
		n := &res.Items[i]
		if n.Description.Hostname != args[0] && !strings.HasPrefix(n.ID, args[0]) {
			continue
		}
		spec := n.Spec
		spec.Availability = want
		if _, err := w.api.NodeUpdate(ctx, n.ID, client.NodeUpdateOptions{
			Version: n.Version,
			Spec:    spec,
		}); err != nil {
			return []string{"lỗi update node: " + err.Error()}
		}
		return []string{"đã đặt " + n.Description.Hostname + " thành " + string(want)}
	}
	return []string{"không có node tên " + args[0]}
}

func (w *worker) stack(ctx context.Context, args []string) []string {
	switch strings.ToLower(args[0]) {
	case "ls", "list":
		return w.stackLS(ctx)
	case "services":
		if len(args) < 2 {
			return []string{"stack services: cần tên stack"}
		}
		return w.serviceLS(ctx, client.Filters{}.Add("label", stackLabel+"="+args[1]))
	}
	return usage()
}

// stackLS gom service theo label com.docker.stack.namespace — Engine API không
// có khái niệm stack, docker CLI cũng chỉ đếm label này.
func (w *worker) stackLS(ctx context.Context) []string {
	res, err := w.api.ServiceList(ctx, client.ServiceListOptions{})
	if err != nil {
		return []string{"lỗi list service: " + err.Error()}
	}
	count := map[string]int{}
	for i := range res.Items {
		if name := res.Items[i].Spec.Labels[stackLabel]; name != "" {
			count[name]++
		}
	}
	if len(count) == 0 {
		return []string{"không có stack nào"}
	}
	names := make([]string, 0, len(count))
	for name := range count {
		names = append(names, name)
	}
	sort.Strings(names)

	rows := []string{headStackLS}
	for _, name := range names {
		rows = append(rows, name+"\t"+strconv.Itoa(count[name]))
	}
	return append([]string{fmt.Sprintf("%d stack", len(names))}, table(rows)...)
}

// nodeHostnames map node id -> hostname để bảng task in tên máy chứ không in id.
// Lỗi ở đây không đáng làm hỏng cả lệnh: trả map rỗng, caller in id ngắn.
func (w *worker) nodeHostnames(ctx context.Context) map[string]string {
	res, err := w.api.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return nil
	}
	hosts := make(map[string]string, len(res.Items))
	for i := range res.Items {
		hosts[res.Items[i].ID] = res.Items[i].Description.Hostname
	}
	return hosts
}
