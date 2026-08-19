# my-k8s-commander

[![CI](https://github.com/minjcore/my-k8s-commander/actions/workflows/ci.yml/badge.svg)](https://github.com/minjcore/my-k8s-commander/actions/workflows/ci.yml)
[![macOS smoke](https://img.shields.io/github/check-runs/minjcore/my-k8s-commander/main?nameFilter=macos-smoke&label=macOS%20smoke&logo=apple&logoColor=white)](https://github.com/minjcore/my-k8s-commander/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![Flutter](https://img.shields.io/badge/Flutter-Dart%203.10-02569B?logo=flutter&logoColor=white)
![Platform](https://img.shields.io/badge/platform-macOS-000000?logo=apple&logoColor=white)
![AI](https://img.shields.io/badge/AI-ollama%20local%20%E2%86%92%20Claude-6E56CF)
![Ngân sách](https://img.shields.io/badge/ng%C3%A2n%20s%C3%A1ch-%245%2Fth%C3%A1ng-2EA043)
![Last commit](https://img.shields.io/github/last-commit/minjcore/my-k8s-commander)
![Code size](https://img.shields.io/github/languages/code-size/minjcore/my-k8s-commander)
![Top language](https://img.shields.io/github/languages/top/minjcore/my-k8s-commander)
![Issues](https://img.shields.io/github/issues/minjcore/my-k8s-commander)
![License](https://img.shields.io/github/license/minjcore/my-k8s-commander)

App desktop quản trị Kubernetes, monorepo kiểu Khau-X: một Supervisor (Khâu X)
chạy các micro-binary worker (k8s, server SSH, AI, console) và nối chúng lên UI
Flutter qua FFI. Trợ lý AI
hỏi model **ollama chạy local** trước — miễn phí — chỉ rơi về Claude API khi
không có daemon, nên cả công cụ vừa trong ngân sách $5/tháng.

## Cấu trúc

```
my-k8s-commander/
├── cmd/
│   ├── supervisor/       # Khâu X (Orchestrator + FFI)
│   │   └── main.go      # Entry point cho FFI .so hoặc Desktop app
│   ├── module-ai/       # Micro-binary AI (LLM)
│   │   ├── main.go
│   │   ├── tools.go     # 2 tool gọi worker khác + allowlist lệnh đọc
│   │   └── approval.go  # duyệt từng lệnh ghi qua `ai yes`
│   ├── module-k8s/      # Micro-binary K8s (client-go)
│   │   ├── main.go
│   │   ├── cluster.go   # Quản lý cluster: đọc/sửa kubeconfig, test kết nối
│   │   ├── node.go      # `node addr`: địa chỉ node dạng TSV cho worker khác
│   │   └── helm.go      # cài/nâng cấp chart qua helm (allowlist verb)
│   ├── module-server/   # Micro-binary Server: sổ server SSH + chạy lệnh từ xa
│   │   ├── main.go
│   │   ├── store.go     # servers.json
│   │   ├── ssh.go       # dial / known_hosts / exec
│   │   └── nodes.go     # ghép node K8s <-> entry trong sổ
│   └── module-console/  # Logger/Terminal: nhận log qua Stdin, in ANSI; protocol MsgConsoleInput/Output
│       └── main.go
├── internal/            # Logic riêng (supervisor)
├── pkg/
│   ├── common/          # Protocol (Byte/Delta), logger
│   │   ├── protocol.go
│   │   └── logger.go
│   ├── diffstream/      # Logic tính Delta bytes
│   ├── helmpolicy/      # Verb helm nào được phép / verb nào đổi cụm
│   └── workerrpc/       # Gọi worker khác như một hàm (spawn + pipe + sentinel)
├── modules/             # Binary sau khi build
│   ├── ai-worker(.exe)
│   ├── k8s-worker(.exe)
│   ├── server-worker(.exe)
│   └── console-worker(.exe)
├── go.mod
└── Makefile
```

## Build

```bash
make build      # supervisor -> ./my-k8s-commander, workers -> modules/
make build-ffi  # shared lib cho Flutter FFI (dylib/so/dll theo OS)
make all        # = build + build-ffi (cần cho Flutter app)
make smoke      # test round-trip StartCore -> SendToModule -> GetLogs, không cần GUI
make app        # (macOS) app .app tự chứa, đem đi máy khác chạy được
make clean      # xoá binary Go + shared lib
make clean-all  # thêm cache Flutter (build/ + .dart_tool/, ~400MB)
```

`flutter build macos` **không** tự nhét core vào bundle: app dựng theo cách đó chỉ
chạy được khi còn nằm trong repo, nhờ đường lui đi ngược cây thư mục của
`native_core.dart`. `make app` copy `libmyk8s_commander.dylib` vào
`Contents/Frameworks/`, `modules/` vào `Contents/Resources/modules/` rồi ký lại
(sửa file bên trong làm hỏng chữ ký của Flutter) — ra bundle tự chứa thật.

## Khóa kiến trúc (Orchestrator + FFI)

- **StartCore(paths)**: Flutter gọi qua FFI; khởi chạy Supervisor với thư mục `paths` (modules), quản lý AI và K8s worker.
- **Buffer DiffStream**: Kết quả DiffStream gom vào buffer dùng chung.
- **GetUpdate() / GetUpdateSize()**: Flutter lấy pointer và size của buffer DiffStream.
- **AppendDiffStream(ptr, n)**: Đẩy thêm bytes DiffStream vào buffer (từ module hoặc logic khác).
- **GetLogs() / GetLogsSize()**: Flutter lấy pointer và size của buffer log (toàn bộ log từ module con), đẩy lên Terminal. Phải gọi `GetLogs()` **trước** `GetLogsSize()` — size chỉ được set bên trong GetLogs. Mỗi lần gọi rút đúng số byte đã copy, phần dư ở lại cho lần sau.
- **SendToModule(name, line)**: đẩy 1 dòng vào stdin của module. `0` = ghi được, `-1` = core chưa start, `-2` = module không chạy.
- **ListModules()**: chuỗi tên module đang chạy, phân cách `,`.
- **StopCore()**: Dừng Supervisor.

Build thư viện shared cho FFI: `make build-ffi` → `libmyk8s_commander.dylib` (macOS), `.so` (Linux), `.dll` (Windows).

## Chạy

```bash
make all              # build workers + shared lib
flutter run -d macos  # app tự tìm shared lib + modules/ rồi gọi StartCore
```

Lớp FFI bên Dart ở [lib/src/native_core.dart](lib/src/native_core.dart). Thứ tự tìm shared lib:
`$K8SC_CORE_ROOT` → trong app bundle (`Contents/Frameworks` + `Contents/Resources/modules`) →
đi ngược lên cây thư mục từ executable để ra gốc repo (dev). Không tìm được thì UI hiện lỗi
kèm danh sách path đã thử, không crash.

Định tuyến ở [lib/src/routing.dart](lib/src/routing.dart), theo thứ tự:

1. **Alias tường minh** — `ai <câu hỏi>` → ai-worker; `k8s`/`kubectl`/`cluster` →
   k8s-worker; `server`/`srv` → server-worker.
2. **Lệnh k8s gõ trống prefix** — `get pods`, `ctx`, `use <ctx>`, `node addr`,
   `nodes`, `help` → k8s-worker.
3. **Còn lại là câu hỏi** → ai-worker.

Mặc định rơi về ai-worker chứ không phải k8s-worker: gõ "pod nào đang crash?"
phải hỏi được AI, chứ không nhận về "lệnh không hiểu".

### k8s-worker (client-go)

```
get pods [-n <ns> | -A]    get nodes    get ns
ctx                        # liệt kê context, * = đang dùng
use <context>              # đổi context (chỉ trong tiến trình worker)
help
```

Đọc `~/.kube/config` qua `clientcmd`, hỗ trợ exec credential plugin (GKE, EKS...).
Lỗi auth được trả về thành 1 dòng log, worker không chết.

#### helm

```
helm repo add <tên> <url>   |  helm repo update  |  helm repo list
helm search repo <từ khoá>  |  helm show values <chart>
helm list [-n <ns>]         |  helm status <release> [-n <ns>]
helm install <release> <chart> [-n <ns>] [--create-namespace] [--set k=v] [-f <file>]
helm upgrade <release> <chart> [...]   |  helm uninstall <release> [-n <ns>]
```

Shell ra binary `helm` (cần cài sẵn), bám `--kube-context` theo context worker
đang dùng. Ba điều đáng biết:

- **Allowlist verb**, không phải passthrough: verb ngoài danh sách bị chặn, kể cả
  `helm template`, `helm plugin install`. Phân loại nằm ở `pkg/helmpolicy`, dùng
  chung với ai-worker để hai bên không lệch nhau.
- **Chặn riêng vài flag**: `--post-renderer` (chạy binary tuỳ ý = RCE),
  `--kubeconfig` (lách cách chọn cluster), `--wait`/`--atomic` (treo tới hết
  timeout RPC). Args truyền qua `exec.Command` tách rời, không qua shell.
- **`install` không chờ pod Ready** — helm trả về ngay sau khi apply manifest,
  rồi poll `get pods -n <ns>`. Chờ đồng bộ sẽ vượt timeout RPC và bị kill.

#### Quản lý cluster

```
cluster list                        # context + endpoint + user + namespace
cluster info [tên]                  # server, CA, version, số node
cluster test [tên|all]              # gọi /version, đo latency từng context
cluster use <tên> [--persist]       # đổi context; --persist ghi vào kubeconfig
cluster add <tên> --server <url> [--ca <file> | --insecure]
                                    [--token-env <VAR> | --token-file <f> | --token <t>]
                                    [--client-cert <f> --client-key <f>] [--ns <ns>]
cluster rm <tên> --yes              # xoá context (+ cluster/user nếu không ai dùng)
```

`add` / `rm` / `use --persist` **ghi vào kubeconfig thật** (file đầu tiên trong
thứ tự nạp, thường `~/.kube/config`) nên mọi lệnh ghi đều in ra đường dẫn đã sửa,
và `rm` bắt buộc `--yes`. Ưu tiên `--token-env` / `--token-file`: `--token` gõ
thẳng sẽ lọt vào buffer log của Terminal.

### server-worker (SSH)

```
server list                                # sổ server
server nodes                               # node của cluster <-> entry trong sổ
server add <tên> [user@]host[:port] [-k <key>] [-t <tag,tag>] [--note "..."] [--force]
server rm <tên>
server trust <selector>                    # xem fingerprint rồi ghi vào known_hosts
server test <selector>                     # thử SSH, in uptime
server run <selector> <lệnh...>            # chạy lệnh từ xa
```

Selector dùng chung cho `trust` / `test` / `run`: `<tên>`, `@tag`, `all`,
`node/<tên node>`, `node/all`.

Sổ server: `~/.k8s-commander/servers.json` (0600, ghi bằng temp + rename), đổi được
bằng `$K8SC_SERVERS_FILE`. **Không lưu mật khẩu** — xác thực qua `ssh-agent` hoặc
private key (`-k`, mặc định thử `~/.ssh/id_ed25519|id_ecdsa|id_rsa`); key có
passphrase thì worker bảo chạy `ssh-add`.

Host key kiểm bằng `~/.ssh/known_hosts`, không có `InsecureIgnoreHostKey`: host lạ
bị chặn kèm hướng dẫn `server trust <tên>` (TOFU — in fingerprint SHA256 để đối
chiếu trước khi ghi). Host key đổi thì báo nghi vấn MITM thay vì im lặng nối tiếp.

Các selector chạy tuần tự để log không đan xen. Lệnh từ xa timeout 60s rồi kill
session, output cắt ở 300 dòng / 256KB.

#### Nối node K8s với server SSH

`server nodes` hỏi k8s-worker (`node addr`) rồi ghép địa chỉ node — internal IP,
external IP, hostname, tên node — với `host` của các entry trong sổ:

```
3 node, 2 khớp với sổ server
NODE    STATUS    INTERNAL-IP  EXTERNAL-IP  SERVER
gke-a   Ready     10.0.0.5     34.1.2.3     web
gke-b   Ready     10.0.0.6     -            db
gke-c   NotReady  10.0.0.7     -            -
node chưa khớp: thêm bằng `server add <tên> <user>@<ip>` với IP ở trên
```

Sau đó `server run node/gke-a uptime` là chạy trên entry khớp với node đó.
`node/<tên>` **chỉ là cách gọi tắt entry đã có sẵn** (đã có user/key/known_hosts) —
cố ý không tự SSH vào node lạ, tức là không mở thêm đường xác thực nào. Node chưa
có entry thì báo rõ và chỉ cách thêm, chứ không im lặng bỏ qua.

### ai-worker (ollama local, rơi về Claude API)

**Thử ollama local trước.** Mỗi câu hỏi, worker dò `http://localhost:11434` bằng
`/api/tags` (timeout 1.5s); có daemon là chạy model local — miễn phí, không tính
vào trần tháng. Không có (hoặc daemon chết) thì gọi Claude API. Dò lại từng lượt
nên bật/tắt ollama giữa phiên vẫn theo đúng; dò trượt thì nghỉ 60s trước khi thử
lại, khỏi cộng 1.5s vào mọi câu.

Worker đọc `/api/show` để biết capability của model:

- **thiếu `tools`** → nó bỏ qua tool và trả lời theo trí nhớ, nên `help` in cảnh
  báo ra Terminal.
- **có `thinking`** → worker gửi `"think": false`. Bật thinking thì model xả hết
  vào field `thinking` rồi dừng, `content` rỗng và UI chỉ thấy "model trả về rỗng".

Câu gõ `!` luôn đi thẳng Claude API, không dùng local.

| Biến | Mặc định | |
|---|---|---|
| `K8SC_AI_OLLAMA` | bật | `off`/`0` = bỏ qua local, đi thẳng API |
| `K8SC_AI_OLLAMA_URL` | `http://localhost:11434` | |
| `K8SC_AI_OLLAMA_MODEL` | model nhiều tham số nhất đang có | tên model, phải có sẵn |

Cần credential cho phần cloud: `export ANTHROPIC_API_KEY=...` (hoặc `ant auth
login` — SDK tự đọc profile). Chưa có credential thì worker vẫn chạy và in ra
hướng dẫn thay vì crash.

Mặc định model **`claude-haiku-4-5`** — rẻ hơn Opus 5 lần và thừa sức cho việc
thường ngày. Gõ `ai !<câu hỏi>` thì riêng câu đó chạy model mạnh (Opus 5).
Giữ history trong tiến trình (`reset` để xoá), bật server-side fallback nên câu bị
classifier từ chối sẽ được model khác trả lời. `help` trả lời tại chỗ, không tốn
một lượt gọi API.

#### Ngân sách

Mỗi câu trả lời kèm một dòng chi phí, và có trần cứng theo tháng:

```
haiku · 1.2k in · 340 out · <$0.01 · tháng này $0.83/$5.00
gemma4:e2b (local) · 817 in · 286 out · miễn phí
```

Số cộng dồn trong `~/.k8s-commander/usage.json`, tự về 0 khi sang tháng. Chạm trần
thì **ngừng gọi API** chứ không âm thầm tiêu tiếp.

| Biến | Mặc định | |
|---|---|---|
| `K8SC_AI_MODEL` | `haiku` | tier thường — `haiku`/`sonnet`/`opus` |
| `K8SC_AI_MODEL_STRONG` | `opus` | tier khi gõ `!` |
| `K8SC_AI_BUDGET` | `5` | USD/tháng, `0` = không chặn |
| `K8SC_AI_USAGE_FILE` | `~/.k8s-commander/usage.json` | |

Hai chỗ dễ vấp: **Haiku 4.5 không nhận `output_config.effort`** (gửi lên là 400),
nên effort chỉ set cho tier mạnh; và output tool cắt ở 50 dòng thay vì 200 —
bảng dài vừa tốn token gửi vào, vừa kéo model viết dài ra.

#### Status bar và cảnh báo bất thường

Thanh dưới cùng của app hiện context k8s đang dùng, model AI (local/cloud), chi
phí tháng, số worker đang chạy, và **cảnh báo bất thường** soi từ stream log.

UI không có kênh hỏi riêng xuống worker (chỉ `SendToModule` + `GetLogs`) nên nó
parse chính dòng log worker in ra — đổi lại, định dạng output của worker là hợp
đồng. Luật dựng sẵn: cột STATUS của `get pods` (`CrashLoopBackOff`,
`ImagePullBackOff`, `OOMKilled`, `Evicted`…), node `NotReady`, và dòng `lỗi …`
của worker. Bấm vào cảnh báo để xoá.

Thêm luật riêng bằng `~/.k8s-commander/alert-patterns.json`
(đè bằng `K8SC_ALERT_PATTERNS`):

```json
{ "patterns": [
  { "name": "disk-full", "regex": "no space left on device.*pod=(\\S+)" },
  { "name": "oom", "regex": "OOMKilled" }
] }
```

Capture group 1 (nếu có) hiện làm tên đối tượng. Trần 32 pattern — mỗi pattern là
một lần match regex trên **mọi** dòng log, chạy trên UI thread. JSON sai hoặc
regex sai thì luật đó bị bỏ và báo ra Terminal, app không crash. UI stat lại file
mỗi 2s nên sửa file (hoặc để AI sửa) là thấy ngay, không cần mở lại app.

Nhờ AI thêm hộ: `ai thêm pattern cảnh báo bắt OOMKilled` → nó gọi tool `alert`,
và vì đây là lệnh ghi nên phải gõ `ai yes` để duyệt.

#### Tool: ai-worker gọi được k8s-worker, server-worker và sổ cảnh báo

Model có 2 tool — `k8s` và `server` — mỗi tool nhận 1 chuỗi `command` rồi đẩy
xuống worker tương ứng, nên hỏi "pod nào đang crash" là nó tự chạy `get pods -A`
và đọc kết quả thay vì đoán. Mỗi lệnh đều in ra Terminal (`→ k8s-worker: ...`)
kèm output, để người dùng nhìn thấy AI vừa làm gì. Tối đa 8 vòng tool cho 1 câu hỏi;
output mỗi lệnh cắt ở 200 dòng.

**Hàng rào an toàn**: lệnh **đọc** (`get`, `ctx`, `cluster list|info|test`, `use`,
`node addr`, `server list|nodes|test`) chạy thẳng. Lệnh đổi trạng thái —
`cluster add/rm`, `cluster use --persist`, `server add/rm/trust`, và nhất là
`server run` (thực thi lệnh tuỳ ý trên máy từ xa) — phải được **người dùng duyệt
từng lệnh**:

```
→ server-worker: server run prod-1 systemctl restart nginx
  ⚠ lệnh này thay đổi hệ thống: server-worker server run prod-1 systemctl restart nginx
    gõ `ai yes` để chạy — gõ gì khác (hoặc để im 2m0s) là bỏ qua
```

Gõ gì khác — kể cả câu hỏi mới — đều tính là từ chối, và model được bảo đừng thử
lại mà đưa lệnh cho người dùng tự gõ. Hết 2 phút không ai trả lời cũng là từ chối,
để chạy headless không treo. Đây là allowlist chứ không phải blocklist: lệnh lạ
mặc định bị coi là lệnh ghi.

`K8SC_AI_APPROVAL` đổi chế độ: `ask` (mặc định), `auto` (duyệt sẵn tất cả — chỉ
dùng khi biết mình làm gì), `deny` (chặn hẳn, không hỏi — hợp với cron/headless).

### pkg/workerrpc: gọi worker khác như một hàm

Các worker chỉ có pipe với supervisor, **không có kênh ngang nào giữa chúng**.
`pkg/workerrpc` cho một worker tự spawn bản sao worker khác trong `modules/`
(lazy, lần gọi đầu tiên) và nói chuyện qua pipe. Worker con chạy với
`K8SC_RPC=1` nên in thêm dòng sentinel `[k8sc-rpc-done]` sau mỗi lệnh — nhờ đó
biết câu trả lời đã hết mà không phải đoán bằng timeout. Đường đi bình thường
lên Terminal không bật cờ này nên không thấy sentinel.

Hiện dùng ở 2 chỗ: ai-worker → k8s-worker/server-worker (tool), và
server-worker → k8s-worker (`server nodes`).

- Timeout 90s/lệnh, quá thì kill worker con — output dở dang còn trong pipe sẽ
  làm bẩn lệnh kế tiếp. Lần gọi sau tự spawn lại.
- Output cắt ở 200 dòng.
- Không cần dọn khi tiến trình cha bị kill: cha giữ đầu ghi duy nhất của pipe
  stdin, cha chết thì con thấy EOF và tự thoát.
- **Hệ quả**: worker con là tiến trình tách biệt với worker mà UI đang dùng, nên
  `use <context>` bên AI không đổi context của Terminal.
- `$K8SC_MODULES_DIR` ghi đè chỗ tìm binary khi chạy worker ngoài `modules/`.

**macOS**: App Sandbox đã tắt trong `macos/Runner/*.entitlements` — sandbox chặn spawn binary
ngoài app bundle (`modules/`) và chặn đọc `~/.kube/config`.

`./my-k8s-commander` (binary standalone) có `func main()` rỗng nên chạy rồi thoát ngay: nó chỉ
tồn tại để cgo export symbol cho FFI. Muốn kiểm tra core mà không mở GUI thì dùng `make smoke`.

Supervisor quét `modules/`, chạy mọi file executable trong đó, tự restart sau 2s nếu module chết.
Stdout của `console-worker` **không** được log lại (nó là sink render ANSI — log lại sẽ thành
vòng lặp vô hạn); xem `SetEchoSink`.

## Giấy phép

MIT — xem [LICENSE](LICENSE).
