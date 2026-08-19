// Lớp FFI nối Flutter với Go core (cmd/supervisor, build bằng `make build-ffi`).
// Exports dùng ở đây: StartCore, GetLogs, GetLogsSize, SendToModule, ListModules, StopCore.
import 'dart:convert';
import 'dart:ffi';
import 'dart:io';

import 'package:ffi/ffi.dart';

typedef _StartCoreC = Int32 Function(Pointer<Utf8>);
typedef _StartCoreDart = int Function(Pointer<Utf8>);

typedef _StopCoreC = Void Function();
typedef _StopCoreDart = void Function();

typedef _GetLogsC = Pointer<Uint8> Function();
typedef _GetLogsDart = Pointer<Uint8> Function();

typedef _GetLogsSizeC = Int32 Function();
typedef _GetLogsSizeDart = int Function();

typedef _SendToModuleC = Int32 Function(Pointer<Utf8>, Pointer<Utf8>);
typedef _SendToModuleDart = int Function(Pointer<Utf8>, Pointer<Utf8>);

typedef _ListModulesC = Pointer<Utf8> Function();
typedef _ListModulesDart = Pointer<Utf8> Function();

/// Kết quả gửi lệnh xuống module.
enum SendResult { ok, coreNotStarted, moduleNotRunning, writeFailed }

/// Vị trí shared library + thư mục modules tương ứng.
class CoreLocation {
  const CoreLocation(this.libraryPath, this.modulesDir);

  final String libraryPath;
  final String modulesDir;
}

/// Ngoại lệ khi không nạp được core — UI hiển thị nguyên văn để biết đường mà sửa.
class CoreLoadException implements Exception {
  CoreLoadException(this.message, this.triedPaths);

  final String message;
  final List<String> triedPaths;

  @override
  String toString() => message;
}

class NativeCore {
  NativeCore._(this._lib, this.location);

  final DynamicLibrary _lib;
  final CoreLocation location;

  late final _StartCoreDart _startCore =
      _lib.lookupFunction<_StartCoreC, _StartCoreDart>('StartCore');
  late final _StopCoreDart _stopCore =
      _lib.lookupFunction<_StopCoreC, _StopCoreDart>('StopCore');
  late final _GetLogsDart _getLogs =
      _lib.lookupFunction<_GetLogsC, _GetLogsDart>('GetLogs');
  late final _GetLogsSizeDart _getLogsSize =
      _lib.lookupFunction<_GetLogsSizeC, _GetLogsSizeDart>('GetLogsSize');
  late final _SendToModuleDart _sendToModule =
      _lib.lookupFunction<_SendToModuleC, _SendToModuleDart>('SendToModule');
  late final _ListModulesDart _listModules =
      _lib.lookupFunction<_ListModulesC, _ListModulesDart>('ListModules');

  /// Dòng chưa trọn vẹn từ lần drain trước (GetLogs có thể cắt giữa dòng).
  String _pending = '';

  static final RegExp _ansi = RegExp(r'\x1B\[[0-9;]*[a-zA-Z]');

  /// Nạp shared library. Ném [CoreLoadException] kèm danh sách path đã thử.
  static NativeCore load() {
    final tried = <String>[];
    for (final candidate in _candidates()) {
      tried.add(candidate.libraryPath);
      if (!File(candidate.libraryPath).existsSync()) continue;
      try {
        return NativeCore._(DynamicLibrary.open(candidate.libraryPath), candidate);
      } on ArgumentError catch (e) {
        throw CoreLoadException('dlopen ${candidate.libraryPath} lỗi: $e', tried);
      }
    }
    throw CoreLoadException(
      'không tìm thấy ${_libraryName()} — chạy `make build-ffi` ở gốc repo',
      tried,
    );
  }

  static String _libraryName() {
    if (Platform.isMacOS) return 'libmyk8s_commander.dylib';
    if (Platform.isWindows) return 'myk8s_commander.dll';
    return 'libmyk8s_commander.so';
  }

  /// Thứ tự tìm: biến môi trường -> trong app bundle -> đi ngược lên tìm gốc repo (dev).
  static Iterable<CoreLocation> _candidates() {
    final name = _libraryName();
    final out = <CoreLocation>[];

    final envRoot = Platform.environment['K8SC_CORE_ROOT'];
    if (envRoot != null && envRoot.isNotEmpty) {
      out.add(CoreLocation('$envRoot/$name', '$envRoot/modules'));
    }

    final exeDir = File(Platform.resolvedExecutable).parent;
    if (Platform.isMacOS) {
      // <App>.app/Contents/MacOS/exe -> Frameworks/lib, Resources/modules
      final contents = exeDir.parent.path;
      out.add(CoreLocation('$contents/Frameworks/$name', '$contents/Resources/modules'));
    }
    out.add(CoreLocation('${exeDir.path}/$name', '${exeDir.path}/modules'));

    // Debug: build/macos/Build/Products/Debug/<App>.app/Contents/MacOS -> gốc repo.
    var dir = exeDir;
    for (var i = 0; i < 12; i++) {
      out.add(CoreLocation('${dir.path}/$name', '${dir.path}/modules'));
      final parent = dir.parent;
      if (parent.path == dir.path) break;
      dir = parent;
    }
    return out;
  }

  /// Khởi động supervisor với thư mục modules tuyệt đối. true nếu StartCore trả 0.
  bool start() {
    final path = location.modulesDir.toNativeUtf8();
    try {
      return _startCore(path) == 0;
    } finally {
      calloc.free(path);
    }
  }

  void stop() => _stopCore();

  /// Lấy log mới từ Go buffer, trả về các dòng đã trọn vẹn.
  /// Phải gọi GetLogs() trước GetLogsSize() — size chỉ được set bên trong GetLogs.
  List<String> drainLogs() {
    final ptr = _getLogs();
    if (ptr == nullptr) return const [];
    final size = _getLogsSize();
    if (size <= 0) return const [];

    _pending += utf8.decode(ptr.asTypedList(size), allowMalformed: true);
    final parts = _pending.split('\n');
    _pending = parts.removeLast(); // phần đuôi chưa có '\n'
    return parts
        .map((line) => line.replaceAll(_ansi, '').trimRight())
        .where((line) => line.isNotEmpty)
        .toList(growable: false);
  }

  /// Danh sách module đang chạy (đọc từ ListModules).
  List<String> runningModules() {
    final ptr = _listModules();
    if (ptr == nullptr) return const [];
    final raw = ptr.toDartString();
    if (raw.isEmpty) return const [];
    return raw.split(',');
  }

  SendResult send(String module, String line) {
    final nativeModule = module.toNativeUtf8();
    final nativeLine = line.toNativeUtf8();
    try {
      switch (_sendToModule(nativeModule, nativeLine)) {
        case 0:
          return SendResult.ok;
        case -2:
          return SendResult.moduleNotRunning;
        case -1:
          return SendResult.coreNotStarted;
        default:
          return SendResult.writeFailed;
      }
    } finally {
      calloc.free(nativeModule);
      calloc.free(nativeLine);
    }
  }
}
