# Mouse Bridge

为 AI Agent 提供对 **Windows 真实鼠标** 的控制能力：人性化轨迹移动、真实左键/右键点击、
拖拽、滚轮。架构完全对齐 [Kimi WebBridge]（守护进程 + Chrome 扩展 + Agent HTTP 接口），
用于扩展其能力边界——浏览器合成事件（`el.click()`，`isTrusted=false`）会被反自动化检测
识别，而本工具驱动的是**真实的 OS 光标**，页面收到的是 `isTrusted=true` 的原生输入。

## 架构

```
AI Agent ──HTTP POST /command──▶ 守护进程 (Go, 127.0.0.1:10087)
                                    │  ▲
                          WebSocket │  │ 定位元素 / 查询鼠标位置
                                    ▼  │
                    Chrome 扩展 (MV3) —— content script: 元素定位、
                    getBoundingClientRect、mousemove 反馈
                                    │
                                    ▼
            SendInput：拟人轨迹移动 → 按下/抬起（真实光标）
```

- **守护进程** `bin/mouse-bridge.exe`（Go + gorilla/websocket，单文件）
  - Per-Monitor-V2 DPI 感知：全程物理像素坐标（125%/150% 缩放实测精确）
  - 坐标换算：**测量式仿射标定（v1.1.0）**——不做第一性原理估算。把真实光标送到页面内
    2-3 个已知物理点，读回页面自己的 `mousemove` clientX/Y，按轴最小二乘拟合
    `css = s·物理 + t`。OS 缩放、浏览器 zoom、标题栏/DWM 隐形边框、窗口位置尺寸全部被
    (s,t) 吸收，无一估算量；实测斜率与 1/devicePixelRatio 交叉校验（防探错窗口）。
    落点残差实测 0.1~0.6 css px（旧闭环方案地板 >1.5px）
  - 严格读回协议：晃动强制产生新鲜事件 → 以页面事件计数器确认到达（不信超时）→ 读回；
    拟合按窗口几何键控（窗口 id|zoom|dpr|位置尺寸），任何变化自动重拟合；每次操作都从
    拟合参数重新预测，残差不跨操作累计
  - 人性化轨迹引擎（运动控制学模型，非解析曲线）：2-3 段子动作（弹道甩动 +
    修正归位，运动控制研究中的标准双段结构）；每段 minimum-jerk 速度曲线
    `s(u)=10u³−15u⁴+6u⁵`（Flash & Hogan 1985，人类伸手动作实测曲线）；低频路径
    游移 + AR(1) 生理性震颤（非平滑解析曲线）；Fitts 定时 `150+130·log2(1+D/100)`ms
    ±随机；不均匀 5-13ms 采样、微停顿、长距离过冲回修
  - 前台保障：`AttachThreadInput` 强制前置浏览器窗口，按窗口矩形+标签页标题
    匹配窗口（多浏览器重叠窗口场景实测可靠），失败则明确报错不盲点
- **扩展** `extension/`（MV3：service worker + content script）
  - 选择器支持：CSS / XPath / `text=文本`
  - 定位：scrollIntoView 居中 → 九点候选 + `elementFromPoint` 遮挡校验
  - WS 自动重连（指数退避 + alarms 保活，Chrome 116+ WS 保活 SW）

## 安装（新电脑）

前置：Windows 10/11 x64；Chrome 或 Edge 116+。方式 A 不需要安装 Go（仓库内含成品）。

### 方式 A：一键安装（推荐，使用仓库内成品）

```powershell
git clone https://github.com/GG22G2/mouse-bridge.git
cd mouse-bridge
powershell -ExecutionPolicy Bypass -File install.ps1
```

