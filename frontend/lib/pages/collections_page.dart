import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import 'group_form.dart';
import 'members_editor.dart';

/// Collection 页是三栏主从布局：Collection → Group → 成员。
/// 编排是个来回对照的过程，拆成多级页面跳转会不断打断它。
class CollectionsPage extends StatefulWidget {
  const CollectionsPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  @override
  State<CollectionsPage> createState() => _CollectionsPageState();
}

class _CollectionsPageState extends State<CollectionsPage> {
  List<CollectionInfo>? _collections;
  Object? _error;

  String? _selected;
  List<Group>? _groups;
  Snapshot? _snapshot;
  Object? _detailError;
  bool _reordering = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      _collections = null;
      _error = null;
    });
    try {
      final list = await widget.client.listCollections();
      if (!mounted) return;
      setState(() {
        _collections = list;
        if (_selected != null && !list.any((c) => c.name == _selected)) {
          _selected = null;
          _groups = null;
          _snapshot = null;
        }
      });
      if (_selected != null) await _loadDetail(_selected!);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    }
  }

  Future<void> _loadDetail(String name) async {
    setState(() {
      _groups = null;
      _snapshot = null;
      _detailError = null;
    });
    try {
      final groups = await widget.client.listGroups(name);
      final snapshot = await widget.client.getSnapshot(name);
      if (!mounted) return;
      setState(() {
        _groups = groups;
        _snapshot = snapshot;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() => _detailError = e);
    }
  }

  void _select(String name) {
    setState(() => _selected = name);
    _loadDetail(name);
  }

  Future<void> _createCollection() async {
    final result = await showDialog<(String, String)>(
      context: context,
      builder: (_) => const _CollectionDialog(),
    );
    if (result == null) return;
    try {
      await widget.client.createCollection(name: result.$1, note: result.$2);
      await _load();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _editNote(CollectionInfo c) async {
    final result = await showDialog<(String, String)>(
      context: context,
      builder: (_) => _CollectionDialog(initial: c),
    );
    if (result == null) return;
    try {
      await widget.client.updateCollectionNote(c.name, result.$2);
      await _load();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _deleteCollection(CollectionInfo c) async {
    final ok = await confirm(
      context,
      title: '删除集合 ${c.name}',
      message: '该操作会级联删除其下全部 Group 与成员编排，且不可撤销。\n'
          '引用了该集合的 user model 将无法调度。',
    );
    if (!ok) return;
    try {
      await widget.client.deleteCollection(c.name);
      await _load();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _createGroup() async {
    final collection = _selected;
    if (collection == null) return;
    final draft = await showGroupForm(
      context,
      collection: collection,
      position: _groups?.length ?? 0,
    );
    if (draft == null) return;
    try {
      await widget.client.createGroup(collection, draft);
      await _loadDetail(collection);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _editGroup(Group g) async {
    final collection = _selected;
    if (collection == null) return;
    final draft = await showGroupForm(
      context,
      collection: collection,
      position: g.position,
      initial: g,
    );
    if (draft == null) return;
    try {
      await widget.client.updateGroup(collection, draft);
      await _loadDetail(collection);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _deleteGroup(Group g) async {
    final collection = _selected;
    if (collection == null) return;
    final ok = await confirm(
      context,
      title: '删除组 ${g.name}',
      message: '该操作会同时删除组内 ${g.members.length} 个成员编排，且不可撤销。',
    );
    if (!ok) return;
    try {
      await widget.client.deleteGroup(collection, g.name);
      await _loadDetail(collection);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  /// 后端没有批量重排端点，只能逐个提交新 position。
  /// 提交期间禁用交互：中途失败会留下半序状态，让用户再拖只会越拖越乱。
  Future<void> _reorderGroups(int from, int to) async {
    final collection = _selected;
    final groups = _groups;
    if (collection == null || groups == null) return;

    final reordered = [...groups];
    final moved = reordered.removeAt(from);
    reordered.insert(to, moved);

    setState(() {
      _groups = reordered;
      _reordering = true;
    });
    try {
      for (var i = 0; i < reordered.length; i++) {
        if (reordered[i].position == i) continue;
        await widget.client
            .updateGroup(collection, reordered[i].copyWith(position: i));
      }
      await _loadDetail(collection);
    } catch (e) {
      if (mounted) showError(context, e);
      await _loadDetail(collection);
    } finally {
      if (mounted) setState(() => _reordering = false);
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
    final collections = _collections;
    if (collections == null) {
      return const Center(child: CircularProgressIndicator());
    }

    return Row(
      children: [
        SizedBox(width: 260, child: _collectionList(collections)),
        const VerticalDivider(width: 1),
        SizedBox(width: 320, child: _groupList()),
        const VerticalDivider(width: 1),
        Expanded(child: _memberPane()),
      ],
    );
  }

  Widget _collectionList(List<CollectionInfo> collections) {
    return Column(
      children: [
        _paneHeader('集合', onAdd: _createCollection, onRefresh: _load),
        Expanded(
          child: collections.isEmpty
              ? const Center(child: Text('还没有集合'))
              : ListView.builder(
                  itemCount: collections.length,
                  itemBuilder: (_, i) {
                    final c = collections[i];
                    return ListTile(
                      selected: c.name == _selected,
                      title: Text(c.name),
                      subtitle: c.note.isEmpty ? null : Text(c.note),
                      onTap: () => _select(c.name),
                      trailing: PopupMenuButton<String>(
                        onSelected: (v) =>
                            v == 'edit' ? _editNote(c) : _deleteCollection(c),
                        itemBuilder: (_) => const [
                          PopupMenuItem(value: 'edit', child: Text('编辑备注')),
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

  Widget _groupList() {
    if (_selected == null) {
      return const Center(child: Text('选择一个集合'));
    }
    if (_detailError != null) {
      return ErrorPanel(
        error: _detailError!,
        onRetry: () => _loadDetail(_selected!),
        onOpenSettings: widget.onOpenSettings,
      );
    }
    final groups = _groups;
    if (groups == null) {
      return const Center(child: CircularProgressIndicator());
    }
    return Column(
      children: [
        _paneHeader(
          '组（可拖拽排序）',
          onAdd: _createGroup,
          onRefresh: () => _loadDetail(_selected!),
        ),
        if (_reordering) const LinearProgressIndicator(),
        Expanded(
          child: groups.isEmpty
              ? const Center(child: Text('该集合下还没有组'))
              : ReorderableListView.builder(
                  buildDefaultDragHandles: !_reordering,
                  itemCount: groups.length,
                  onReorderItem: _reordering ? (_, _) {} : _reorderGroups,
                  itemBuilder: (_, i) {
                    final g = groups[i];
                    return ListTile(
                      key: ValueKey(g.name),
                      leading: CircleAvatar(
                        radius: 14,
                        child: Text('${i + 1}',
                            style: const TextStyle(fontSize: 12)),
                      ),
                      title: Text(g.name),
                      subtitle: Text(
                        '类型 ${g.type.isEmpty ? "（未设）" : g.type}  ·  '
                        '${g.members.length} 个成员'
                        '${(g.config?.isNotEmpty ?? false) ? "  ·  带配置" : ""}',
                      ),
                      selected: _selectedGroup == g.name,
                      onTap: () => setState(() => _selectedGroup = g.name),
                      trailing: PopupMenuButton<String>(
                        onSelected: (v) =>
                            v == 'edit' ? _editGroup(g) : _deleteGroup(g),
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

  String? _selectedGroup;

  Widget _memberPane() {
    final collection = _selected;
    final groupName = _selectedGroup;
    final snapshot = _snapshot;
    if (collection == null || groupName == null || snapshot == null) {
      return const Center(child: Text('选择一个组来编排成员'));
    }
    final group = snapshot.groups.where((g) => g.name == groupName).firstOrNull;
    if (group == null) {
      return const Center(child: Text('选择一个组来编排成员'));
    }
    return MembersEditor(
      key: ValueKey('$collection/$groupName'),
      client: widget.client,
      collection: collection,
      group: group,
      onSaved: () => _loadDetail(collection),
    );
  }

  Widget _paneHeader(String title, {VoidCallback? onAdd, VoidCallback? onRefresh}) {
    return Padding(
      padding: const EdgeInsets.only(left: 16, right: 8, top: 8, bottom: 4),
      child: Row(
        children: [
          Expanded(
            child: Text(title, style: Theme.of(context).textTheme.titleSmall),
          ),
          if (onRefresh != null)
            IconButton(
              tooltip: '刷新',
              icon: const Icon(Icons.refresh, size: 20),
              onPressed: onRefresh,
            ),
          if (onAdd != null)
            IconButton(
              tooltip: '新建',
              icon: const Icon(Icons.add, size: 20),
              onPressed: onAdd,
            ),
        ],
      ),
    );
  }
}

class _CollectionDialog extends StatefulWidget {
  const _CollectionDialog({this.initial});

  final CollectionInfo? initial;

  @override
  State<_CollectionDialog> createState() => _CollectionDialogState();
}

class _CollectionDialogState extends State<_CollectionDialog> {
  late final TextEditingController _name =
      TextEditingController(text: widget.initial?.name ?? '');
  late final TextEditingController _note =
      TextEditingController(text: widget.initial?.note ?? '');

  @override
  void dispose() {
    _name.dispose();
    _note.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final editing = widget.initial != null;
    return AlertDialog(
      title: Text(editing ? '编辑集合备注' : '新建集合'),
      content: SizedBox(
        width: 400,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            TextField(
              controller: _name,
              enabled: !editing,
              decoration: const InputDecoration(
                labelText: '名称',
                border: OutlineInputBorder(),
              ),
            ),
            const SizedBox(height: 16),
            TextField(
              controller: _note,
              decoration: const InputDecoration(
                labelText: '备注',
                border: OutlineInputBorder(),
              ),
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: const Text('取消'),
        ),
        FilledButton(
          onPressed: () => Navigator.of(context)
              .pop((_name.text.trim(), _note.text.trim())),
          child: const Text('保存'),
        ),
      ],
    );
  }
}
