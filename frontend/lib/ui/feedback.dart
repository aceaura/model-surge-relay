/// 错误展示与进行中状态的共用件。三类失败在此统一成可操作提示。
library;

import 'package:flutter/material.dart';

import '../api_client.dart';

/// describeError 把异常转成面向运维者的文案。
/// 带 field 的校验错标出字段名，省去运维者在表单上逐项猜。
String describeError(Object error) => switch (error) {
      UnreachableException e => '${e.message}\n请确认服务已启动、地址可达。',
      UnauthorizedException _ => '管理密钥无效，请到设置页更新。',
      ValidationException e =>
        e.field == null || e.field!.isEmpty ? e.message : '${e.field}：${e.message}',
      _ => '$error',
    };

/// needsSettings 判断该错误是否应引导运维者去设置页。
bool needsSettings(Object error) =>
    error is UnauthorizedException || error is UnreachableException;

class ErrorPanel extends StatelessWidget {
  const ErrorPanel({
    super.key,
    required this.error,
    this.onRetry,
    this.onOpenSettings,
  });

  final Object error;
  final VoidCallback? onRetry;
  final VoidCallback? onOpenSettings;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 520),
        child: Card(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Icon(Icons.error_outline,
                        color: Theme.of(context).colorScheme.error),
                    const SizedBox(width: 8),
                    const Text('请求失败'),
                  ],
                ),
                const SizedBox(height: 12),
                SelectableText(describeError(error)),
                const SizedBox(height: 16),
                Row(
                  children: [
                    if (onRetry != null)
                      FilledButton(onPressed: onRetry, child: const Text('重试')),
                    if (onRetry != null && onOpenSettings != null)
                      const SizedBox(width: 8),
                    if (onOpenSettings != null && needsSettings(error))
                      OutlinedButton(
                        onPressed: onOpenSettings,
                        child: const Text('打开设置'),
                      ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// showError 用于提交类操作的失败提示（列表已有内容，不该整页替换）。
void showError(BuildContext context, Object error) {
  ScaffoldMessenger.of(context).showSnackBar(
    SnackBar(content: Text(describeError(error))),
  );
}

void showInfo(BuildContext context, String message) {
  ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(message)));
}

/// BusyButton 在请求进行中禁用自身并显示进度，避免重复提交。
class BusyButton extends StatelessWidget {
  const BusyButton({
    super.key,
    required this.busy,
    required this.onPressed,
    required this.child,
  });

  final bool busy;
  final VoidCallback? onPressed;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return FilledButton(
      onPressed: busy ? null : onPressed,
      child: busy
          ? const SizedBox(
              width: 16,
              height: 16,
              child: CircularProgressIndicator(strokeWidth: 2),
            )
          : child,
    );
  }
}

/// confirm 是删除类操作的二次确认。返回 true 表示运维者确认执行。
Future<bool> confirm(
  BuildContext context, {
  required String title,
  required String message,
  String confirmLabel = '删除',
}) async {
  final ok = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: Text(title),
      content: Text(message),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(ctx).pop(false),
          child: const Text('取消'),
        ),
        FilledButton(
          onPressed: () => Navigator.of(ctx).pop(true),
          style: FilledButton.styleFrom(
            backgroundColor: Theme.of(ctx).colorScheme.error,
          ),
          child: Text(confirmLabel),
        ),
      ],
    ),
  );
  return ok ?? false;
}
