/// JSON 编辑控件。内容不是合法 JSON object 时给出提示，由外层据此阻止提交。
library;

import 'dart:convert';

import 'package:flutter/material.dart';

/// parseJsonObject 返回 (对象, 错误说明)。
/// 空内容解为 null 而非 {}：Group 的 config 缺省与 config={} 在服务端是两种语义。
(Map<String, dynamic>?, String?) parseJsonObject(String raw) {
  final text = raw.trim();
  if (text.isEmpty) return (null, null);
  try {
    final decoded = jsonDecode(text);
    if (decoded is! Map<String, dynamic>) {
      return (null, '必须是 JSON 对象，例如 {"weight":3}');
    }
    return (decoded, null);
  } on FormatException catch (e) {
    return (null, 'JSON 格式错误：${e.message}');
  }
}

String prettyJson(Map<String, dynamic>? value) =>
    value == null || value.isEmpty
        ? ''
        : const JsonEncoder.withIndent('  ').convert(value);

class JsonField extends StatefulWidget {
  const JsonField({
    super.key,
    required this.label,
    required this.helper,
    required this.controller,
    required this.onValidityChanged,
    this.minLines = 3,
    this.maxLines = 6,
  });

  final String label;
  final String helper;
  final TextEditingController controller;
  final ValueChanged<bool> onValidityChanged;
  final int minLines;
  final int maxLines;

  @override
  State<JsonField> createState() => _JsonFieldState();
}

class _JsonFieldState extends State<JsonField> {
  String? _error;

  @override
  void initState() {
    super.initState();
    widget.controller.addListener(_validate);
    _validate();
  }

  @override
  void dispose() {
    widget.controller.removeListener(_validate);
    super.dispose();
  }

  void _validate() {
    final (_, error) = parseJsonObject(widget.controller.text);
    if (error != _error) {
      setState(() => _error = error);
      widget.onValidityChanged(error == null);
    }
  }

  @override
  Widget build(BuildContext context) {
    return TextField(
      controller: widget.controller,
      minLines: widget.minLines,
      maxLines: widget.maxLines,
      style: const TextStyle(fontFamily: 'Consolas'),
      decoration: InputDecoration(
        labelText: widget.label,
        helperText: widget.helper,
        errorText: _error,
        border: const OutlineInputBorder(),
        alignLabelWithHint: true,
      ),
    );
  }
}
