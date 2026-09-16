/// backend JSON 与界面之间的数据类。
///
/// 缺省字段一律解为 null 而非空值：Group.config 缺省与 config={} 在服务端
/// 是两种语义，替换成空 Map 就是一次静默改写。
library;

/// 协议取值，对齐 model-surge-upstream 命名。
const protocols = <String>[
  'anthropic',
  'chat_completions',
  'responses',
  'gemini',
];

/// 策略语言的封闭集合，与 backend 注册的运行时一致。
const policyLanguages = <String>['lua', 'javascript', 'typescript'];

DateTime? _time(Object? raw) {
  if (raw is! String || raw.isEmpty) return null;
  return DateTime.tryParse(raw)?.toLocal();
}

int _int(Object? raw) => raw is num ? raw.toInt() : 0;

String _str(Object? raw) => raw is String ? raw : '';

class CollectionInfo {
  const CollectionInfo({
    required this.name,
    required this.note,
    this.createdAt,
    this.updatedAt,
  });

  final String name;
  final String note;
  final DateTime? createdAt;
  final DateTime? updatedAt;

  factory CollectionInfo.fromJson(Map<String, dynamic> json) => CollectionInfo(
        name: _str(json['name']),
        note: _str(json['note']),
        createdAt: _time(json['created_at']),
        updatedAt: _time(json['updated_at']),
      );
}

/// Group 的 type 是服务端也不解释的自由文本，config 同理保留原样。
class Group {
  const Group({
    required this.collection,
    required this.name,
    required this.type,
    required this.position,
    this.config,
    this.members = const [],
  });

  final String collection;
  final String name;
  final String type;
  final int position;
  final Map<String, dynamic>? config;
  final List<String> members;

  factory Group.fromJson(Map<String, dynamic> json) => Group(
        collection: _str(json['collection']),
        name: _str(json['name']),
        type: _str(json['type']),
        position: _int(json['position']),
        config: json['config'] is Map<String, dynamic>
            ? json['config'] as Map<String, dynamic>
            : null,
        members: (json['members'] as List<dynamic>? ?? const []).cast<String>(),
      );

  Group copyWith({int? position, String? type, Map<String, dynamic>? config}) =>
      Group(
        collection: collection,
        name: name,
        type: type ?? this.type,
        position: position ?? this.position,
        config: config ?? this.config,
        members: members,
      );
}

/// Member 是成员引用叠加 upstream 目录属性的结果。
/// known 为假表示该引用在目录中已消失——那是配置错误，与 enabled=false
/// 的"上游临时禁用"是两回事，界面需分别呈现。
class Member {
  const Member({
    required this.modelId,
    required this.account,
    required this.providerId,
    required this.protocol,
    required this.nativeModel,
    required this.contextWindow,
    required this.position,
    required this.enabled,
    required this.known,
  });

  final String modelId;
  final String account;
  final String providerId;
  final String protocol;
  final String nativeModel;
  final int contextWindow;
  final int position;
  final bool enabled;
  final bool known;

  factory Member.fromJson(Map<String, dynamic> json) => Member(
        modelId: _str(json['model_id']),
        account: _str(json['account']),
        providerId: _str(json['provider_id']),
        protocol: _str(json['protocol']),
        nativeModel: _str(json['native_model']),
        contextWindow: _int(json['context_window']),
        position: _int(json['position']),
        enabled: json['enabled'] == true,
        known: json['known'] == true,
      );
}

class GroupSnapshot {
  const GroupSnapshot({
    required this.name,
    required this.type,
    required this.position,
    this.config,
    this.members = const [],
  });

  final String name;
  final String type;
  final int position;
  final Map<String, dynamic>? config;
  final List<Member> members;

