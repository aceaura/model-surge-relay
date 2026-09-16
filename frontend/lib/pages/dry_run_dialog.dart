import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import '../ui/json_field.dart';

Future<void> showDryRunDialog(
  BuildContext context, {
  required ApiClient client,
  required Policy policy,
}) =>
    showDialog<void>(
      context: context,
      builder: (_) => _DryRunDialog(client: client, policy: policy),
    );

/// 试运行读真实 Collection 快照，但运行态由调用方给定，不碰真实运行态、
/// 不解析目标。这是服务端保证的性质，界面上说清楚才能让人放心在生产实例上试。
class _DryRunDialog extends StatefulWidget {
  const _DryRunDialog({required this.client, required this.policy});

  final ApiClient client;
  final Policy policy;

  @override
  State<_DryRunDialog> createState() => _DryRunDialogState();
}

class _DryRunDialogState extends State<_DryRunDialog> {
  final _userModel = TextEditingController();
  final _requestId = TextEditingController(text: 'dry-run');
  final _estTokens = TextEditingController(text: '0');
  final _triedIds = TextEditingController();
  final _runtime = TextEditingController();

  List<CollectionInfo>? _collections;
  String? _collection;
  bool _runtimeValid = true;
  bool _busy = false;
  DryRunResult? _result;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _loadCollections();
  }

  @override
  void dispose() {
    _userModel.dispose();
    _requestId.dispose();
    _estTokens.dispose();
    _triedIds.dispose();
    _runtime.dispose();
    super.dispose();
  }

  Future<void> _loadCollections() async {
    try {
      final list = await widget.client.listCollections();
      if (!mounted) return;
      setState(() {
        _collections = list;
        _collection = list.isEmpty ? null : list.first.name;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    }
  }

  Future<void> _run() async {
    final collection = _collection;
    if (collection == null) return;
    final (runtime, runtimeError) = parseJsonObject(_runtime.text);
    if (runtimeError != null) return;

    setState(() {
      _busy = true;
      _error = null;
      _result = null;
    });
    try {
      final result = await widget.client.dryRunPolicy(
        widget.policy.name,
        collection: collection,
        request: RequestContext(
          userModel: _userModel.text.trim(),
          inboundProtocol: 'anthropic',
          estTokens: int.tryParse(_estTokens.text.trim()) ?? 0,
          triedIds: _triedIds.text
              .split(',')
              .map((s) => s.trim())
              .where((s) => s.isNotEmpty)
              .toList(),
          requestId: _requestId.text.trim(),
        ),
        runtime: runtime,
      );
      if (!mounted) return;
      setState(() => _result = result);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final collections = _collections;
    return AlertDialog(
      title: Text('试运行 ${widget.policy.name}'),
      content: SizedBox(
        width: 620,
        child: SingleChildScrollView(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(
                '读真实集合配置，但运行态由此处给定，不影响任何真实运行态，也不会解析目标。',
                style: Theme.of(context).textTheme.bodySmall,
              ),
              const SizedBox(height: 16),
              if (collections == null)
                const Center(child: CircularProgressIndicator())
              else
                DropdownButtonFormField<String>(
                  initialValue: _collection,
                  decoration: const InputDecoration(
                    labelText: '集合',
                    border: OutlineInputBorder(),
                  ),
                  items: collections
                      .map((c) =>
                          DropdownMenuItem(value: c.name, child: Text(c.name)))
                      .toList(),
                  onChanged: (v) => setState(() => _collection = v),
                ),
              const SizedBox(height: 16),
              Row(
                children: [
                  Expanded(
                    child: TextField(
                      controller: _userModel,
                      decoration: const InputDecoration(
                        labelText: 'user_model',
                        border: OutlineInputBorder(),
                      ),
                    ),
                  ),
                  const SizedBox(width: 12),
                  SizedBox(
                    width: 140,
                    child: TextField(
                      controller: _estTokens,
                      keyboardType: TextInputType.number,
                      decoration: const InputDecoration(
                        labelText: 'est_tokens',
                        border: OutlineInputBorder(),
                      ),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 16),
              Row(
                children: [
                  Expanded(
                    child: TextField(
                      controller: _requestId,
                      decoration: const InputDecoration(
                        labelText: 'request_id',
                        border: OutlineInputBorder(),
                      ),
                    ),
                  ),
                  const SizedBox(width: 12),
                  Expanded(
                    child: TextField(
                      controller: _triedIds,
                      decoration: const InputDecoration(
                        labelText: 'tried_ids',
                        helperText: '逗号分隔',
                        border: OutlineInputBorder(),
                      ),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 16),
              JsonField(
                label: '运行态覆盖（可留空）',
                helper: '形如 {"kimi-1/k3":{"cooling":true,"consecutive_failures":3}}，'
                    '用于测试冷却分支',
                controller: _runtime,
                onValidityChanged: (ok) => setState(() => _runtimeValid = ok),
              ),
              if (_result != null) ...[
                const SizedBox(height: 16),
                _resultPanel(_result!),
              ],
              if (_error != null) ...[
                const SizedBox(height: 16),
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
          child: const Text('关闭'),
        ),
        BusyButton(
          busy: _busy,
          onPressed: _collection != null && _runtimeValid ? _run : null,
          child: const Text('运行'),
        ),
      ],
    );
  }

  Widget _resultPanel(DryRunResult r) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: scheme.surfaceContainerHighest,
        borderRadius: BorderRadius.circular(8),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('v${r.policyVersion} 返回 ${r.candidates.length} 个候选',
              style: Theme.of(context).textTheme.titleSmall),
          if (r.note.isNotEmpty) ...[
            const SizedBox(height: 4),
            Text('note: ${r.note}',
                style: Theme.of(context).textTheme.bodySmall),
          ],
          const SizedBox(height: 8),
          if (r.candidates.isEmpty)
            const Text('候选为空——调度会直接返回 target_unavailable，不发起解析。')
          else
            SelectableText(
              [
                for (var i = 0; i < r.candidates.length; i++)
                  '${i + 1}. ${r.candidates[i]}'
              ].join('\n'),
              style: const TextStyle(fontFamily: 'Consolas', fontSize: 12),
            ),
        ],
      ),
    );
  }
}
