package main

// Quản lý cluster: đọc/sửa kubeconfig (context + cluster + user) và kiểm tra
// kết nối tới API server.
//
//	cluster list                       liệt kê context kèm endpoint
//	cluster info [tên]                 version + số node của context
//	cluster test [tên|all]             đo kết nối tới API server
//	cluster use <tên> [--persist]      đổi context (thêm --persist để ghi kubeconfig)
//	cluster add <tên> --server <url> ...
//	cluster rm <tên> --yes
//
// add/rm/--persist GHI vào kubeconfig thật (mặc định ~/.kube/config), nên rm
// bắt buộc --yes và mọi lệnh ghi đều in ra đường dẫn file đã sửa.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func clusterUsage() []string {
	return []string{
		"lệnh cluster:",
		"  cluster list                        context + endpoint trong kubeconfig",
		"  cluster info [tên]                  version, số node của context",
		"  cluster test [tên|all]              đo kết nối tới API server",
		"  cluster use <tên> [--persist]       đổi context (--persist ghi vào kubeconfig)",
		"  cluster add <tên> --server <url> [--ca <file>|--insecure]",
		"                                      [--token-env <VAR>|--token-file <f>|--token <t>]",
		"                                      [--client-cert <f> --client-key <f>] [--ns <ns>]",
		"  cluster rm <tên> --yes              xoá context khỏi kubeconfig",
	}
}

func (w *worker) cluster(args []string) []string {
	if len(args) == 0 {
		return w.clusterList()
	}
	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		return clusterUsage()
	case "list", "ls":
		return w.clusterList()
	case "info":
		name := w.currentContext()
		if len(args) > 1 {
			name = args[1]
		}
		return w.clusterInfo(name)
	case "test", "ping":
		selector := w.currentContext()
		if len(args) > 1 {
			selector = args[1]
		}
		return w.clusterTest(selector)
	case "use", "switch":
		if len(args) < 2 {
			return []string{"cluster use: thiếu tên context"}
		}
		if hasFlag(args[2:], "--persist") {
			return w.clusterUsePersist(args[1])
		}
		return w.useContext(args[1])
	case "add":
		return w.clusterAdd(args[1:])
	case "rm", "remove", "del":
		if len(args) < 2 {
			return []string{"cluster rm: thiếu tên context"}
		}
		return w.clusterRemove(args[1], hasFlag(args[2:], "--yes"))
	default:
		return append([]string{"cluster: không hiểu " + args[0]}, clusterUsage()...)
	}
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// currentContext: context đã `use` trong tiến trình, nếu chưa thì current-context của file.
func (w *worker) currentContext() string {
	if w.contextName != "" {
		return w.contextName
	}
	cfg, err := w.loader.Load()
	if err != nil {
		return ""
	}
	return cfg.CurrentContext
}

// kubeconfigPath: file đầu tiên trong thứ tự nạp — cũng là file clientcmd sẽ ghi vào.
func (w *worker) kubeconfigPath() string {
	files := w.loader.GetLoadingPrecedence()
	if len(files) == 0 {
		return clientcmd.RecommendedHomeFile
	}
	return files[0]
}

func (w *worker) clusterList() []string {
	cfg, err := w.loader.Load()
	if err != nil {
		return []string{"lỗi đọc kubeconfig: " + err.Error()}
	}
	if len(cfg.Contexts) == 0 {
		return []string{"kubeconfig không có context nào (" + strings.Join(w.loader.GetLoadingPrecedence(), ", ") + ")"}
	}
	current := w.currentContext()

	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)

	rows := []string{"CURRENT\tCONTEXT\tCLUSTER\tSERVER\tUSER\tNAMESPACE"}
	for _, name := range names {
		ctx := cfg.Contexts[name]
		server := "<không rõ>"
		if c := cfg.Clusters[ctx.Cluster]; c != nil {
			server = c.Server
			if c.InsecureSkipTLSVerify {
				server += " (insecure)"
			}
		}
		mark := ""
		if name == current {
			mark = "*"
		}
		ns := ctx.Namespace
		if ns == "" {
			ns = "default"
		}
		rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
			mark, name, ctx.Cluster, server, ctx.AuthInfo, ns))
	}
	header := fmt.Sprintf("%d context (%s)", len(names), strings.Join(w.loader.GetLoadingPrecedence(), ", "))
	return append([]string{header}, table(rows)...)
}

