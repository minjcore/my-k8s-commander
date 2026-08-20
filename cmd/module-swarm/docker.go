package main

// Dựng client Docker và hỗ trợ DOCKER_HOST=ssh://.
//
// SDK không tự nói được ssh:// — `connhelper` nằm trong docker/cli chứ không
// nằm trong module client. Nên ở đây tự nối theo đúng cách CLI làm: chạy
// `ssh <host> docker system dial-stdio` rồi bơm HTTP qua stdin/stdout của tiến
// trình đó. Máy từ xa phải có docker CLI.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

const (
	dockerHostEnvVar = "DOCKER_HOST"
	sshScheme        = "ssh://"

	// Host giả đưa cho SDK khi đi qua ssh: mọi kết nối đều bị DialContext bắt
	// lại nên địa chỉ này không bao giờ được nối tới thật.
	sshDummyHost = "tcp://docker"

	sshBin           = "ssh"
	dialStdioCommand = "docker system dial-stdio"
)

// dockerAPI: đúng những method worker dùng. Tách interface để test bằng fake,
// không cần swarm thật.
type dockerAPI interface {
	Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error)
	ServiceList(ctx context.Context, options client.ServiceListOptions) (client.ServiceListResult, error)
	ServiceInspect(ctx context.Context, id string, options client.ServiceInspectOptions) (client.ServiceInspectResult, error)
	ServiceUpdate(ctx context.Context, id string, options client.ServiceUpdateOptions) (client.ServiceUpdateResult, error)
	ServiceRemove(ctx context.Context, id string, options client.ServiceRemoveOptions) (client.ServiceRemoveResult, error)
	NodeList(ctx context.Context, options client.NodeListOptions) (client.NodeListResult, error)
	NodeUpdate(ctx context.Context, id string, options client.NodeUpdateOptions) (client.NodeUpdateResult, error)
	NodeRemove(ctx context.Context, id string, options client.NodeRemoveOptions) (client.NodeRemoveResult, error)
	TaskList(ctx context.Context, options client.TaskListOptions) (client.TaskListResult, error)
}

// resolveHost chọn daemon theo đúng thứ tự ưu tiên của docker CLI:
// DOCKER_HOST -> docker context đang chọn -> socket mặc định.
//
// Phải tự đọc context vì đó là khái niệm của CLI, SDK không biết: bỏ qua nó thì
// máy dùng colima/Rancher/OrbStack sẽ thấy "daemon not running" trong khi
// `docker ps` gõ tay vẫn chạy.
func resolveHost() (host, source string) {
	if h := os.Getenv(dockerHostEnvVar); h != "" {
		return h, dockerHostEnvVar
	}
	if h, err := contextHost(); err == nil && h != "" {
		return h, "docker context"
	}
	return "", "socket mặc định"
}

// dockerHost in ra chỗ đang điều khiển, kèm nguồn để người dùng biết vì sao.
func dockerHost() string {
	host, source := resolveHost()
	if host == "" {
		return source
	}
	return host + " (" + source + ")"
}

// newDockerClient dựng client. Lỗi ở đây được trả lên để worker in hướng dẫn
// thay vì crash — supervisor sẽ restart mãi nếu worker chết.
func newDockerClient() (*client.Client, error) {
	host, _ := resolveHost()
	if strings.HasPrefix(host, sshScheme) {
		return sshClient(host)
	}
	if host != "" {
		return client.New(client.WithHost(host), client.WithAPIVersionNegotiation())
	}
	return client.New(client.FromEnv, client.WithAPIVersionNegotiation())
}

func sshClient(host string) (*client.Client, error) {
	target, err := sshTarget(host)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath(sshBin); err != nil {
		return nil, fmt.Errorf("cần ssh trên máy này để dùng %s", host)
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialViaSSH(ctx, target)
	}
	return client.New(
		client.WithHost(sshDummyHost),
		client.WithDialContext(dial),
		client.WithAPIVersionNegotiation(),
	)
}

// sshTarget đổi "ssh://user@host:2222" thành đối số cho ssh: "-p 2222 user@host".
func sshTarget(host string) ([]string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("%s không đọc được: %w", dockerHostEnvVar, err)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("%s thiếu host: %s", dockerHostEnvVar, host)
	}
	var args []string
	if p := u.Port(); p != "" {
		args = append(args, "-p", p)
	}
	dest := u.Hostname()
	if u.User != nil && u.User.Username() != "" {
		dest = u.User.Username() + "@" + dest
	}
	return append(args, dest), nil
}

func dialViaSSH(ctx context.Context, target []string) (net.Conn, error) {
	args := append(append([]string{}, target...), strings.Fields(dialStdioCommand)...)
	cmd := exec.CommandContext(ctx, sshBin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// stderr của ssh đi thẳng ra stderr worker: supervisor log lại, người dùng
	// thấy được "Permission denied" thay vì một lỗi HTTP mù mờ.
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &stdioConn{in: stdin, out: stdout, cmd: cmd}, nil
}

// stdioConn biến stdin/stdout của một tiến trình thành net.Conn.
// Deadline không hỗ trợ được (pipe không có), nên timeout dựa vào context của
// từng lệnh — đã có ở lớp trên (dockerTimeout).
type stdioConn struct {
	in  interface{ Write([]byte) (int, error) }
	out interface{ Read([]byte) (int, error) }
	cmd *exec.Cmd
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.in.Write(p) }

func (c *stdioConn) Close() error {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return nil
}

func (c *stdioConn) LocalAddr() net.Addr                { return stdioAddr{} }
func (c *stdioConn) RemoteAddr() net.Addr               { return stdioAddr{} }
func (c *stdioConn) SetDeadline(t time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(t time.Time) error { return nil }

type stdioAddr struct{}

func (stdioAddr) Network() string { return "ssh" }
func (stdioAddr) String() string  { return "docker-dial-stdio" }
