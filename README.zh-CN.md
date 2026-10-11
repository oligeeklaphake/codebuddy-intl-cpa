# CodeBuddy Intl CPA 简体中文版

简体中文 | [English](README.md)

面向腾讯 **CodeBuddy 国际版（Global / Intl）账号**的 CLIProxyAPI（CPA）插件，提供积分管理、多账号登录、自动签到、国际账号每日奖励和用量记录。

当前源码的账号管理面板已汉化，包括按钮、地区筛选、积分统计、请求记录、凭据导入、操作通知和管理密钥提示。产品名、模型名和配置字段保留原名；上游返回的错误详情按原文显示，便于排查。

面板右上角可选择 **中文 / English**，切换立即生效，并保存在当前浏览器中。首次打开时根据浏览器语言选择。

## 使用汉化版本

管理面板通过 Go 的 `go:embed` 编译进插件。**需要用当前源码重新编译、替换插件并重启 CPA，才能显示中文界面**。只修改 HTML 不会改变已安装的插件；插件商店已有的发行包也不会随本地修改自动更新。

编译环境需要 Go 1.26 及相应平台的 C 编译器，并开启 CGO。

### Linux / macOS

```bash
cd workbuddy
CGO_ENABLED=1 go build -buildmode=c-shared -o workbuddy.so .
```

将生成的 `workbuddy.so` 放入 CPA 的插件目录，重启 CPA 后从管理中心打开 CodeBuddy 面板。如果仍显示旧界面，可强制刷新浏览器。

### Windows（PowerShell）

使用与 Go 架构匹配的 MinGW C 编译器，并确保 `go` 和 `gcc` 已加入 PATH：

```powershell
cd workbuddy
$env:CGO_ENABLED = '1'
go build -buildmode=c-shared -o workbuddy.dll .
```

将生成的 `workbuddy.dll` 放入 CPA 的插件目录，重启 CPA 后打开 CodeBuddy 面板。共享库需要与 CPA 的运行平台和架构匹配。

如尚未启用插件，在 CPA 配置中添加：

```yaml
plugins:
  enabled: true
```

面板也可通过 CPA 服务下的 `/v0/management/plugins/workbuddy/panel` 路径访问。独立打开时输入 CPA 的**管理密钥**；从 CPA 主面板打开时会自动获取密钥。

## 主要功能

### 国际账号每日奖励

插件向成长中心上报活跃事件，点亮当天的活跃记录。按项目已有验证，国际账号可在次日获得 **30 积分奖励包**，有效期 30 天。

- 默认开启 `daily_bonus`，在服务所在环境的本地时间 `08:00 / 12:00 / 16:00 / 20:00` 运行。
- 每次先读取活跃热力图，已点亮的日期会跳过，避免重复上报。
- 日期以接口热力图为准；奖励次日到账，不是点击后立即到账。
- 可在面板的“每日奖励（国际）”开关中启用或关闭。
- 手动触发接口：`POST /v0/management/plugins/workbuddy/dailybonus`。
- 状态查询接口：`GET /v0/management/plugins/workbuddy/dailybonus/status`。

国内账号使用每日签到流程。国际账号的“领取专家体验包”是另一项一次性权益：**250 积分，有效期 14 天，每个账号限领一次**。

### 多账号切换与限流重试

遇到上游 `429` 或 `code 6004` 时，插件会记录账号冷却时间，并尝试使用其他已启用账号发送同一请求。面板可选择当前使用的账号。

### 请求用量记录

记录账号、模型、输入/输出/缓存 Token 和上游返回的积分消耗值。数据以 JSONL 保存在认证目录附近，文件名为 `request-usage-<YYYY-MM>.jsonl`，约 50 MB 时轮转。

面板支持今天、昨天、近 7 天、自定义日期和账号筛选。表格最多显示当前查询的 100 条记录；底部统计针对已显示记录，顶部汇总使用接口返回的总计。日志不记录账号凭据。

### 模型与国际账号路由

插件提供动态模型列表和额外模型注册，并识别 `workbuddy.ai`、`codebuddy.ai` 两类国际账号域名，以选择对应的聊天和计费服务。具体模型以当前源码和上游实际可用情况为准。

### 积分生命周期管理

默认启用 `lifecycle_auto`：国内账号积分耗尽后自动禁用，签到恢复积分后可重新启用；国际账号积分耗尽后自动删除。可在插件配置中调整。

## 面板操作

| 操作 | 作用 |
| --- | --- |
| 导入凭据 | 粘贴嵌套或扁平结构的账号 JSON 并保存 |
| 刷新数据 | 重新获取全部账号的数据 |
| 全部签到 | 对需要签到的国内账号执行签到 |
| 全部领取 | 对尚未领取体验包的国际账号尝试领取 |
| 自动签到（国内） | 每日 `09:00 / 21:00` 自动签到 |
| 每日奖励（国际） | 定时上报活跃事件，获取次日积分奖励 |
| 选择账号 | 指定当前使用的账号 |
| 刷新 | 刷新单个账号的积分 |

“国内 / 国际 / 已耗尽”筛选会同步更新积分汇总。积分总额度可能随签到奖励增加，已用积分表示实际消耗。

## 配置说明

配置字段名保留英文，在 `plugins.configs.workbuddy` 下设置。

| 字段 | 说明 |
| --- | --- |
| `checkin_auto` | 国内账号自动签到，默认开启 |
| `daily_bonus` | 国际账号每日奖励，默认开启 |
| `lifecycle_auto` | 积分耗尽后的自动禁用/删除，默认开启 |
| `token_keepalive` | 每日 `22:00` 刷新访问令牌，默认开启 |
| `scheduler_mode` | `off` 使用宿主调度；`credits` 优先选择剩余积分最多的账号 |
| `models` | 可选模型列表，可设置 `id`、`name`、`alias`、`context`、`max_tokens`、`enabled`、`reasoning` |
| `usage_report_url` | 可选的 CPAMP 用量导入地址 |
| `usage_report_key` | 可选的 CPAMP 管理密钥 |

当 `scheduler_mode: off` 且 `lifecycle_auto: false` 时，宿主仍可能选中积分耗尽的账号。可开启生命周期管理，或使用 `scheduler_mode: credits`。

## 兼容性与项目标识

- 最低宿主版本：CLIProxyAPI 7.2.30；原项目记录的验证版本为 7.2.145。
- 插件内部标识：`workbuddy`。
- 凭据文件名：`workbuddy-<uid>.json`。
- 配置路径：`plugins.configs.workbuddy`。

这些标识是与宿主和现有凭据约定的名称，汉化不修改它们。

## 安全与致谢

CPA 插件作为原生共享库在宿主进程内运行，拥有宿主可访问的文件、网络和凭据权限。请使用可信源码，并核验发行包的 SHA-256 校验值。

原始执行器和账号提供方实现来自 `Sliverkiss/cpa-plugin`，其基础来自 `lovingfish` 的 `workbuddy`。本项目在此基础上增加了国际账号每日奖励、限流切换、请求用量记录和国际账号域名处理等功能。

许可证：MIT，见 [LICENSE](LICENSE)。更多开发资料见 [开发指南](workbuddy/docs/development.md)。
