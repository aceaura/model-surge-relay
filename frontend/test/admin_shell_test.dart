import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:msr_admin/main.dart';
import 'package:msr_admin/settings_store.dart';
import 'package:msr_admin/theme.dart';

class _FakeStore extends SettingsStore {
  @override
  Future<Settings> load() async => const Settings(
        baseUrl: 'http://127.0.0.1:9',
        adminKey: 'secret-key-123456',
      );

  @override
  Future<void> save(Settings settings) async {}
}

void main() {
  testWidgets('侧栏分组导航渲染且连接设置内嵌打开', (tester) async {
    tester.view.physicalSize = const Size(1400, 900);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);

    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: AdminShell(store: _FakeStore()),
    ));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    // 品牌头与两个分组、五个导航项齐全。
    expect(find.text('ModelSurge Relay'), findsOneWidget);
    expect(find.text('调度'), findsOneWidget);
    expect(find.text('系统'), findsOneWidget);
    expect(find.text('集合'), findsOneWidget);
    expect(find.text('策略'), findsOneWidget);
    expect(find.text('模型名'), findsOneWidget);
    expect(find.text('运行态'), findsOneWidget);
    expect(find.text('连接设置'), findsOneWidget);
    // 底部连接状态卡显示地址与脱敏密钥。
    expect(find.text('http://127.0.0.1:9'), findsOneWidget);
    expect(find.text('key secr***3456'), findsOneWidget);

    // 点连接设置：内嵌打开（无返回箭头），表单出现。
    await tester.tap(find.text('连接设置'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.text('服务地址'), findsOneWidget);
    expect(find.byType(BackButton), findsNothing);
  });
}
