package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultPort = 22

// Server là một máy chủ trong sổ địa chỉ. Không có trường mật khẩu: xác thực
// chỉ qua ssh-agent hoặc private key, để không phải lưu secret ra đĩa.
type Server struct {
	Name    string    `json:"name"`
	Host    string    `json:"host"`
	Port    int       `json:"port"`
	User    string    `json:"user"`
	KeyPath string    `json:"key_path,omitempty"` // "" = ssh-agent + key mặc định trong ~/.ssh
	Tags    []string  `json:"tags,omitempty"`
	Note    string    `json:"note,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

func (s Server) Addr() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

func (s Server) Target() string { return s.User + "@" + s.Addr() }

func (s Server) hasTag(tag string) bool {
	for _, t := range s.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// store là sổ địa chỉ trên đĩa. Ghi bằng temp file + rename để lần ghi hỏng
// không làm mất inventory cũ.
type store struct {
	path    string
	servers []Server
}

// storePath: $K8SC_SERVERS_FILE nếu có (dùng cho test), mặc định
// ~/.k8s-commander/servers.json.
func storePath() (string, error) {
	if p := os.Getenv("K8SC_SERVERS_FILE"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".k8s-commander", "servers.json"), nil
}

func loadStore() (*store, error) {
	path, err := storePath()
	if err != nil {
		return nil, err
	}
	st := &store{path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil // chưa có file = sổ rỗng, không phải lỗi
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, &st.servers); err != nil {
		return nil, fmt.Errorf("%s hỏng: %w", path, err)
	}
	st.sort()
	return st, nil
}

func (st *store) sort() {
	sort.Slice(st.servers, func(i, j int) bool { return st.servers[i].Name < st.servers[j].Name })
}

func (st *store) save() error {
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	st.sort()
	data, err := json.MarshalIndent(st.servers, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

func (st *store) find(name string) (*Server, bool) {
	for i := range st.servers {
		if st.servers[i].Name == name {
			return &st.servers[i], true
		}
	}
	return nil, false
}

func (st *store) put(s Server) {
	if existing, ok := st.find(s.Name); ok {
		*existing = s
		return
	}
	st.servers = append(st.servers, s)
	st.sort()
}

func (st *store) remove(name string) (Server, bool) {
	for i := range st.servers {
		if st.servers[i].Name == name {
			removed := st.servers[i]
			st.servers = append(st.servers[:i], st.servers[i+1:]...)
			return removed, true
		}
	}
	return Server{}, false
}

// resolve đổi selector thành danh sách server: "all", "@tag", "node/<tên>",
// hoặc tên cụ thể.
func (st *store) resolve(selector string) ([]Server, error) {
	switch {
	case strings.HasPrefix(selector, nodeSelector):
		return resolveNode(st, selector)
	case selector == "all" || selector == "*":
		if len(st.servers) == 0 {
			return nil, fmt.Errorf("sổ server đang rỗng")
		}
		return append([]Server(nil), st.servers...), nil
	case strings.HasPrefix(selector, "@"):
		tag := strings.TrimPrefix(selector, "@")
		var out []Server
		for _, s := range st.servers {
			if s.hasTag(tag) {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("không server nào có tag %q", tag)
		}
		return out, nil
	default:
		s, ok := st.find(selector)
		if !ok {
			return nil, fmt.Errorf("không có server %q (xem `server list`)", selector)
		}
		return []Server{*s}, nil
	}
}

// parseTarget đọc "[user@]host[:port]" và trả về user/host/port đã điền mặc định.
// Hỗ trợ IPv6 dạng "[::1]:22".
func parseTarget(target string) (userName, host string, port int, err error) {
	rest := target
	if at := strings.LastIndex(target, "@"); at >= 0 {
		userName, rest = target[:at], target[at+1:]
	}
	if userName == "" {
		userName = currentUser()
	}

	port = defaultPort
	host = rest
	if h, p, splitErr := net.SplitHostPort(rest); splitErr == nil {
		parsed, convErr := strconv.Atoi(p)
		if convErr != nil || parsed <= 0 || parsed > 65535 {
			return "", "", 0, fmt.Errorf("port không hợp lệ: %q", p)
		}
		host, port = h, parsed
	} else {
		host = strings.Trim(rest, "[]") // "[::1]" không kèm port
	}
	if host == "" {
		return "", "", 0, fmt.Errorf("thiếu host trong %q", target)
	}
	return userName, host, port, nil
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "root"
}
