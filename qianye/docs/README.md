# qianye 扩展 — 设计文档索引

本目录是 fork 新增功能的设计文档。**全部位于 `qianye/` 命名空间下,与上游零冲突。**

## 核心原则

1. 所有新功能数据存进**独立 MySQL**,通过独立 YAML 配置(`./data/qianye.yaml`)
2. 原项目文件只做"只增不改"的单行 hook,把合并冲突面压到最小
3. 配置缺失 → 扩展静默禁用,主程序行为与上游**逐字节一致**

## 已锁定的决策

| # | 决策 | 选择 |
|---|---|---|
| D1 | 佣金提现形态 | 站内额度兑换 + 线下法币打款,用户自选 |
| D2 | "隐藏分组" | 修复无权分组泄漏(白名单交集裁剪),不新建可见性体系 |
| D3 | 改动预算 | ≤10 个后端文件 / ≤40 行;钱包页直接改原文件不 fork |
| D4 | 返佣口径 | 宽松(默认全返)+ 三个风控 YAML 开关;违规扣费硬排除 |

## 预算核算结果

**后端:9 个文件 / 20 行**(预算 10 文件 / 40 行)✅

`main.go`、`model/log.go`、`model/redemption.go`、`service/log_info_generate.go`、
`service/text_quota.go`、`controller/pricing.go`、`controller/perf_metrics.go`、
`pkg/perf_metrics/metrics.go`、`controller/relay.go`

关键技巧:**hook 变量同包注入** —— 在上游包内新增 `qy_export.go`(纯新文件)声明
默认 no-op 的 hook 变量,调用点直接调同包符号,**上游文件的 import 块零改动**。

## 文档结构

### 设计(按实施顺序阅读)

| 文件 | 内容 |
|---|---|
| [design-00-foundation.md](design-00-foundation.md) | **地基**:YAML 配置、独立库、降级、跨库两阶段、租约、审计 |
| [design-08-frontend.md](design-08-frontend.md) | **前端骨架**:路由、菜单、i18n、共享组件、API 客户端 |
| [design-06-plaza-uptime.md](design-06-plaza-uptime.md) | 需求 5+6:分组泄漏修复、模型可用率监控 |
| [design-04-wallet-ui.md](design-04-wallet-ui.md) | 需求 3:钱包页选项卡、套餐详情截断、订阅弹窗 |
| [design-01-transfer.md](design-01-transfer.md) | 需求 1:用户余额划转 |
| [design-05-logs.md](design-05-logs.md) | 需求 4:使用日志推理强度/缓存百分比两列 |
| [design-02-commission.md](design-02-commission.md) | 需求 2a:佣金账本与返佣触发 |
| [design-03-withdraw.md](design-03-withdraw.md) | 需求 2b:提现申请、审核、历史 |
| [design-07-violation.md](design-07-violation.md) | 需求 7:违规检测 |

### UI 主题

| 文件 | 内容 |
|---|---|
| [design-14-midnight-signal.md](design-14-midnight-signal.md) | **现行口径**:全站 UI(近黑画布 + 单支紫),色板推导、六条签名形状、三个契约测试 |
| [ui-reference/](ui-reference/) | 参考稿原件(dope.security 抽取):`DESIGN.md` 是文字规范,其余是抽取出来的取值 |
| ~~design-10-steins-gate-theme.md~~ | **已作废**,只保留「为什么只换调色板不算换主题」那段论证 |
| ~~design-11-sg-hud-layer.md~~ | **已作废**,只保留「遮住颜色看轮廓」这条验收标准的由来 |

### 裁定(**实施前必读**)

[99-coherence-review.md](99-coherence-review.md) — 跨模块一致性审查。
9 个模块并行设计产生了 34 处冲突(表名、单号格式、状态机取值、API 路径、金额精度…),
本文逐条裁定。**各模块设计文档与本文冲突时,以本文为准。**

### 勘察底稿

[recon/](recon/) — 代码库勘察报告,含 [缺口分析](recon/00-gap-analysis.md)。
实施时查证具体 file:line 用。

## 实施阶段

| 阶段 | 内容 | 占比 | 依赖 |
|---|---|---|---|
| P0 | 地基 | 22% | — |
| P0b | 前端骨架 | 8% | P0 |
| P1 | 分组泄漏修复 + 钱包 UI(零风险快速交付) | 10% | P0b |
| P2 | 余额划转 | 14% | P0 |
| P3 | 日志两列 | 5% | P0 |
| P4 | 返佣 | 16% | P0 |
| P5 | 提现 | 11% | **P4** |
| P6 | 可用率 | 9% | P0 |
| P7 | 违规检测 | 5% | P0,建议最后做 |

P2 / P3 / P4 / P6 四条线可并行。P7 动 `controller/relay.go`(最高冲突风险),建议在其他模块合并上游一轮后再做。

## 上游合并流程

1. `web/src/routeTree.gen.ts` 冲突 → 删除后重新 `bun run build` 生成
2. i18n 的 qy 键在独立的 `web/src/i18n/qy/*.json`,**不进 `locales/`**,不跑 `bun run i18n:sync`
3. 需要重点验证的三处中/高风险改动:
   - `controller/relay.go` 的插入点是否仍存在
   - `service/text_quota.go` 的 `cacheWriteTokens` 变量名是否被重构
   - `router/relay-router.go`(若启用违规中间件)

## 建议向上游提 PR

需求 5 的分组泄漏是**信息泄漏级 bug**(用户能看到无权分组的名称和倍率)。
修复很小(3 行),建议提 PR 给上游,合并后这块永久免维护。
