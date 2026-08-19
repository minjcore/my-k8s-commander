// Micro-binary Server: quản lý sổ server SSH và chạy lệnh từ xa.
// Đọc lệnh từ stdin (1 dòng = 1 lệnh), trả kết quả ra stdout. Build ra modules/server-worker.
//
// Lệnh hỗ trợ (tiền tố "server"/"srv" là tuỳ chọn):
//
//	list                                   # sổ server
//	add <tên> [user@]host[:port] [-k key] [-t tag1,tag2] [--note "..."] [--force]
//	rm <tên>
//	trust <tên|@tag|all>                   # xem fingerprint rồi ghi vào known_hosts
//	test <tên|@tag|all>                    # thử SSH, in uptime
//	run <tên|@tag|all> <lệnh...>           # chạy lệnh từ xa
//	help
//
// Sổ server nằm ở ~/.k8s-commander/servers.json (0600). Không lưu mật khẩu:
// xác thực qua ssh-agent hoặc private key.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"my-k8s-commander/pkg/common"

	"golang.org/x/crypto/ssh"
)

const prefix = "[server-worker]"

func main() {
	out := bufio.NewWriter(os.Stdout)
	rpc := common.RPCMode()

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		for _, l := range handle(line) {
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

func handle(line string) []string {
	// UI gửi nguyên câu ("server list") nên bóc tiền tố ở đây.
	fields := strings.Fields(line)
	if verb := strings.ToLower(fields[0]); verb == "server" || verb == "srv" {
		if len(fields) == 1 {
			return usage()
		}
		line = strings.TrimSpace(line[len(fields[0]):])
		fields = fields[1:]
	}

	switch strings.ToLower(fields[0]) {
	case "help", "-h", "--help":
		return usage()
	case "list", "ls":
		return list()
	case "nodes", "node":
		return nodesTable()
	case "add":
		return add(fields[1:])
	case "rm", "remove", "del":
		if len(fields) < 2 {
			return []string{"rm: thiếu tên server"}
		}
		return remove(fields[1])
	case "trust":
		if len(fields) < 2 {
			return []string{"trust: thiếu tên server"}
		}
		return trust(fields[1])
	case "test", "ping":
		if len(fields) < 2 {
			return []string{"test: thiếu tên server (hoặc @tag / all)"}
		}
		return test(fields[1])
	case "run", "exec":
		if len(fields) < 3 {
			return []string{"run: cú pháp `run <tên|@tag|all> <lệnh...>`"}
		}
		// Lệnh từ xa giữ nguyên văn (quote, pipe, redirect) — chỉ cắt đúng 2 token đầu.
		afterVerb := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		command := strings.TrimSpace(strings.TrimPrefix(afterVerb, fields[1]))
		return run(fields[1], command)
	default:
		return append([]string{"lệnh không hiểu: " + fields[0]}, usage()...)
	}
}

func usage() []string {
	return []string{
		"lệnh server:",
		"  server list                                    sổ server",
		"  server nodes                                   node của cluster <-> entry trong sổ",
		"  server add <tên> [user@]host[:port] [-k <key>] [-t <tag,tag>] [--note \"...\"] [--force]",
		"  server rm <tên>                                xoá khỏi sổ",
		"  server trust <selector>                        thêm host key vào known_hosts",
		"  server test <selector>                         thử kết nối SSH",
		"  server run <selector> <lệnh...>                chạy lệnh từ xa",
		"selector: <tên> | @tag | all | node/<tên node> | node/all",
		"xác thực qua ssh-agent hoặc private key (không lưu mật khẩu)",
	}
}

func list() []string {
	st, err := loadStore()
	if err != nil {
		return []string{"lỗi đọc sổ server: " + err.Error()}
	}
	if len(st.servers) == 0 {
		return []string{"sổ server rỗng (" + st.path + ")", "thêm bằng: server add prod-1 ubuntu@10.0.0.5"}
	}
	rows := []string{"NAME\tTARGET\tAUTH\tTAGS\tNOTE"}
	for _, s := range st.servers {
		auth := "agent/default-key"
		if s.KeyPath != "" {
			auth = s.KeyPath
		}
		rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			s.Name, s.Target(), auth, strings.Join(s.Tags, ","), s.Note))
	}
	return append([]string{fmt.Sprintf("%d server (%s)", len(st.servers), st.path)}, table(rows)...)
}

