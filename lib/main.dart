import 'dart:async';

import 'package:flutter/material.dart';

import 'src/native_core.dart';
import 'src/routing.dart';
import 'src/status.dart';
import 'src/status_config.dart';

void main() {
  runApp(const K8sCommanderApp());
}

class K8sCommanderApp extends StatelessWidget {
  const K8sCommanderApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'K8S Commander',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        useMaterial3: true,
        brightness: Brightness.dark,
        colorScheme: ColorScheme.dark(
          primary: const Color(0xFF00E5A0),
          surface: const Color(0xFF0D1117),
          onSurface: const Color(0xFFE6EDF3),
        ),
        scaffoldBackgroundColor: const Color(0xFF010409),
      ),
      home: const TerminalScreen(),
    );
  }
}

/// Nguồn của một dòng trong Terminal — quyết định màu.
enum _LineKind { log, command, uiError }

class _LogLine {
  const _LogLine(this.text, this.kind);

  final String text;
  final _LineKind kind;
}

class _TerminalScreenState extends State<TerminalScreen> {
  static const _pollInterval = Duration(milliseconds: 250);
  static const _maxLines = 2000;

  /// Worker giữ context trong tiến trình của nó, UI không đọc được kubeconfig —
  /// nên hỏi `ctx` một lần khi worker vừa lên để biết context đang dùng.
  static const _k8sWorker = 'k8s-worker';
  static const _ctxCommand = 'ctx';

  /// Cứ ngần này nhịp poll thì stat lại file pattern (8 x 250ms = 2s). Đủ nhanh
  /// để AI vừa thêm pattern là thấy, đủ thưa để không ai đếm được chi phí.
  static const _reloadEveryTicks = 8;

  /// Tự gọi `health` mỗi 120 nhịp (30s) để status bar biết cụm có vấn đề mà
  /// không cần người dùng gõ gì. Lệnh này im lặng khi cụm ổn nên không làm rác
  /// Terminal; mỗi lần chạy là một lần list pod + node trên cụm.
  static const _healthEveryTicks = 120;
  static const _healthCommand = 'health';

  final TextEditingController _searchController = TextEditingController();
  final List<_LogLine> _logLines = [];
  final ScrollController _scrollController = ScrollController();

  NativeCore? _core;
  String? _coreError;
  List<String> _modules = const [];
  Timer? _poll;
  StatusModel _status = const StatusModel();
  StatusRules _rules = StatusRules.builtin;
  AlertRulesWatcher? _rulesWatcher;
  bool _askedContext = false;
  int _tick = 0;
  DateTime? _lastHealthCheck;

  @override
  void initState() {
    super.initState();
    _loadAlertRules();
    _bootCore();
  }

  void _loadAlertRules() {
    final watcher = AlertRulesWatcher();
    _rulesWatcher = watcher;
    _applyRules(loadStatusRules(path: watcher.path));
  }

  /// _maybeReloadRules nạp lại luật khi file đổi — AI thêm pattern bằng tool
  /// `alert` là thấy ngay, không phải mở lại app.
  void _maybeReloadRules() {
    final watcher = _rulesWatcher;
    if (watcher == null || !watcher.changed()) return;
    _append('[UI] cấu hình cảnh báo đổi, nạp lại: ${watcher.path}', _LineKind.log);
    _applyRules(watcher.reload());
  }

  void _applyRules(StatusRulesParse parsed) {
    _rules = parsed.rules;
    if (parsed.rules.custom.isNotEmpty) {
      _append('[UI] ${parsed.rules.custom.length} pattern cảnh báo tự định nghĩa',
          _LineKind.log);
    }
    for (final problem in parsed.problems) {
      _append('[UI] cấu hình cảnh báo: $problem', _LineKind.uiError);
    }
  }

  void _bootCore() {
    try {
      final core = NativeCore.load();
      _core = core;
      _append('[UI] core: ${core.location.libraryPath}', _LineKind.log);
      _append('[UI] modules: ${core.location.modulesDir}', _LineKind.log);
      if (!core.start()) {
        _append('[UI] StartCore trả về lỗi — xem log bên dưới', _LineKind.uiError);
      }
      _poll = Timer.periodic(_pollInterval, (_) {
        _tick++;
        if (_tick % _reloadEveryTicks == 0) _maybeReloadRules();
        if (_tick % _healthEveryTicks == 0) _sendHealth();
        _drain();
      });
    } on CoreLoadException catch (e) {
      setState(() => _coreError = e.message);
      _append('[UI] ${e.message}', _LineKind.uiError);
      for (final path in e.triedPaths) {
        _append('[UI]   đã thử: $path', _LineKind.uiError);
      }
    }
  }

