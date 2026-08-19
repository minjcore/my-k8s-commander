// Package helmpolicy: verb helm nào được phép, verb nào đổi cụm.
//
// Dùng chung cho k8s-worker (nơi thật sự chạy helm) và ai-worker (nơi quyết định
// lệnh có phải qua duyệt hay không). Để ở một chỗ vì hai bản riêng sẽ lệch nhau,
// mà lệch theo hướng nới lỏng thì thành lỗ hổng.
package helmpolicy

import "strings"

// readVerbs: chỉ đọc, không đổi gì trên cụm.
var readVerbs = map[string]bool{
	"list": true, "ls": true, "status": true, "history": true,
	"version": true, "get": true, "show": true, "search": true,
}

// writeVerbs: thay đổi cụm.
var writeVerbs = map[string]bool{
	"install": true, "upgrade": true, "uninstall": true, "rollback": true,
}

// repoSub: lệnh con của `helm repo` -> có đổi cụm hay không.
// add/update/list chỉ ghi cache helm cục bộ (~/.config/helm), không đụng cụm.
var repoSub = map[string]bool{
	"add": false, "update": false, "list": false, "remove": true, "rm": true,
}

// BannedFlags: flag chặn bất kể verb.
//
//	--post-renderer  chạy binary tuỳ ý -> RCE
//	--kubeconfig     lách cách chọn cluster của worker
//	--wait/--atomic  treo tới hết timeout RPC; install trả về ngay rồi poll pod
var BannedFlags = []string{"--post-renderer", "--kubeconfig", "--wait", "--atomic"}

// Writes phân loại 1 lệnh helm (args KHÔNG gồm chữ "helm").
// ok=false nghĩa là verb ngoài allowlist — caller phải chặn, và phía duyệt phải
// coi như lệnh ghi.
func Writes(args []string) (writes bool, ok bool) {
	if len(args) == 0 {
		return false, false
	}
	verb := strings.ToLower(args[0])
	if verb == "repo" {
		if len(args) < 2 {
			return false, false
		}
		w, exists := repoSub[strings.ToLower(args[1])]
		return w, exists
	}
	if readVerbs[verb] {
		return false, true
	}
	if writeVerbs[verb] {
		return true, true
	}
	return false, false
}

// Banned trả về flag bị chặn đầu tiên tìm thấy, "" nếu sạch.
func Banned(args []string) string {
	for _, a := range args {
		for _, banned := range BannedFlags {
			if a == banned || strings.HasPrefix(a, banned+"=") {
				return banned
			}
		}
	}
	return ""
}
