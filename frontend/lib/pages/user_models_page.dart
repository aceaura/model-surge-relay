import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import 'user_model_form.dart';

class UserModelsPage extends StatefulWidget {
  const UserModelsPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  @override
  State<UserModelsPage> createState() => _UserModelsPageState();
}

class _UserModelsPageState extends State<UserModelsPage> {
  List<UserModel>? _models;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      _models = null;
      _error = null;
    });
    try {
      final list = await widget.client.listUserModels();
      if (!mounted) return;
      setState(() => _models = list);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    }
  }

  Future<void> _openForm({UserModel? initial}) async {
    final saved = await showUserModelForm(
      context,
      client: widget.client,
      initial: initial,
    );
    if (saved == null) return;
    await _load();
  }

  Future<void> _delete(UserModel m) async {
    final ok = await confirm(
      context,
      title: '删除模型名 ${m.name}',
      message: '删除后数据面用该名字发起的请求将失败。该操作不可撤销。',
    );
    if (!ok) return;
    try {
      await widget.client.deleteUserModel(m.name);
      await _load();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_error != null) {
      return ErrorPanel(
        error: _error!,
        onRetry: _load,
        onOpenSettings: widget.onOpenSettings,
      );
    }
    final models = _models;
    if (models == null) {
      return const Center(child: CircularProgressIndicator());
    }

    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 8, 4),
          child: Row(
            children: [
              Expanded(
                child:
                    Text('对外模型名', style: Theme.of(context).textTheme.titleSmall),
              ),
              IconButton(
                tooltip: '刷新',
                icon: const Icon(Icons.refresh, size: 20),
                onPressed: _load,
              ),
              const SizedBox(width: 8),
              FilledButton.icon(
                onPressed: () => _openForm(),
                icon: const Icon(Icons.add, size: 18),
                label: const Text('新建'),
              ),
              const SizedBox(width: 8),
            ],
          ),
        ),
        Expanded(
          child: models.isEmpty
              ? const Center(child: Text('还没有对外模型名'))
              : ListView.separated(
                  itemCount: models.length,
                  separatorBuilder: (_, _) => const Divider(height: 1),
                  itemBuilder: (_, i) {
                    final m = models[i];
                    return ListTile(
                      leading: Icon(
                        m.enabled ? Icons.check_circle : Icons.block,
                        color: m.enabled
                            ? Theme.of(context).colorScheme.primary
                            : Theme.of(context).disabledColor,
                        size: 20,
                      ),
                      title: Text(m.name),
                      subtitle: Text(
                        '集合 ${m.collection}  ·  '
                        '策略 ${m.policy.isEmpty ? "（兜底顺序）" : m.policy}  ·  '
                        '${m.protocol.isEmpty ? "任意协议" : m.protocol}',
                      ),
                      onTap: () => _openForm(initial: m),
                      trailing: PopupMenuButton<String>(
                        onSelected: (v) => v == 'edit'
                            ? _openForm(initial: m)
                            : _delete(m),
                        itemBuilder: (_) => const [
                          PopupMenuItem(value: 'edit', child: Text('编辑')),
                          PopupMenuItem(value: 'delete', child: Text('删除')),
                        ],
                      ),
                    );
                  },
                ),
        ),
      ],
    );
  }
}
