package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// TestSSHRoundTrip dựng một sshd in-process rồi đi hết đường: add -> run (chặn
// vì host lạ) -> trust -> run (chạy được). Không đụng tới ~/.ssh thật: HOME và
// sổ server đều trỏ vào t.TempDir().
func TestSSHRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("K8SC_SERVERS_FILE", filepath.Join(home, "servers.json"))
	t.Setenv("SSH_AUTH_SOCK", "") // đừng để agent thật của máy chen vào

	clientKeyPath := writeKey(t, filepath.Join(home, "id_ed25519"))
	clientSigner := parseKey(t, clientKeyPath)
	addr := startTestServer(t, clientSigner.PublicKey())

	if out := handle("server add box " + addr + " -k " + clientKeyPath); !contains(out, "đã thêm box") {
		t.Fatalf("add: %v", out)
	}

	// known_hosts chưa có gì -> phải chặn kèm hướng dẫn, không được cứ thế kết nối.
	out := handle("server run box echo hi")
	if !contains(out, "server trust") {
		t.Fatalf("run trước trust phải hướng dẫn trust, nhận: %v", out)
	}

	if out := handle("server trust box"); !contains(out, "SHA256:") {
		t.Fatalf("trust: %v", out)
	}

	out = handle("server run box echo hi")
	if !contains(out, "box| xin chào từ sshd giả") {
		t.Fatalf("run sau trust phải có output, nhận: %v", out)
	}
	if contains(out, "exit ") || contains(out, "lỗi:") {
		t.Fatalf("run không được báo lỗi, nhận: %v", out)
	}

	if out := handle("server test box"); !contains(out, "OK") {
		t.Fatalf("test: %v", out)
	}
}

// Hồi quy: ssh-agent RỖNG từng nuốt mất key thật.
//
// x/crypto loại AuthMethod theo tên, nên hai method cùng tên "publickey" thì
// cái đầu fail là cái sau không bao giờ được thử — agent rỗng đứng trước làm
// hỏng cả lần xác thực dù key hoàn toàn hợp lệ. Bug này chỉ lộ ra khi gặp sshd
// thật, sshd giả trong test cũ vẫn xanh.
func TestSSHVoiAgentRong(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("K8SC_SERVERS_FILE", filepath.Join(home, "servers.json"))

	clientKeyPath := writeKey(t, filepath.Join(home, "id_ed25519"))
	clientSigner := parseKey(t, clientKeyPath)
	addr := startTestServer(t, clientSigner.PublicKey())
	t.Setenv("SSH_AUTH_SOCK", startEmptyAgent(t))

	_, host, port, err := parseTarget(addr)
	if err != nil {
		t.Fatal(err)
	}
	s := Server{Name: "box", Host: host, Port: port, User: "u", KeyPath: clientKeyPath}

	// Bất biến giữ cho bug không quay lại: mọi signer nằm trong ĐÚNG 1 method.
	methods, notes, cleanup := authMethods(s)
	defer cleanup()
	if len(methods) != 1 {
		t.Fatalf("muốn 1 AuthMethod publickey, nhận %d (%v)", len(methods), notes)
	}

	hostKey, err := probeHostKey(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendKnownHost(s, hostKey); err != nil {
		t.Fatal(err)
	}

	client, err := dial(s)
	if err != nil {
		t.Fatalf("agent rỗng không được làm hỏng xác thực bằng key: %v (đã thử: %v)", err, notes)
	}
	defer client.Close()
	if res := runCommand(client, "echo hi"); res.err != nil {
		t.Fatalf("run: %v", res.err)
	}
}

// startEmptyAgent chạy một ssh-agent không giữ key nào, trả về đường dẫn socket.
func startEmptyAgent(t *testing.T) string {
	t.Helper()
	// Socket unix có giới hạn ~104 ký tự đường dẫn trên macOS, t.TempDir() dài
	// nên đặt trong os.TempDir().
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("k8sc-agent-%d.sock", os.Getpid()))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = os.Remove(sock) })

	keyring := agent.NewKeyring()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()
	return sock
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in         string
		user, host string
		port       int
	}{
		{"10.0.0.5", currentUser(), "10.0.0.5", 22},
		{"ubuntu@10.0.0.5", "ubuntu", "10.0.0.5", 22},
		{"ubuntu@10.0.0.5:2222", "ubuntu", "10.0.0.5", 2222},
		{"[::1]:2222", currentUser(), "::1", 2222},
		{"[::1]", currentUser(), "::1", 22},
	}
	for _, c := range cases {
		user, host, port, err := parseTarget(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if user != c.user || host != c.host || port != c.port {
			t.Errorf("%s -> %s/%s/%d, muốn %s/%s/%d", c.in, user, host, port, c.user, c.host, c.port)
		}
	}
	if _, _, _, err := parseTarget("host:abc"); err == nil {
		t.Error("port chữ phải báo lỗi")
	}
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func writeKey(t *testing.T, path string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseKey(t *testing.T, path string) ssh.Signer {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// startTestServer chạy một sshd tối giản: chỉ nhận đúng authorized key, mọi
// lệnh exec đều trả cùng một dòng. Trả về "127.0.0.1:<port>".
func startTestServer(t *testing.T, authorized ssh.PublicKey) string {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(authorized.Marshal()) {
				return nil, fmt.Errorf("key lạ")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener đã đóng khi test xong
			}
			go serveConn(conn, cfg)
		}
	}()
	return ln.Addr().String()
}

func serveConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return // probeHostKey bỏ ngang sau handshake là chuyện bình thường
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "chỉ hỗ trợ session")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			return
		}
		go func() {
			defer ch.Close()
			for req := range chReqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				fmt.Fprintln(ch, "xin chào từ sshd giả")
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}()
	}
}
