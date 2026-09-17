import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msr_admin/api_client.dart';
import 'package:msr_admin/models.dart';

/// _client 用一个可编程的假传输构造客户端，并把收到的请求记进 captured。
ApiClient _client(
  List<http.BaseRequest> captured, {
  int status = 200,
  String body = '{}',
  bool throwOnSend = false,
}) {
  final mock = MockClient((request) async {
    captured.add(request);
    if (throwOnSend) throw http.ClientException('boom');
    return http.Response(body, status,
        headers: {'content-type': 'application/json'});
  });
  return ApiClient(
    baseUrl: 'http://relay.test',
    adminKey: 'admin-secret',
    httpClient: mock,
  );
}

void main() {
  group('认证头', () {
    test('管理面请求带 Authorization: Bearer', () async {
      final captured = <http.BaseRequest>[];
      final client = _client(captured, body: '{"collections":[]}');
      await client.listCollections();

      expect(captured.single.headers['Authorization'], 'Bearer admin-secret');
      // 旧版用的自定义头已废弃，后端不再识别。
      expect(captured.single.headers.containsKey('X-Admin-Key'), isFalse);
    });
  });

  group('失败映射', () {
    test('401 映射为 UnauthorizedException', () async {
      final client = _client([], status: 401, body: '{}');
      expect(client.listCollections(), throwsA(isA<UnauthorizedException>()));
    });

    test('传输异常映射为 UnreachableException 并带所用地址', () async {
      final client = _client([], throwOnSend: true);
      try {
        await client.listCollections();
        fail('应当抛出 UnreachableException');
      } on UnreachableException catch (e) {
        expect(e.baseUrl, 'http://relay.test');
        expect(e.message, contains('http://relay.test'));
      }
    });

    test('错误信封解出 code / message / field', () async {
      final client = _client(
        [],
        status: 400,
        body: jsonEncode({
          'error': {
            'code': 'invalid_request',
            'message': 'collection c9 does not exist',
            'field': 'collection',
          }
        }),
      );
      try {
        await client.listCollections();
        fail('应当抛出 ValidationException');
      } on ValidationException catch (e) {
        expect(e.code, 'invalid_request');
        expect(e.message, 'collection c9 does not exist');
        expect(e.field, 'collection');
        expect(e.status, 400);
      }
    });

    test('非 JSON 错误体退化为带状态码的通用说明', () async {
      final client = _client([], status: 500, body: 'oops');
      try {
        await client.listCollections();
        fail('应当抛出 ValidationException');
      } on ValidationException catch (e) {
        expect(e.code, 'unknown');
        expect(e.message, contains('500'));
      }
    });

    test('204 空响应不当作解析失败', () async {
      final client = _client([], status: 204, body: '');
      await client.deleteCollection('c1');
    });
  });

  group('路径构造', () {
    test('含斜杠的 model_id 逐段编码，不出现 %2F', () async {
      final captured = <http.BaseRequest>[];
      final client = _client(captured, status: 204, body: '');
      await client.resetRuntime('kimi-1/k3');

      final path = captured.single.url.path;
      expect(path, '/admin/runtime/kimi-1/k3');
      expect(captured.single.url.toString(), isNot(contains('%2F')));
    });

    test('快照走 /snapshot 子路径，而非集合详情端点', () async {
      final captured = <http.BaseRequest>[];
      final client = _client(captured, body: jsonEncode({'name': 'c1'}));
      await client.getSnapshot('c1');

      expect(captured.single.url.path, '/admin/collections/c1/snapshot');
    });

    test('含空格等特殊字符的名字被编码', () async {
      final captured = <http.BaseRequest>[];
      final client = _client(captured, status: 204, body: '');
      await client.deleteCollection('my set');

      expect(captured.single.url.toString(), contains('my%20set'));
    });

    test('baseUrl 尾部斜杠不产生双斜杠', () async {
      final captured = <http.BaseRequest>[];
      final mock = MockClient((request) async {
        captured.add(request);
        return http.Response('{"collections":[]}', 200);
      });
      final client = ApiClient(
        baseUrl: 'http://relay.test/',
        adminKey: 'k',
        httpClient: mock,
      );
      await client.listCollections();
      expect(captured.single.url.path, '/admin/collections');
    });
  });

  group('响应形态', () {
    test('列表端点解包装', () async {
      final client = _client(
        [],
        body: jsonEncode({
          'collections': [
            {'name': 'c1', 'note': 'n1'},
            {'name': 'c2', 'note': ''},
          ]
        }),
      );
      final list = await client.listCollections();
      expect(list.map((c) => c.name), ['c1', 'c2']);
    });

    test('单体端点裸返实体', () async {
      final client = _client(
        [],
        body: jsonEncode({'name': 'c1', 'note': 'hello'}),
      );
      final created = await client.createCollection(name: 'c1', note: 'hello');
      expect(created.name, 'c1');
      expect(created.note, 'hello');
    });

    test('user model 响应不含 client_key 时仍能构造', () async {
      final client = _client(
        [],
        body: jsonEncode({
          'name': 'pool',
          'collection': 'c1',
          'policy': 'failover',
          'protocol': 'anthropic',
          'enabled': true,
        }),
      );
      final m = await client.getUserModel('pool');
      expect(m.name, 'pool');
      expect(m.policy, 'failover');
    });
  });

  group('请求体', () {
    test('写入 user model 时密钥放进请求体', () async {
      final captured = <http.BaseRequest>[];
      final client = _client(captured, body: jsonEncode({'name': 'pool'}));
      await client.createUserModel(
        const UserModel(
          name: 'pool',
          collection: 'c1',
          policy: 'failover',
          protocol: 'anthropic',
          enabled: true,
        ),
        'sk-live-1',
      );
      final body =
          jsonDecode((captured.single as http.Request).body) as Map<String, dynamic>;
      expect(body['client_key'], 'sk-live-1');
      expect(body['collection'], 'c1');
    });

    test('整组替换成员时提交完整有序列表', () async {
      final captured = <http.BaseRequest>[];
      final client = _client(captured, status: 204, body: '');
      await client.replaceMembers('c1', 'g1', ['b/2', 'a/1', 'c/3']);

      final body =
          jsonDecode((captured.single as http.Request).body) as Map<String, dynamic>;
      expect(body['members'], ['b/2', 'a/1', 'c/3']);
    });

    test('组配置为 null 时不出现在请求体里', () async {
      final captured = <http.BaseRequest>[];
      final client = _client(captured, body: jsonEncode({'name': 'g1'}));
      await client.createGroup(
        'c1',
        const Group(collection: 'c1', name: 'g1', type: 'fast', position: 0),
      );

      final body =
          jsonDecode((captured.single as http.Request).body) as Map<String, dynamic>;
      expect(body.containsKey('config'), isFalse);
    });
  });

  test('dry-run 解出候选与说明', () async {
    final client = _client(
      [],
      body: jsonEncode({
        'policy': 'failover',
        'policy_version': 3,
        'decision': {
          'candidates': ['kimi-1/k3', 'ark-2/doubao'],
          'note': 'group-by-group failover',
        },
      }),
    );
    final result = await client.dryRunPolicy(
      'failover',
      collection: 'c1',
      request: const RequestContext(userModel: 'pool', requestId: 'r-1'),
    );
    expect(result.policyVersion, 3);
    expect(result.candidates, ['kimi-1/k3', 'ark-2/doubao']);
    expect(result.note, 'group-by-group failover');
  });
}
