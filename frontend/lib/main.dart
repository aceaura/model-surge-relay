import 'package:flutter/material.dart';
import 'package:window_manager/window_manager.dart';

import 'api_client.dart';
import 'pages/collections_page.dart';
import 'pages/runtime_page.dart';
import 'pages/settings_page.dart';
import 'pages/user_models_page.dart';
import 'settings_store.dart';
import 'theme.dart';

/// 应用版本号（侧栏展示；发版时与 pubspec version 同步）。
const kAppVersion = '1.0.0';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await windowManager.ensureInitialized();
  await windowManager.waitUntilReadyToShow(
    const WindowOptions(
      size: Size(1320, 820),
      minimumSize: Size(1060, 680),
      title: 'ModelSurge Relay 调度管理',
      titleBarStyle: TitleBarStyle.normal,
    ),
    () async {
      await windowManager.show();
      await windowManager.focus();
    },
  );
  runApp(const AdminApp());
}

class AdminApp extends StatelessWidget {
  const AdminApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'ModelSurge Relay 调度管理',
      debugShowCheckedModeBanner: false,
      theme: buildAppTheme(),
      darkTheme: buildAppDarkTheme(),
      home: const AdminShell(),
    );
  }
}

/// AdminShell 只做三件事：读本机配置、按配置构造客户端、把客户端注入各页。
/// 配置不完整时渲染不可跳过的设置页，因此各页可以假定客户端已配置完整。
/// 布局同峰神管理版：左侧边栏（品牌头 + 分组导航 + 连接状态），右侧内容区。
class AdminShell extends StatefulWidget {
  const AdminShell({super.key, this.store});

  final SettingsStore? store;

  @override
  State<AdminShell> createState() => _AdminShellState();
}

class _AdminShellState extends State<AdminShell> {
  late final SettingsStore _store = widget.store ?? SettingsStore();

  Settings? _settings;
  ApiClient? _client;
  String _page = 'collections';

  @override
  void initState() {
    super.initState();
    _restore();
  }

  Future<void> _restore() async {
    final settings = await _store.load();
    if (!mounted) return;
    setState(() {
      _settings = settings;
      _client = settings.complete ? _clientFor(settings) : null;
    });
  }

  ApiClient _clientFor(Settings s) =>
      ApiClient(baseUrl: s.baseUrl, adminKey: s.adminKey);

  Future<void> _apply(Settings s) async {
    await _store.save(s);
    if (!mounted) return;
    setState(() {
      _client?.close();
      _settings = s;
      _client = _clientFor(s);
    });
  }

  void _openSettings() => setState(() => _page = 'settings');

  @override
  void dispose() {
    _client?.close();
    super.dispose();
  }

  List<_NavGroup> get _groups {
    final client = _client!;
    return [
      _NavGroup('调度', [
        _NavItem('collections', Icons.layers_outlined, '集合',
            () => CollectionsPage(client: client, onOpenSettings: _openSettings)),
        _NavItem('models', Icons.badge_outlined, '模型名',
            () => UserModelsPage(client: client, onOpenSettings: _openSettings)),
        _NavItem('runtime', Icons.monitor_heart_outlined, '运行态',
            () => RuntimePage(client: client, onOpenSettings: _openSettings)),
      ]),
      _NavGroup('系统', [
        _NavItem(
          'settings',
          Icons.settings_outlined,
          '连接设置',
          () => SettingsPage(
            initial: _settings ?? Settings.empty,
            onSaved: _apply,
            embedded: true,
          ),
        ),
      ]),
    ];
  }

  @override
  Widget build(BuildContext context) {
    final settings = _settings;
    if (settings == null) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    if (!settings.complete || _client == null) {
      return SettingsPage(
        initial: settings,
        onSaved: _apply,
        dismissible: false,
      );
    }

    final groups = _groups;
    final all = [for (final g in groups) ...g.items];
    final cur = all.firstWhere((i) => i.id == _page, orElse: () => all[0]);
    return Scaffold(
      body: Row(
        children: [
          _sidebar(context.tokens, groups, cur.id, settings),
          Expanded(child: SafeArea(child: cur.build())),
        ],
      ),
    );
  }

