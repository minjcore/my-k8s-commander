import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:k8s_commander_app/main.dart';

void main() {
  testWidgets('Terminal render được và báo lỗi khi không nạp được core',
      (WidgetTester tester) async {
    // Trong môi trường test không có libmyk8s_commander.dylib cạnh dart binary,
    // nên NativeCore.load() phải ném CoreLoadException và UI hiển thị lỗi
    // thay vì crash.
    await tester.pumpWidget(const K8sCommanderApp());
    await tester.pump();

    expect(find.text('Terminal'), findsOneWidget);
    expect(find.byIcon(Icons.terminal), findsOneWidget);
    expect(
      find.textContaining('libmyk8s_commander', findRichText: false),
      findsWidgets,
      reason: 'phải in ra path đã thử tìm shared library',
    );
  });
}
