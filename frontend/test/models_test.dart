import 'package:flutter_test/flutter_test.dart';
import 'package:msr_admin/models.dart';

void main() {
  group('缺省字段不被改写成空值', () {
    test('Group.config 缺省解为 null 而非空 Map', () {
      final g = Group.fromJson({
        'collection': 'c1',
        'name': 'g1',
        'type': 'fast',
        'position': 0,
      });
      // config 缺省与 config={} 在服务端是两种语义，替换成空 Map 就是静默改写。
      expect(g.config, isNull);
    });

    test('Group.config 存在时原样保留', () {
      final g = Group.fromJson({
        'collection': 'c1',
        'name': 'g1',
        'type': 'fast',
        'position': 1,
        'config': {'weight': 3},
      });
      expect(g.config, {'weight': 3});
    });

    test('cooling_until 缺失解为 null', () {
      final s = RuntimeState.fromJson({
        'model_id': 'kimi-1/k3',
        'cooling': false,
        'consecutive_failures': 0,
      });
      expect(s.coolingUntil, isNull);
      expect(s.remaining, isNull);
    });

    test('usage 缺失解为零值而非崩溃', () {
      final s = RuntimeState.fromJson({'model_id': 'a/b', 'cooling': false});
      expect(s.usage.requestCount, 0);
      expect(s.usage.inputTokens, 0);
    });
  });

  group('Member 的两种"不可用"分开表达', () {
    test('known 与 enabled 各自独立解出', () {
      final gone = Member.fromJson({
        'model_id': 'dead/model',
        'enabled': false,
        'known': false,
      });
      final disabled = Member.fromJson({
        'model_id': 'kimi-1/k3',
        'account': 'kimi-1',
        'protocol': 'anthropic',
        'native_model': 'k3',
        'context_window': 262144,
        'enabled': false,
        'known': true,
      });
      expect(gone.known, isFalse);
      expect(disabled.known, isTrue);
      expect(disabled.enabled, isFalse);
      expect(disabled.contextWindow, 262144);
    });
  });

  group('冷却剩余时长只作展示', () {
    test('冷却中返回正剩余', () {
      final s = RuntimeState(
        modelId: 'a/b',
        cooling: true,
        coolingUntil: DateTime.now().add(const Duration(seconds: 90)),
      );
      expect(s.remaining!.inSeconds, greaterThan(60));
    });

    test('已过期的冷却截止返回零而非负值', () {
      final s = RuntimeState(
        modelId: 'a/b',
        cooling: true,
        coolingUntil: DateTime.now().subtract(const Duration(minutes: 5)),
      );
      expect(s.remaining, Duration.zero);
    });

    test('未冷却时无剩余，即便带了截止时间', () {
      final s = RuntimeState(
        modelId: 'a/b',
        cooling: false,
        coolingUntil: DateTime.now().add(const Duration(minutes: 5)),
      );
      expect(s.remaining, isNull);
    });
  });

  group('Snapshot', () {
    test('组与成员顺序按服务端给的次序保留', () {
      final snap = Snapshot.fromJson({
        'name': 'c1',
        'groups': [
          {
            'name': 'primary',
            'type': 'fast',
            'position': 0,
            'members': [
              {'model_id': 'a/1', 'known': true, 'enabled': true},
              {'model_id': 'b/2', 'known': true, 'enabled': true},
            ],
          },
          {'name': 'backup', 'type': 'cheap', 'position': 1, 'members': []},
        ],
      });
      expect(snap.groups.map((g) => g.name), ['primary', 'backup']);
      expect(snap.groups.first.members.map((m) => m.modelId), ['a/1', 'b/2']);
      expect(snap.groups.last.members, isEmpty);
    });

    test('groups 缺失解为空列表', () {
      final snap = Snapshot.fromJson({'name': 'empty'});
      expect(snap.groups, isEmpty);
    });
  });

  group('Policy 往返', () {
    test('toJson 只提交可写字段，不回传 version', () {
      const p = Policy(
        name: 'failover',
        language: 'lua',
        source: 'return {}',
        note: 'n',
        version: 7,
      );
      final json = p.toJson();
      expect(json.keys, containsAll(['name', 'language', 'source', 'note']));
      // version 由服务端裁定，客户端回传它没有意义。
      expect(json.containsKey('version'), isFalse);
    });

    test('fromJson 解出服务端返回的版本', () {
      final p = Policy.fromJson({
        'name': 'failover',
        'language': 'lua',
        'source': 'return {}',
        'note': '',
        'version': 7,
      });
      expect(p.version, 7);
    });
  });
}
