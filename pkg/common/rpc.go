package common

import "os"

// Chế độ RPC: worker in thêm 1 dòng RPCDone sau khi trả lời xong 1 lệnh, để
// caller biết câu trả lời đã hết mà không phải đoán bằng timeout.
//
// Chỉ ai-worker bật cờ này (khi tự spawn worker con làm tool). Supervisor không
// bật, nên đường đi bình thường lên Terminal không thấy dòng sentinel nào.
const (
	RPCEnvVar = "K8SC_RPC"
	RPCDone   = "[k8sc-rpc-done]"
)

func RPCMode() bool { return os.Getenv(RPCEnvVar) == "1" }
