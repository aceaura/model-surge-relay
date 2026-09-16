import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msr_admin/api_client.dart';
import 'package:msr_admin/models.dart';
import 'package:msr_admin/pages/policy_form.dart';

const _initial = Policy(
  name: 'failover',
  language: 'lua',
  source: 'return { "a/1" }',
  note: '',
  version: 2,
);

/// 该页面是为桌面窗口尺寸设计的（应用最小宽 1060）。
/// 测试画布默认 800x600，会把为宽屏排的两栏布局挤到溢出，因此显式设成桌面尺寸。
void _useDesktopSurface(WidgetTester tester) {
  tester.view.physicalSize = const Size(1400, 900);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
}

Future<void> _pump(WidgetTester tester, ApiClient client) async {
  _useDesktopSurface(tester);
  await tester.pumpWidget(MaterialApp(
    home: PolicyForm(client: client, initial: _initial),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('编译失败时源码保留且错误条出现', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((_) async => http.Response(
            jsonEncode({
              'error': {
                'code': 'policy_error',
                'message': 'lua compile error at line 3:7: unexpected symbol',
              }
            }),
            400,
          )),
    );

    await _pump(tester, client);
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();

    // 编译失败是写策略的常态，清掉输入等于让人重写。
    expect(find.text('return { "a/1" }'), findsOneWidget);
    expect(find.textContaining('line 3:7'), findsOneWidget);
  });

  testWidgets('保存成功回传服务端返回的策略', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((_) async => http.Response(
            jsonEncode({
              'name': 'failover',
              'language': 'lua',
              'source': 'return { "a/1" }',
              'note': '',
              'version': 3,
            }),
            200,
          )),
    );

    Policy? popped;
    _useDesktopSurface(tester);
    await tester.pumpWidget(MaterialApp(
      home: Builder(
        builder: (context) => ElevatedButton(
          onPressed: () async {
            popped = await Navigator.of(context).push<Policy>(
              MaterialPageRoute(
                builder: (_) => PolicyForm(client: client, initial: _initial),
              ),
            );
          },
          child: const Text('open'),
        ),
      ),
    ));
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();

    expect(popped?.version, 3);
  });

  testWidgets('提交时提交的是编辑区里的源码与所选语言', (tester) async {
    final captured = <http.BaseRequest>[];
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((r) async {
        captured.add(r);
        return http.Response(
          jsonEncode({
            'name': 'failover',
            'language': 'lua',
            'source': 'x',
            'note': 'edited',
            'version': 3,
          }),
          200,
        );
      }),
    );

    await _pump(tester, client);
    await tester.enterText(find.widgetWithText(TextField, ''), 'edited');
    await tester.pumpAndSettle();
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();

    final body =
        jsonDecode((captured.single as http.Request).body) as Map<String, dynamic>;
    expect(body['name'], 'failover');
    expect(body['language'], 'lua');
    expect(body['source'], 'return { "a/1" }');
  });

  testWidgets('编辑态下名称不可改', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((_) async => http.Response('{}', 200)),
    );
    await _pump(tester, client);

    final nameField = tester.widget<TextField>(
      find.ancestor(
        of: find.text('failover'),
        matching: find.byType(TextField),
      ),
    );
    expect(nameField.enabled, isFalse);
  });

  testWidgets('侧栏带输入结构速查', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((_) async => http.Response('{}', 200)),
    );
    await _pump(tester, client);

    expect(find.text('输入结构速查'), findsOneWidget);
    expect(find.textContaining('tried_ids'), findsOneWidget);
  });
}
