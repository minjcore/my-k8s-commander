/// Nạp luật cảnh báo tự định nghĩa từ đĩa.
///
/// Tách khỏi status.dart để phần model vẫn thuần (không dart:io) và test được
/// không cần filesystem. Ở đây chỉ có việc tìm đường dẫn + đọc file.
library;

import 'dart:convert';
import 'dart:io';

import 'status.dart';

/// Env đè đường dẫn file cấu hình — dùng khi test hoặc chạy nhiều profile.
const String alertPatternsEnvVar = 'K8SC_ALERT_PATTERNS';

/// Cùng thư mục với servers.json / usage.json của worker.
const String configDirName = '.k8s-commander';
const String alertPatternsFileName = 'alert-patterns.json';

const String _homeEnvVar = 'HOME';

/// alertPatternsPath: env đè, không thì ~/.k8s-commander/alert-patterns.json.
/// Trả về null khi không biết HOME (không đoán bừa).
String? alertPatternsPath({Map<String, String>? env}) {
  final vars = env ?? Platform.environment;
  final override = vars[alertPatternsEnvVar];
  if (override != null && override.isNotEmpty) return override;

  final home = vars[_homeEnvVar];
  if (home == null || home.isEmpty) return null;
  return '$home${Platform.pathSeparator}$configDirName'
      '${Platform.pathSeparator}$alertPatternsFileName';
}

/// loadStatusRules đọc file cấu hình.
///
/// Không có file = không có luật riêng, KHÔNG phải lỗi (đa số người dùng không
/// tạo file này). Chỉ file tồn tại mà nội dung sai mới báo vào `problems`.
StatusRulesParse loadStatusRules({String? path, Map<String, String>? env}) {
  final target = path ?? alertPatternsPath(env: env);
  if (target == null) return const StatusRulesParse(StatusRules.builtin, []);

  final file = File(target);
  if (!file.existsSync()) {
    return const StatusRulesParse(StatusRules.builtin, []);
  }
  final String raw;
  try {
    raw = file.readAsStringSync();
  } on FileSystemException catch (e) {
    return StatusRulesParse(
        StatusRules.builtin, ['không đọc được $target: ${e.message}']);
  }
  return StatusRules.fromJsonString(raw, jsonDecode);
}

/// AlertRulesWatcher: theo dõi file pattern để nạp lại khi AI (qua tool `alert`)
/// hoặc người dùng sửa nó, không cần khởi động lại app.
///
/// Dùng stat (mtime + size) chứ không `File.watch`: worker ghi kiểu tmp+rename
/// nên inode đổi — watch trên chính file sẽ mất tín hiệu; watch trên thư mục thì
/// hỏng khi thư mục còn chưa tồn tại. Một lần stat mỗi vài giây rẻ hơn cả hai.
class AlertRulesWatcher {
  AlertRulesWatcher({String? path, Map<String, String>? env})
      : path = path ?? alertPatternsPath(env: env) {
    _seen = _stamp();
  }

  final String? path;

  /// Dấu vết lần nhìn gần nhất: "mtime/size", hoặc null khi file không tồn tại.
  String? _seen;

  String? _stamp() {
    final target = path;
    if (target == null) return null;
    final file = File(target);
    if (!file.existsSync()) return null;
    try {
      final stat = file.statSync();
      return '${stat.modified.microsecondsSinceEpoch}/${stat.size}';
    } on FileSystemException {
      return null;
    }
  }

  /// changed trả true khi file vừa xuất hiện, đổi nội dung, hoặc bị xoá kể từ
  /// lần gọi trước. Gọi xong là coi như đã nhìn.
  bool changed() {
    final now = _stamp();
    if (now == _seen) return false;
    _seen = now;
    return true;
  }

  /// reload đọc lại file và đồng thời cập nhật dấu vết.
  StatusRulesParse reload() {
    _seen = _stamp();
    return loadStatusRules(path: path);
  }
}
