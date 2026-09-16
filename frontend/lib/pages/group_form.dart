import 'package:flutter/material.dart';

import '../models.dart';
import '../ui/json_field.dart';

/// showGroupForm 返回待提交的 Group，取消则返回 null。
Future<Group?> showGroupForm(
  BuildContext context, {
  required String collection,
  required int position,
  Group? initial,
}) =>
    showDialog<Group>(
      context: context,
      builder: (_) => _GroupForm(
        collection: collection,
        position: position,
        initial: initial,
      ),
    );

class _GroupForm extends StatefulWidget {
  const _GroupForm({
    required this.collection,
    required this.position,
    this.initial,
  });

  final String collection;
  final int position;
  final Group? initial;

  @override
  State<_GroupForm> createState() => _GroupFormState();
}

class _GroupFormState extends State<_GroupForm> {
  late final TextEditingController _name =
      TextEditingController(text: widget.initial?.name ?? '');
  late final TextEditingController _type =
      TextEditingController(text: widget.initial?.type ?? '');
  late final TextEditingController _config =
      TextEditingController(text: prettyJson(widget.initial?.config));

  bool _configValid = true;

  @override
  void dispose() {
    _name.dispose();
    _type.dispose();
    _config.dispose();
    super.dispose();
  }

  void _submit() {
    final (config, error) = parseJsonObject(_config.text);
    if (error != null) return;
    Navigator.of(context).pop(Group(
      collection: widget.collection,
      name: _name.text.trim(),
      type: _type.text.trim(),
      position: widget.position,
      config: config,
      members: widget.initial?.members ?? const [],
    ));
  }

  @override
  Widget build(BuildContext context) {
    final editing = widget.initial != null;
    return AlertDialog(
      title: Text(editing ? '编辑组 ${widget.initial!.name}' : '新建组'),
      content: SizedBox(
        width: 520,
        child: SingleChildScrollView(
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
                controller: _type,
                decoration: const InputDecoration(
                  labelText: '类型',
                  helperText: '自由文本，由策略脚本按业务语义读取，服务端不解释',
                  border: OutlineInputBorder(),
                ),
              ),
              const SizedBox(height: 16),
              JsonField(
                label: '组配置（可留空）',
                helper: '策略脚本可从 group.config 读取，服务端同样不解释其含义',
                controller: _config,
                onValidityChanged: (ok) => setState(() => _configValid = ok),
              ),
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: const Text('取消'),
        ),
        FilledButton(
          onPressed: _configValid ? _submit : null,
          child: const Text('保存'),
        ),
      ],
    );
  }
}
