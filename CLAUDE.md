# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

App desktop Flutter (macOS) + core Go: một Supervisor chạy các micro-binary
("worker") và nối chúng lên UI qua FFI. Comment và message hướng tới người dùng
viết bằng **tiếng Việt** — giữ nguyên quy ước đó.

## Lệnh

```bash
make all                  # workers -> modules/, shared lib FFI -> gốc repo. Chạy trước mọi thứ.
make smoke                # round-trip StartCore -> SendToModule -> GetLogs, không cần GUI
make app                  # (macOS) .app tự chứa — xem mục Bundle bên dưới
go test ./...             # test Go
flutter test              # test Dart
flutter analyze lib test tool
gofmt -l cmd pkg internal # phải rỗng — CI chặn nếu có file lệch
```

Chạy 1 test: `go test ./cmd/module-server/ -run TestSSHVoiAgentRong -v`.

Worker gõ tay được, không cần app: `printf 'get pods -A\n' | ./modules/k8s-worker`.

## Kiến trúc

`cmd/supervisor` build hai lần: ra binary rỗng (chỉ để cgo export symbol) và ra
shared lib cho Flutter FFI. `internal/supervisor` quét `modules/`, chạy mọi file
executable trong đó, tự restart sau 2s. UI (`lib/main.dart`) poll `GetLogs()`
250ms một lần và gửi lệnh xuống bằng `SendToModule`.

Worker đọc **1 dòng stdin = 1 lệnh**, in kết quả ra stdout, mỗi dòng tự gắn
prefix `[tên-worker]`. Phải `Flush()` ngay sau mỗi lệnh — supervisor đọc theo dòng.

### Những chỗ dễ phá, đã trả giá

- **Worker không có kênh ngang.** Muốn worker A gọi worker B thì dùng
  `pkg/workerrpc`: spawn bản sao B, chạy với `K8SC_RPC=1` để B in thêm sentinel
  `[k8sc-rpc-done]` sau mỗi lệnh. Đừng đoán "hết output" bằng timeout.
  Hệ quả: worker con là tiến trình riêng, không dùng chung state (`use <context>`)
  với worker mà UI đang chạy.
- **Stdout của `console-worker` không được log lại** (`SetEchoSink`) — nó là sink
  render ANSI, log lại thành vòng lặp vô hạn.
- **`GetLogs()` phải gọi trước `GetLogsSize()`** — size chỉ được set bên trong GetLogs.
- **`flutter build macos` KHÔNG nhét core vào bundle.** App dựng kiểu đó chỉ chạy
  trong repo nhờ đường lui đi ngược cây thư mục của `native_core.dart`. Dùng
  `make app` (copy dylib + modules vào bundle rồi **ký lại**).
- **Toàn bộ signer SSH phải nằm trong ĐÚNG MỘT `ssh.AuthMethod`** (`cmd/module-server/ssh.go`).
  x/crypto loại method theo tên, nên hai method cùng tên `publickey` thì cái đầu
  fail là cái sau không bao giờ được thử — ssh-agent rỗng từng nuốt mất key thật.
- **`trimHistory` không được cắt vào giữa cặp tool_use/tool_result** — message user
  mở đầu mà chứa `tool_result` sẽ làm API trả 400.
- **`node addr` in TSV thô, không tabwriter** — server-worker tách theo `\t`.
- **helm: allowlist verb, không passthrough.** Phân loại ở `pkg/helmpolicy` —
  dùng chung k8s-worker (chạy helm) và ai-worker (quyết định có phải duyệt).
  Sửa một chỗ, đừng nhân bản. Verb lạ = coi như lệnh ghi.
- **`helm install` không được dùng `--wait`** — treo quá timeout RPC 90s của
  ai-worker rồi bị kill. Trả về ngay, rồi poll `get pods`.
- **Haiku 4.5 không nhận `output_config.effort`** — gửi lên là 400. Chỉ set effort
  cho tier nào có `supportsEffort` (`cmd/module-ai/budget.go`).
- **ai-worker thử ollama local trước, Claude API sau** (`cmd/module-ai/ollama.go`).
  Trong nhà chỉ có một dạng history: `[]anthropic.BetaMessageParam`. Response của
  ollama được dịch sang **JSON wire của Anthropic rồi `json.Unmarshal`** vào
  `BetaMessage` — đừng dựng struct tay, `JSON.Input.Raw()` (tools.go đọc input
  tool bằng nó) và `AsAny()` sẽ rỗng. Thinking của model local bị bỏ, không nối
  vào history: khối thinking của Anthropic cần signature.
  Model local không ghi gì vào `usage.json` và không bị trần tháng chặn.
- **Model local phải TẮT thinking** (`"think": false` trong `/api/chat`). Bật thì
  qwen3.5 xả hết vào field `thinking` rồi dừng, `content` rỗng — UI hiện "model
  trả về rỗng" dù model chạy đúng. Chỉ gửi field `think` khi `/api/show` khai
  capability `thinking`; model không có mà gửi là ollama báo lỗi.

### Hàng rào an toàn (đừng nới nếu không được yêu cầu)

- `module-server` không dùng `InsecureIgnoreHostKey`; host lạ bị chặn, phải qua
  `server trust` (TOFU, in fingerprint). Không lưu mật khẩu, chỉ agent/private key.
- `module-ai` cho model gọi 2 tool (`k8s`, `server`) nhưng theo **allowlist lệnh
  đọc**; lệnh ghi phải người dùng gõ `ai yes` để duyệt (`K8SC_AI_APPROVAL=ask|auto|deny`).
  Allowlist chứ không blocklist: lệnh lạ mặc định coi là lệnh ghi.
- `cluster add/rm`, `cluster use --persist` ghi vào kubeconfig **thật** — luôn in
  ra đường dẫn file đã sửa, `rm` bắt buộc `--yes`.

## Test

Không dùng cluster/server thật trong test. Mẫu đang dùng:

- **sshd in-process** (`cmd/module-server/ssh_test.go`) — `ssh.NewServerConn` với
  authorized key sinh tại chỗ.
- **Worker giả qua `TestMain`** — binary test tự đóng vai worker khi thấy biến môi
  trường (`K8SC_FAKE_WORKER`, `K8SC_FAKE_K8S`), symlink vào thư mục modules giả.

Biến môi trường để tách khỏi máy thật: `K8SC_MODULES_DIR`, `K8SC_SERVERS_FILE`,
`KUBECONFIG`, và `HOME` (đổi chỗ `~/.ssh`). Dùng chúng thay vì đụng file thật.

`module-ai` cần `ANTHROPIC_API_KEY` hoặc `ant auth login`; không có thì worker vẫn
chạy và in hướng dẫn thay vì crash — vòng gọi model không test tự động được.

## Ngân sách

Công cụ chạy trên **$5/tháng**, nên chi phí là ràng buộc thiết kế chứ không phải
chuyện phụ: mặc định model rẻ (`haiku`), model mạnh chỉ khi người dùng gõ `!`,
output tool cắt ở 50 dòng, và mỗi câu in ra chi phí. Trần tháng chặn cứng
(`K8SC_AI_BUDGET`). Đừng thêm gì làm phình token mỗi lượt mà không nói rõ cái giá.
