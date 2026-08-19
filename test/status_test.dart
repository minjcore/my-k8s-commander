import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:k8s_commander_app/src/status.dart';

/// Những chuỗi dưới đây phải khớp NGUYÊN VĂN output của worker — parser đọc log
/// chứ không có protocol riêng, nên đây chính là hợp đồng giữa hai bên.
void main() {
  group('trạng thái gom từ log', () {
    test('use <ctx> đổi context', () {
      final s = const StatusModel()
          .applyLine('[k8s-worker] đang dùng context: colima');
      expect(s.kubeContext, 'colima');
    });

    test('dòng có dấu * trong output ctx là context đang dùng', () {
      final s = const StatusModel().applyLine('[k8s-worker] * colima');
      expect(s.kubeContext, 'colima');
    });

    test('dòng bảng khác mở đầu bằng * không bị nhận nhầm', () {
      final s = const StatusModel().applyLine('[k8s-worker] * a b c');
      expect(s.kubeContext, isNull);
    });

    test('dòng chi phí model local', () {
      final s = const StatusModel().applyLine(
          '[ai-worker] qwen3.5:9b (local) · 2.6k in · 62 out · miễn phí');
      expect(s.aiModel, 'qwen3.5:9b');
      expect(s.aiLocal, isTrue);
      expect(s.monthCost, isNull);
    });

    test('dòng chi phí model cloud lấy cả tiền tháng', () {
      final s = const StatusModel().applyLine(
          '[ai-worker] haiku · 1.2k in · 340 out · <\$0.01 · tháng này \$0.83/\$5.00');
      expect(s.aiModel, 'haiku');
      expect(s.aiLocal, isFalse);
      expect(s.monthCost, '\$0.83/\$5.00');
    });

    test('chuyển local -> cloud vẫn giữ tiền tháng đã biết', () {
      final s = const StatusModel()
          .applyLine('[ai-worker] haiku · 1.2k in · 3 out · <\$0.01 · tháng này \$1.00/\$5.00')
          .applyLine('[ai-worker] qwen3.5:9b (local) · 1k in · 2 out · miễn phí');
      expect(s.aiLocal, isTrue);
      expect(s.monthCost, '\$1.00/\$5.00');
    });

    test('dòng không liên quan trả về đúng object cũ', () {
      const before = StatusModel();
      expect(before.applyLine('[console-worker] gì đó'), same(before));
    });
  });

  group('phát hiện bất thường', () {
    test('pod CrashLoopBackOff', () {
      final s = const StatusModel().applyLine(
          '[k8s-worker] default      crashloop-test    0/1    CrashLoopBackOff  5   4m');
      expect(s.hasAlert, isTrue);
      expect(s.latestAlert!.kind, AlertKind.podCrash);
      expect(s.latestAlert!.subject, 'crashloop-test');
      expect(s.latestAlert!.detail, 'CrashLoopBackOff');
    });

    test('pod Running không phải bất thường', () {
      final s = const StatusModel().applyLine(
          '[k8s-worker] kube-system  coredns-64fd  1/1  Running  0  12m');
      expect(s.hasAlert, isFalse);
    });

    test('node NotReady', () {
      final s = const StatusModel()
          .applyLine('[k8s-worker] colima  NotReady  v1.33.4+k3s1  5m');
      expect(s.latestAlert!.kind, AlertKind.nodeNotReady);
      expect(s.latestAlert!.subject, 'colima');
    });

    test('chữ Error trong câu văn không thành cảnh báo', () {
      final s = const StatusModel()
          .applyLine('[k8s-worker] không có pod nào Error trong namespace');
      expect(s.hasAlert, isFalse);
    });

    test('dòng lỗi của worker thành cảnh báo', () {
      final s = const StatusModel()
          .applyLine('[k8s-worker] lỗi list nodes: connection refused');
      expect(s.latestAlert!.kind, AlertKind.workerError);
      expect(s.latestAlert!.subject, 'k8s-worker');
    });

    test('cùng pod cùng lý do chỉ đếm một lần', () {
      const row =
          '[k8s-worker] default  crashloop-test  0/1  CrashLoopBackOff  5  4m';
      var s = const StatusModel();
      for (var i = 0; i < 5; i++) {
        s = s.applyLine(row);
      }
      expect(s.alerts.length, 1);
    });

    test('không giữ quá maxAlerts cảnh báo', () {
      var s = const StatusModel();
      for (var i = 0; i < maxAlerts + 10; i++) {
        s = s.applyLine(
            '[k8s-worker] default  pod-$i  0/1  CrashLoopBackOff  1  1m');
      }
      expect(s.alerts.length, maxAlerts);
      expect(s.latestAlert!.subject, 'pod-${maxAlerts + 9}');
    });

    test('clearAlerts xoá hết nhưng giữ trạng thái khác', () {
      final s = const StatusModel()
          .applyLine('[k8s-worker] đang dùng context: colima')
          .applyLine('[k8s-worker] lỗi gì đó')
          .clearAlerts();
      expect(s.hasAlert, isFalse);
      expect(s.kubeContext, 'colima');
    });
  });

  group('pattern tự định nghĩa', () {
    StatusRules rulesFrom(String raw) {
      final parsed = StatusRules.fromJsonString(raw, jsonDecode);
      expect(parsed.problems, isEmpty);
      return parsed.rules;
    }

    test('bắt theo regex, capture group thành subject', () {
      final rules = rulesFrom(
          '{"patterns":[{"name":"disk đầy","regex":"no space left on device.*pod=(\\\\S+)"}]}');
      final s = const StatusModel().applyLine(
          '[server-worker] no space left on device pod=web-1', rules: rules);
      expect(s.latestAlert!.kind, AlertKind.custom);
      expect(s.latestAlert!.subject, 'web-1');
      expect(s.latestAlert!.detail, 'disk đầy');
    });

    test('không có capture group thì subject là tên luật', () {
      final rules = rulesFrom('{"patterns":[{"name":"oom","regex":"OOMKilled"}]}');
      final s = const StatusModel()
          .applyLine('[server-worker] dmesg: OOMKilled', rules: rules);
      expect(s.latestAlert!.subject, 'oom');
    });

    test('luật riêng soi được cả worker khác, không chỉ k8s', () {
      final rules = rulesFrom('{"patterns":[{"name":"ssh fail","regex":"permission denied"}]}');
      final s = const StatusModel()
          .applyLine('[server-worker] permission denied (publickey)', rules: rules);
      expect(s.hasAlert, isTrue);
    });

    test('JSON sai không làm sập, báo problems và dùng luật dựng sẵn', () {
      final parsed = StatusRules.fromJsonString('{khong-phai-json', jsonDecode);
      expect(parsed.rules.custom, isEmpty);
      expect(parsed.problems, isNotEmpty);
    });

    test('regex sai chỉ bỏ luật đó, luật còn lại vẫn chạy', () {
      final parsed = StatusRules.fromJsonString(
          '{"patterns":[{"name":"xau","regex":"([unclosed"},{"name":"tot","regex":"OOMKilled"}]}',
          jsonDecode);
      expect(parsed.problems.length, 1);
      expect(parsed.rules.custom.length, 1);
      expect(parsed.rules.custom.first.name, 'tot');
    });

    test('pattern thiếu field bị bỏ qua', () {
      final parsed = StatusRules.fromJsonString(
          '{"patterns":[{"name":"thieu-regex"},{"regex":"thieu-name"}]}', jsonDecode);
      expect(parsed.problems.length, 2);
      expect(parsed.rules.custom, isEmpty);
    });

    test('quá maxCustomPatterns thì cắt và báo', () {
      final many = List.generate(
          maxCustomPatterns + 5, (i) => '{"name":"p$i","regex":"x$i"}').join(',');
      final parsed =
          StatusRules.fromJsonString('{"patterns":[$many]}', jsonDecode);
      expect(parsed.rules.custom.length, maxCustomPatterns);
      expect(parsed.problems, isNotEmpty);
    });
  });
}
