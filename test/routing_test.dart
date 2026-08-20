import 'package:flutter_test/flutter_test.dart';
import 'package:k8s_commander_app/src/routing.dart';

void main() {
  group('alias tường minh', () {
    test('ai <câu hỏi> -> ai-worker, cắt prefix', () {
      final r = routeCommand('ai pod nào crash?');
      expect(r.module, aiWorker);
      expect(r.payload, 'pod nào crash?');
    });

    test('alias gõ trống -> worker tự in usage', () {
      expect(routeCommand('ai').payload, 'help');
      expect(routeCommand('k8s').payload, 'help');
    });

    test('kubectl/cluster/server giữ nguyên từ khoá đầu cho worker', () {
      expect(routeCommand('kubectl get pods'),
          (module: k8sWorker, payload: 'kubectl get pods'));
      expect(routeCommand('cluster list'),
          (module: k8sWorker, payload: 'cluster list'));
      expect(routeCommand('server run prod-1 uptime'),
          (module: serverWorker, payload: 'server run prod-1 uptime'));
      expect(routeCommand('srv list').module, serverWorker);
    });
  });

  group('không có alias', () {
    test('lệnh k8s quen thuộc vẫn vào k8s-worker', () {
      for (final cmd in [
        'get pods -A',
        'ctx',
        'contexts',
        'use dev',
        'node addr',
        'nodes',
        'help',
      ]) {
        expect(routeCommand(cmd), (module: k8sWorker, payload: cmd),
            reason: cmd);
      }
    });

    // Đây là lỗi đã sửa: trước kia mặc định là k8s-worker nên gõ tiếng Việt
    // nhận về "lệnh không hiểu: hỏi".
    test('câu hỏi tự nhiên đi tới AI, không phải k8s-worker', () {
      for (final q in [
        'hỏi gì',
        'pod nào đang crash?',
        'giải thích CrashLoopBackOff',
        'why is my node NotReady',
      ]) {
        expect(routeCommand(q), (module: aiWorker, payload: q), reason: q);
      }
    });
  });

  test('không phân biệt hoa thường, tự trim', () {
    expect(routeCommand('  AI xin chào  '),
        (module: aiWorker, payload: 'xin chào'));
    expect(routeCommand('GET pods').module, k8sWorker);
  });

  group('swarm', () {
    test('swarm/docker đi tới swarm-worker, cắt prefix', () {
      for (final line in ['swarm service ls', 'docker service ls']) {
        final r = routeCommand(line);
        expect(r.module, swarmWorker, reason: line);
        expect(r.payload, 'service ls', reason: line);
      }
    });

    test('gõ trống alias thì worker tự in usage', () {
      expect(routeCommand('swarm').payload, 'help');
    });

    // "service ls" trống prefix KHÔNG vào swarm-worker: nó không nằm trong danh
    // sách verb k8s, nên rơi về AI như mọi câu hỏi tự nhiên khác.
    test('service ls trống prefix rơi về AI', () {
      expect(routeCommand('service ls').module, aiWorker);
    });
  });
}

