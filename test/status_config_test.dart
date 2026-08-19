import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:k8s_commander_app/src/status_config.dart';

void main() {
  late Directory tmp;

  setUp(() => tmp = Directory.systemTemp.createTempSync('k8sc-alert-'));
  tearDown(() => tmp.deleteSync(recursive: true));

  String pathIn(String name) => '${tmp.path}${Platform.pathSeparator}$name';

  group('đường dẫn file pattern', () {
    test('env đè tất cả', () {
      final p = alertPatternsPath(env: {
        alertPatternsEnvVar: '/tuy/chon.json',
        'HOME': '/nha',
      });
      expect(p, '/tuy/chon.json');
    });

    test('không có env thì theo HOME', () {
      final p = alertPatternsPath(env: {'HOME': '/nha'});
      expect(p, contains(configDirName));
      expect(p, endsWith(alertPatternsFileName));
      expect(p, startsWith('/nha'));
    });

    test('không biết HOME thì trả null chứ không đoán', () {
      expect(alertPatternsPath(env: const {}), isNull);
    });
  });

  group('nạp cấu hình', () {
    test('chưa có file không phải lỗi', () {
      final parsed = loadStatusRules(path: pathIn('khong-co.json'));
      expect(parsed.rules.custom, isEmpty);
      expect(parsed.problems, isEmpty);
    });

    test('file hợp lệ nạp được pattern', () {
      final p = pathIn('ok.json');
      File(p).writeAsStringSync('{"patterns":[{"name":"oom","regex":"OOMKilled"}]}');
      final parsed = loadStatusRules(path: p);
      expect(parsed.problems, isEmpty);
      expect(parsed.rules.custom.single.name, 'oom');
    });

    test('file hỏng thì báo problems, không ném exception', () {
      final p = pathIn('hong.json');
      File(p).writeAsStringSync('{khong-phai-json');
      final parsed = loadStatusRules(path: p);
      expect(parsed.rules.custom, isEmpty);
      expect(parsed.problems, isNotEmpty);
    });

    test('không biết HOME và không truyền path thì dùng luật dựng sẵn', () {
      final parsed = loadStatusRules(env: const {});
      expect(parsed.rules.custom, isEmpty);
      expect(parsed.problems, isEmpty);
    });
  });

  group('theo dõi file', () {
    test('không đổi thì không báo đổi', () {
      final p = pathIn('w.json');
      File(p).writeAsStringSync('{"patterns":[]}');
      final w = AlertRulesWatcher(path: p);
      expect(w.changed(), isFalse);
      expect(w.changed(), isFalse);
    });

    test('file mới xuất hiện là một lần đổi', () {
      final p = pathIn('sau.json');
      final w = AlertRulesWatcher(path: p);
      expect(w.changed(), isFalse);
      File(p).writeAsStringSync('{"patterns":[{"name":"a","regex":"x"}]}');
      expect(w.changed(), isTrue);
      expect(w.changed(), isFalse);
    });

    test('nội dung đổi thì reload ra pattern mới', () {
      final p = pathIn('doi.json');
      File(p).writeAsStringSync('{"patterns":[{"name":"a","regex":"x"}]}');
      final w = AlertRulesWatcher(path: p);

      File(p).writeAsStringSync(
          '{"patterns":[{"name":"a","regex":"x"},{"name":"b","regex":"y"}]}');
      expect(w.changed(), isTrue);
      final parsed = w.reload();
      expect(parsed.rules.custom.length, 2);
      // reload đã ghi nhận dấu vết mới -> không báo đổi lần nữa.
      expect(w.changed(), isFalse);
    });

    test('file bị xoá cũng là một lần đổi', () {
      final p = pathIn('xoa.json');
      File(p).writeAsStringSync('{"patterns":[]}');
      final w = AlertRulesWatcher(path: p);
      File(p).deleteSync();
      expect(w.changed(), isTrue);
    });

    test('không biết đường dẫn thì im lặng, không crash', () {
      final w = AlertRulesWatcher(env: const {});
      expect(w.path, isNull);
      expect(w.changed(), isFalse);
      expect(w.reload().rules.custom, isEmpty);
    });
  });
}
