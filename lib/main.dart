import 'dart:async';

import 'package:flutter/material.dart';

import 'src/native_core.dart';
import 'src/routing.dart';

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

  final TextEditingController _searchController = TextEditingController();
  final List<_LogLine> _logLines = [];
  final ScrollController _scrollController = ScrollController();

  NativeCore? _core;
  String? _coreError;
  List<String> _modules = const [];
  Timer? _poll;

  @override
  void initState() {
    super.initState();
    _bootCore();
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
      _poll = Timer.periodic(_pollInterval, (_) => _drain());
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
    setState(() {
      _modules = modules;
      for (final line in lines) {
        _logLines.add(_LogLine(line, _LineKind.log));
      }
      if (_logLines.length > _maxLines) {
        _logLines.removeRange(0, _logLines.length - _maxLines);
      }
    });
    _scrollToEnd();
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
