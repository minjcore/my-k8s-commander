/// Trạng thái tóm tắt cho status bar, dựng bằng cách NGHE KÉ stream log.
///
/// UI không có kênh hỏi riêng xuống worker (chỉ có SendToModule + GetLogs), nên
/// thay vì thêm protocol thì đọc chính dòng log worker đã in ra. Đổi lại: định
/// dạng output của worker trở thành hợp đồng — sửa chuỗi bên worker phải sửa
/// parser ở đây (test bám đúng những chuỗi đó).
///
/// Mọi hàm trong file này là hàm thuần: không I/O, không widget, test được trực
/// tiếp. `applyLine` nằm trên hot path (poll 250ms mỗi dòng log) nên chỉ dùng
/// `contains`/`startsWith` trên bảng hằng, chỉ tách field khi đã khớp.
library;

const String prefixK8sWorker = '[k8s-worker] ';
const String prefixAiWorker = '[ai-worker] ';

/// Chuỗi worker in ra khi đổi context (`use <ctx>`).
const String _markUsingContext = 'đang dùng context: ';

/// Dòng context đang dùng trong output của `ctx`: `* tên-context`.
const String _markCurrentContext = '* ';

/// Dòng chi phí của ai-worker luôn có " out · "; model local có " (local)".
const String _markUsage = ' out · ';
const String _markLocal = ' (local)';
const String _markMonth = 'tháng này ';
const String _usageSeparator = ' · ';

/// Dòng lỗi của worker bắt đầu bằng "lỗi ".
const String _markError = 'lỗi ';

/// Giữ tối đa ngần này cảnh báo — status bar chỉ hiện cái mới nhất + số đếm.
const int maxAlerts = 20;

/// Độ dài tối đa của phần chi tiết cảnh báo, để một dòng lỗi dài không phá layout.
const int _maxDetailLength = 120;

/// Trần số pattern tự định nghĩa. Mỗi pattern là một lần match regex trên MỌI
/// dòng log, mà việc đó chạy trên UI thread — để mở là tự treo app.
const int maxCustomPatterns = 32;

enum AlertKind { podCrash, nodeNotReady, workerError, custom }

/// Bảng trạng thái pod coi là bất thường. Cột STATUS của `get pods` in lý do
/// container (CrashLoopBackOff...) chứ không phải phase, nên so thẳng được.
const List<String> podProblemStatuses = [
  'CrashLoopBackOff',
  'ImagePullBackOff',
  'ErrImagePull',
  'CreateContainerConfigError',
  'OOMKilled',
  'Evicted',
  'Error',
];

/// Trạng thái node bất thường.
const String nodeNotReadyStatus = 'NotReady';

/// AlertPattern: luật do người dùng tự định nghĩa, nạp từ file cấu hình.
/// Regex biên dịch ngay lúc dựng — không bao giờ dựng lại trong vòng đọc log.
class AlertPattern {
  AlertPattern({required this.name, required this.source})
      : matcher = RegExp(source);

  final String name;

  /// Regex gốc dạng chuỗi, giữ lại để in ra khi cần chẩn đoán.
  final String source;

  final RegExp matcher;

  /// match trả về subject nếu khớp, null nếu không. Có capture group thì group 1
  /// là subject (tên pod/node/…); không có thì lấy chính tên luật.
  String? match(String line) {
    final m = matcher.firstMatch(line);
    if (m == null) return null;
    if (m.groupCount >= 1) {
      final g = m.group(1);
      if (g != null && g.isNotEmpty) return g;
    }
    return name;
  }
}

/// StatusRules: bảng luật phát hiện bất thường = luật dựng sẵn + luật người dùng.
class StatusRules {
  const StatusRules({this.custom = const []});

  final List<AlertPattern> custom;

  static const StatusRules builtin = StatusRules();

