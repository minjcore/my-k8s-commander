// Package swarmpolicy: lệnh swarm nào được phép, lệnh nào đổi cụm.
//
// Dùng chung cho swarm-worker (nơi thật sự gọi Docker API) và ai-worker (nơi
// quyết định lệnh có phải qua duyệt hay không) — giống helmpolicy. Để ở một chỗ
// vì hai bản riêng sẽ lệch nhau, mà lệch theo hướng nới lỏng thì thành lỗ hổng.
//
// Bảng này chỉ liệt kê những gì worker THẬT SỰ làm được. Cặp lạ = chặn, và phía
// duyệt coi như lệnh ghi. Đáng chú ý những thứ CỐ TÌNH không có:
//
//	swarm init|join|leave  `leave --force` trên manager là xoá sổ cả cụm
//	stack deploy           cần parse compose file (nằm trong docker/cli, không
//	                       phải SDK) — dùng `docker stack deploy` bằng tay
//	service create         số flag quá lớn để allowlist cho có ý nghĩa
package swarmpolicy

import "strings"

// object -> verb -> có đổi cụm hay không.
var allow = map[string]map[string]bool{
	"service": {
		"ls": false, "list": false, "ps": false, "inspect": false,
		"scale": true, "rm": true,
	},
	"node": {
		"ls": false, "list": false, "inspect": false,
		"update": true, "rm": true,
	},
	"stack": {
		"ls": false, "list": false, "services": false,
	},
}

// Lệnh không có đối tượng đi kèm.
var bareVerbs = map[string]bool{
	"info": false, "health": false, "help": false,
}

// Writes phân loại 1 lệnh (args KHÔNG gồm chữ "swarm"/"docker").
// ok=false nghĩa là ngoài allowlist — caller phải chặn, phía duyệt coi như ghi.
func Writes(args []string) (writes bool, ok bool) {
	if len(args) == 0 {
		return false, false
	}
	head := strings.ToLower(args[0])
	if w, exists := bareVerbs[head]; exists {
		return w, true
	}
	verbs, exists := allow[head]
	if !exists || len(args) < 2 {
		return false, false
	}
	w, exists := verbs[strings.ToLower(args[1])]
	return w, exists
}
