// Micro-binary K8s: đọc lệnh từ stdin (1 dòng = 1 lệnh), trả kết quả ra stdout.
// Build ra modules/k8s-worker. Supervisor gom stdout vào buffer log cho Flutter.
//
// Lệnh hỗ trợ:
//
//	get pods [-n <ns> | -A]   get nodes   get ns
//	ctx                        # liệt kê context, * = đang dùng
//	use <context>              # đổi context (chỉ trong tiến trình này)
//	cluster ...                # quản lý cluster trong kubeconfig, xem cluster.go
//	help
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"my-k8s-commander/pkg/common"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	opTimeout = 20 * time.Second
	prefix    = "[k8s-worker]"

	// Trạng thái pod hiển thị, xem podStatus.
	statusTerminating = "Terminating"
	statusInitPrefix  = "Init:"
	reasonCompleted   = "Completed"
)

type worker struct {
	loader      clientcmd.ClientConfigLoader
	contextName string // "" = dùng current-context của kubeconfig
	client      *kubernetes.Clientset
}

func main() {
	w := &worker{loader: clientcmd.NewDefaultClientConfigLoadingRules()}
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
	switch strings.ToLower(fields[0]) {
	case "help":
		return usage()
	case "ctx", "contexts":
		return w.listContexts()
	case "cluster", "clusters":
		return w.cluster(fields[1:])
	case "node":
		return w.node(fields[1:])
	case "helm":
		return w.helm(fields[1:])
	case "use":
		if len(fields) < 2 {
			return []string{"use: thiếu tên context"}
		}
		return w.useContext(fields[1])
	case "kubectl":
		// Cho phép gõ quen tay "kubectl get pods" — bỏ chữ kubectl rồi xử lý tiếp.
		if len(fields) < 2 {
			return usage()
		}
		return w.handle(strings.Join(fields[1:], " "))
	case "get":
		if len(fields) < 2 {
			return []string{"get: thiếu resource (pods|nodes|ns)"}
		}
		return w.get(fields[1], fields[2:])
	default:
		return append([]string{"lệnh không hiểu: " + fields[0]}, usage()...)
	}
}

func usage() []string {
	return append([]string{
		"lệnh: get pods [-n <ns> | -A] | get nodes | get ns | ctx | use <context> | help",
		"      node addr  # địa chỉ node dạng TSV, cho worker khác gọi",
		"      helm ...   # cài/nâng cấp chart, xem `helm help`",
	}, clusterUsage()...)
}

func (w *worker) listContexts() []string {
	cfg, err := w.loader.Load()
	if err != nil {
		return []string{"lỗi đọc kubeconfig: " + err.Error()}
	}
	current := cfg.CurrentContext
	if w.contextName != "" {
		current = w.contextName
	}
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]string, 0, len(names)+1)
	out = append(out, "kubeconfig: "+strings.Join(w.loader.GetLoadingPrecedence(), ", "))
	for _, name := range names {
		mark := " "
		if name == current {
			mark = "*"
		}
		out = append(out, mark+" "+name)
	}
	return out
}

func (w *worker) useContext(name string) []string {
	cfg, err := w.loader.Load()
	if err != nil {
		return []string{"lỗi đọc kubeconfig: " + err.Error()}
	}
	if _, ok := cfg.Contexts[name]; !ok {
		return []string{"không có context: " + name}
	}
	w.contextName = name
	w.client = nil // buộc dựng lại client cho context mới
	return []string{"đang dùng context: " + name}
}

func (w *worker) clientConfig() clientcmd.ClientConfig {
	overrides := &clientcmd.ConfigOverrides{}
	if w.contextName != "" {
		overrides.CurrentContext = w.contextName
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(w.loader, overrides)
}

// clientset dựng client-go từ kubeconfig, cache lại cho các lệnh sau.
func (w *worker) clientset() (*kubernetes.Clientset, error) {
	if w.client != nil {
		return w.client, nil
	}
	restCfg, err := w.clientConfig().ClientConfig()
	if err != nil {
		return nil, err
	}
	restCfg.Timeout = opTimeout
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	w.client = client
	return client, nil
}

func (w *worker) get(resource string, args []string) []string {
	// Chặn trước khi gọi API: flag lạ mà bỏ qua im lặng thì người gọi (nhất là
	// ai-worker) tưởng output đã được lọc theo ý mình.
	if bad := unsupportedArgs(args); len(bad) > 0 {
		return []string{
			"get: không hiểu " + strings.Join(bad, " "),
			"chỉ hỗ trợ -n <ns> và -A. Không có -o/--output, --field-selector, " +
				"--selector, lọc theo tên, hay pipe/grep — tự lọc trên output trả về.",
		}
	}
	client, err := w.clientset()
	if err != nil {
		return []string{"lỗi tạo client: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	switch strings.ToLower(strings.TrimSuffix(resource, "s")) {
	case "pod", "po":
		return w.getPods(ctx, client, args)
	case "node", "no":
		return w.getNodes(ctx, client)
	case "n", "ns", "namespace":
		return w.getNamespaces(ctx, client)
	default:
		return []string{"chưa hỗ trợ resource: " + resource}
	}
}

// unsupportedArgs trả về những token của `get` mà worker không hiểu. Chỉ -n <ns>
// và -A là hợp lệ; mọi thứ khác phải báo lỗi chứ không âm thầm bỏ qua.
func unsupportedArgs(args []string) []string {
	var bad []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-A", "--all-namespaces":
		case "-n", "--namespace":
			i++ // giá trị đi kèm, không phải flag lạ
		default:
			bad = append(bad, args[i])
		}
	}
	return bad
}