脚本做三件事：把 `bin\mouse-bridge.exe`（成品）与 `extension\` 复制到
`%USERPROFILE%\.mouse-bridge\`、启动守护进程（127.0.0.1:10087）、打印浏览器扩展的
加载步骤。

### 方式 B：源码构建

需要 Go 1.26+：

```bash
go build -o bin/mouse-bridge.exe ./cmd/mouse-bridge
powershell -ExecutionPolicy Bypass -File install.ps1 -Build   # 先构建再安装
```

### 方式 C：手动安装

1. 守护进程：复制 `bin/mouse-bridge.exe` 到 `C:\Users\<you>\.mouse-bridge\bin\`，执行 `start`
2. 扩展（一次性，Chrome/Edge 137+ 已封锁所有静默安装通道，开发模式加载是唯一正规途径）：
   - Edge：`edge://extensions` → 开发人员模式 → 加载解压缩的扩展
   - Chrome：`chrome://extensions` → 开发者模式 → 加载已解压的扩展程序
   - 选择 `C:\Users\<you>\.mouse-bridge\extension`（解包扩展 ID 由路径派生，各浏览器一致）
   - `curl http://127.0.0.1:10087/status` 确认 `"extension_connected":true`

### 更新到新版

`git pull` 后重跑 `install.ps1`（自动停旧 daemon → 覆盖文件 → 重启）。若 `extension/`
有变动，还需在浏览器扩展页点击 Mouse Bridge 卡片上的刷新（↻）图标并刷新目标页面。
打包 CRX（可选，需保持 `extension.pem` 不变以维持扩展 ID）：

```
chrome.exe --pack-extension="...\.mouse-bridge\extension" --pack-extension-key="...\.mouse-bridge\extension.pem"
```

## 使用

### 扩展面板（无需 AI，日常手动使用）

点击浏览器工具栏的 Mouse Bridge 图标弹出面板，面板会**自动切换成浏览器侧栏**
（Side Panel，整个右侧区域、不会因失焦而关闭）。浏览器的手势限制可能拦住
自动切换（Edge 实测：`sidePanel.open() may only be called in response to a
user gesture`），此时面板里会出现「在侧栏中打开」按钮，点一下即可。侧栏
还有两个原生入口：扩展菜单（拼图图标）里的「在侧边栏中打开」、工具栏图标
右键菜单的「打开边栏」（Edge）。

面板功能：

- **连接状态**：守护进程运行/版本、扩展 WS 连接状态；守护进程未运行时提供
  「启动守护进程」按钮；
- **页面右下角实时坐标**（开关）：开启后每个页面右下角显示 `css(x,y) dev(x,y)`
  双坐标（与测试页同款），移动鼠标即刷新，供你读取任意位置的页面坐标；
- **移动表单**：把读到的 X/Y 填进去点「移动」，守护进程按页面视口 css 坐标
  换算（走同一套仿射标定）并以拟人轨迹移动真实光标，面板回显页面确认的
  落点残差。

### 浏览器自动唤醒守护进程（v1.2.1）

不依赖登录自启：扩展检测到 WS 连不上时，通过 **Native Messaging**
（`com.mousebridge.daemon`，install.ps1 注册到 Edge/Chrome 的 HKCU 注册表）
让浏览器拉起原生宿主，宿主检查 10087 端口——已运行则直接复用，未运行则
分离式启动守护进程。防重复机制：wake 先查 `/status` + 端口绑定本身即单实例锁
（第二个实例绑定失败自动退出）。实测：杀掉守护进程后约 12 秒浏览器自动将其
唤醒；重复 wake 返回 `already_running:true`，pid 不变。

### 命令

```bash
# 元素级（推荐，自动处理 DPI/zoom/前台/校准）
curl -X POST http://127.0.0.1:10087/command -d '{"action":"click_element","args":{"selector":"#submit","button":"left"}}'
curl -X POST http://127.0.0.1:10087/command -d '{"action":"drag_element","args":{"from_selector":"#src","to_selector":"#dst"}}'
curl -X POST http://127.0.0.1:10087/command -d '{"action":"click_element","args":{"selector":"text=立即购买","button":"right"}}'

# 页面视口坐标移动（扩展面板「移动」按钮使用的就是它）
curl -X POST http://127.0.0.1:10087/command -d '{"action":"move_css","args":{"x":500,"y":300}}'

# 坐标级（物理像素，不经扩展）
curl -X POST http://127.0.0.1:10087/command -d '{"action":"move","args":{"x":960,"y":540}}'
curl -X POST http://127.0.0.1:10087/command -d '{"action":"drag","args":{"x":100,"y":200,"to_x":800,"to_y":600}}'
curl -X POST http://127.0.0.1:10087/command -d '{"action":"wheel","args":{"dy":-3}}'
```

