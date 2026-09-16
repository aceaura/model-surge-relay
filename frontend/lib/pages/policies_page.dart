import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import 'dry_run_dialog.dart';
import 'policy_form.dart';

class PoliciesPage extends StatefulWidget {
  const PoliciesPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  @override
  State<PoliciesPage> createState() => _PoliciesPageState();
}

class _PoliciesPageState extends State<PoliciesPage> {
  List<Policy>? _policies;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      _policies = null;
      _error = null;
    });
    try {
      final list = await widget.client.listPolicies();
      if (!mounted) return;
      setState(() => _policies = list);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    }
  }

  Future<void> _openForm({Policy? initial}) async {
    final saved = await Navigator.of(context).push<Policy>(MaterialPageRoute(
      builder: (_) => PolicyForm(client: widget.client, initial: initial),
    ));
    if (saved == null) return;
    await _load();
    if (mounted) showInfo(context, '策略 ${saved.name} 已保存，版本 ${saved.version}');
  }

  Future<void> _edit(Policy p) async {
    try {
      // 列表接口已带源码，但重新取一次可避免拿到别人并发改过的旧版本。
      final fresh = await widget.client.getPolicy(p.name);
      if (!mounted) return;
      await _openForm(initial: fresh);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _delete(Policy p) async {
    final ok = await confirm(
      context,
      title: '删除策略 ${p.name}',
      message: '该操作不可撤销。若仍有 user model 绑定该策略，服务端会拒绝删除。',
    );
    if (!ok) return;
    try {
      await widget.client.deletePolicy(p.name);
      await _load();
    } catch (e) {
      // 409 的 message 里带全部引用者名单，原样展示比自造文案有用。
      if (mounted) showError(context, e);
    }
  }

  Future<void> _dryRun(Policy p) => showDryRunDialog(
        context,
        client: widget.client,
        policy: p,
      );

  @override
  Widget build(BuildContext context) {
    if (_error != null) {
      return ErrorPanel(
        error: _error!,
        onRetry: _load,
        onOpenSettings: widget.onOpenSettings,
      );
    }
    final policies = _policies;
    if (policies == null) {
      return const Center(child: CircularProgressIndicator());
    }

    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 8, 4),
          child: Row(
            children: [
              Expanded(
                child: Text('策略', style: Theme.of(context).textTheme.titleSmall),
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
                label: const Text('新建策略'),
              ),
              const SizedBox(width: 8),
            ],
          ),
        ),
        Expanded(
          child: policies.isEmpty
              ? const Center(child: Text('还没有策略。未绑定策略的 user model 走兜底顺序。'))
              : ListView.separated(
                  itemCount: policies.length,
                  separatorBuilder: (_, _) => const Divider(height: 1),
                  itemBuilder: (_, i) {
                    final p = policies[i];
                    return ListTile(
                      title: Text(p.name),
                      subtitle: Text(
                        '${p.language} · v${p.version}'
                        '${p.note.isEmpty ? "" : " · ${p.note}"}',
                      ),
                      onTap: () => _edit(p),
                      trailing: Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          TextButton.icon(
                            onPressed: () => _dryRun(p),
                            icon: const Icon(Icons.play_arrow, size: 18),
                            label: const Text('试运行'),
                          ),
                          PopupMenuButton<String>(
                            onSelected: (v) =>
                                v == 'edit' ? _edit(p) : _delete(p),
                            itemBuilder: (_) => const [
                              PopupMenuItem(value: 'edit', child: Text('编辑')),
                              PopupMenuItem(value: 'delete', child: Text('删除')),
                            ],
                          ),
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
