import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/code_editor.dart';
import '../ui/feedback.dart';

/// 策略编辑走整页路由而非对话框：编辑区需要高度，挤在对话框里没法写脚本。
class PolicyForm extends StatefulWidget {
  const PolicyForm({super.key, required this.client, this.initial});

  final ApiClient client;
  final Policy? initial;

  @override
  State<PolicyForm> createState() => _PolicyFormState();
}

class _PolicyFormState extends State<PolicyForm> {
  late final TextEditingController _name =
      TextEditingController(text: widget.initial?.name ?? '');
  late final TextEditingController _note =
      TextEditingController(text: widget.initial?.note ?? '');
  late final TextEditingController _source =
      TextEditingController(text: widget.initial?.source ?? _luaTemplate);

  late String _language = widget.initial?.language ?? 'lua';
  bool _busy = false;
  String? _compileError;

  static const _luaTemplate = '''
-- 返回有序的 upstream 模型引用列表。
local out = {}
for _, group in ipairs(input.collection.groups) do
  for _, member in ipairs(group.members) do
    if member.known and member.enabled and not input.runtime[member.model_id].cooling then
      table.insert(out, member.model_id)
    end
  end
end
return { candidates = out, note = "first available" }
''';

  @override
  void dispose() {
    _name.dispose();
    _note.dispose();
    _source.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    setState(() {
      _busy = true;
      _compileError = null;
    });
    final draft = Policy(
      name: _name.text.trim(),
      language: _language,
      source: _source.text,
      note: _note.text.trim(),
    );
    try {
      final saved = widget.initial == null
          ? await widget.client.createPolicy(draft)
          : await widget.client.updatePolicy(draft);
      if (!mounted) return;
      Navigator.of(context).pop(saved);
    } catch (e) {
      if (!mounted) return;
      // 编译错误显示在编辑区下方，源码保持不动：
      // 编译失败是写策略的常态，清掉输入等于让人重写。
      if (e is ValidationException &&
          (e.code == 'policy_error' ||
              e.code == 'invalid_request' ||
              e.code == 'policy_timeout')) {
        setState(() => _compileError = describeError(e));
      } else {
        showError(context, e);
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final editing = widget.initial != null;
    return Scaffold(
      appBar: AppBar(
        title: Text(editing ? '编辑策略 ${widget.initial!.name}' : '新建策略'),
        actions: [
          if (editing)
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 8),
              child: Center(child: Text('当前版本 v${widget.initial!.version}')),
            ),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12),
            child: BusyButton(busy: _busy, onPressed: _save, child: const Text('保存')),
          ),
        ],
      ),
      body: Row(
        children: [
          Expanded(
            flex: 3,
            child: Padding(
              padding: const EdgeInsets.all(16),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Row(
                    children: [
                      Expanded(
                        child: TextField(
                          controller: _name,
                          enabled: !editing,
                          decoration: const InputDecoration(
                            labelText: '名称',
                            border: OutlineInputBorder(),
                          ),
                        ),
                      ),
                      const SizedBox(width: 16),
                      SizedBox(
                        width: 220,
                        child: DropdownButtonFormField<String>(
                          initialValue: _language,
                          decoration: const InputDecoration(
                            labelText: '语言',
                            border: OutlineInputBorder(),
                          ),
                          items: policyLanguages
                              .map((l) =>
                                  DropdownMenuItem(value: l, child: Text(l)))
                              .toList(),
                          onChanged: (v) =>
                              setState(() => _language = v ?? _language),
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 16),
                  TextField(
                    controller: _note,
                    decoration: const InputDecoration(
                      labelText: '备注',
                      helperText: '只改备注不会递增版本号',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 16),
                  Expanded(
                    child: CodeEditor(
                      controller: _source,
                      error: _compileError,
                    ),
                  ),
                ],
              ),
            ),
          ),
          const VerticalDivider(width: 1),
          SizedBox(
            width: 380,
            child: Padding(
              padding: const EdgeInsets.all(16),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Text('输入结构速查',
                      style: Theme.of(context).textTheme.titleSmall),
                  const SizedBox(height: 8),
                  const Expanded(
                    child: SingleChildScrollView(
                      child: SelectableText(
                        policyInputReference,
                        style: TextStyle(fontFamily: 'Consolas', fontSize: 11),
                      ),
                    ),
                  ),
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }
}
