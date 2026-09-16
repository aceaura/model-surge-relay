import 'package:flutter/material.dart';

import '../api_client.dart';
import '../settings_store.dart';
import '../ui/feedback.dart';

/// 服务地址与管理密钥的设置页。保存前做一次连通性探测，
/// 让运维者在这里就发现地址或密钥错误，而不是回到列表页才看到空白。
class SettingsPage extends StatefulWidget {
  const SettingsPage({
    super.key,
    required this.initial,
    required this.onSaved,
    this.dismissible = true,
  });

  final Settings initial;
  final Future<void> Function(Settings) onSaved;
  final bool dismissible;

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  late final TextEditingController _baseUrl =
      TextEditingController(text: widget.initial.baseUrl);
  late final TextEditingController _adminKey =
      TextEditingController(text: widget.initial.adminKey);

  bool _busy = false;
  bool _revealKey = false;
  Object? _error;
  String? _probeResult;

  @override
  void dispose() {
    _baseUrl.dispose();
    _adminKey.dispose();
    super.dispose();
  }

  Settings get _draft => Settings(
        baseUrl: _baseUrl.text.trim(),
        adminKey: _adminKey.text.trim(),
      );

  Future<void> _save() async {
    final draft = _draft;
    if (!draft.complete) {
      setState(() => _error = const ValidationException(
          'invalid_request', '服务地址与管理密钥都不能为空', 400));
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
      _probeResult = null;
    });
    final client = ApiClient(baseUrl: draft.baseUrl, adminKey: draft.adminKey);
    try {
      final collections = await client.listCollections();
      await widget.onSaved(draft);
      if (!mounted) return;
      setState(() => _probeResult = '连接成功，已有 Collection ${collections.length} 个');
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    } finally {
      client.close();
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final onboarding = !widget.initial.complete;
    return Scaffold(
      appBar: AppBar(
        title: const Text('连接设置'),
        automaticallyImplyLeading: widget.dismissible,
      ),
      body: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 560),
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (onboarding)
                  const Padding(
                    padding: EdgeInsets.only(bottom: 16),
                    child: Text('首次使用请先填写 relay 服务地址与管理密钥。'),
                  ),
                TextField(
                  controller: _baseUrl,
                  decoration: const InputDecoration(
                    labelText: '服务地址',
                    hintText: 'http://127.0.0.1:8081',
                    border: OutlineInputBorder(),
                  ),
                ),
                const SizedBox(height: 16),
                TextField(
                  controller: _adminKey,
                  obscureText: !_revealKey,
                  decoration: InputDecoration(
                    labelText: '管理密钥（MSR_ADMIN_KEY）',
                    helperText: '不是调度密钥；两把密钥在服务端是隔离的',
                    border: const OutlineInputBorder(),
                    suffixIcon: IconButton(
                      tooltip: _revealKey ? '隐藏' : '显示',
                      icon: Icon(
                          _revealKey ? Icons.visibility_off : Icons.visibility),
                      onPressed: () => setState(() => _revealKey = !_revealKey),
                    ),
                  ),
                ),
                const SizedBox(height: 24),
                BusyButton(
                  busy: _busy,
                  onPressed: _save,
                  child: const Text('测试连接并保存'),
                ),
                if (_probeResult != null)
                  Padding(
                    padding: const EdgeInsets.only(top: 16),
                    child: Text(_probeResult!,
                        style: TextStyle(
                            color: Theme.of(context).colorScheme.primary)),
                  ),
                if (_error != null)
                  Padding(
                    padding: const EdgeInsets.only(top: 16),
                    child: SelectableText(
                      describeError(_error!),
                      style:
                          TextStyle(color: Theme.of(context).colorScheme.error),
                    ),
                  ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