// namespaceFrom đọc -n <ns> / -A / --all-namespaces. Mặc định lấy namespace của context.
func (w *worker) namespaceFrom(args []string) string {
	for i, a := range args {
		switch a {
		case "-A", "--all-namespaces":
			return metav1.NamespaceAll
		case "-n", "--namespace":
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	ns, _, err := w.clientConfig().Namespace()
	if err != nil || ns == "" {
		return "default"
	}
	return ns
}

func (w *worker) getPods(ctx context.Context, client *kubernetes.Clientset, args []string) []string {
	ns := w.namespaceFrom(args)
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return []string{"lỗi list pods: " + err.Error()}
	}
	scope := "namespace " + ns
	if ns == metav1.NamespaceAll {
		scope = "tất cả namespace"
	}
	if len(pods.Items) == 0 {
		return []string{"không có pod nào trong " + scope}
	}
	rows := []string{"NAMESPACE\tNAME\tREADY\tSTATUS\tRESTARTS\tAGE"}
	for i := range pods.Items {
		p := &pods.Items[i]
		ready, restarts := containerStats(p)
		rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s",
			p.Namespace, p.Name, ready, podStatus(p), restarts, age(p.CreationTimestamp)))
	}
	return append([]string{fmt.Sprintf("%d pod trong %s", len(pods.Items), scope)}, table(rows)...)
}

// podStatus: trạng thái hiển thị, giống kubectl — KHÔNG phải p.Status.Phase.
// Pod đang CrashLoopBackOff vẫn có phase "Running", in phase ra thì đúng câu hỏi
// hay gặp nhất ("pod nào crash?") lại trả lời sai.
func podStatus(p *corev1.Pod) string {
	if p.DeletionTimestamp != nil {
		return statusTerminating
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if r := containerProblem(cs); r != "" {
			return statusInitPrefix + r
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if r := containerProblem(cs); r != "" {
			return r
		}
	}
	return string(p.Status.Phase)
}

// containerProblem: lý do container chưa chạy được (CrashLoopBackOff,
// ImagePullBackOff, Error, OOMKilled, ContainerCreating...). "" = không có gì lạ.
func containerProblem(cs corev1.ContainerStatus) string {
	if w := cs.State.Waiting; w != nil && w.Reason != "" {
		return w.Reason
	}
	if t := cs.State.Terminated; t != nil && t.Reason != "" && t.Reason != reasonCompleted {
		return t.Reason
	}
	return ""
}

func containerStats(p *corev1.Pod) (ready string, restarts int32) {
	var readyCount int
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			readyCount++
		}
		restarts += cs.RestartCount
	}
	return fmt.Sprintf("%d/%d", readyCount, len(p.Spec.Containers)), restarts
}

func (w *worker) getNodes(ctx context.Context, client *kubernetes.Clientset) []string {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return []string{"lỗi list nodes: " + err.Error()}
	}
	if len(nodes.Items) == 0 {
		return []string{"không có node nào"}
	}
	rows := []string{"NAME\tSTATUS\tVERSION\tAGE"}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		status := "NotReady"
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				status = "Ready"
				break
			}
		}
		rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s",
			n.Name, status, n.Status.NodeInfo.KubeletVersion, age(n.CreationTimestamp)))
	}
	return append([]string{fmt.Sprintf("%d node", len(nodes.Items))}, table(rows)...)
}

func (w *worker) getNamespaces(ctx context.Context, client *kubernetes.Clientset) []string {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return []string{"lỗi list namespaces: " + err.Error()}
	}
	rows := []string{"NAME\tSTATUS\tAGE"}
	for i := range list.Items {
		ns := &list.Items[i]
		rows = append(rows, fmt.Sprintf("%s\t%s\t%s", ns.Name, ns.Status.Phase, age(ns.CreationTimestamp)))
	}
	return append([]string{fmt.Sprintf("%d namespace", len(list.Items))}, table(rows)...)
}

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

func age(t metav1.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}
