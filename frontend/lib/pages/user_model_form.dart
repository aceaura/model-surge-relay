import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';

Future<UserModel?> showUserModelForm(
  BuildContext context, {
  required ApiClient client,
  UserModel? initial,
}) =>
    showDialog<UserModel>(
      context: context,
      builder: (_) => _UserModelForm(client: client, initial: initial),
    );

class _UserModelForm extends StatefulWidget {
  const _UserModelForm({required this.client, this.initial});

  final ApiClient client;
  final UserModel? initial;

  @override
  State<_UserModelForm> createState() => _UserModelFormState();
}

class _UserModelFormState extends State<_UserModelForm> {
  late final TextEditingController _name =
      TextEditingController(text: widget.initial?.name ?? '');
  final _clientKey = TextEditingController();

  late String? _collection = widget.initial?.collection;
  late String _policy = widget.initial?.policy ?? '';
  late String _protocol = widget.initial?.protocol ?? protocols.first;
  late bool _enabled = widget.initial?.enabled ?? true;

  List<CollectionInfo>? _collections;
  List<Policy>? _policies;
  bool _busy = false;
  Object? _error;
  String? _fieldError;

  @override
  void initState() {
    super.initState();
    _loadOptions();
  }

  @override
  void dispose() {
    _name.dispose();
    _clientKey.dispose();
    super.dispose();
  }

  Future<void> _loadOptions() async {
    try {
      final collections = await widget.client.listCollections();
      final policies = await widget.client.listPolicies();
      if (!mounted) return;
      setState(() {
        _collections = collections;
        _policies = policies;
        _collection ??= collections.isEmpty ? null : collections.first.name;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    }
  }

  Future<void> _save() async {
    final collection = _collection;
    if (collection == null) return;
    setState(() {
      _busy = true;
      _error = null;
      _fieldError = null;
    });
    final draft = UserModel(
      name: _name.text.trim(),
      collection: collection,
      policy: _policy,
      protocol: _protocol,
      enabled: _enabled,
    );
    try {
      final saved = widget.initial == null
          ? await widget.client.createUserModel(draft, _clientKey.text)
          : await widget.client.updateUserModel(draft, _clientKey.text);
      if (!mounted) return;
      Navigator.of(context).pop(saved);
    } catch (e) {
      if (!mounted) return;
      // 带 field 的校验错落到对应下拉项旁，省去在表单上逐项猜。
      setState(() {
        _error = e;
        _fieldError = e is ValidationException ? e.field : null;
      });
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final editing = widget.initial != null;
    final collections = _collections;
    final policies = _policies;

    return AlertDialog(
      title: Text(editing ? '编辑 ${widget.initial!.name}' : '新建对外模型名'),
      content: SizedBox(
        width: 520,
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              TextField(
                controller: _name,
                enabled: !editing,
                decoration: const InputDecoration(
                  labelText: '名称',
                  helperText: '数据面用这个名字发起请求',
                  border: OutlineInputBorder(),
                ),
              ),
              const SizedBox(height: 16),
              if (collections == null || policies == null)
                const Center(child: CircularProgressIndicator())
              else ...[
                DropdownButtonFormField<String>(
                  initialValue: _collection,
                  decoration: InputDecoration(
                    labelText: '集合',
                    border: const OutlineInputBorder(),
                    errorText: _fieldError == 'collection'
                        ? describeError(_error!)
                        : null,
                  ),
                  items: collections
                      .map((c) =>
                          DropdownMenuItem(value: c.name, child: Text(c.name)))
                      .toList(),
                  onChanged: (v) => setState(() => _collection = v),
                ),
                const SizedBox(height: 16),
                DropdownButtonFormField<String>(
                  initialValue: _policy,
                  decoration: InputDecoration(
                    labelText: '策略',
                    border: const OutlineInputBorder(),
                    errorText:
                        _fieldError == 'policy' ? describeError(_error!) : null,
                  ),
                  items: [
                    const DropdownMenuItem(
                        value: '', child: Text('（不绑定，走兜底顺序）')),
                    ...policies.map((p) => DropdownMenuItem(
                        value: p.name, child: Text('${p.name}  v${p.version}'))),
                  ],
                  onChanged: (v) => setState(() => _policy = v ?? ''),
                ),
              ],
              const SizedBox(height: 16),
              DropdownButtonFormField<String>(
                initialValue: _protocol,
                decoration: const InputDecoration(
                  labelText: '入站协议',
                  border: OutlineInputBorder(),
                ),
                items: protocols
                    .map((p) => DropdownMenuItem(value: p, child: Text(p)))
                    .toList(),
                onChanged: (v) => setState(() => _protocol = v ?? _protocol),
              ),
              const SizedBox(height: 16),
              TextField(
                controller: _clientKey,
                obscureText: true,
                decoration: InputDecoration(
                  labelText: '客户端密钥',
                  // 后端 Update 是全量覆盖，不做"空则保留"。说实话比沿用别处的措辞重要。
                  helperText: editing
                      ? '留空将提交空密钥，导致该模型名无法通过鉴权。要保留原密钥请重新填入。'
                      : '数据面调用时携带的密钥',
                  border: const OutlineInputBorder(),
                ),
              ),
              const SizedBox(height: 8),
              SwitchListTile(
                value: _enabled,
                onChanged: (v) => setState(() => _enabled = v),
                title: const Text('启用'),
                subtitle: const Text('禁用后调度直接拒绝该模型名'),
                contentPadding: EdgeInsets.zero,
              ),
              if (_error != null && _fieldError == null) ...[
                const SizedBox(height: 8),
                SelectableText(
                  describeError(_error!),
                  style: TextStyle(color: Theme.of(context).colorScheme.error),
                ),
              ],
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: const Text('取消'),
        ),
        BusyButton(
          busy: _busy,
          onPressed: _collection == null ? null : _save,
          child: const Text('保存'),
        ),
      ],
    );
  }
}
