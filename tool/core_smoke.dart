// Smoke test round-trip Go core <-> FFI mà không cần GUI:
// StartCore -> đợi worker chạy -> SendToModule -> đọc lại echo qua GetLogs.
//
//   make build build-ffi
//   K8SC_CORE_ROOT=$PWD dart run tool/core_smoke.dart
import 'dart:io';

import 'package:k8s_commander_app/src/native_core.dart';

Future<void> main() async {
  final NativeCore core;
  try {
    core = NativeCore.load();
  } on CoreLoadException catch (e) {
    stderr.writeln('FAIL: $e');
    for (final p in e.triedPaths) {
      stderr.writeln('  đã thử: $p');
    }
    exit(1);
  }

  stdout.writeln('lib     : ${core.location.libraryPath}');
  stdout.writeln('modules : ${core.location.modulesDir}');
  if (!core.start()) {
    stderr.writeln('FAIL: StartCore != 0');
    exit(1);
  }

  final seen = <String>[];
  void drain() => seen.addAll(core.drainLogs());

  // Đợi supervisor spawn xong workers (ai, k8s, console, server).
  for (var i = 0; i < 20 && core.runningModules().length < 4; i++) {
    await Future<void>.delayed(const Duration(milliseconds: 100));
    drain();
  }
  drain();
  stdout.writeln('modules đang chạy: ${core.runningModules().join(", ")}');

  // `ctx` đọc kubeconfig thật -> chứng minh cả đường đi lẫn client-go hoạt động.
  // `server list` đọc sổ server -> module-server cũng nhận được lệnh.
  const expect = 'kubeconfig:';
  const expectServer = '[server-worker]';
  final sent = core.send('k8s-worker', 'ctx');
  final sentServer = core.send('server-worker', 'server list');
  stdout.writeln('send k8s-worker ctx -> $sent');
  stdout.writeln('send server-worker list -> $sentServer');

  // Chờ output từ stdout của worker quay lại buffer log.
  var echoed = false;
  for (var i = 0; i < 50 && !echoed; i++) {
    await Future<void>.delayed(const Duration(milliseconds: 100));
    drain();
    echoed = seen.any((l) => l.contains(expect)) &&
        seen.any((l) => l.contains(expectServer));
  }

  stdout.writeln('--- log thu được ---');
  for (final line in seen) {
    stdout.writeln(line);
  }

  core.stop();

  if (sent != SendResult.ok || sentServer != SendResult.ok || !echoed) {
    stderr.writeln('FAIL: không thấy "$expect" + "$expectServer" quay về');
    exit(1);
  }
  stdout.writeln('OK: round-trip Flutter -> Go -> worker -> log hoạt động');
}
