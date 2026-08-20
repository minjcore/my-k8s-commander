package main

// Đọc "docker context" như docker CLI làm.
//
// Docker context là khái niệm CỦA CLI, không có trong Engine API và cũng không
// có trong module client — SDK chỉ biết DOCKER_HOST. Người dùng colima /
// Rancher Desktop / OrbStack đều chạy qua context, nên nếu không đọc chỗ này thì
// worker đi tìm /var/run/docker.sock và báo "daemon not running" trong khi
// `docker ps` gõ tay vẫn chạy.
//
// Cách CLI lưu: ~/.docker/config.json giữ currentContext, còn endpoint nằm ở
// ~/.docker/contexts/meta/<sha256(tên context)>/meta.json.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	dockerConfigDirEnvVar = "DOCKER_CONFIG"
	dockerContextEnvVar   = "DOCKER_CONTEXT"

	dockerConfigDirName = ".docker"
	dockerConfigFile    = "config.json"
	contextsMetaDir     = "contexts"
	contextsMetaSubDir  = "meta"
	contextMetaFile     = "meta.json"

	// Tên endpoint docker trong meta.json (context còn có endpoint "kubernetes").
	dockerEndpointKey = "docker"

	// Context mặc định không có file meta — nó chính là DOCKER_HOST/socket chuẩn.
	defaultContextName = "default"
)

// dockerConfigDir: $DOCKER_CONFIG, không thì ~/.docker.
func dockerConfigDir() (string, error) {
	if d := os.Getenv(dockerConfigDirEnvVar); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dockerConfigDirName), nil
}

// currentContextName: $DOCKER_CONTEXT đè, không thì currentContext trong
// config.json. Rỗng nghĩa là dùng mặc định.
func currentContextName(dir string) string {
	if c := os.Getenv(dockerContextEnvVar); c != "" {
		return c
	}
	data, err := os.ReadFile(filepath.Join(dir, dockerConfigFile))
	if err != nil {
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.CurrentContext
}

// contextHost trả về host của context đang chọn. "" nghĩa là không có context
// nào cần theo — để SDK tự dùng mặc định.
func contextHost() (string, error) {
	dir, err := dockerConfigDir()
	if err != nil {
		return "", nil
	}
	name := currentContextName(dir)
	if name == "" || name == defaultContextName {
		return "", nil
	}

	// CLI băm tên context bằng sha256 rồi lấy hex làm tên thư mục.
	sum := sha256.Sum256([]byte(name))
	path := filepath.Join(dir, contextsMetaDir, contextsMetaSubDir,
		hex.EncodeToString(sum[:]), contextMetaFile)

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("context %q không đọc được (%s)", name, path)
	}
	var meta struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("meta của context %q không phải JSON: %w", name, err)
	}
	host := meta.Endpoints[dockerEndpointKey].Host
	if host == "" {
		return "", fmt.Errorf("context %q không có endpoint docker", name)
	}
	return host, nil
}
