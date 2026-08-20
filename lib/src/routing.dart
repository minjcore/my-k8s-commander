/// Quyết định dòng người dùng gõ sẽ đi tới worker nào.
///
/// Tách khỏi widget để test được như hàm thuần.
library;

/// Kết quả định tuyến: gửi [payload] tới module [module].
typedef Route = ({String module, String payload});

const aiWorker = 'ai-worker';
const k8sWorker = 'k8s-worker';
const serverWorker = 'server-worker';
const swarmWorker = 'swarm-worker';

/// Alias người dùng gõ -> tên binary trong modules/.
const _aliases = <String, String>{
  'ai': aiWorker,
  'k8s': k8sWorker,
  'kubectl': k8sWorker,
  'cluster': k8sWorker,
  'server': serverWorker,
  'srv': serverWorker,
  'swarm': swarmWorker,
  'docker': swarmWorker,
};

/// Alias mà worker cần thấy lại từ khoá đầu (`kubectl get pods`, `cluster list`,
/// `server run ...`) — với các alias còn lại thì cắt bỏ prefix.
const _keepPrefix = <String>{'kubectl', 'cluster', 'server', 'srv'};

/// Lệnh k8s gõ trống prefix vẫn phải vào k8s-worker (`get pods`, `ctx`...).
/// Ngoài danh sách này thì coi là câu hỏi cho AI.
const _bareK8sVerbs = <String>{
  'get',
  'ctx',
  'contexts',
  'use',
  'node',
  'nodes',
  'help',
};

/// Định tuyến 1 dòng lệnh.
///
/// Thứ tự: alias tường minh -> lệnh k8s quen thuộc -> còn lại là câu hỏi cho AI.
/// Mặc định rơi về ai-worker (chứ không phải k8s-worker) vì gõ tự nhiên —
/// "pod nào đang crash?" — phải hỏi được AI thay vì nhận "lệnh không hiểu".
Route routeCommand(String input) {
  final trimmed = input.trim();
  final space = trimmed.indexOf(' ');
  final head = space > 0 ? trimmed.substring(0, space) : trimmed;

  final module = _aliases[head.toLowerCase()];
  if (module != null) {
    if (_keepPrefix.contains(head.toLowerCase())) {
      return (module: module, payload: trimmed);
    }
    // Alias gõ trống ("ai") thì để worker tự in usage.
    final rest = space > 0 ? trimmed.substring(space + 1).trim() : '';
    return (module: module, payload: rest.isEmpty ? 'help' : rest);
  }

  if (_bareK8sVerbs.contains(head.toLowerCase())) {
    return (module: k8sWorker, payload: trimmed);
  }
  return (module: aiWorker, payload: trimmed);
}