// clientFor dựng client cho một context bất kỳ mà không đổi context đang dùng.
func (w *worker) clientFor(contextName string) (*kubernetes.Clientset, error) {
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(w.loader, overrides).ClientConfig()
	if err != nil {
		return nil, err
	}
	restCfg.Timeout = opTimeout
	return kubernetes.NewForConfig(restCfg)
}

func (w *worker) clusterInfo(name string) []string {
	cfg, err := w.loader.Load()
	if err != nil {
		return []string{"lỗi đọc kubeconfig: " + err.Error()}
	}
	ctx, ok := cfg.Contexts[name]
	if !ok {
		return []string{"không có context: " + name}
	}
	out := []string{"context: " + name}
	if c := cfg.Clusters[ctx.Cluster]; c != nil {
		out = append(out, "  server:    "+c.Server)
		if c.CertificateAuthority != "" {
			out = append(out, "  ca:        "+c.CertificateAuthority)
		}
		if c.InsecureSkipTLSVerify {
			out = append(out, "  tls:       insecure-skip-tls-verify")
		}
	}
	ns := ctx.Namespace
	if ns == "" {
		ns = "default"
	}
	out = append(out, "  user:      "+ctx.AuthInfo, "  namespace: "+ns)

	client, err := w.clientFor(name)
	if err != nil {
		return append(out, "  trạng thái: lỗi tạo client: "+err.Error())
	}
	version, err := client.Discovery().ServerVersion()
	if err != nil {
		return append(out, "  trạng thái: không kết nối được: "+err.Error())
	}
	out = append(out, "  version:   "+version.GitVersion)

	reqCtx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	nodes, err := client.CoreV1().Nodes().List(reqCtx, metav1.ListOptions{})
	if err != nil {
		return append(out, "  nodes:     không list được: "+err.Error())
	}
	return append(out, fmt.Sprintf("  nodes:     %d", len(nodes.Items)))
}

func (w *worker) clusterTest(selector string) []string {
	cfg, err := w.loader.Load()
	if err != nil {
		return []string{"lỗi đọc kubeconfig: " + err.Error()}
	}
	var names []string
	if selector == "all" || selector == "*" {
		for name := range cfg.Contexts {
			names = append(names, name)
		}
		sort.Strings(names)
	} else {
		if _, ok := cfg.Contexts[selector]; !ok {
			return []string{"không có context: " + selector}
		}
		names = []string{selector}
	}
	if len(names) == 0 {
		return []string{"kubeconfig không có context nào"}
	}

	out := make([]string, 0, len(names))
	for _, name := range names {
		start := time.Now()
		client, err := w.clientFor(name)
		if err != nil {
			out = append(out, name+": lỗi tạo client: "+err.Error())
			continue
		}
		version, err := client.Discovery().ServerVersion()
		if err != nil {
			out = append(out, name+": FAIL sau "+time.Since(start).Round(time.Millisecond).String()+": "+err.Error())
			continue
		}
		out = append(out, fmt.Sprintf("%s: OK %s (%s)",
			name, version.GitVersion, time.Since(start).Round(time.Millisecond)))
	}
	return out
}

// clusterUsePersist ghi current-context vào kubeconfig — đổi cả cho kubectl
// ngoài app, nên phải gọi kèm --persist chứ không mặc định.
func (w *worker) clusterUsePersist(name string) []string {
	cfg, err := w.loader.Load()
	if err != nil {
		return []string{"lỗi đọc kubeconfig: " + err.Error()}
	}
	if _, ok := cfg.Contexts[name]; !ok {
		return []string{"không có context: " + name}
	}
	cfg.CurrentContext = name
	path := w.kubeconfigPath()
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return []string{"lỗi ghi kubeconfig: " + err.Error()}
	}
	w.contextName = name
	w.client = nil
	return []string{"đã ghi current-context = " + name + " vào " + path}
}