  factory GroupSnapshot.fromJson(Map<String, dynamic> json) => GroupSnapshot(
        name: _str(json['name']),
        type: _str(json['type']),
        position: _int(json['position']),
        config: json['config'] is Map<String, dynamic>
            ? json['config'] as Map<String, dynamic>
            : null,
        members: (json['members'] as List<dynamic>? ?? const [])
            .map((e) => Member.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

class Snapshot {
  const Snapshot({required this.name, this.groups = const []});

  final String name;
  final List<GroupSnapshot> groups;

  factory Snapshot.fromJson(Map<String, dynamic> json) => Snapshot(
        name: _str(json['name']),
        groups: (json['groups'] as List<dynamic>? ?? const [])
            .map((e) => GroupSnapshot.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

class Policy {
  const Policy({
    required this.name,
    required this.language,
    required this.source,
    required this.note,
    this.version = 0,
    this.createdAt,
    this.updatedAt,
  });

  final String name;
  final String language;
  final String source;
  final String note;
  final int version;
  final DateTime? createdAt;
  final DateTime? updatedAt;

  factory Policy.fromJson(Map<String, dynamic> json) => Policy(
        name: _str(json['name']),
        language: _str(json['language']),
        source: _str(json['source']),
        note: _str(json['note']),
        version: _int(json['version']),
        createdAt: _time(json['created_at']),
        updatedAt: _time(json['updated_at']),
      );

  Map<String, dynamic> toJson() => {
        'name': name,
        'language': language,
        'source': source,
        'note': note,
      };
}

/// UserModel 刻意没有密钥字段：服务端从不返回它，客户端也就无从持有。
/// 密钥只作为写入方法的独立参数存在。
class UserModel {
  const UserModel({
    required this.name,
    required this.collection,
    required this.policy,
    required this.protocol,
    required this.enabled,
    this.note = '',
    this.createdAt,
    this.updatedAt,
  });

  final String name;
  final String collection;
  final String policy;
  final String protocol;
  final bool enabled;
  final String note;
  final DateTime? createdAt;
  final DateTime? updatedAt;

  factory UserModel.fromJson(Map<String, dynamic> json) => UserModel(
        name: _str(json['name']),
        collection: _str(json['collection']),
        policy: _str(json['policy']),
        protocol: _str(json['protocol']),
        enabled: json['enabled'] == true,
        note: _str(json['note']),
        createdAt: _time(json['created_at']),
        updatedAt: _time(json['updated_at']),
      );

  Map<String, dynamic> toJson() => {
        'name': name,
        'collection': collection,
        'policy': policy,
        'protocol': protocol,
        'enabled': enabled,
      };

  @override
  String toString() => 'UserModel($name -> $collection, policy=$policy, '
      'protocol=$protocol, enabled=$enabled)';
}

class Usage {
  const Usage({
    this.inputTokens = 0,
    this.outputTokens = 0,
    this.cacheReadTokens = 0,
    this.requestCount = 0,
  });

  final int inputTokens;
  final int outputTokens;
  final int cacheReadTokens;
  final int requestCount;

  factory Usage.fromJson(Map<String, dynamic> json) => Usage(
        inputTokens: _int(json['input_tokens']),
        outputTokens: _int(json['output_tokens']),
        cacheReadTokens: _int(json['cache_read_tokens']),
        requestCount: _int(json['request_count']),
      );

  Map<String, dynamic> toJson() => {
        'input_tokens': inputTokens,
        'output_tokens': outputTokens,
        'cache_read_tokens': cacheReadTokens,
        'request_count': requestCount,
      };
}

/// coolingUntil 为 null 表示未冷却：服务端标了 omitzero，零值不出现在 JSON 里。
class RuntimeState {
  const RuntimeState({
    required this.modelId,
    required this.cooling,
    this.coolingUntil,
    this.consecutiveFailures = 0,
    this.usage = const Usage(),
    this.updatedAt,
  });

  final String modelId;
  final bool cooling;
  final DateTime? coolingUntil;
  final int consecutiveFailures;
  final Usage usage;
  final DateTime? updatedAt;

  factory RuntimeState.fromJson(Map<String, dynamic> json) => RuntimeState(
        modelId: _str(json['model_id']),
        cooling: json['cooling'] == true,
        coolingUntil: _time(json['cooling_until']),
        consecutiveFailures: _int(json['consecutive_failures']),
        usage: json['usage'] is Map<String, dynamic>
            ? Usage.fromJson(json['usage'] as Map<String, dynamic>)
            : const Usage(),
        updatedAt: _time(json['updated_at']),
      );

  /// remaining 是剩余冷却时长，仅供展示；判断冷却与否始终以服务端的
  /// cooling 字段为准，本机时钟不参与决策。
  Duration? get remaining {
    final until = coolingUntil;
    if (!cooling || until == null) return null;
    final left = until.difference(DateTime.now());
    return left.isNegative ? Duration.zero : left;
  }
}

/// RequestContext 是试运行时填给策略的请求上下文。
class RequestContext {
  const RequestContext({
    this.userModel = '',
    this.inboundProtocol = 'anthropic',
    this.estTokens = 0,
    this.triedIds = const [],
    this.requestId = '',
  });

  final String userModel;
  final String inboundProtocol;
  final int estTokens;
  final List<String> triedIds;
  final String requestId;

  Map<String, dynamic> toJson() => {
        'user_model': userModel,
        'inbound_protocol': inboundProtocol,
        'est_tokens': estTokens,
        'tried_ids': triedIds,
        'request_id': requestId,
      };
}

class DryRunResult {
  const DryRunResult({
    required this.policy,
    required this.policyVersion,
    required this.candidates,
    required this.note,
  });

  final String policy;
  final int policyVersion;
  final List<String> candidates;
  final String note;

  factory DryRunResult.fromJson(Map<String, dynamic> json) {
    final decision = json['decision'] as Map<String, dynamic>? ?? const {};
    return DryRunResult(
      policy: _str(json['policy']),
      policyVersion: _int(json['policy_version']),
      candidates:
          (decision['candidates'] as List<dynamic>? ?? const []).cast<String>(),
      note: _str(decision['note']),
    );
  }
}