与 Kimi WebBridge 组合（AI 标准工作流）：
WebBridge `navigate`/`snapshot` 找到元素并生成选择器 → Mouse Bridge `click_element`
真实点击 → WebBridge `evaluate` 验证页面反应。

## 测试

内建测试页：`http://127.0.0.1:10087/test`（事件记录、isTrusted 断言、拖放、画布轨迹、
深处目标、悬停验证）。

```bash
node tools/run-tests.mjs <CDP端口>   # 52 项断言：见下
```

**本机实测（Windows 11 / Chrome 152 / DPI 125%）**：52/52 通过 —
真实移动触发 CSS :hover；左右键点击/拖放全部 `isTrusted=true` 且落点 ±3px；
14×14px 精准目标命中；视口外元素自动滚动定位；画布按住轨迹 ≥40 连续点；
浏览器 zoom 150% 换算精确；事件 `screenX×1.25` 与守护进程物理坐标一致；
PowerShell 独立读数（768,432）= 守护进程坐标（960,540）÷1.25；
自定义窗口 1100×800 / 最大化 / 1200×850 三种几何均自动重校准且落点达标；
每次点击前"微移探针"验证落点在可交互页面上，被浏览器 UI（恢复气泡/信息栏）遮挡时
拒绝盲点并报错（`allow_blind` 覆盖）；布局在定位后发生变化时自动重定位重试一次。

**v1.1.0 测量式仿射标定（Windows 11 / Edge / DPI 125% 实测）**：拟合残差 0.46px；
真实点击 14×14px 目标落点残差 0.40px（页面确认命中）；浏览器 zoom 1→1.5 触发键变化
自动重拟合（实测斜率 0.5335 vs 理论 0.5333），落点残差 0.16px 并真实点击确认。

## 故障排查

- **报错 "landing point not verified / cursor is not over the page viewport"**：
  落点反馈（页面真实 mousemove 回读）需要浏览器窗口**未被遮挡且在前台**。Chrome 对被
  遮挡/后台窗口会节流渲染与输入回读，事件可能延迟数秒才到达（实测）。先关掉压在浏览器
  上的其他窗口（含残留的系统对话框，如文件选择器），再重试；确认不可交互时可显式传
  `allow_blind:true`（不推荐）。可用 `measure` 动作回读反馈状态（含 `events_seen`/
  `age_ms`/`href`）诊断。
- **仿射拟合按窗口几何键控**（窗口 id|zoom|dpr|位置尺寸），几何变化后首击自动重新拟合
  （~0.7s，期间真实光标会扫过页面上的 3 个探针点）。`calibrate` 动作强制立即重拟合并
  返回参数；`get_calibration` 列出全部缓存拟合；`reset_calibration` 清空。

## 关键兼容性结论（Chrome 137+ / 152 实测）

| 安装方式 | 结果 |
|---|---|
| `--load-extension` | 品牌版 Chrome 已移除（对非默认 profile 亦然）；**Chrome for Testing 可用** |
| 注册表 + 本地 CRX | 可安装但被标记 `suspiciousInstall` 禁用，用户侧无法启用 |
| `ExtensionInstallForcelist` 策略 | 非企业管理机器直接 `[BLOCKED]`（仅允许商店扩展） |
| 开发者模式加载已解压扩展 | ✅ 唯一正规途径（个人机器） |

## 目录

```
cmd/mouse-bridge/     守护进程入口（start/run/stop/status/restart）
cmd/nativehost/       Native Messaging 宿主（浏览器唤醒守护进程，防重复）
internal/win/         Win32 API（SendInput、DPI、窗口管理）
internal/mouse/       人性化轨迹引擎
internal/server/      HTTP/WS、坐标换算、校准、编排、测试页
extension/            MV3 扩展（manifest/background.js/content.js/panel 侧栏/popup 外壳/icons）
extension.pem         CRX 打包签名密钥（保持不变以维持扩展 ID）
bin/mouse-bridge.exe  成品守护进程（随仓库分发，install.ps1 直接使用）
extension.crx         打包好的 CRX（配合 pem，ID 恒定）
install.ps1           新电脑一键安装（复制成品+扩展、启动 daemon）
tools/run-tests.mjs   全链路测试套件（52 断言）
tools/cdp.mjs 等      调试辅助（CDP/服务Worker 求值、启用扩展、图标生成）
```
