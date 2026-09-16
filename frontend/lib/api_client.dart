/// 客户端与 relay backend 管理面的唯一 HTTP 出口。
///
/// 三类失败分开建模，让界面能给出可操作提示而不是通用文案：
/// 服务不可达 / 管理密钥无效 / 服务端校验错误。
/// 本文件不打印任何日志——管理密钥会出现在请求头上。
library;

import 'dart:convert';

import 'package:http/http.dart' as http;

import 'models.dart';

sealed class ApiException implements Exception {
  const ApiException(this.message);
  final String message;
  @override
  String toString() => message;
}

/// 服务不可达。携带所用地址，便于运维者核对配置。
class UnreachableException extends ApiException {
  const UnreachableException(this.baseUrl, super.message);
  final String baseUrl;
}

/// 管理密钥无效。
class UnauthorizedException extends ApiException {
  const UnauthorizedException() : super('管理密钥无效');
}

/// 服务端校验类错误。field 来自后端错误信封，用于把错误落到具体表单项。
class ValidationException extends ApiException {
  const ValidationException(this.code, super.message, this.status, {this.field});
  final String code;
  final int status;
  final String? field;
}

class ApiClient {
  ApiClient({
    required this.baseUrl,
    required this.adminKey,
    http.Client? httpClient,
  }) : _http = httpClient ?? http.Client();

  final String baseUrl;
  final String adminKey;
  final http.Client _http;

  static const _timeout = Duration(seconds: 15);

  /// 管理面用 X-Admin-Key。调度面才用 Bearer，走错头会被后端当作另一个面拒掉。
  Map<String, String> get _headers => {
        'X-Admin-Key': adminKey,
        'Content-Type': 'application/json',
      };

  Uri _uri(String path) {
    final root =
        baseUrl.endsWith('/') ? baseUrl.substring(0, baseUrl.length - 1) : baseUrl;
    return Uri.parse('$root$path');
  }

  Future<Map<String, dynamic>> _send(
    String method,
    String path, {
    Map<String, dynamic>? body,
  }) async {
    final request = http.Request(method, _uri(path))..headers.addAll(_headers);
    if (body != null) {
      request.body = jsonEncode(body);
    }

    http.Response response;
    try {
      final streamed = await _http.send(request).timeout(_timeout);
      response = await http.Response.fromStream(streamed);
    } catch (_) {
      throw UnreachableException(baseUrl, '无法连接服务：$baseUrl');
    }

    if (response.statusCode == 401) {
      throw const UnauthorizedException();
    }
    if (response.statusCode == 204) {
      return const {};
    }
    if (response.statusCode >= 200 && response.statusCode < 300) {
      if (response.body.isEmpty) return const {};
      return jsonDecode(response.body) as Map<String, dynamic>;
    }
    throw _validationOf(response);
  }

  ValidationException _validationOf(http.Response response) {
    try {
      final decoded = jsonDecode(response.body) as Map<String, dynamic>;
      final error = decoded['error'] as Map<String, dynamic>;
      return ValidationException(
        error['code'] as String? ?? 'unknown',
        error['message'] as String? ?? '请求失败',
        response.statusCode,
        field: error['field'] as String?,
      );
    } catch (_) {
      return ValidationException(
        'unknown',
        '请求失败（HTTP ${response.statusCode}）',
        response.statusCode,
      );
    }
  }

  /// _segment 编码单个路径段。model_id 形如 kimi-1/k3，含斜杠，
  /// 必须逐段编码后再拼 `/`：整体编码会把斜杠变成 %2F，后端多段通配就匹配不到。
  static String _segment(String value) => Uri.encodeComponent(value);

  static String _multiSegment(String value) =>
      value.split('/').map(_segment).join('/');

  // ---- Collection ----

