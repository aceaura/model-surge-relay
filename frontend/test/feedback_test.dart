import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:msr_admin/api_client.dart';
import 'package:msr_admin/ui/feedback.dart';
import 'package:msr_admin/ui/json_field.dart';

void main() {
  group('describeError', () {
    test('不可达带上所用地址与排查方向', () {
      const e = UnreachableException('http://relay.test', '无法连接服务：http://relay.test');
      expect(describeError(e), contains('http://relay.test'));
      expect(describeError(e), contains('确认服务已启动'));
    });

    test('密钥无效引导去设置页', () {
      expect(describeError(const UnauthorizedException()), contains('设置页'));
    });

    test('带 field 的校验错标出字段名', () {
      const e = ValidationException(
          'invalid_request', 'collection c9 does not exist', 400,
          field: 'collection');
      expect(describeError(e), 'collection：collection c9 does not exist');
    });

    test('不带 field 的校验错原样展示服务端说明', () {
      const e = ValidationException('conflict', 'policy is referenced by pool', 409);
      expect(describeError(e), 'policy is referenced by pool');
    });
  });

  group('needsSettings', () {
    test('仅密钥与连通性问题需要跳设置页', () {
      expect(needsSettings(const UnauthorizedException()), isTrue);
      expect(needsSettings(const UnreachableException('u', 'm')), isTrue);
      expect(
        needsSettings(const ValidationException('not_found', 'x', 404)),
        isFalse,
      );
    });
  });

  testWidgets('BusyButton 进行中禁用自身', (tester) async {
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: BusyButton(busy: true, onPressed: () {}, child: const Text('保存')),
      ),
    ));
    final button = tester.widget<FilledButton>(find.byType(FilledButton));
    expect(button.onPressed, isNull);
  });

  testWidgets('BusyButton 空闲时可点击', (tester) async {
    var tapped = false;
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: BusyButton(
          busy: false,
          onPressed: () => tapped = true,
          child: const Text('保存'),
        ),
      ),
    ));
    await tester.tap(find.text('保存'));
    expect(tapped, isTrue);
  });

  group('parseJsonObject', () {
    test('空内容解为 null，保住"缺省"与"空对象"的区别', () {
      expect(parseJsonObject('   '), (null, null));
    });

    test('合法对象解出内容', () {
      final (value, error) = parseJsonObject('{"weight":3}');
      expect(value, {'weight': 3});
      expect(error, isNull);
    });

    test('数组被拒', () {
      final (value, error) = parseJsonObject('[1,2]');
      expect(value, isNull);
      expect(error, contains('JSON 对象'));
    });

    test('语法错误带说明', () {
      final (value, error) = parseJsonObject('{oops');
      expect(value, isNull);
      expect(error, contains('JSON 格式错误'));
    });
  });

  test('prettyJson 对 null 与空对象都给空串', () {
    expect(prettyJson(null), '');
    expect(prettyJson(const {}), '');
    expect(prettyJson(const {'a': 1}), contains('"a"'));
  });
}
