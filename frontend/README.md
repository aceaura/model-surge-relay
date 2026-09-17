# msr_admin

model-surge-relay 的管理面 UI：Flutter Windows 桌面应用，对接 backend 的 `/admin/*`。

## 它做什么

四个页面对应 backend 管理面的四组能力：

- **集合** — Collection / Group / 成员三栏主从编排，Group 与成员均可拖拽排序
- **策略** — 策略列表、源码编辑、试运行
- **模型名** — user model 的 Collection 与策略绑定
- **运行态** — 冷却与用量查看、按目标清除

它是纯客户端：除本机连接配置外没有自有存储，除表单校验外没有业务逻辑。成员引用是否存在、策略能否编译、策略是否被引用，全部交由 backend 裁决并回显其错误。

## 运行

```bash
flutter pub get
flutter run -d windows
```

首次启动会要求填 relay 服务地址与 `MSR_ADMIN_KEY`，保存后持久化到本机，之后直接进主界面。密钥以 `Authorization: Bearer` 发出，与调度面同一个头、不同的密钥，注意填的是**管理密钥**而不是调度密钥——两把密钥在服务端挂在不同的路由子树上，用错会一直 401。

想跳过首次配置，可以预写 `%APPDATA%\com.aceaura\msr_admin\shared_preferences.json`（键带 `flutter.` 前缀）：

```json
{
  "flutter.msr.base_url": "http://127.0.0.1:8081",
  "flutter.msr.admin_key": "msr-admin-dev-2026"
}
```

## 测试

```bash
flutter analyze
flutter test
```

Widget 测试用 `http` 的 `MockClient`，不需要起 backend。策略编辑页的测试显式把画布设成 1400×900：该页面是为桌面窗口排版的，测试默认的 800×600 会把两栏布局挤到溢出。

自动化测试覆盖的是代码正确性。界面正确性只能手动确认——起真 backend，走一遍建集合 → 建组 → 加成员 → 写策略 → 试运行 → 建模型名 → 看运行态。

## 几处与直觉不同的地方

- **成员编排是整组替换**。移除、重排、添加在点保存前都只是本地状态，不发请求。界面上用"有未保存改动"标出这一点。
- **`known` 与 `enabled` 是两回事**。前者为假表示该引用在 upstream 目录里已经不存在了，是配置错误；后者为假是上游临时禁用，调度会正常跳过。两者用不同样式标注，混为一谈会让人误删有效配置。
- **编辑 user model 时密钥留空就是存空密钥**。backend 的 update 是全量覆盖，没有"留空则保留"的语义，要保留原密钥必须重新填一遍。
- **策略保存失败不清源码**。编译错误是写策略的常态，服务端返回的行列位置显示在编辑区下方。
- **试运行不碰真实运行态**。它读真实集合快照，但运行态由对话框里给定，因此可以安全地在生产实例上试。