  /// fromJsonString đọc cấu hình dạng:
  ///   {"patterns": [{"name": "oom", "regex": "OOMKilled"}]}
  /// Hàm thuần: không đọc file. Pattern lỗi bị bỏ qua và được kể trong
  /// `problems` thay vì làm sập UI.
  static StatusRulesParse fromJsonString(String raw, dynamic Function(String) decode) {
    final problems = <String>[];
    dynamic root;
    try {
      root = decode(raw);
    } catch (e) {
      return StatusRulesParse(builtin, ['cấu hình cảnh báo không phải JSON: $e']);
    }
    if (root is! Map) {
      return StatusRulesParse(builtin, ['cấu hình cảnh báo phải là object JSON']);
    }
    final list = root['patterns'];
    if (list is! List) {
      return StatusRulesParse(builtin, ['thiếu mảng "patterns"']);
    }
    final out = <AlertPattern>[];
    for (final item in list) {
      if (out.length >= maxCustomPatterns) {
        problems.add('bỏ qua pattern thứ ${out.length + 1}+: quá $maxCustomPatterns');
        break;
      }
      if (item is! Map) {
        problems.add('pattern không phải object: $item');
        continue;
      }
      final name = item['name'];
      final regex = item['regex'];
      if (name is! String || name.isEmpty || regex is! String || regex.isEmpty) {
        problems.add('pattern thiếu "name" hoặc "regex": $item');
        continue;
      }
      try {
        out.add(AlertPattern(name: name, source: regex));
      } on FormatException catch (e) {
        problems.add('regex sai ở "$name": ${e.message}');
      }
    }
    return StatusRulesParse(StatusRules(custom: out), problems);
  }
}

/// Kết quả nạp cấu hình: luật dùng được + những chỗ sai để hiện cho người dùng.
class StatusRulesParse {
  const StatusRulesParse(this.rules, this.problems);

  final StatusRules rules;
  final List<String> problems;
}

class StatusAlert {
  const StatusAlert(this.kind, this.subject, this.detail);

  final AlertKind kind;

  /// Đối tượng bị ảnh hưởng: tên pod, tên node, hoặc tên worker.
  final String subject;

  /// Mô tả ngắn hiện trên status bar.
  final String detail;

  /// Khoá chống trùng: cùng pod + cùng lý do thì chỉ đếm một lần.
  String get key => '${kind.name}/$subject/$detail';

  @override
  String toString() => '$subject: $detail';
}

class StatusModel {
  const StatusModel({
    this.kubeContext,
    this.aiModel,
    this.aiLocal = false,
    this.monthCost,
    this.alerts = const [],
  });

  /// Context kubeconfig đang dùng, null khi chưa biết.
  final String? kubeContext;

  /// Model AI của lượt trả lời gần nhất.
  final String? aiModel;

  /// Model đó chạy local (miễn phí) hay gọi API.
  final bool aiLocal;

  /// Chi phí tháng dạng "$0.83/$5.00", chỉ có sau một lượt gọi API thật.
  final String? monthCost;

  final List<StatusAlert> alerts;

  /// Tách field của bảng worker. Biên dịch một lần: `_scanTable` nằm trên hot
  /// path, dựng RegExp mỗi dòng là tự đốt CPU.
  static final RegExp _spaces = RegExp(r'\s+');

  bool get hasAlert => alerts.isNotEmpty;

  StatusAlert? get latestAlert => alerts.isEmpty ? null : alerts.last;

  StatusModel copyWith({
    String? kubeContext,
    String? aiModel,
    bool? aiLocal,
    String? monthCost,
    List<StatusAlert>? alerts,
  }) {
    return StatusModel(
      kubeContext: kubeContext ?? this.kubeContext,
      aiModel: aiModel ?? this.aiModel,
      aiLocal: aiLocal ?? this.aiLocal,
      monthCost: monthCost ?? this.monthCost,
      alerts: alerts ?? this.alerts,
    );
  }

  StatusModel clearAlerts() => copyWith(alerts: const []);