  Future<List<CollectionInfo>> listCollections() async {
    final body = await _send('GET', '/admin/collections');
    return (body['collections'] as List<dynamic>? ?? const [])
        .map((e) => CollectionInfo.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<CollectionInfo> createCollection({
    required String name,
    String note = '',
  }) async {
    final body = await _send('POST', '/admin/collections',
        body: {'name': name, 'note': note});
    return CollectionInfo.fromJson(body);
  }

  Future<void> updateCollectionNote(String name, String note) =>
      _send('PUT', '/admin/collections/${_segment(name)}', body: {'note': note});

  Future<void> deleteCollection(String name) =>
      _send('DELETE', '/admin/collections/${_segment(name)}');

  Future<Snapshot> getSnapshot(String name) async {
    final body =
        await _send('GET', '/admin/collections/${_segment(name)}/snapshot');
    return Snapshot.fromJson(body);
  }

  // ---- Group ----

  Future<List<Group>> listGroups(String collection) async {
    final body =
        await _send('GET', '/admin/collections/${_segment(collection)}/groups');
    return (body['groups'] as List<dynamic>? ?? const [])
        .map((e) => Group.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<Group> createGroup(String collection, Group g) async {
    final body = await _send(
      'POST',
      '/admin/collections/${_segment(collection)}/groups',
      body: {
        'name': g.name,
        'type': g.type,
        'position': g.position,
        'config': ?g.config,
      },
    );
    return Group.fromJson(body);
  }

  Future<void> updateGroup(String collection, Group g) => _send(
        'PUT',
        '/admin/collections/${_segment(collection)}/groups/${_segment(g.name)}',
        body: {
          'type': g.type,
          'position': g.position,
          'config': ?g.config,
        },
      );

  Future<void> deleteGroup(String collection, String group) => _send(
        'DELETE',
        '/admin/collections/${_segment(collection)}/groups/${_segment(group)}',
      );

  /// replaceMembers 是整组替换：提交的列表就是该组的全部成员与顺序。
  Future<void> replaceMembers(
    String collection,
    String group,
    List<String> modelIds,
  ) =>
      _send(
        'PUT',
        '/admin/collections/${_segment(collection)}/groups/${_segment(group)}/members',
        body: {'members': modelIds},
      );

  // ---- 策略 ----

  Future<List<Policy>> listPolicies() async {
    final body = await _send('GET', '/admin/policies');
    return (body['policies'] as List<dynamic>? ?? const [])
        .map((e) => Policy.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<Policy> getPolicy(String name) async {
    final body = await _send('GET', '/admin/policies/${_segment(name)}');
    return Policy.fromJson(body);
  }

  Future<Policy> createPolicy(Policy p) async {
    final body = await _send('POST', '/admin/policies', body: p.toJson());
    return Policy.fromJson(body);
  }

  Future<Policy> updatePolicy(Policy p) async {
    final body = await _send('PUT', '/admin/policies/${_segment(p.name)}',
        body: p.toJson());
    return Policy.fromJson(body);
  }

  Future<void> deletePolicy(String name) =>
      _send('DELETE', '/admin/policies/${_segment(name)}');

  Future<DryRunResult> dryRunPolicy(
    String name, {
    required String collection,
    required RequestContext request,
    Map<String, dynamic>? runtime,
  }) async {
    final body = await _send(
      'POST',
      '/admin/policies/${_segment(name)}/dry-run',
      body: {
        'collection': collection,
        'request': request.toJson(),
        'runtime': ?runtime,
      },
    );
    return DryRunResult.fromJson(body);
  }

  // ---- user model ----

  Future<List<UserModel>> listUserModels() async {
    final body = await _send('GET', '/admin/user-models');
    return (body['user_models'] as List<dynamic>? ?? const [])
        .map((e) => UserModel.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<UserModel> getUserModel(String name) async {
    final body = await _send('GET', '/admin/user-models/${_segment(name)}');
    return UserModel.fromJson(body);
  }

  Future<UserModel> createUserModel(UserModel m, String clientKey) async {
    final body = await _send('POST', '/admin/user-models',
        body: {...m.toJson(), 'client_key': clientKey});
    return UserModel.fromJson(body);
  }

  /// 后端 Update 是全量覆盖：clientKey 传什么就存什么，空串会让鉴权失败。
  Future<UserModel> updateUserModel(UserModel m, String clientKey) async {
    final body = await _send('PUT', '/admin/user-models/${_segment(m.name)}',
        body: {...m.toJson(), 'client_key': clientKey});
    return UserModel.fromJson(body);
  }

  Future<void> deleteUserModel(String name) =>
      _send('DELETE', '/admin/user-models/${_segment(name)}');

  // ---- 运行态 ----

  Future<List<RuntimeState>> listRuntime() async {
    final body = await _send('GET', '/admin/runtime');
    return (body['runtime'] as List<dynamic>? ?? const [])
        .map((e) => RuntimeState.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<void> resetRuntime(String modelId) =>
      _send('DELETE', '/admin/runtime/${_multiSegment(modelId)}');

  void close() => _http.close();
}
