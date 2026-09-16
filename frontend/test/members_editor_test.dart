import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msr_admin/api_client.dart';
import 'package:msr_admin/models.dart';
import 'package:msr_admin/pages/members_editor.dart';

Member _member(String id) => Member(
      modelId: id,
      account: id.split('/').first,
      providerId: 'p',
      protocol: 'anthropic',
      nativeModel: id.split('/').last,
      contextWindow: 1024,
      position: 0,
      enabled: true,
      known: true,
    );

Future<void> _pump(
  WidgetTester tester, {
  required ApiClient client,
  required GroupSnapshot group,
}) async {
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      body: MembersEditor(
        client: client,
        collection: 'c1',
        group: group,
        onSaved: () {},
      ),
    ),
  ));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('未保存的移除不发请求', (tester) async {
    final captured = <http.BaseRequest>[];
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((r) async {
        captured.add(r);
        return http.Response('', 204);
      }),
    );

    await _pump(
      tester,
      client: client,
      group: GroupSnapshot(
        name: 'g1',
        type: 'fast',
        position: 0,
        members: [_member('a/1'), _member('b/2')],
      ),
    );

    await tester.tap(find.byTooltip('移除').first);
    await tester.pumpAndSettle();

    // 整组替换语义下，本地移除必须等到点保存才生效。
    expect(captured, isEmpty);
    expect(find.text('a/1'), findsNothing);
    expect(find.textContaining('有未保存改动'), findsOneWidget);
  });

  testWidgets('保存提交界面上的完整有序列表', (tester) async {
    final captured = <http.BaseRequest>[];
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((r) async {
        captured.add(r);
        return http.Response('', 204);
      }),
    );

    await _pump(
      tester,
      client: client,
      group: GroupSnapshot(
        name: 'g1',
        type: 'fast',
        position: 0,
        members: [_member('a/1'), _member('b/2'), _member('c/3')],
      ),
    );

    await tester.tap(find.byTooltip('移除').at(1));
    await tester.pumpAndSettle();
    await tester.tap(find.text('保存编排'));
    await tester.pumpAndSettle();

    final body =
        jsonDecode((captured.single as http.Request).body) as Map<String, dynamic>;
    expect(body['members'], ['a/1', 'c/3']);
  });

  testWidgets('放弃改动恢复到服务端顺序', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((_) async => http.Response('', 204)),
    );

    await _pump(
      tester,
      client: client,
      group: GroupSnapshot(
        name: 'g1',
        type: 'fast',
        position: 0,
        members: [_member('a/1'), _member('b/2')],
      ),
    );

    await tester.tap(find.byTooltip('移除').first);
    await tester.pumpAndSettle();
    expect(find.text('a/1'), findsNothing);

    await tester.tap(find.text('放弃改动'));
    await tester.pumpAndSettle();
    expect(find.text('a/1'), findsOneWidget);
    expect(find.textContaining('有未保存改动'), findsNothing);
  });

  testWidgets('目录中已消失的成员与上游已禁用的成员分别标注', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((_) async => http.Response('', 204)),
    );

    await _pump(
      tester,
      client: client,
      group: GroupSnapshot(
        name: 'g1',
        type: 'fast',
        position: 0,
        members: [
          const Member(
            modelId: 'gone/model',
            account: '',
            providerId: '',
            protocol: '',
            nativeModel: '',
            contextWindow: 0,
            position: 0,
            enabled: false,
            known: false,
          ),
          const Member(
            modelId: 'kimi-1/k3',
            account: 'kimi-1',
            providerId: 'anthropic',
            protocol: 'anthropic',
            nativeModel: 'k3',
            contextWindow: 262144,
            position: 1,
            enabled: false,
            known: true,
          ),
        ],
      ),
    );

    // 两种状态一个是配置错误、一个是正常状态，混为一谈会让人误删有效配置。
    expect(find.text('目录中已消失'), findsOneWidget);
    expect(find.text('上游已禁用'), findsOneWidget);
  });

  testWidgets('空组可直接保存', (tester) async {
    final client = ApiClient(
      baseUrl: 'http://relay.test',
      adminKey: 'k',
      httpClient: MockClient((_) async => http.Response('', 204)),
    );

    await _pump(
      tester,
      client: client,
      group: const GroupSnapshot(
          name: 'g1', type: 'fast', position: 0, members: []),
    );

    expect(find.textContaining('空组是合法状态'), findsOneWidget);
  });
}