  /// applyLine đọc 1 dòng log và trả về trạng thái mới. Trả về `this` khi dòng
  /// đó không mang thông tin nào — tránh setState vô ích ở phía UI.
  ///
  /// Luật người dùng chạy trên MỌI dòng (kể cả dòng của worker khác), vì họ có
  /// thể muốn bắt cả log server-worker; luật dựng sẵn chỉ soi k8s-worker.
  StatusModel applyLine(String line, {StatusRules rules = StatusRules.builtin}) {
    final next = _applyBuiltin(line);
    return next._applyCustom(line, rules);
  }

  StatusModel _applyBuiltin(String line) {
    if (line.startsWith(prefixAiWorker)) {
      return _applyAi(line.substring(prefixAiWorker.length));
    }
    if (line.startsWith(prefixK8sWorker)) {
      return _applyK8s(line.substring(prefixK8sWorker.length));
    }
    return this;
  }

  StatusModel _applyCustom(String line, StatusRules rules) {
    var model = this;
    for (final pattern in rules.custom) {
      final subject = pattern.match(line);
      if (subject == null) continue;
      model = model._withAlert(
          StatusAlert(AlertKind.custom, subject, pattern.name));
    }
    return model;
  }

  StatusModel _applyAi(String body) {
    if (!body.contains(_markUsage)) return this;

    final parts = body.split(_usageSeparator);
    if (parts.isEmpty) return this;

    final head = parts.first;
    final local = head.endsWith(_markLocal);
    final model =
        local ? head.substring(0, head.length - _markLocal.length) : head;
    if (model.isEmpty) return this;

    String? cost = monthCost;
    for (final part in parts) {
      if (part.startsWith(_markMonth)) {
        cost = part.substring(_markMonth.length);
      }
    }
    return copyWith(aiModel: model, aiLocal: local, monthCost: cost);
  }

  StatusModel _applyK8s(String body) {
    if (body.startsWith(_markUsingContext)) {
      final name = body.substring(_markUsingContext.length).trim();
      return name.isEmpty ? this : copyWith(kubeContext: name);
    }
    if (body.startsWith(_markCurrentContext)) {
      final name = body.substring(_markCurrentContext.length).trim();
      // "* " cũng có thể mở đầu dòng khác; tên context không chứa dấu cách.
      return name.isEmpty || name.contains(' ') ? this : copyWith(kubeContext: name);
    }
    if (body.startsWith(_markError)) {
      return _withAlert(StatusAlert(
        AlertKind.workerError,
        'k8s-worker',
        _shorten(body),
      ));
    }
    return _scanTable(body);
  }

  /// _scanTable soi một dòng bảng của `get pods` / `get nodes`.
  ///
  /// pods:  NAMESPACE NAME READY STATUS RESTARTS AGE  -> STATUS ở index 3
  /// nodes: NAME STATUS VERSION AGE                   -> STATUS ở index 1
  StatusModel _scanTable(String body) {
    if (body.contains(nodeNotReadyStatus)) {
      final fields = body.split(_spaces);
      final i = fields.indexOf(nodeNotReadyStatus);
      if (i == 1) {
        return _withAlert(
            StatusAlert(AlertKind.nodeNotReady, fields[0], nodeNotReadyStatus));
      }
      return this;
    }
    for (final status in podProblemStatuses) {
      if (!body.contains(status)) continue;
      final fields = body.split(_spaces);
      final i = fields.indexOf(status);
      // Chỉ nhận khi đúng vị trí cột STATUS của bảng pod — tránh bắt nhầm chữ
      // "Error" nằm trong câu văn.
      if (i != 3) continue;
      return _withAlert(StatusAlert(AlertKind.podCrash, fields[1], status));
    }
    return this;
  }

  StatusModel _withAlert(StatusAlert alert) {
    for (final existing in alerts) {
      if (existing.key == alert.key) return this;
    }
    final next = [...alerts, alert];
    if (next.length > maxAlerts) {
      next.removeRange(0, next.length - maxAlerts);
    }
    return copyWith(alerts: next);
  }

  static String _shorten(String s) {
    final one = s.replaceAll('\n', ' ').trim();
    if (one.length <= _maxDetailLength) return one;
    return '${one.substring(0, _maxDetailLength)}…';
  }
}
