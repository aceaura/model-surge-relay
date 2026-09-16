/// 策略源码编辑区：等宽多行输入 + 服务端编译错误条。
///
/// 不做语法高亮：编译错误由服务端裁定并带行列位置回显，本地高亮既无法
/// 判断策略语义，也会与服务端的判断口径不一致。
library;

import 'package:flutter/material.dart';

class CodeEditor extends StatelessWidget {
  const CodeEditor({
    super.key,
    required this.controller,
    this.error,
    this.label = '源码',
  });

  final TextEditingController controller;

  /// error 是服务端返回的编译或运行错误。出现时源码保持不动——
  /// 编译错误是编写策略的常态，清掉输入会让人抓狂。
  final String? error;
  final String label;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Expanded(
          child: TextField(
            controller: controller,
            expands: true,
            maxLines: null,
            style: const TextStyle(fontFamily: 'Consolas', fontSize: 13),
            decoration: InputDecoration(
              labelText: label,
              border: const OutlineInputBorder(),
              alignLabelWithHint: true,
            ),
          ),
        ),
        if (error != null) ...[
          const SizedBox(height: 8),
          Container(
            width: double.infinity,
            padding: const EdgeInsets.all(12),
            decoration: BoxDecoration(
              color: scheme.errorContainer,
              borderRadius: BorderRadius.circular(8),
            ),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(Icons.report_outlined, size: 18, color: scheme.onErrorContainer),
                const SizedBox(width: 8),
                Expanded(
                  child: SelectableText(
                    error!,
                    style: TextStyle(
                      fontFamily: 'Consolas',
                      fontSize: 12,
                      color: scheme.onErrorContainer,
                    ),
                  ),
                ),
              ],
            ),
          ),
        ],
      ],
    );
  }
}

/// policyInputReference 是策略输入结构与返回值形态的就地速查，
/// 让编写者不必切到 README。
const policyInputReference = '''
input（Lua 为全局变量，JS/TS 为函数参数）

request
  user_model         对外模型名
  inbound_protocol   anthropic | chat_completions | responses | gemini
  est_tokens         预估输入 token
  tried_ids          本次请求已试过的目标，应当排除
  request_id         请求标识

collection
  name
  groups[]           已按 position 排好序
    name / type / position / config
    members[]
      model_id       形如 kimi-1/k3
      account / provider_id / protocol / native_model
      context_window
      enabled        上游是否启用
      known          目录中是否仍存在
      position

runtime[model_id]
  cooling / cooling_until
  consecutive_failures
  input_tokens / output_tokens / request_count

返回值（两种形态均可）
  return { "kimi-1/k3", "ark-2/doubao" }
  return { candidates = {...}, note = "为什么这么排" }

沙箱：无 io / os / package / debug，无网络与文件能力。
服务端会再过滤一遍候选（不在集合内 / 已试过 / 不存在 / 已禁用 / 冷却中），
但那是兜底，正确的冷却判断仍应写在脚本里。
''';
