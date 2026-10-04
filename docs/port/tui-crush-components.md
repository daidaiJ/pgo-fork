# P0 设计原型：TUI 组件改造（thinking 展示 + 流式 markdown，参考 crush）

> 参考项目：charmbracelet/crush（Go，`D:\CODE\ai\crush` shallow clone，main）。
> pigo 侧现状为本机核实（2026-10-04）。本文先给"抄什么/怎么落"，组件级细节
> 直接读 crush 源码与其 `internal/ui/AGENTS.md`（官方架构文档）。

## 1. 前提：栈完全同源，参考成本极低

pigo TUI（`internal/cli/tui`）与 crush 用同一代 charm 库：
bubbletea v2 + lipgloss v2 + glamour。这意味着 crush 的绝大多数渲染代码可以
近乎直译，不存在框架迁移成本。

## 2. pigo 现状与缺口（A 级，已核实）

| 面 | 现状 | 缺口 |
|---|---|---|
| markdown | glamour 已接入（`internal/cli/tui/markdown.go`），按宽度 memoize `TermRenderer`（`mdCache`），**回合结束才渲染**（doc comment：glamour 只能对完整块排版） | 流式增量渲染缺失：长回答流式期间是纯文本，结束后才"跳变"成 markdown |
| thinking | `agentcore.ThinkingContent` 已是独立消息块（`internal/agentcore/content.go:42`，"Never folded into text"）；provider 层已解析 DeepSeek-R1/Kimi 的 `reasoning_content`/`reasoning` 双字段（`internal/provider/openai.go:69-136`） | **TUI/REPL 均不渲染 thinking 块**（grep 全库无渲染点）——国模 reasoning 模型的思考过程用户完全看不到 |
| 主题 | `internal/cli/tui/theme.go` 自有主题 | 无 thinking 专用样式；终端降级依赖库默认，无工具输出色彩修正 |
| 工具展示 | `toolcard.go` 单卡片组件 | 输出折叠/展开交互缺失 |

## 3. 从 crush 抄什么（按优先级）

### 3.1 thinking 三态视图机（P0，直接对应用户需求）

crush 实现：`internal/ui/chat/assistant.go:695-770` `renderThinking`。

- 三态：`collapsed`（默认，最多 10 行 + "… N lines hidden [space to expand]"）
  → `tail-window`（展开时超 200 行只显尾部 + "N earlier lines hidden"）→ `full`。
- **灰显渲染**：thinking 用暗色调 markdown（`QuietMarkdownRenderer`）+
  `ThinkingBox` 边框样式；块结束后 footer "Thought for 2.3s"。
- **关键细节**：tail 切割发生在 glamour 渲染**之后**（避免把代码块/表格从
  中间切开），从尾部反向扫描找切点。
- pigo 落点：`transcript.go` 消息渲染层新增 thinking 分支；样式进
  `theme.go`（新增 `ThinkingBox`/`ThinkingFooter` 语义组）；数据源已有
  （`ThinkingContent` 块直接进渲染管线）。REPL 侧同样补渲染
  （`internal/cli/repl` 的回合打印，折叠为一行 + 可选全文）。

### 3.2 流式 markdown 稳定前缀缓存（P0）

crush 实现：`internal/ui/chat/streaming_markdown.go`（附完整测试与 bench）。

- `findSafeMarkdownBoundary` 找"稳定前缀"分界（空行后、无未闭合
  fence/list/table/blockquote，fence 计数为偶）；每次流式 flush 只重渲染尾部，
  前缀渲染结果缓存拼接；**任何不确定就整篇重渲**（保守回退保正确性）。
- pigo 落点：`markdown.go` 增加 streaming 入口——流式期间对增量文本跑
  boundary 判定，稳定前缀进缓存；回合结束的现有"整块渲染"路径保留为兜底。
- 配套（P1，可选）：分 section 缓存（thinking/content/error 各自 FNV-64
  哈希 key，`assistantSection`）与 thinking 增量哈希——thinking 与正文互不失效。

