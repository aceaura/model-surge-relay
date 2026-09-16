import 'package:flutter/material.dart';
import 'package:window_manager/window_manager.dart';

import 'api_client.dart';
import 'pages/collections_page.dart';
import 'pages/policies_page.dart';
import 'pages/runtime_page.dart';
import 'pages/settings_page.dart';
import 'pages/user_models_page.dart';
import 'settings_store.dart';

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
      theme: ThemeData(colorSchemeSeed: Colors.teal, useMaterial3: true),
      home: const AdminShell(),
    );
  }
}

/// AdminShell 只做三件事：读本机配置、按配置构造客户端、把客户端注入各页。
/// 配置不完整时渲染不可跳过的设置页，因此各页可以假定客户端已配置完整。
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
  int _tab = 0;

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

  void _openSettings() {
    final current = _settings ?? Settings.empty;
    Navigator.of(context).push(MaterialPageRoute(
      builder: (_) => SettingsPage(initial: current, onSaved: _apply),
    ));
  }

  @override
  void dispose() {
    _client?.close();
    super.dispose();
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

    final client = _client!;
    return Scaffold(
      appBar: AppBar(
        title: const Text('ModelSurge Relay 调度管理'),
        actions: [
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 8),
            child: Center(
              child: Text('${settings.baseUrl}  key ${mask(settings.adminKey)}'),
            ),
          ),
          IconButton(
            tooltip: '连接设置',
            icon: const Icon(Icons.settings),
            onPressed: _openSettings,
          ),
        ],
      ),
      body: Row(
        children: [
          NavigationRail(
            selectedIndex: _tab,
            labelType: NavigationRailLabelType.all,
            onDestinationSelected: (i) => setState(() => _tab = i),
            destinations: const [
              NavigationRailDestination(
                icon: Icon(Icons.layers_outlined),
                selectedIcon: Icon(Icons.layers),
                label: Text('集合'),
              ),
              NavigationRailDestination(
                icon: Icon(Icons.code_outlined),
                selectedIcon: Icon(Icons.code),
                label: Text('策略'),
              ),
              NavigationRailDestination(
                icon: Icon(Icons.badge_outlined),
                selectedIcon: Icon(Icons.badge),
                label: Text('模型名'),
              ),
              NavigationRailDestination(
                icon: Icon(Icons.monitor_heart_outlined),
                selectedIcon: Icon(Icons.monitor_heart),
                label: Text('运行态'),
              ),
            ],
          ),
          const VerticalDivider(width: 1),
          Expanded(
            child: switch (_tab) {
              1 => PoliciesPage(client: client, onOpenSettings: _openSettings),
              2 => UserModelsPage(client: client, onOpenSettings: _openSettings),
              3 => RuntimePage(client: client, onOpenSettings: _openSettings),
              _ => CollectionsPage(client: client, onOpenSettings: _openSettings),
            },
          ),
        ],
      ),
    );
  }
}