  void _drain() {
    final core = _core;
    if (core == null) return;
    final lines = core.drainLogs();
    final modules = core.runningModules();
    if (lines.isEmpty && _listEquals(modules, _modules)) return;
    var status = _status;
    setState(() {
      _modules = modules;
      for (final line in lines) {
        _logLines.add(_LogLine(line, _LineKind.log));
        status = status.applyLine(line, rules: _rules);
      }
      _status = status;
      if (_logLines.length > _maxLines) {
        _logLines.removeRange(0, _logLines.length - _maxLines);
      }
    });
    _askContextOnce(core, modules);
    _scrollToEnd();
  }

  void _askContextOnce(NativeCore core, List<String> modules) {
    if (_askedContext || !modules.contains(_k8sWorker)) return;
    _askedContext = true;
    core.send(_k8sWorker, _ctxCommand);
    // Quét sức khoẻ ngay lần đầu, đừng để người dùng chờ hết 30s mới biết.
    _sendHealth();
  }

  /// _sendHealth bảo k8s-worker quét pod/node. Worker chỉ in ra dòng bất thường,
  /// nên vòng poll này không đổ bảng vào Terminal; cảnh báo (nếu có) tự chảy qua
  /// parser của status bar như mọi dòng log khác.
  void _sendHealth() {
    final core = _core;
    if (core == null || !_modules.contains(_k8sWorker)) return;
    if (core.send(_k8sWorker, _healthCommand) == SendResult.ok) {
      setState(() => _lastHealthCheck = DateTime.now());
    }
  }

  /// Giờ dạng HH:MM:SS — hiện mốc tuyệt đối chứ không "N giây trước", vì status
  /// bar chỉ vẽ lại khi có log mới, số đếm tương đối sẽ đứng im và gây hiểu sai.
  static String _clock(DateTime t) {
    String two(int v) => v.toString().padLeft(2, '0');
    return '${two(t.hour)}:${two(t.minute)}:${two(t.second)}';
  }

  static bool _listEquals(List<String> a, List<String> b) {
    if (a.length != b.length) return false;
    for (var i = 0; i < a.length; i++) {
      if (a[i] != b[i]) return false;
    }
    return true;
  }

  void _append(String text, _LineKind kind) {
    setState(() {
      _logLines.add(_LogLine(text, kind));
      if (_logLines.length > _maxLines) {
        _logLines.removeRange(0, _logLines.length - _maxLines);
      }
    });
    _scrollToEnd();
  }

