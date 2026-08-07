# 退役表清单

这份文件登记**已经不再被 AutoMigrate 管理、但数据仍然留在扩展库里**的表。

## 为什么要有这份清单

摘出 `Tables()` 之后,表不会消失,只是没人管它了。半年后有人打开扩展库看到一张
不在任何 `Tables()` 里的表,第一反应是「是不是漏迁移了」,第二反应是「删掉试试」。
两个反应都错,而且第二个不可逆。

所以三件事必须同时成立,缺一条这份清单就白写:

1. 这里写清楚它是什么、什么时候退役的、观察期到什么时候;
2. `/admin/health` 的 schema 段把它标成 `retired`(不是 missing);
3. **不写自动 DROP**。到期后由人手工执行,一张一张地执行。

自动 DROP 是整个下线流程里唯一真正不可逆的一步,而它买到的只是「少几张空表」。

---

## grouppricing(模型按分组单独定价)

- **退役时间**:本轮(grouppricing 模块整体下线)
- **观察期**:60 天
- **取代它的东西**:`(用户分组, 模型分组)` 的分组倍率矩阵
  (真相源是上游 `options.GroupGroupRatio` / `options.GroupRatio`,管理端「分组矩阵」页)

| 表名                       | 退役时行数 | 说明                                     |
| -------------------------- | ---------- | ---------------------------------------- |
| `qy_group_price_rule`      | 2          | 规则表。两行 remark 均为 `qy-optest`,是运维测试夹具 |
| `qy_group_price_shadow`    | 4          | 影子差额聚合桶,来自同一次测试             |
| `qy_group_price_rule_version` | 1       | 单行版本号表,驱动规则快照刷新             |

**下线前必做(运维动作,不写进代码)**:把三张表的全量内容导出成一条审计记录
(`qy_audit_log` 的 `detail` 字段,JSON)。成本几乎为零,而这样即使表将来真被删了,
那几行的内容仍然可追溯。

**观察期满后的手工 DROP**(SQLite / MySQL / PostgreSQL 通用):

```sql
DROP TABLE qy_group_price_shadow;
DROP TABLE qy_group_price_rule;
DROP TABLE qy_group_price_rule_version;
```

### 能力上的损失(如实记录,不要含糊)

新方案给的是每个 `(用户分组, 模型分组)` **一个标量**,对该分组下所有模型等比缩放。
grouppricing 能表达而新方案表达不了的:

- **同一模型分组内不同模型的不同价格**(G 分组里 gpt-4o 打 5 折、claude 不打折)。
  替代表达:新建一个专属模型分组 + 调整 abilities。
- **把某模型从按 token 计费切成按次固定价**(`usePrice` false → true)。彻底放弃。
- **阶梯表达式计价的分组级乘数**。彻底放弃(阶梯模型只剩全局表达式 × 分组倍率)。
- **计价影子模式**(先算差额再决定是否真扣)。倍率的真相源是上游 options,写进去
  就立刻真实生效,中间没有可以拦截的接缝。

  **这一项目前没有替代物 —— 不要读成"换了个说法"。** 现状是:

  | 期望的替代物 | 现状 |
  | --- | --- |
  | 写入前整份 `GroupGroupRatio` JSON 版本快照 + 一键回到第 N 版 | **未实现**(`qy_group_ratio_revision` 表不存在) |
  | 保存前强制预览影响面 | **只对 `revoke` 动作强制**(`requirePreviewedRevoke`)。**纯改倍率的保存没有任何事前闸门**,`base_ratio_hash` 对上就直接发布进 `options` |
  | 事后回滚 | 只能人工去 `qy_audit_log` 读 `matrixAuditEntry` 的 `before` 快照逐格抄回来 |

  也就是说:把 `(vip, premium)` 从 `0.1` 手滑敲成 `1`,点保存,全站下一笔请求就按
  10 倍扣费,没有二次确认、没有版本可回退。**这是本轮能力退步最实质的一处**,
  在补上版本快照与保存确认之前,改分组倍率必须当成一次不可撤销的操作来对待。

评估:项目方的运营从未使用过分组定价(仅有的 2 条规则全是 `qy-optest` 夹具),
所以损失是**能力上的**而不是**在用功能上的**。将来真需要「同分组内某个模型单独
定价」时,答案是重新挂 hook(接缝还在,见下),不是把标量矩阵掰弯去表达它。

### 代码侧留了什么

模块删了,但 **5 个计价接缝没有删**,它们保持恒等:

```
relay/helper/price.go     QyGroupModelPrice ×2、QyGroupModelRatio ×2、QyGroupTieredQuota
service/task_billing.go   QyGroupTaskRatio
service/tiered_settle.go  QyGroupTieredSettle ×2
```

