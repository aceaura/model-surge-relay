/// 密钥不外泄的不变式测试。
///
/// 这些性质靠类型形态与文件内容守住，而不是靠每个调用点自觉：
/// UserModel 结构上没有密钥字段，api_client 结构上不打日志。
library;

import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:msr_admin/models.dart';

void main() {
  test('UserModel 的 toString 与 toJson 都不含密钥值', () {
    const secret = 'sk-live-must-not-appear';
    const m = UserModel(
      name: 'pool',
      collection: 'c1',
      policy: 'failover',
      protocol: 'anthropic',
      enabled: true,
      note: secret, // 就算有人把密钥误填进备注，也只影响备注，不新增泄漏面
    );
    // 类型本身没有密钥字段，所以密钥无从被序列化出去。
    expect(m.toJson().containsKey('client_key'), isFalse);
    expect(m.toString(), isNot(contains('client_key')));
  });

  test('UserModel.fromJson 即便服务端误传 client_key 也不会持有它', () {
    final m = UserModel.fromJson({
      'name': 'pool',
      'collection': 'c1',
      'policy': '',
      'protocol': 'anthropic',
      'enabled': true,
      'client_key': 'sk-should-be-ignored',
    });
    expect(m.toJson().values.join(' '), isNot(contains('sk-should-be-ignored')));
    expect(m.toString(), isNot(contains('sk-should-be-ignored')));
  });

  test('api_client.dart 不含任何日志调用', () {
    final source = File('lib/api_client.dart').readAsStringSync();
    for (final banned in ['print(', 'debugPrint(', 'stdout.', 'log(']) {
      expect(source, isNot(contains(banned)),
          reason: '管理密钥出现在请求头上，该文件不得有任何输出：$banned');
    }
  });
}