  void _scrollToEnd() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!_scrollController.hasClients) return;
      _scrollController.jumpTo(_scrollController.position.maxScrollExtent);
    });
  }

  void _submit(String value) {
    final input = value.trim();
    if (input.isEmpty) return;
    _searchController.clear();

    final core = _core;
    if (core == null) {
      _append('> $input', _LineKind.command);
      _append('[UI] core chưa nạp được, không gửi đi đâu cả', _LineKind.uiError);
      return;
    }

    final route = routeCommand(input);
    final module = route.module;
    _append('> [$module] ${route.payload}', _LineKind.command);
    switch (core.send(module, route.payload)) {
      case SendResult.ok:
        break;
      case SendResult.moduleNotRunning:
        _append('[UI] $module không chạy (đang chạy: ${_modules.join(", ")})',
            _LineKind.uiError);
      case SendResult.coreNotStarted:
        _append('[UI] core chưa start', _LineKind.uiError);
      case SendResult.writeFailed:
        _append('[UI] ghi vào stdin $module thất bại', _LineKind.uiError);
    }
  }

  @override
  void dispose() {
    _poll?.cancel();
    _core?.stop();
    _searchController.dispose();
    _scrollController.dispose();
    super.dispose();
  }

  Color _colorFor(_LineKind kind) {
    switch (kind) {
      case _LineKind.command:
        return const Color(0xFF00E5A0).withValues(alpha: 0.9);
      case _LineKind.uiError:
        return const Color(0xFFFF7B72);
      case _LineKind.log:
        return const Color(0xFFE6EDF3).withValues(alpha: 0.9);
    }
  }

  /// Một ô nhỏ trên status bar: icon + chữ, cắt bằng ellipsis khi hẹp.
  Widget _statusChip(IconData icon, String text, {Color? color}) {
    final tint = color ?? Colors.white.withValues(alpha: 0.55);
    return Padding(
      padding: const EdgeInsets.only(right: 16),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 12, color: tint),
          const SizedBox(width: 5),
          Text(
            text,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: TextStyle(fontFamily: 'monospace', fontSize: 11, color: tint),
          ),
        ],
      ),
    );
  }

  /// Status bar: trạng thái gom từ stream log + cảnh báo bất thường. Bấm vào
  /// cảnh báo để xoá (đã xem rồi).
  Widget _statusBar() {
    const accent = Color(0xFF00E5A0);
    const danger = Color(0xFFFF7B72);
    final alert = _status.latestAlert;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 8),
      decoration: BoxDecoration(
        color: const Color(0xFF0D1117),
        border: Border(top: BorderSide(color: Colors.white.withValues(alpha: 0.08))),
      ),
      child: Row(
        children: [
          _statusChip(Icons.hub_outlined, _status.kubeContext ?? 'context ?'),
          _statusChip(
            _status.aiLocal ? Icons.memory : Icons.cloud_outlined,
            _status.aiModel ?? 'ai ?',
            color: _status.aiLocal ? accent.withValues(alpha: 0.8) : null,
          ),
          if (_status.monthCost != null)
            _statusChip(Icons.payments_outlined, _status.monthCost!),
          _statusChip(Icons.dns_outlined, '${_modules.length} worker'),
          if (_lastHealthCheck != null)
            _statusChip(Icons.monitor_heart_outlined,
                'quét ${_clock(_lastHealthCheck!)}'),
          const Spacer(),
          if (alert == null)
            _statusChip(Icons.check_circle_outline, 'không có bất thường',
                color: accent.withValues(alpha: 0.7))
          else
            Flexible(
              child: InkWell(
                onTap: () => setState(() => _status = _status.clearAlerts()),
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    const Icon(Icons.warning_amber_rounded, size: 13, color: danger),
                    const SizedBox(width: 5),
                    Flexible(
                      child: Text(
                        '${_status.alerts.length} · $alert',
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(
                          fontFamily: 'monospace',
                          fontSize: 11,
                          color: danger,
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final healthy = _core != null;
    return Scaffold(
      body: SafeArea(
        child: Column(
          children: [
            const SizedBox(height: 48),
            // Thanh search kiểu Command Palette
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 24),
              child: TextField(
                controller: _searchController,
                autofocus: true,
                style: const TextStyle(
                  fontSize: 18,
                  color: Color(0xFFE6EDF3),
                  fontFamily: 'monospace',
                ),
                decoration: InputDecoration(
                  hintText: 'hỏi bất cứ điều gì · kubectl get pods · cluster list · server nodes',
                  hintStyle: TextStyle(color: Colors.white.withValues(alpha: 0.4)),
                  filled: true,
                  fillColor: const Color(0xFF161B22),
                  border: OutlineInputBorder(
                    borderRadius: BorderRadius.circular(12),
                    borderSide: BorderSide(color: const Color(0xFF00E5A0).withValues(alpha: 0.5)),
                  ),
                  enabledBorder: OutlineInputBorder(
                    borderRadius: BorderRadius.circular(12),
                    borderSide: BorderSide(color: Colors.white.withValues(alpha: 0.1)),
                  ),
                  focusedBorder: OutlineInputBorder(
                    borderRadius: BorderRadius.circular(12),
                    borderSide: const BorderSide(color: Color(0xFF00E5A0), width: 2),
                  ),
                  prefixIcon: const Icon(Icons.terminal, color: Color(0xFF00E5A0), size: 24),
                ),
                onSubmitted: _submit,
              ),
            ),
            const SizedBox(height: 24),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 24),
              child: Row(
                children: [
                  const Text(
                    'Terminal',
                    style: TextStyle(
                      color: Color(0xFF00E5A0),
                      fontSize: 12,
                      fontWeight: FontWeight.w600,
                      letterSpacing: 1,
                    ),
                  ),
                  const Spacer(),
                  Container(
                    width: 8,
                    height: 8,
                    decoration: BoxDecoration(
                      shape: BoxShape.circle,
                      color: healthy ? const Color(0xFF00E5A0) : const Color(0xFFFF7B72),
                    ),
                  ),
                  const SizedBox(width: 8),
                  // Flexible + ellipsis: message lỗi core có thể rất dài.
                  Flexible(
                    child: Text(
                      healthy
                          ? (_modules.isEmpty
                              ? 'core loaded · chưa có module nào chạy'
                              : 'core loaded · ${_modules.join(" · ")}')
                          : (_coreError ?? 'core lỗi'),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(
                        fontFamily: 'monospace',
                        fontSize: 11,
                        color: Colors.white.withValues(alpha: 0.55),
                      ),
                    ),
                  ),
                ],
              ),
            ),
            const SizedBox(height: 8),
            // Vùng log — CustomScrollView
            Expanded(
              child: CustomScrollView(
                controller: _scrollController,
                slivers: [
                  SliverPadding(
                    padding: const EdgeInsets.symmetric(horizontal: 24),
                    sliver: SliverList(
                      delegate: SliverChildBuilderDelegate(
                        (context, index) {
                          final line = _logLines[index];
                          return Padding(
                            padding: const EdgeInsets.only(bottom: 4),
                            child: SelectableText(
                              line.text,
                              style: TextStyle(
                                fontFamily: 'monospace',
                                fontSize: 13,
                                color: _colorFor(line.kind),
                              ),
                            ),
                          );
                        },
                        childCount: _logLines.length,
                      ),
                    ),
                  ),
                ],
              ),
            ),
            _statusBar(),
          ],
        ),
      ),
    );
  }
}

class TerminalScreen extends StatefulWidget {
  const TerminalScreen({super.key});

  @override
  State<TerminalScreen> createState() => _TerminalScreenState();
}
