import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';

/// 集合的策略组合编辑器：优先级链 + 超长压缩托管 + 试运行。
/// 做成对话框而非第四栏——策略是低频配置项，三栏主从布局要为日常的
/// 编排操作留宽度。
Future<void> showStrategyEditor(
  BuildContext context, {
  required ApiClient client,
  required String collection,
}) =>
    showDialog(
      context: context,
      builder: (_) => _StrategyEditor(client: client, collection: collection),
    );

class _StrategyEditor extends StatefulWidget {
  const _StrategyEditor({required this.client, required this.collection});

  final ApiClient client;
  final String collection;

  @override
  State<_StrategyEditor> createState() => _StrategyEditorState();
}

class _StrategyEditorState extends State<_StrategyEditor> {
  List<Group>? _groups;

  // 可编辑副本：保存前不动服务端。
  List<String> _chain = [];
  bool _overflowEnabled = false;
  final _threshold = TextEditingController();
  Set<String> _compactGroups = {};

  final _estTokens = TextEditingController(text: '0');
  final _triedIds = TextEditingController();
  DryRunDecision? _dryRun;
  bool _dryRunBusy = false;

  bool _loaded = false;
  bool _saving = false;
  Object? _error;
  String? _fieldError;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _threshold.dispose();
    _estTokens.dispose();
    _triedIds.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final groups = await widget.client.listGroups(widget.collection);
      final st = await widget.client.getStrategy(widget.collection);
      if (!mounted) return;
      setState(() {
        _groups = groups;
        _chain = [...st.priorityChain];
        _overflowEnabled = st.overflow.enabled;
        _threshold.text =
            st.overflow.thresholdTokens == 0 ? '' : '${st.overflow.thresholdTokens}';
        _compactGroups = {...st.overflow.compactGroups};
        _loaded = true;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    }
  }

  int get _thresholdValue => int.tryParse(_threshold.text.trim()) ?? 0;

  Strategy get _draft => Strategy(
        priorityChain: _chain,
        overflow: OverflowConfig(
          enabled: _overflowEnabled,
          thresholdTokens: _thresholdValue,
          compactGroups: _compactGroups.toList()..sort(),
        ),
      );