  Widget _sidebar(
    AppTokens t,
    List<_NavGroup> groups,
    String currentId,
    Settings settings,
  ) {
    return Container(
      width: 236,
      decoration: BoxDecoration(
        color: t.surface,
        border: Border(right: BorderSide(color: t.border)),
      ),
      padding: const EdgeInsets.fromLTRB(12, 18, 12, 14),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(10, 2, 10, 16),
            child: Row(
              children: [
                Container(
                  width: 36,
                  height: 36,
                  decoration: BoxDecoration(
                    borderRadius: BorderRadius.circular(10),
                    gradient: LinearGradient(
                      begin: Alignment.topLeft,
                      end: Alignment.bottomRight,
                      colors: [t.brandA, t.brandB],
                    ),
                  ),
                  child: const Icon(Icons.alt_route,
                      size: 20, color: Colors.white),
                ),
                const SizedBox(width: 11),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        'ModelSurge Relay',
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(
                          fontSize: 15,
                          fontWeight: FontWeight.w700,
                          color: t.ink,
                        ),
                      ),
                      Text(
                        '调度管理 v$kAppVersion',
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(fontSize: 11, color: t.faint),
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
          Expanded(
            child: ListView(
              padding: EdgeInsets.zero,
              children: [
                for (final g in groups) ...[
                  if (g.section.isNotEmpty) _SectionLabel(g.section),
                  for (final it in g.items) _navItem(t, it, currentId == it.id),
                  const SizedBox(height: 10),
                ],
              ],
            ),
          ),
          // 底部连接状态卡：取代原 AppBar 的地址+密钥摘要，点击直达连接设置。
          InkWell(
            borderRadius: BorderRadius.circular(10),
            onTap: _openSettings,
            child: Container(
              width: double.infinity,
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
              decoration: BoxDecoration(
                borderRadius: BorderRadius.circular(10),
                border: Border.all(color: t.border),
              ),
              child: Row(
                children: [
                  Icon(Icons.dns_outlined, size: 16, color: t.faint),
                  const SizedBox(width: 9),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          settings.baseUrl,
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(
                            fontSize: 12,
                            fontWeight: FontWeight.w600,
                            color: t.ink,
                          ),
                        ),
                        Text(
                          'key ${mask(settings.adminKey)}',
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(fontSize: 11, color: t.faint),
                        ),
                      ],
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

  Widget _navItem(AppTokens t, _NavItem item, bool on) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 1),
      child: InkWell(
        borderRadius: BorderRadius.circular(8),
        onTap: () => setState(() => _page = item.id),
        child: Container(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
          decoration: BoxDecoration(
            color: on ? t.primarySoft : Colors.transparent,
            borderRadius: BorderRadius.circular(8),
            border:
                on ? Border(left: BorderSide(color: t.primary, width: 3)) : null,
          ),
          child: Row(
            children: [
              Icon(item.icon, size: 17, color: on ? t.primaryInk : t.faint),
              const SizedBox(width: 11),
              Text(
                item.label,
                style: TextStyle(
                  fontSize: 13.5,
                  fontWeight: on ? FontWeight.w600 : FontWeight.w500,
                  color: on ? t.primaryInk : t.dim,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _NavGroup {
  final String section;
  final List<_NavItem> items;
  const _NavGroup(this.section, this.items);
}

class _NavItem {
  final String id;
  final IconData icon;
  final String label;
  final Widget Function() build;
  const _NavItem(this.id, this.icon, this.label, this.build);
}

/// 侧栏节标题（峰神管理版同款）：主色指示条 + 加粗墨色 + 延伸分隔线。
class _SectionLabel extends StatelessWidget {
  final String text;
  const _SectionLabel(this.text);

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 14, 12, 8),
      child: Row(
        children: [
          Container(
            width: 3,
            height: 13,
            decoration: BoxDecoration(
              color: t.primary,
              borderRadius: BorderRadius.circular(2),
            ),
          ),
          const SizedBox(width: 7),
          Text(
            text,
            style: TextStyle(
              fontSize: 12.5,
              fontWeight: FontWeight.w700,
              letterSpacing: 1.5,
              color: t.ink,
            ),
          ),
          const SizedBox(width: 10),
          Expanded(child: Container(height: 1, color: t.border)),
        ],
      ),
    );
  }
}
