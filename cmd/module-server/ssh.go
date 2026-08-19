package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	dialTimeout = 10 * time.Second
	cmdTimeout  = 60 * time.Second

	// Cắt output để một lệnh lỡ tay (`cat /var/log/...`) không nhấn chìm buffer log.
	maxOutputLines = 300
	maxOutputBytes = 256 * 1024
)

// defaultKeys là các key thử theo thứ tự khi entry không chỉ định -k.
var defaultKeys = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

func sshDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ssh"
	}
	return filepath.Join(home, ".ssh")
}

func knownHostsPath() string { return filepath.Join(sshDir(), "known_hosts") }

// authMethods gom MỌI signer vào đúng MỘT AuthMethod publickey.
//
// Không được tách thành nhiều method: x/crypto loại method theo *tên*, nên hai
// method cùng tên "publickey" thì cái đầu fail là cái sau không bao giờ được
// thử. Một ssh-agent rỗng đứng trước sẽ nuốt mất key thật đứng sau.
//
// Thứ tự signer = thứ tự thử: key chỉ định tay trước (đúng ý người dùng), rồi
// tới ssh-agent, cuối cùng là key mặc định trong ~/.ssh.
// notes để in ra khi lỗi auth (biết đã thử những gì); cleanup đóng kết nối agent.
func authMethods(s Server) (methods []ssh.AuthMethod, notes []string, cleanup func()) {
	cleanup = func() {}
	var signers []ssh.Signer

	if s.KeyPath != "" {
		signers, notes = appendKey(signers, notes, expandHome(s.KeyPath), true)
	}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		switch {
		case err != nil:
			notes = append(notes, "ssh-agent lỗi: "+err.Error())
		default:
			cleanup = func() { _ = conn.Close() }
			agentSigners, err := agent.NewClient(conn).Signers()
			if err != nil {
				notes = append(notes, "ssh-agent lỗi: "+err.Error())
			} else if len(agentSigners) == 0 {
				notes = append(notes, "ssh-agent (rỗng)")
			} else {
				signers = append(signers, agentSigners...)
				notes = append(notes, fmt.Sprintf("ssh-agent (%d key)", len(agentSigners)))
			}
		}
	}

	if s.KeyPath == "" {
		for _, name := range defaultKeys {
			signers, notes = appendKey(signers, notes, filepath.Join(sshDir(), name), false)
		}
	}

	if len(signers) == 0 {
		return nil, notes, cleanup
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, notes, cleanup
}

// appendKey đọc 1 private key. loud = key do người dùng chỉ định, đọc hỏng thì
// phải nói ra; key mặc định thiếu là chuyện bình thường, im lặng bỏ qua.
func appendKey(signers []ssh.Signer, notes []string, path string, loud bool) ([]ssh.Signer, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if loud {
			notes = append(notes, "key "+path+": "+err.Error())
		}
		return signers, notes
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			notes = append(notes, "key "+path+" có passphrase — chạy `ssh-add "+path+"` rồi thử lại")
		} else {
			notes = append(notes, "key "+path+": "+err.Error())
		}
		return signers, notes
	}
	return append(signers, signer), append(notes, "key "+path)
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

// hostKeyCallback đọc ~/.ssh/known_hosts. Không dùng InsecureIgnoreHostKey:
// host lạ phải qua `server trust <tên>` để người dùng nhìn fingerprint trước.
func hostKeyCallback() (ssh.HostKeyCallback, error) {
	path := knownHostsPath()
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("chưa có %s — chạy `server trust <tên>` để thêm host key", path)
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("đọc %s: %w", path, err)
	}
	return cb, nil
}

// hostKeyHint dịch lỗi từ knownhosts thành câu hướng dẫn thay vì dump lỗi thô.
func hostKeyHint(s Server, err error) []string {
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) {
		return nil
	}
	if len(keyErr.Want) == 0 {
		return []string{
			"host key của " + s.Addr() + " chưa có trong " + knownHostsPath(),
			"chạy `server trust " + s.Name + "` để xem fingerprint rồi thêm vào",
		}
	}
	return []string{
		"HOST KEY ĐÃ ĐỔI cho " + s.Addr() + " — có thể là MITM, cũng có thể server vừa cài lại",
		"đối chiếu với admin trước; nếu đúng là cài lại thì xoá dòng cũ trong " + knownHostsPath(),
	}
}

func dial(s Server) (*ssh.Client, error) {
	hostKey, err := hostKeyCallback()
	if err != nil {
		return nil, err
	}
	methods, notes, cleanup := authMethods(s)
	defer cleanup()
	if len(methods) == 0 {
		return nil, fmt.Errorf("không có cách xác thực nào (%s)", strings.Join(append(notes, "thử `ssh-add <key>`"), "; "))
	}
	client, err := ssh.Dial("tcp", s.Addr(), &ssh.ClientConfig{
		User:            s.User,
		Auth:            methods,
		HostKeyCallback: hostKey,
		Timeout:         dialTimeout,
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

type runResult struct {
	stdout   []string
	stderr   []string
	exitCode int
	err      error
}

// runCommand chạy 1 lệnh qua SSH. Timeout thì kill session — không để một lệnh
// treo giữ worker mãi mãi.
func runCommand(client *ssh.Client, command string) runResult {
	sess, err := client.NewSession()
	if err != nil {
		return runResult{err: err}
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &limitedBuffer{buf: &stdout, max: maxOutputBytes}
	sess.Stderr = &limitedBuffer{buf: &stderr, max: maxOutputBytes}

	done := make(chan error, 1)
	go func() { done <- sess.Run(command) }()

	select {
	case runErr := <-done:
		res := runResult{stdout: splitLines(stdout.String()), stderr: splitLines(stderr.String())}
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			res.exitCode = exitErr.ExitStatus()
		} else if runErr != nil {
			res.err = runErr
		}
		return res
	case <-time.After(cmdTimeout):
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		return runResult{
			stdout: splitLines(stdout.String()),
			stderr: splitLines(stderr.String()),
			err:    fmt.Errorf("quá %s, đã kill session", cmdTimeout),
		}
	}
}

// limitedBuffer ngừng ghi sau max byte (io.Writer vẫn báo đã ghi hết để lệnh
// từ xa không nhận EPIPE giữa chừng).
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxOutputLines {
		kept := lines[:maxOutputLines]
		return append(kept, fmt.Sprintf("... (cắt bớt %d dòng)", len(lines)-maxOutputLines))
	}
	return lines
}

// probeHostKey mở kết nối chỉ để lấy host key. Chạy trước khi xác thực nên
// auth hỏng vẫn có key — dùng cho `server trust`.
func probeHostKey(s Server) (ssh.PublicKey, error) {
	var got ssh.PublicKey
	methods, _, cleanup := authMethods(s)
	defer cleanup()
	_, err := ssh.Dial("tcp", s.Addr(), &ssh.ClientConfig{
		User: s.User,
		Auth: methods,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return nil
		},
		Timeout: dialTimeout,
	})
	if got != nil {
		return got, nil // auth có thể hỏng, nhưng host key thì đã lấy được
	}
	return nil, err
}

func appendKnownHost(s Server, key ssh.PublicKey) (string, error) {
	if err := os.MkdirAll(sshDir(), 0o700); err != nil {
		return "", err
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(s.Addr())}, key)
	f, err := os.OpenFile(knownHostsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		return "", err
	}
	return line, nil
}