func (w *worker) clusterAdd(args []string) []string {
	if len(args) == 0 {
		return []string{"cluster add: thiếu tên context"}
	}
	name := args[0]
	cluster := clientcmdapi.NewCluster()
	auth := clientcmdapi.NewAuthInfo()
	var namespace string
	var warnings []string

	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		flag := rest[i]
		if flag == "--insecure" || flag == "--insecure-skip-tls-verify" {
			cluster.InsecureSkipTLSVerify = true
			continue
		}
		// Các flag còn lại đều cần 1 giá trị đi kèm.
		if i+1 >= len(rest) {
			return []string{"cluster add: " + flag + " thiếu giá trị"}
		}
		i++
		value := rest[i]
		switch flag {
		case "--server":
			cluster.Server = value
		case "--ca", "--certificate-authority":
			cluster.CertificateAuthority = value
		case "--token":
			auth.Token = value
			// Dòng lệnh này đã nằm trong buffer log của app rồi.
			warnings = append(warnings, "cảnh báo: token gõ thẳng đã lọt vào log terminal — ưu tiên --token-env/--token-file")
		case "--token-env":
			token := os.Getenv(value)
			if token == "" {
				return []string{"cluster add: biến môi trường " + value + " rỗng"}
			}
			auth.Token = token
		case "--token-file":
			data, err := os.ReadFile(value)
			if err != nil {
				return []string{"cluster add: đọc " + value + ": " + err.Error()}
			}
			auth.Token = strings.TrimSpace(string(data))
		case "--client-cert", "--client-certificate":
			auth.ClientCertificate = value
		case "--client-key":
			auth.ClientKey = value
		case "--ns", "--namespace":
			namespace = value
		default:
			return []string{"cluster add: không hiểu tham số " + flag}
		}
	}

	if cluster.Server == "" {
		return []string{"cluster add: thiếu --server <https://host:port>"}
	}
	if cluster.CertificateAuthority == "" && !cluster.InsecureSkipTLSVerify {
		warnings = append(warnings, "không có --ca: sẽ dùng CA hệ thống, tự-ký sẽ lỗi x509 (thêm --ca <file> hoặc --insecure)")
	}
	if auth.Token == "" && auth.ClientCertificate == "" {
		warnings = append(warnings, "không có credential: thêm --token-env/--token-file hoặc --client-cert/--client-key")
	}

	cfg, err := w.loader.Load()
	if err != nil {
		return []string{"lỗi đọc kubeconfig: " + err.Error()}
	}
	if _, exists := cfg.Contexts[name]; exists {
		return []string{"đã có context " + name + " — xoá bằng `cluster rm " + name + " --yes` trước"}
	}
	// Đặt tên cluster/user theo context để `cluster rm` dọn được đúng phần mình tạo.
	cfg.Clusters[name] = cluster
	cfg.AuthInfos[name] = auth
	cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name, Namespace: namespace}

	path := w.kubeconfigPath()
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return []string{"lỗi ghi kubeconfig: " + err.Error()}
	}
	out := []string{
		"đã thêm context " + name + " -> " + cluster.Server,
		"ghi vào " + path,
	}
	out = append(out, warnings...)
	return append(out, "kiểm tra: cluster test "+name)
}

// clusterRemove xoá context, kèm cluster/user nếu không context nào khác dùng.
func (w *worker) clusterRemove(name string, confirmed bool) []string {
	cfg, err := w.loader.Load()
	if err != nil {
		return []string{"lỗi đọc kubeconfig: " + err.Error()}
	}
	ctx, ok := cfg.Contexts[name]
	if !ok {
		return []string{"không có context: " + name}
	}
	path := w.kubeconfigPath()
	if !confirmed {
		return []string{
			fmt.Sprintf("sẽ xoá context %q (cluster %s, user %s) khỏi %s", name, ctx.Cluster, ctx.AuthInfo, path),
			"thêm --yes để thực hiện: cluster rm " + name + " --yes",
		}
	}

	delete(cfg.Contexts, name)
	removed := []string{"context " + name}
	// Cluster/user dùng chung với context khác thì giữ lại.
	clusterUsed, authUsed := false, false
	for _, c := range cfg.Contexts {
		if c.Cluster == ctx.Cluster {
			clusterUsed = true
		}
		if c.AuthInfo == ctx.AuthInfo {
			authUsed = true
		}
	}
	if !clusterUsed {
		delete(cfg.Clusters, ctx.Cluster)
		removed = append(removed, "cluster "+ctx.Cluster)
	}
	if !authUsed {
		delete(cfg.AuthInfos, ctx.AuthInfo)
		removed = append(removed, "user "+ctx.AuthInfo)
	}
	if cfg.CurrentContext == name {
		cfg.CurrentContext = ""
	}
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return []string{"lỗi ghi kubeconfig: " + err.Error()}
	}
	if w.contextName == name {
		w.contextName = ""
		w.client = nil
	}
	return []string{"đã xoá " + strings.Join(removed, ", ") + " khỏi " + path}
}