### 3.3 兼容性三件（P1）

- **RemapANSI16**（crush `internal/ui/common/ansi16.go`）：把工具输出的裸
  ANSI 16 色 SGR 重映射到主题调色板（深底默认 16 色不可读的问题）；
  扩展色（38/48;5/2）透传。落点：pigo 工具结果渲染前过一层 filter。
- **colorprofile 降级确认**：crush 全靠 lipgloss v2/bubbletea v2 内建
  colorprofile 自动降级（真彩→256→16），Windows 差异被库抹平——pigo 同栈
  天然获得，只需在 `run.go` 启动时确认 profile 送达（对齐 crush 的
  `Capabilities` 汇总模式，`internal/ui/common/capabilities.go`）。
- **窄宽度回退**：组件显式最小宽度 + 自动降级布局（crush 权限对话框
  `<77` 列转全屏、diff `<140` 列回退单栏）。pigo 落点：toolcard 与
  后续权限确认组件遵循同一规则。

### 3.4 组件组织原则（P2，渐进）

- **"主模型 + 命令式子组件"**：子组件不带 Update 循环，只暴露命令式方法
  （`HandleMouseDown/ScrollBy/Render(width)`），路由集中在主模型——比每组件
  一套 Elm 循环简单，渲染密集的聊天列表尤其合适。pigo 现有 transcript/toolcard
  已接近此风格，明确为约定即可。
- **每工具一文件的渲染器注册表 + generic 兜底**（crush `internal/ui/chat/tools.go`
  工厂模式）：pigo `toolcard.go` 拆分为按工具注册的渲染器（bash/file/edit 各自
  折叠细节），generic 兜底未知工具。
- **能力接口 opt-in**：`Expandable/Highlightable/Focusable` 按需实现 +
  `cachedMessageItem` 缓存基元。
- **对话框排版规则**：crush `internal/ui/AGENTS.md` 的 lipgloss v2 排版血泪
  规则（`Width(n)` 含边框、Padding 不用 Margin、分段 Render 防内层 reset 吃掉
  外层颜色）——直接吸收进 pigo TUI 组件约定。

### 3.5 明确不抄

- ultraviolet 屏幕缓冲 + 矩形 layout（依赖新库，与 bubbletea 标准 View 路线冲突）。
- 整帧 framecache（TTL+GC，复杂度与 pigo 流量不匹配）。
- 4000+ 行巨型主模型（crush 自己承认是债务，pigo 按区域拆文件）。
- 按 provider 换主题、charmtone 产品定制。

## 4. 与来源项目的关系

grok-build-proxy 的 scrollback 块化管线（`blocks/ → entry → state → render`，
ThinkingBlock 是独立块类型）与 crush 的 section 化渲染是同一思想的两实现；
pigo 走 crush 路线（同栈直译），grok 侧只吸收"thinking 作为一等渲染块 +
结束后摘要 footer"的语义校准。grok 的流式 markdown crate（`xai-grok-markdown`）
不搬——crush 方案已覆盖且有测试。

## 5. 验收标准

1. 国模 reasoning 模型（DeepSeek-R1/Kimi）流式回合：thinking 块灰显展示，
   默认折叠 ≤10 行，可展开/收起，结束后显示 "Thought for Xs"。
2. markdown 流式渲染：流式期间即有格式（标题/列表/代码块），回合结束无跳变
   或仅有一次排版收敛；fence 未闭合时不产生破碎渲染（保守回退生效）。
3. 兼容性：Windows Terminal / 256 色 / 16 色终端三档截图回归；窄宽度
   （<80 列）无溢出。
4. 既有 TUI 测试（`internal/cli/tui/*_test.go`）不回归；新增组件带表驱动测试。

## 6. 规模

C 级粗估：thinking 渲染 1–2 人天；流式 markdown 2–3 人天（含边界测试）；
RemapANSI16 + 窄宽度 1 人天；toolcard 注册表化 1–2 人天。
