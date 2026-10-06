# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 6 期第二批：T6.5 真链路实跑验收 + T6.1 会话写租约
  实作收口（2026-10-06，全量 41 包 0 FAIL）**——
  ①**T6.5 实跑验收（上卡遗留①关闭）**：用户提供 sensenova key，隔离环境
  在 `.gtmp/t65env/`（XDG_CONFIG_HOME/PIGO_HOME/PIGO_SKILLS_DIR；key 落在
  被 gitignore 的 `config.toml`）→ 钉模型路由**成立**（frontmatter
  `model: deepseek-v4-flash` → `PINNED_OK`），反证：钉不存在模型时上游 404
  经 **D-7 信封**返回（不静默降级）；**踩坑**：skill 物化工具非 ReadOnly，
  headless 未信任目录 fail-closed，`[permissions]` allow 规则的 **pattern
  必须为空**（其他工具 = 整个工具，非 glob）；**实测**18 内置技能全物化 →
  首轮 input 6074→11509（+89%，单技能 ≈300 token），是否并入 deferred 面
  待拍板。②**T6.1 会话写租约**：锁 = **内核锁**（`flock`/`LockFileEx`，
  常驻 `<id>.jsonl.lock` 不删）+ 同进程带超时信号量，acquire+release
  **76μs vs O_EXCL 版 429μs**；`atomicWrite` 补 temp 名唯一/fsync/sha256
  幂等 sentinel；截断恢复**只认末行**（中间损坏仍报错），`Append` 自愈、
  `Repair` 显式修复（**未接线**）；根治 Windows"残留锁文件使目录不可删 →
  挂掉不相干测试"的 flake（runtime 17.5s→5.1s）。规格 =
  [session-write-lease.md](wiki/port/session-write-lease.md)。
  ③**T6.4 pstack 盘点完成**（只出清单）：pstack 作者集合 50 个技能 = 23 条
  `principle-*` + 23 个 playbook + `setup-pstack`（已被 T6.5 吸收，不立项）
  + benny 3 件（依赖 headless 续跑，不立项）；**49/50 是 slash-only，内置不
  增每轮工具面 token**；建议精选 4+3 件待拍板。清单 =
  [pstack-skill-inventory.md](wiki/port/pstack-skill-inventory.md)。
  **下一步**：T6.4 精选清单拍板；之后第 6 期第三批排序待拍板。本机跑测试
  带 `TMP/TEMP/TMPDIR=D:\tmp`。其余见 handoff。
- **长期口径（2026-10-06 用户点名）**：**MCP 与 code mode 是后续确定方向**
  ——盘点与排期一律按"前置依赖"分层，**不要用"不做"把件永久封死**（依赖前置
  表见 `wiki/port/pstack-skill-inventory.md` §5）。两条硬约束：MCP 工具默认
  进 deferred/hidden 面（不得默认 direct，实测 18 个 model-invocable 内置
  全物化已 +5435 token/轮）；code mode 须先钉死沙箱边界与 T5.2 副作用契约，
  脚本不得比工具拿到更宽的口子。
- **交接台账（L0.5 必读，2026-10-05 起瘦身版）**：
  [wiki/port/handoff.md](wiki/port/handoff.md) 只保留**最新一条交接卡**，
  历史条目在 [wiki/port/handoff-archive.md](wiki/port/handoff-archive.md)
  ——新会话先读最新卡，再 `git log` + `git status` 核对现场，禁止凭记忆
  续写；更早脉络查 archive。
- **施工权威**：[wiki/port/implementation-plan.md](wiki/port/implementation-plan.md)
  （六期任务表，状态每期收口回填；第 1 期已清空）。
- **施工纪律**：[wiki/port/design-principles.md](wiki/port/design-principles.md)
  （R1–R11，R11=多参照实现择优：能融合则融合、不能融合取相对最优并在规格
  登记互斥原因；附录已登记 7 个 Windows 测试差异族 + `internal/testgate`
  门控机制，
  本机全量测试基线 = 门控后全绿）。
- **T1.1 规格**：[wiki/port/startup-exit-probes.md](wiki/port/startup-exit-probes.md)
  （span 名以 §2.2 埋点表为唯一权威，已含实现偏差登记）。

## 环境事实

- `wiki` 是指向 `D:\wiki\pandawiki\projects\pgo-fork\wiki` 的符号链接，已在
  `.gitignore`（`/wiki`）——**wiki 内容不进 git**，改 wiki 即改 pandawiki 侧文件。
- Windows 开发机：`go build` 间歇被卡巴斯基锁 `%TEMP%`，构建统一加
  `GOTMPDIR=<repo>/.gtmp`（R9）——**只用于 go build**：go test 不受卡巴
  斯基影响，加了反而让 `t.TempDir()` 走 GOTMPDIR 产出混合斜杠路径打破
  trust 测试；全量测试后台跑、输出落文件（R6）。
- dev 分支本地开发，推送/PR 策略待用户拍板。