  Future<void> _save() async {
    setState(() {
      _saving = true;
      _error = null;
      _fieldError = null;
    });
    try {
      await widget.client.putStrategy(widget.collection, _draft);
      if (!mounted) return;
      Navigator.of(context).pop();
    } catch (e) {
      if (!mounted) return;
      // 带 field 的校验错落到对应配置区，省去在表单上逐项猜。
      setState(() {
        _error = e;
        _fieldError = e is ValidationException ? e.field : null;
      });
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _dryRunNow() async {
    setState(() {
      _dryRunBusy = true;
      _error = null;
      _fieldError = null;
    });
    try {
      final tried = _triedIds.text
          .split(',')
          .map((s) => s.trim())
          .where((s) => s.isNotEmpty)
          .toList();
      final result = await widget.client.dryRunStrategy(
        widget.collection,
        estTokens: int.tryParse(_estTokens.text.trim()) ?? 0,
        triedIds: tried,
      );
      if (!mounted) return;
      setState(() => _dryRun = result);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    } finally {
      if (mounted) setState(() => _dryRunBusy = false);
    }
  }

  void _move(int index, int delta) {
    final next = index + delta;
    if (next < 0 || next >= _chain.length) return;
    setState(() {
      final item = _chain.removeAt(index);
      _chain.insert(next, item);
    });
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: Text('策略组合 · ${widget.collection}'),
      content: SizedBox(
        width: 620,
        child: !_loaded && _error == null
            ? const Center(child: CircularProgressIndicator())
            : SingleChildScrollView(
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    if (_error != null && !_loaded) ...[
                      SelectableText(
                        describeError(_error!),
                        style: TextStyle(
                            color: Theme.of(context).colorScheme.error),
                      ),
                    ] else ...[
                      _chainSection(),
                      const Divider(height: 28),
                      _overflowSection(),
                      const Divider(height: 28),
                      _dryRunSection(),
                      if (_error != null && _fieldError == null) ...[
                        const SizedBox(height: 12),
                        SelectableText(
                          describeError(_error!),
                          style: TextStyle(
                              color: Theme.of(context).colorScheme.error),
                        ),
                      ],
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
          busy: _saving,
          onPressed: _loaded ? _save : null,
          child: const Text('保存'),
        ),
      ],
    );
  }

  Widget _chainSection() {
    final groups = _groups ?? const <Group>[];
    final unchained = groups.where((g) => !_chain.contains(g.name)).toList();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('优先级链', style: Theme.of(context).textTheme.titleSmall),
        const SizedBox(height: 4),
        Text(
          '按顺序执行；组内成员全部不可用（含限额冷却）时自动跳到下一组。'
          '不选任何组表示按组的排列顺序执行。',
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 10),
        if (_fieldError == 'priority_chain')
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: SelectableText(
              describeError(_error!),
              style:
                  TextStyle(color: Theme.of(context).colorScheme.error),
            ),
          ),
        if (_chain.isEmpty)
          const Padding(
            padding: EdgeInsets.symmetric(vertical: 8),
            child: Text('（未配置链，按组顺序执行）'),
          )
        else
          for (var i = 0; i < _chain.length; i++)
            ListTile(
              dense: true,
              contentPadding: EdgeInsets.zero,
              leading: CircleAvatar(
                radius: 13,
                child: Text('${i + 1}', style: const TextStyle(fontSize: 12)),
              ),
              title: Text(_chain[i]),
              trailing: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  IconButton(
                    tooltip: '上移',
                    icon: const Icon(Icons.arrow_upward, size: 18),
                    onPressed: i == 0 ? null : () => _move(i, -1),
                  ),
                  IconButton(
                    tooltip: '下移',
                    icon: const Icon(Icons.arrow_downward, size: 18),
                    onPressed:
                        i == _chain.length - 1 ? null : () => _move(i, 1),
                  ),
                  IconButton(
                    tooltip: '移出链',
                    icon: const Icon(Icons.remove_circle_outline, size: 18),
                    onPressed: () =>
                        setState(() => _chain.remove(_chain[i])),
                  ),
                ],
              ),
            ),
        if (unchained.isNotEmpty) ...[
          const SizedBox(height: 4),
          Wrap(
            spacing: 8,
            runSpacing: 4,
            children: [
              for (final g in unchained)
                ActionChip(
                  avatar: const Icon(Icons.add, size: 16),
                  label: Text(g.name),
                  onPressed: () => setState(() => _chain.add(g.name)),
                ),
            ],
          ),
        ],
      ],
    );
  }

  Widget _overflowSection() {
    final groups = _groups ?? const <Group>[];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('超长压缩托管', style: Theme.of(context).textTheme.titleSmall),
        const SizedBox(height: 4),
        Text(
          '上下文超过阈值时，先由压缩组托管压缩上下文，压缩完成后回到链上继续执行。',
          style: Theme.of(context).textTheme.bodySmall,
        ),
        SwitchListTile(
          value: _overflowEnabled,
          onChanged: (v) => setState(() => _overflowEnabled = v),
          title: const Text('启用'),
          contentPadding: EdgeInsets.zero,
        ),
        TextField(
          controller: _threshold,
          enabled: _overflowEnabled,
          keyboardType: TextInputType.number,
          inputFormatters: [FilteringTextInputFormatter.digitsOnly],
          decoration: InputDecoration(
            labelText: '触发阈值（tokens）',
            helperText: '估算 token 数达到该值即进入压缩托管',
            border: const OutlineInputBorder(),
            errorText: _fieldError == 'threshold_tokens'
                ? describeError(_error!)
                : null,
          ),
        ),
        const SizedBox(height: 12),
        Text('压缩组（可多选）', style: Theme.of(context).textTheme.bodyMedium),
        const SizedBox(height: 6),
        if (_fieldError == 'compact_groups')
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: SelectableText(
              describeError(_error!),
              style:
                  TextStyle(color: Theme.of(context).colorScheme.error),
            ),
          ),
        Wrap(
          spacing: 8,
          runSpacing: 4,
          children: [
            for (final g in groups)
              FilterChip(
                label: Text(g.name),
                selected: _compactGroups.contains(g.name),
                onSelected: _overflowEnabled
                    ? (on) => setState(() {
                          on
                              ? _compactGroups.add(g.name)
                              : _compactGroups.remove(g.name);
                        })
                    : null,
              ),
          ],
        ),
      ],
    );
  }

  Widget _dryRunSection() {
    final result = _dryRun;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('试运行', style: Theme.of(context).textTheme.titleSmall),
        const SizedBox(height: 4),
        Text(
          '试运行基于已保存的配置，与真实调度共用同一个组合器：'
          '看到的候选序列就是上线后的序列。',
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 10),
        Row(
          children: [
            Expanded(
              child: TextField(
                controller: _estTokens,
                keyboardType: TextInputType.number,
                inputFormatters: [FilteringTextInputFormatter.digitsOnly],
                decoration: const InputDecoration(
                  labelText: '估算 tokens',
                  border: OutlineInputBorder(),
                  isDense: true,
                ),
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              flex: 2,
              child: TextField(
                controller: _triedIds,
                decoration: const InputDecoration(
                  labelText: '已试过（逗号分隔，可空）',
                  border: OutlineInputBorder(),
                  isDense: true,
                ),
              ),
            ),
            const SizedBox(width: 12),
            BusyButton(
              busy: _dryRunBusy,
              onPressed: _dryRunNow,
              child: const Text('试运行'),
            ),
          ],
        ),
        if (result != null) ...[
          const SizedBox(height: 12),
          _dryRunResult(result),
        ],
      ],
    );
  }

  Widget _dryRunResult(DryRunDecision result) {
    return Container(
      decoration: BoxDecoration(
        border: Border.all(color: Theme.of(context).dividerColor),
        borderRadius: BorderRadius.circular(8),
      ),
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (result.candidates.isEmpty)
            const Text('没有可用候选')
          else
            for (var i = 0; i < result.candidates.length; i++)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 2),
                child: Row(
                  children: [
                    SizedBox(width: 24, child: Text('${i + 1}.')),
                    Expanded(
                        child: SelectableText(result.candidates[i].modelId)),
                    _phaseChip(result.candidates[i].phase),
                  ],
                ),
              ),
          if (result.groupSkips.isNotEmpty) ...[
            const SizedBox(height: 8),
            Text('整组跳过', style: Theme.of(context).textTheme.bodySmall),
            for (final s in result.groupSkips)
              Text('${s.group}：${s.detail.isEmpty ? s.reason : s.detail}',
                  style: Theme.of(context).textTheme.bodySmall),
          ],
          if (result.skipped.isNotEmpty) ...[
            const SizedBox(height: 8),
            Text('成员跳过', style: Theme.of(context).textTheme.bodySmall),
            for (final s in result.skipped)
              Text('${s.modelId}（${s.group}）：${s.reason}',
                  style: Theme.of(context).textTheme.bodySmall),
          ],
        ],
      ),
    );
  }

  Widget _phaseChip(String phase) {
    final (label, color) = switch (phase) {
      'compact' => ('压缩', Colors.deepPurple),
      'resume' => ('回落', Colors.teal),
      _ => ('常规', Colors.blueGrey),
    };
    return Chip(
      label: Text(label, style: const TextStyle(fontSize: 11)),
      visualDensity: VisualDensity.compact,
      side: BorderSide(color: color),
      labelStyle: TextStyle(color: color),
    );
  }
}
