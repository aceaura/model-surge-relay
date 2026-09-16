import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';

/// MembersEditor 编排单个组的成员。
///
/// 后端 `PUT .../members` 是整组替换，所以这里持有一份本地列表：
/// 重排、移除、添加都只改本地状态，点保存才整体提交。
/// 这意味着未保存的移除不会发请求——界面上必须让这一点看得出来。
class MembersEditor extends StatefulWidget {
  const MembersEditor({
    super.key,
    required this.client,
    required this.collection,
    required this.group,
    required this.onSaved,
  });

  final ApiClient client;
  final String collection;
  final GroupSnapshot group;
  final VoidCallback onSaved;

  @override
  State<MembersEditor> createState() => _MembersEditorState();
}

class _MembersEditorState extends State<MembersEditor> {
  late List<Member> _members = [...widget.group.members];
  bool _dirty = false;
  bool _busy = false;

  @override
  void didUpdateWidget(MembersEditor old) {
    super.didUpdateWidget(old);
    if (old.group != widget.group && !_dirty) {
      _members = [...widget.group.members];
    }
  }

  void _reorder(int from, int to) {
    setState(() {
      final moved = _members.removeAt(from);
      _members.insert(to, moved);
      _dirty = true;
    });
  }

  void _remove(Member m) {
    setState(() {
      _members.removeWhere((x) => x.modelId == m.modelId);
      _dirty = true;
    });
  }

  void _revert() {
    setState(() {
      _members = [...widget.group.members];
      _dirty = false;
    });
  }

  Future<void> _add() async {
    final ids = await showDialog<List<String>>(
      context: context,
      builder: (_) => const _AddMembersDialog(),
    );
    if (ids == null || ids.isEmpty) return;
    final existing = _members.map((m) => m.modelId).toSet();
    setState(() {
      for (final id in ids) {
        if (existing.contains(id)) continue;
        // 目录属性要等服务端回话才知道，先按未知占位。
        _members.add(Member(
          modelId: id,
          account: '',
          providerId: '',
          protocol: '',
          nativeModel: '',
          contextWindow: 0,
          position: _members.length,
          enabled: false,
          known: false,
        ));
      }
      _dirty = true;
    });
  }

  Future<void> _save() async {
    setState(() => _busy = true);
    try {
      await widget.client.replaceMembers(
        widget.collection,
        widget.group.name,
        _members.map((m) => m.modelId).toList(),
      );
      if (!mounted) return;
      setState(() => _dirty = false);
      widget.onSaved();
      showInfo(context, '成员编排已保存');
    } catch (e) {
      // 保留界面上的编辑内容：服务端拒绝的往往只是其中一条引用。
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 8, 4),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  '成员 · ${widget.group.name}'
                  '${_dirty ? "（有未保存改动）" : ""}',
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ),
              if (_dirty)
                TextButton(onPressed: _busy ? null : _revert, child: const Text('放弃改动')),
              IconButton(
                tooltip: '添加成员',
                icon: const Icon(Icons.add, size: 20),
                onPressed: _busy ? null : _add,
              ),
              const SizedBox(width: 8),
              BusyButton(
                busy: _busy,
                onPressed: _dirty ? _save : null,
                child: const Text('保存编排'),
              ),
              const SizedBox(width: 8),
            ],
          ),
        ),
        Expanded(
          child: _members.isEmpty
              ? const Center(child: Text('该组没有成员（空组是合法状态，可直接保存）'))
              : ReorderableListView.builder(
                  itemCount: _members.length,
                  onReorderItem: _reorder,
                  itemBuilder: (_, i) => _tile(_members[i], i),
                ),
        ),
      ],
    );
  }

  Widget _tile(Member m, int index) {
    final scheme = Theme.of(context).colorScheme;
    return ListTile(
      key: ValueKey(m.modelId),
      leading: CircleAvatar(
        radius: 14,
        child: Text('${index + 1}', style: const TextStyle(fontSize: 12)),
      ),
      title: Row(
        children: [
          Text(m.modelId),
          const SizedBox(width: 8),
          // known 与 enabled 是两回事：前者是配置错了该修，
          // 后者是上游临时禁用、调度会跳过。混在一起会让人误删有效配置。
          if (!m.known)
            _chip('目录中已消失', scheme.error, scheme.onError)
          else if (!m.enabled)
            _chip('上游已禁用', scheme.tertiaryContainer, scheme.onTertiaryContainer),
        ],
      ),
      subtitle: Text(m.known
          ? '${m.account} · ${m.protocol} · ${m.nativeModel}'
              '${m.contextWindow > 0 ? " · ${m.contextWindow} ctx" : ""}'
          : '该引用在 upstream 目录中不存在，调度时会被跳过，建议移除或修正'),
      trailing: IconButton(
        tooltip: '移除',
        icon: const Icon(Icons.remove_circle_outline, size: 20),
        onPressed: _busy ? null : () => _remove(m),
      ),
    );
  }

  Widget _chip(String label, Color bg, Color fg) => Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
        decoration: BoxDecoration(
          color: bg,
          borderRadius: BorderRadius.circular(10),
        ),
        child: Text(label, style: TextStyle(fontSize: 11, color: fg)),
      );
}

class _AddMembersDialog extends StatefulWidget {
  const _AddMembersDialog();

  @override
  State<_AddMembersDialog> createState() => _AddMembersDialogState();
}

class _AddMembersDialogState extends State<_AddMembersDialog> {
  final _input = TextEditingController();

  @override
  void dispose() {
    _input.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: const Text('添加成员'),
      content: SizedBox(
        width: 460,
        child: TextField(
          controller: _input,
          minLines: 4,
          maxLines: 8,
          style: const TextStyle(fontFamily: 'Consolas'),
          decoration: const InputDecoration(
            labelText: 'upstream 模型引用',
            helperText: '每行一个，形如 kimi-1/k3。引用是否存在由服务端校验。',
            border: OutlineInputBorder(),
            alignLabelWithHint: true,
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: const Text('取消'),
        ),
        FilledButton(
          onPressed: () {
            final ids = _input.text
                .split('\n')
                .map((s) => s.trim())
                .where((s) => s.isNotEmpty)
                .toList();
            Navigator.of(context).pop(ids);
          },
          child: const Text('添加'),
        ),
      ],
    );
  }
}
