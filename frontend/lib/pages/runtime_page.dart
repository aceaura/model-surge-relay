import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';

class RuntimePage extends StatefulWidget {
  const RuntimePage({
    super.key,
    required this.client,
    required this.onOpenSettings,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  @override
  State<RuntimePage> createState() => _RuntimePageState();
}

class _RuntimePageState extends State<RuntimePage> {
  List<RuntimeState>? _states;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      _states = null;
      _error = null;
    });
    try {
      final list = await widget.client.listRuntime();
      if (!mounted) return;
      setState(() => _states = list);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    }
  }

  Future<void> _reset(RuntimeState s) async {
    final ok = await confirm(
      context,
      title: '清除 ${s.modelId} 的运行态',
      message: '清除冷却状态与连续失败计数，保留累计用量（用量是审计数据）。\n'
          '该目标会立即重新参与调度。',
      confirmLabel: '清除',
    );
    if (!ok) return;
    try {
      await widget.client.resetRuntime(s.modelId);
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
    final states = _states;
    if (states == null) {
      return const Center(child: CircularProgressIndicator());
    }

    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 8, 4),
          child: Row(
            children: [
              Expanded(
                child: Text('运行态', style: Theme.of(context).textTheme.titleSmall),
              ),
              IconButton(
                tooltip: '刷新',
                icon: const Icon(Icons.refresh, size: 20),
                onPressed: _load,
              ),
              const SizedBox(width: 8),
            ],
          ),
        ),
        Expanded(
          child: states.isEmpty
              ? const Center(child: Text('还没有运行态记录。结果上报后才会产生。'))
              : SingleChildScrollView(
                  padding: const EdgeInsets.symmetric(horizontal: 16),
                  child: _table(states),
                ),
        ),
      ],
    );
  }

  Widget _table(List<RuntimeState> states) {
    final scheme = Theme.of(context).colorScheme;
    return DataTable(
      columnSpacing: 24,
      columns: const [
        DataColumn(label: Text('目标')),
        DataColumn(label: Text('状态')),
        DataColumn(label: Text('连续失败'), numeric: true),
        DataColumn(label: Text('请求数'), numeric: true),
        DataColumn(label: Text('输入 tokens'), numeric: true),
        DataColumn(label: Text('输出 tokens'), numeric: true),
        DataColumn(label: Text('更新时间')),
        DataColumn(label: Text('')),
      ],
      rows: states.map((s) {
        final remaining = s.remaining;
        return DataRow(
          cells: [
            DataCell(Text(s.modelId,
                style: const TextStyle(fontFamily: 'Consolas'))),
            DataCell(s.cooling
                ? Row(
                    children: [
                      Icon(Icons.ac_unit, size: 16, color: scheme.error),
                      const SizedBox(width: 4),
                      Text(
                        remaining == null
                            ? '冷却中'
                            : '冷却中 剩余 ${_short(remaining)}',
                        style: TextStyle(color: scheme.error),
                      ),
                    ],
                  )
                : const Text('正常')),
            DataCell(Text('${s.consecutiveFailures}')),
            DataCell(Text('${s.usage.requestCount}')),
            DataCell(Text('${s.usage.inputTokens}')),
            DataCell(Text('${s.usage.outputTokens}')),
            DataCell(Text(s.updatedAt == null ? '-' : _stamp(s.updatedAt!))),
            DataCell(TextButton(
              onPressed: () => _reset(s),
              child: const Text('清除'),
            )),
          ],
        );
      }).toList(),
    );
  }

  static String _short(Duration d) {
    if (d.inMinutes >= 1) return '${d.inMinutes}m${d.inSeconds % 60}s';
    return '${d.inSeconds}s';
  }

  static String _stamp(DateTime t) =>
      '${t.month.toString().padLeft(2, '0')}-${t.day.toString().padLeft(2, '0')} '
      '${t.hour.toString().padLeft(2, '0')}:${t.minute.toString().padLeft(2, '0')}:'
      '${t.second.toString().padLeft(2, '0')}';
}
