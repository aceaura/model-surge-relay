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

/// 候选阶段取值，与 relayv1 契约字面量一致：未触发超长只有 standard；
/// 触发后先 compact（压缩组托管）再 resume（回落原链继续）。
const phases = <String>['standard', 'compact', 'resume'];

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

/// Strategy 是挂在 Collection 上的策略组合：优先级链 + 超长压缩托管。
/// 两组字段缺省都按服务端 omitempty 语义解：空链表示"按组顺序"，
/// overflow.enabled=false 表示托管关闭。
class Strategy {
  const Strategy({
    this.priorityChain = const [],
    this.overflow = const OverflowConfig(),
  });

  final List<String> priorityChain;
  final OverflowConfig overflow;

  factory Strategy.fromJson(Map<String, dynamic> json) => Strategy(
        priorityChain:
            (json['priority_chain'] as List<dynamic>? ?? const []).cast<String>(),
        overflow: json['overflow'] is Map<String, dynamic>
            ? OverflowConfig.fromJson(json['overflow'] as Map<String, dynamic>)
            : const OverflowConfig(),
      );

  Map<String, dynamic> toJson() => {
        'priority_chain': priorityChain,
        'overflow': overflow.toJson(),
      };
}

class OverflowConfig {
  const OverflowConfig({
    this.enabled = false,
    this.thresholdTokens = 0,
    this.compactGroups = const [],
  });

  final bool enabled;
  final int thresholdTokens;
  final List<String> compactGroups;

  factory OverflowConfig.fromJson(Map<String, dynamic> json) => OverflowConfig(
        enabled: json['enabled'] == true,
        thresholdTokens: _int(json['threshold_tokens']),
        compactGroups:
            (json['compact_groups'] as List<dynamic>? ?? const []).cast<String>(),
      );

  Map<String, dynamic> toJson() => {
        'enabled': enabled,
        'threshold_tokens': thresholdTokens,
        'compact_groups': compactGroups,
      };

  OverflowConfig copyWith({
    bool? enabled,
    int? thresholdTokens,
    List<String>? compactGroups,
  }) =>
      OverflowConfig(
        enabled: enabled ?? this.enabled,
        thresholdTokens: thresholdTokens ?? this.thresholdTokens,
        compactGroups: compactGroups ?? this.compactGroups,
      );
}

class PhasedCandidate {
  const PhasedCandidate({required this.modelId, required this.phase});

  final String modelId;
  final String phase;

  factory PhasedCandidate.fromJson(Map<String, dynamic> json) =>
      PhasedCandidate(
        modelId: _str(json['model_id']),
        phase: _str(json['phase']),
      );
}

class GroupSkip {
  const GroupSkip({required this.group, required this.reason, this.detail = ''});

  final String group;
  final String reason;
  final String detail;

  factory GroupSkip.fromJson(Map<String, dynamic> json) => GroupSkip(
        group: _str(json['group']),
        reason: _str(json['reason']),
        detail: _str(json['detail']),
      );
}

class MemberSkip {
  const MemberSkip({
    required this.modelId,
    required this.group,
    required this.reason,
  });

  final String modelId;
  final String group;
  final String reason;

  factory MemberSkip.fromJson(Map<String, dynamic> json) => MemberSkip(
        modelId: _str(json['model_id']),
        group: _str(json['group']),
        reason: _str(json['reason']),
      );
}

/// DryRunDecision 是集合策略试运行的结果：与真实调度同一个 compose 核，
/// 看到的序列就是上线后的序列。
class DryRunDecision {
  const DryRunDecision({
    required this.collection,
    this.candidates = const [],
    this.groupSkips = const [],
    this.skipped = const [],
  });

  final String collection;
  final List<PhasedCandidate> candidates;
  final List<GroupSkip> groupSkips;
  final List<MemberSkip> skipped;

  factory DryRunDecision.fromJson(Map<String, dynamic> json) =>
      DryRunDecision(
        collection: _str(json['collection']),
        candidates: (json['candidates'] as List<dynamic>? ?? const [])
            .map((e) => PhasedCandidate.fromJson(e as Map<String, dynamic>))
            .toList(),
        groupSkips: (json['group_skips'] as List<dynamic>? ?? const [])
            .map((e) => GroupSkip.fromJson(e as Map<String, dynamic>))
            .toList(),
        skipped: (json['skipped'] as List<dynamic>? ?? const [])
            .map((e) => MemberSkip.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

/// UserModel 刻意没有密钥字段：服务端从不返回它，客户端也就无从持有。
/// 密钥只作为写入方法的独立参数存在。
class UserModel {
  const UserModel({
    required this.name,
    required this.collection,
    required this.protocol,
    required this.enabled,
    this.note = '',
    this.createdAt,
    this.updatedAt,
  });

  final String name;
  final String collection;
  final String protocol;
  final bool enabled;
  final String note;
  final DateTime? createdAt;
  final DateTime? updatedAt;

  factory UserModel.fromJson(Map<String, dynamic> json) => UserModel(
        name: _str(json['name']),
        collection: _str(json['collection']),
        protocol: _str(json['protocol']),
        enabled: json['enabled'] == true,
        note: _str(json['note']),
        createdAt: _time(json['created_at']),
        updatedAt: _time(json['updated_at']),
      );

  Map<String, dynamic> toJson() => {
        'name': name,
        'collection': collection,
        'protocol': protocol,
        'enabled': enabled,
      };

  @override
  String toString() => 'UserModel($name -> $collection, '
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