func add(args []string) []string {
	if len(args) < 2 {
		return []string{"add: cú pháp `add <tên> [user@]host[:port] [-k <key>] [-t <tag,tag>] [--note \"...\"]`"}
	}
	name, target := args[0], args[1]
	if strings.HasPrefix(name, "@") || name == "all" {
		return []string{"add: tên không được là `all` hay bắt đầu bằng @ (trùng cú pháp selector)"}
	}

	s := Server{Name: name, AddedAt: time.Now()}
	var force bool
	rest := args[2:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "-k", "--key":
			if i+1 >= len(rest) {
				return []string{"add: -k thiếu đường dẫn key"}
			}
			i++
			s.KeyPath = rest[i]
		case "-t", "--tag", "--tags":
			if i+1 >= len(rest) {
				return []string{"add: -t thiếu tag"}
			}
			i++
			for _, t := range strings.Split(rest[i], ",") {
				if t = strings.TrimSpace(t); t != "" {
					s.Tags = append(s.Tags, t)
				}
			}
		case "--note":
			if i+1 >= len(rest) {
				return []string{"add: --note thiếu nội dung"}
			}
			i++
			s.Note = strings.Trim(strings.Join(rest[i:], " "), `"`)
			i = len(rest) // --note nuốt phần còn lại của dòng
		case "--force", "-f":
			force = true
		default:
			return []string{"add: không hiểu tham số " + rest[i]}
		}
	}

	userName, host, port, err := parseTarget(target)
	if err != nil {
		return []string{"add: " + err.Error()}
	}
	s.User, s.Host, s.Port = userName, host, port

	st, err := loadStore()
	if err != nil {
		return []string{"lỗi đọc sổ server: " + err.Error()}
	}
	if old, exists := st.find(name); exists && !force {
		return []string{fmt.Sprintf("đã có server %q (%s) — thêm --force để ghi đè", name, old.Target())}
	}
	st.put(s)
	if err := st.save(); err != nil {
		return []string{"lỗi ghi sổ server: " + err.Error()}
	}
	return []string{
		fmt.Sprintf("đã thêm %s -> %s", s.Name, s.Target()),
		"kiểm tra: server trust " + s.Name + " rồi server test " + s.Name,
	}
}

func remove(name string) []string {
	st, err := loadStore()
	if err != nil {
		return []string{"lỗi đọc sổ server: " + err.Error()}
	}
	removed, ok := st.remove(name)
	if !ok {
		return []string{"không có server " + name}
	}
	if err := st.save(); err != nil {
		return []string{"lỗi ghi sổ server: " + err.Error()}
	}
	// In lại entry để gõ lại được nếu xoá nhầm.
	return []string{
		"đã xoá " + removed.Name + " (" + removed.Target() + ")",
		"thêm lại: server add " + removed.Name + " " + removed.Target(),
	}
}

func trust(selector string) []string {
	st, err := loadStore()
	if err != nil {
		return []string{"lỗi đọc sổ server: " + err.Error()}
	}
	targets, err := st.resolve(selector)
	if err != nil {
		return []string{err.Error()}
	}

	out := []string{"known_hosts: " + knownHostsPath()}
	for _, s := range targets {
		key, err := probeHostKey(s)
		if err != nil {
			out = append(out, s.Name+": không lấy được host key: "+err.Error())
			continue
		}
		fp := ssh.FingerprintSHA256(key)
		line, err := appendKnownHost(s, key)
		if err != nil {
			out = append(out, s.Name+": ghi known_hosts lỗi: "+err.Error())
			continue
		}
		// TOFU: in fingerprint để người dùng còn đối chiếu với admin.
		out = append(out,
			fmt.Sprintf("%s (%s): đã thêm %s %s", s.Name, s.Addr(), key.Type(), fp),
			"  dòng: "+truncate(line, 100))
	}
	return out
}

func test(selector string) []string {
	return forEach(selector, func(s Server, client *ssh.Client) []string {
		res := runCommand(client, "uptime")
		if res.err != nil {
			return []string{s.Name + ": kết nối OK nhưng chạy uptime lỗi: " + res.err.Error()}
		}
		info := strings.TrimSpace(strings.Join(res.stdout, " "))
		if info == "" {
			info = string(client.ServerVersion())
		}
		return []string{fmt.Sprintf("%s (%s): OK — %s", s.Name, s.Target(), info)}
	})
}

func run(selector, command string) []string {
	return forEach(selector, func(s Server, client *ssh.Client) []string {
		out := []string{fmt.Sprintf("--- %s (%s) $ %s", s.Name, s.Target(), command)}
		res := runCommand(client, command)
		for _, l := range res.stdout {
			out = append(out, s.Name+"| "+l)
		}
		for _, l := range res.stderr {
			out = append(out, s.Name+"! "+l)
		}
		switch {
		case res.err != nil:
			out = append(out, s.Name+": lỗi: "+res.err.Error())
		case res.exitCode != 0:
			out = append(out, fmt.Sprintf("%s: exit %d", s.Name, res.exitCode))
		}
		return out
	})
}

// forEach mở SSH tới từng server của selector rồi gọi fn. Chạy tuần tự để log
// của các server không đan xen vào nhau.
func forEach(selector string, fn func(Server, *ssh.Client) []string) []string {
	st, err := loadStore()
	if err != nil {
		return []string{"lỗi đọc sổ server: " + err.Error()}
	}
	targets, err := st.resolve(selector)
	if err != nil {
		return []string{err.Error()}
	}

	var out []string
	for _, s := range targets {
		client, err := dial(s)
		if err != nil {
			out = append(out, s.Name+" ("+s.Target()+"): "+err.Error())
			if hint := hostKeyHint(s, err); hint != nil {
				for _, h := range hint {
					out = append(out, "  "+h)
				}
			} else if _, notes, cleanup := authMethods(s); len(notes) > 0 {
				cleanup()
				out = append(out, "  đã thử: "+strings.Join(notes, "; "))
			}
			continue
		}
		out = append(out, fn(s, client)...)
		_ = client.Close()
	}
	return out
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