保留是上游成本更低的一侧:保留 = 上游 diff 变化 **0 行**;删除 = 要改 **8 行**位于真
上游文件里的调用,跨 3 个文件、其中两个在计费主路径上。

两个状态都被测试钉死,所以它不是一个没人说得清的疑问:

- 接缝**存在** → `relay/helper/qy_pricing_hookpoint_test.go`、`service/qy_pricing_hookpoint_test.go`
- 接缝**空着** → `qianye/pricing_seam_vacancy_guard_test.go`

回装路径:`git revert` 下线那个提交即可复活整包 + 配置 + 前端页;表与数据都在,
`rule_version` 也在,复活后快照第一次刷新就能读到原来的规则。**上游侧 0 行变化,
因为接缝从未被触碰。**

### 配置段

`group_pricing:` 这一段**不能从 YAML 里删字段**:`qianye/config` 是严格解析
(`KnownFields(true)`),删掉字段会让每一个还写着这一段的部署在升级二进制的那一刻
启动失败。因此 `Config` 上保留了一个 `GroupPricingDeprecated map[string]any` 占位,
加载时由 `adoptRetiredGroupPricing` 发一条 `SysError` 并整段忽略。

它**不是开关**。运维确认无误后可以把这一段从自己的 YAML 里删掉。

---

## groupmatrix 的「新分组默认全遮断」登记簿

- **退役时间**:本轮(口径反转,自动接管整体撤销)
- **观察期**:60 天
- **取代它的东西**:没有取代物 —— 这件事**不再发生**。

### 撤销的是什么

上一轮实现过一条默认:后台对账任务发现 `options.GroupRatio` 里出现了一个从未见过的
key,就自动为它建一条 `mode=enforce`、零 grant 的 scope 行,那一档的人从此一个模型
分组都选不了,直到有人在矩阵页为它勾上。

本轮项目方把口径**反转**了:

> 「用户组 B 若未设定范围(新添加用户组),默认模型用户可选已选择的,
> 模型组 CDF 可以直接使用,按照模型组的兜底倍率显示。」

自动接管与它正好相反,因此连同配置项、后台任务、登记簿表、界面提示一起撤掉。
撤销在代码层面是**净删除**,在可观测行为上是**空操作**。

| 表名             | 退役时行数 | 说明                                             |
| ---------------- | ---------- | ------------------------------------------------ |
| `qy_group_seen`  | 0(生产)  | 「这个用户分组名扩展侧已经见过」的登记簿,幂等键 |

### 为什么这次反转是无损的,以及为什么必须现在做

`qy_group_scopes` / `qy_group_grants` / `qy_group_write_denies` 在生产上**都是 0 行** ——
从来没有任何用户分组被自动接管过,也没有任何运营手动设定过范围。所以"行不存在"的
含义从「继承上游」改写成「未设定范围 = 全部模型分组可用」时,没有任何一行数据需要
迁移,也没有任何用户的可选范围发生变化。

**一旦有运营开始配范围,反转就不再无损。** 这是"现在做"的全部理由。

三张表本身**保留**:表、代码、路由、`Tables()` 登记全都不动,语义反转但字段不变。
退役的只有 `qy_group_seen` 一张,因为只有它是为自动接管而存在的。

### 观察期满后的手工 DROP

`qy_group_seen` 是「上一轮到底自动遮断过谁」的唯一证据。生产上它是 0 行,但**这个 0
本身也是结论** —— 它证明了那台机器从未在生产上动过手。观察期内保留,期满后由人执行:

```sql
DROP TABLE qy_group_seen;
```

### 不会再长回来

`qianye/modules/groupmatrix/noautoscope_test.go` 的 `TestNoAutoScopeCreation` 用 AST
扫全包,断言除管理端 `adminPutScope` 之外**没有任何函数**会写出一条 scope 行。
没有这条守卫,半年后有人"顺手"加回一个自动接管,这次撤销就白做了 —— 而表现是
一批用户在没有任何人操作的情况下突然选不到任何模型分组。

配套的还有 `TestUnsetScopeAllowsAnyTokenGroup`:它同时钉住反面 ——
「已设定范围且范围为空」必须真的拦得住,否则前一条测试只是在证明"写侧永远放行"。

### 配置段

`group_matrix.new_group_default_deny` / `new_group_scan_interval_seconds` 同样
**不能从 YAML 里删字段**(理由与 `group_pricing` 一致:`KnownFields(true)` 严格解析)。
`GroupMatrix` 上保留了两个 `*Deprecated` 指针占位,加载时由 `adoptRetiredNewGroupDeny`
各发一条 `SysError` 并置 nil。

它们**不是开关**:填 `true` 也不会让任何东西收紧。
存量 YAML 仍能启动这件事由 `qianye/config/retired_keys_test.go` 钉死。
