# b-level

复核完毕。编译通过；`go test ./qianye/...` 全绿（首轮有一次 `TestMatchTypes` 偶发失败，见文末，与 B 级修复无关）。

---

# B 级 8 条逐条判定

## B1 违规退款 Resolver + `RowsAffected==0` 堵死自愈 — **真堵住了（但遗留数据无自愈，且新增一处审计虚报）**

**Resolver 确实注册，且注册在必定被调用的位置**
- `qianye/modules/violation/module.go:43` — `twophase.RegisterResolver(qymodel.KindViolationFee, resolveAfterCompensation)`，**排在 `if !config.Get().Violation.Enabled { return }`（:45）之前**。关掉违规检测不会让历史退款单失去 Resolver。
- 调用链：`qianye/bootstrap.go:65-67` `for _, m := range module.All() { m.InstallHooks() }`，`Init()` 内、任何 HTTP 与后台协程之前。`compensate.go:98` 查的就是这张表。三个模块（transfer/withdraw/violation）都在其中。

**第二次点击能自愈**
- `api_admin.go:479-507` `claimRevoke`：CAS 落空时不再 `return 0, nil`，而是回读最新行 `*rec = latest` 并返回 `first=false`；`api_admin.go:427-429` 的早退判据改成 `rec.Status != RecordRevoked`，回读后是 `revoked`，会继续走到 `api_admin.go:444-456` 的退款分支，由**回读到的** `fee_status` 决定补做。
- `refundFee` 补了 `LocalCommit`（`api_admin.go:587-589`），与 `markSuccess` 的 CAS 同事务（`execute.go:330-332`）。补偿侧同语义（`api_admin.go:529-536`）。

**残留复现路径（遗留数据）**
修复部署之前已经被旧 `resolveApplied` 推成 `Success` 的退款单（即报告 B1 第 4-5 步的产物），记录仍是 `fee_status=charged`。此时管理员再点一次：
1. `claimRevoke` 回读 → `revoked` / `charged` → 进入退款分支（`api_admin.go:446`）；
2. `refundFee` → `twophase.Execute` → 幂等命中 → `execute.go:255-257` `case StatusSuccess: return order, nil`，**`LocalCommit` 在这条路径上根本不会执行**；
3. `api_admin.go:454` `refunded = rec.FeeQuota` → 接口返回 `refunded_quota=500000`，`api_admin.go:458-468` 写下一条 `records.revoke / AmountQuota=500000` 的审计；
4. 库里 `fee_status` 仍是 `charged`、`refund_quota` 仍是 0，`api_user.go` 的 `SUM(fee_quota)` 继续显示罚款在被收取。**点几次就有几条虚报的"已退款"审计。**

全仓没有任何针对这种状态的对账任务：`qianye/modules/violation/tasks.go` 只有 `runRetentionGC` 与 `runBanCompensate`，`grep fee_status` 在 tasks 里零命中。新部署不会再产生这种状态（`Success` ⟹ Resolver/LocalCommit 已跑），但已有的坏数据需要一次性人工修，且现在**修之前会先被界面骗一次**。

另一处小缺口：进程崩在 `applyOnMainDB` 与 `markSuccess` 之间时第二次点击拿到 `ErrInProgress`，`api_admin.go:450` 只 `SysError` 并返回 200 `{refunded_quota: 0}`，管理员分不清"没退成"和"正在退"。

**回归测试真实性**：`refund_test.go:219-263` `TestClaimRevokeKeepsSelfHealPathOpen` 回滚修复必挂（旧实现 `first=false` 时 `rec` 不会被刷新成 `revoked`/回读 `fee_status`）。`TestMarkRecordRefundedIsIdempotent` 有效。
**缺口**：没有任何测试断言 `resolverRegistry[KindViolationFee]` 存在。把 `module.go:43` 挪回 `enabled` 判断之后、或整行删掉，全套测试照样绿。

---

## B2 判定池 vs 扣款池 — **真堵住了；pre 阶段没被改坏（已逐行确认）**

- `qianye/modules/violation/fee.go:170-203` 新增 `poolBalance(info) (int64, bool)`，分支判据 `info.BillingSource != service.BillingSourceSubscription`（:174）。
- 与扣款端完全同口径：`service/quota.go:414` `if relayInfo != nil && relayInfo.BillingSource == BillingSourceSubscription`。订阅分支再判 `SubscriptionId <= 0`（fee.go:182 ↔ quota.go:415-417 的 `"subscription id is missing"`）。
- 订阅余额算法 `AmountTotal - AmountUsed`、`AmountTotal <= 0 视为不限额`（fee.go:195-202）与 `model.PostConsumeUserSubscriptionDelta`（`model/subscription.go:1514-1520`：`newUsed<0 夹 0`、`AmountTotal>0 才校验上限`）一致。

**pre 阶段确认未被改坏**：`BillingSource` 全仓只有一处赋值 —— `service/billing_session.go:321 info.BillingSource = s.funding.Source()`。`PreRelayGuard` 挂在它之前，此刻 `BillingSource == ""`，`poolBalance` 走 `!= subscription` 的钱包分支，与那一刻 `PostConsumeQuota` 的 else 分支（钱包）一致。**修复没有把 pre 阶段改成读订阅池。**

- 额外加固：余额读不出来时返回 `known=false`，`fee.go:217-226` 直接放弃扣费并写 `FeeStatusFailed`，而不是拿 0 当"余额不足"去触发 `ForceBanWeight`。这条挡住了"一次 DB 抖动 = 一次自动封号"。

**回归测试**：`pool_test.go:44-84` 是真表驱动（`useMainDB` 换真 `model.DB` 并关 `common.RedisEnabled`），订阅子用例在旧实现下会读到钱包的 120 而不是 400000，回滚必挂。`TestUnknownBalanceNeverForcesBan` 先固化"0 余额 = 封号"再证明"未知 ≠ 0"，写法正确。
**缺口**：没测到 `chargeFee` 是否真的调用了 `poolBalance`（需要 gin.Context + PostConsumeQuota）。`poolBalance` 只有一个调用点，风险低。

---

## B3 跨阈值信号被消费掉 — **真堵住了（但 deferred 是死胡同，与注释不符）**

- 判据从"恰好跨越"改成"已达"：`counter.go:96-98` `reachedThreshold(after, threshold) = threshold > 0 && after >= threshold`，`counter.go:91` 由**持久化的 `hit_count`** 推导。`crossedThreshold` 全仓已删除。
- 速率闸不再吞信号：`counter.go:204-220`，`rateExceeded` 时以 `BanDeferred` 状态**落行**（而不是先 pending 再改，避免崩在两步之间被补偿任务绕过闸门），仍返回 nil 不执行。
- 阻碍解除后可提升：`counter.go:221-237`，`deferred → pending` 的 CAS。
- 报告的并发/超时路径（`guard.go:234` 的 `Updates` 撞 200ms deadline → `maybeAutoBan` 未被调用）现在可恢复：`bumpCounter` 的事务已提交，下一次违规重新推导 `Reached` 即可重走封号判定。

**残留（不影响资金，但与代码注释矛盾）**：`tasks.go:82-85` 明确写"deferred 行由管理员在封禁列表里处理"，但全仓 `grep BanDeferred` 只有 counter.go/model.go/tasks.go 三个文件，**没有任何 API 能把 deferred 提升成执行**：`adminUnban` 只认 `BanBanned/BanPending/BanFailed`（`api_admin.go:731-732`），前端也没有 deferred 的处理入口。实际唯一的执行路径是"该用户下次再违规"。用户被速率闸挡下后就此收手，那次封号永远不会发生 —— 比丢信号好（至少留了行、管理端能看见），但注释承诺的人工处置能力并不存在。

**回归测试**：`counter_test.go` 重写得很扎实，`TestResolveBanClaimPersistsRateLimitedCrossing` 用真 sqlite 跑 `(user_id, ban_cycle)` 唯一索引 + 状态 CAS，覆盖"落 deferred → 影子期不重复生行 → 窗口滚过后提升为 pending"整条链；`TestResolveBanClaimRespectsExistingOutcome` 逐个终态确认不被覆盖。回滚必挂。
**缺口**：`bumpCounter` 里 `st.Reached = reachedThreshold(...)` 这一行没有测试（`ON DUPLICATE KEY UPDATE` 在 sqlite 上跑不了）。改成 `st.Reached = false` 全套测试仍绿。

---

## B4 幂等命中不校验请求指纹 — **真堵住了；空值处理正确（重点核查项）**

**空值语义（题目点名的高危点）—— 写对了**
`qianye/service/twophase/execute.go:249-254`：
```go
if want != "" && order.Fingerprint != "" && want != order.Fingerprint {
    return order, fmt.Errorf("%w: 单号 %s", ErrIdemConflict, order.OrderNo)
}
```
两侧任一为空即跳过。历史行 `Fingerprint` 由 `gorm:"...;default:''"`（`model/fund_order.go:35`）补空，未接入指纹的调用方（`violation/api_admin.go:551` 的退款）传空。**升级瞬间不会把全部幂等重放变成 409。** 校验排在状态分支之前，参数对不上时无论原单成功还是失败都拒绝。

**调用链接上了**
- `transfer/service.go:121-133` `fundingFacts` 统一算指纹（`Digest()` 覆盖 Kind/Scope/User/Peer/Amount/Fee/RefType/RefId，`execute.go:115-131`，分隔符用 `\x1f` 防边界挪动）；`service.go:99-105` 命中 `ErrIdemConflict` 时**在 `releaseOnFailure` 之前**返回 409 `errIdemKeyConflict`（`errors.go:74-75`，`http.StatusConflict`），只 `SysError` 不写审计。
- 第二道防线：`service.go:351-364` `transferCreatedAudit(order, actorName)` 的金额/收款人/发起人**全部取自资金单**，函数签名里根本拿不到本次请求 —— 历史空指纹单的重放也污染不了审计表。
- withdraw 侧：`credit.go:64` 也算了指纹；申请侧 `create.go:380-385` `ensureReplayMatches` 比对 `Quota`/`Method`，两条重放入口（事务内预读 `create.go:94-97`、唯一索引冲突后回读 `create.go:368-370`）都接上了，冲突返回 409 `errIdemConflict`（`errors.go:73`），且重放路径不写 submit 审计。

**回归测试**：`execute_state_test.go:173-210` 六个用例把空值三种组合全部钉死；`TestRequestDigest_CoversFundingFacts` 逐要素变异 + 分隔符歧义。`transfer_test.go` 的 `TestTransferCreatedAuditUsesOrderNotRequest` 直接断言"审计金额 ≠ 本次请求金额"。回滚必挂。

---

## B5 管理端人工冲正幂等命中不校验参数 — **真堵住了**

- `accrual.go:157` `writeAccrual` 改签名返回 `(bool, error)`，`inserted := res.RowsAffected == 1`（MySQL 下 `INSERT IGNORE` 冲突为 0、`ON DUPLICATE KEY UPDATE` 命中为 2，`==1` 在两种模式下都恰好是"新插入"）。
- `clawback.go:177-185`：`!inserted` 时调 `sameClawbackRequest`，不匹配返回 `ErrClawbackIdemConflict`，**不返回旧单**。
- `clawback.go:208-218` 指纹分量选得对：比 `RefAccrualId`（换 accrual_id）与 `BaseQuota == -quota`（换金额），**不比 `GrossAmount`** —— 后者被 `remaining` 削过，同一请求不同时刻值不同，拿它比会把合法重试误判成 409。历史行 `BaseQuota == 0` 跳过金额维、仍受 `RefAccrualId` 维约束（该列一直都写）。
- 审计接上了：`api_admin.go:205-209` 命中冲突返回 409 `qy_idem_key_conflict`（不写审计）；`api_admin.go:227` `AmountQuota: clawbackAuditAmount(created)` 取**回读行的真实 Gross**（`clawback.go:200-202`，先 `Abs().Floor()` 再取整，宁小勿虚报），`req.Quota` 已从审计路径彻底移除。
- `clawback.go:95-97` 自动冲正路径的 `clawbackCreated.Add(1)` 也改成只在 `inserted` 时自增。

**回归测试**：`clawback_test.go` 只测了两个纯函数。回滚代码这两个函数会消失、测试编译不过，但**没有任何测试断言 `manualClawback` 真的调用了 `sameClawbackRequest`、`adminClawback` 真的用了 `clawbackAuditAmount`** —— 把 `clawback.go:181` 那三行删掉、把 `api_admin.go:227` 改回 `req.Quota`，测试全绿。接线我已逐行读过，是通的；但这层保护没有测试兜底。

---

## B6 审计截断切断 UTF-8 — **真堵住了，三处 `msg[:512]` 一并改掉了**

- `qianye/service/audit/audit.go:128-148`：`truncate` 改名导出为 `Truncate` 并 rune 安全（`safeCut` 回退到 `utf8.RuneStart`），标记位与截断位都过 `safeCut`。`build()` 的 `Reason/ActorName/BeforeSnap/AfterSnap`（:88-97）与 `fillFromContext`（:108-111）全部走它。
- twophase 三处已确认改完：`execute.go:358` `msg := audit.Truncate(cause.Error(), maxErrBytes)`、`compensate.go:173` `updates["last_error"] = audit.Truncate(...)`、`compensate.go:189` `reason = audit.Truncate(reason, maxErrBytes)`。全仓 `grep '\[:512\]'` 零命中。
- 其余三处原本就安全的截断未被改坏：`transfer/service.go:394-402`（RuneStart 回退）、`withdraw/mask.go:123-131`（按 rune 计数）、`violation/rules.go:506-514`（safeCut）。

**回归测试**：`audit/audit_test.go:20-44` 覆盖纯中文/中英混排/emoji/上限小于标记/上限等于标记 5 类边界，断言 `utf8.ValidString` + `len<=max`。旧实现在"纯中文 512"上必挂（498 不是 3 的倍数）。`execute_state_test.go:101-113` 另测了 `markFailed` 写入的 `last_error`。真测试。

---

## B7 `markFailed` 不校验 `RowsAffected` — **真堵住了；调用方拿到的状态是对的**

- `execute.go:356-382`：`res.RowsAffected == 0` → `reloadStatus(gdb, order)` 回读真实状态 → `SysError` → **`return`，既不改内存也不写审计**。只有 CAS 命中才 `order.Status = StatusFailed` 并 `auditTransition(ResultFail)`。
- `reloadStatus`（:388-400）回读失败时**刻意保留内存里的 pending** —— 调用方对 pending 一律不回滚，是最安全的默认。

**回滚闸门确认（题目点名项）**
- `transfer/service.go:285-288` `releaseOnFailure`：`if order == nil || order.Status != qymodel.StatusFailed { return }`。补偿任务已推成 `Uncertain` 时，`order.Status` 现在是 `Uncertain` → 直接返回，**不清 `risk_held`**。明细行留在 `statusPending`，`transfer/reconcile.go:89` 扫的正是 `{statusPending, statusUncertain}`，会被接手。
- `withdraw/credit.go:73-79`：`if order != nil && order.Status == qymodel.StatusFailed` 才 `failWithdrawal`（解冻佣金），同样不会被 `Uncertain` 触发。

**报告要求的另一半（`markSuccess` 也要区分）也做了**：`execute.go:185-190` CAS 落空时不再补 `ResultOK` 审计，改为 `divergedError(order)`（:408-414）—— 对方推成 Success/Reversed 返回 nil（等同幂等重放），推成 Failed/Uncertain 返回 `ErrOrderDiverged`，调用方走各自的探针复核/转人工。`markSuccess`（:309-342）里 `localCommit` 只在赢下 CAS 时执行，不会重复扣佣金余额。

**回归测试**：`execute_state_test.go:68-96 / 117-157` 用真 sqlite 跑 `UPDATE ... WHERE status = pending`，四种 seeded 状态 × 库内状态 / 内存状态 / 是否允许回滚三重断言，并显式断言 `LocalCommit` 是否执行。回滚必挂。这是这批测试里质量最高的一组。

---

## B8 密钥轮换空承诺 — **真堵住了；旧钥缺失的行为是显式 500，不是静默"单据损坏"（题目点名项）**

- 走了报告的方案 (a)：`config/config.go:187` 新增 `PIIKeysRetired map[int]string`；`crypto.go:72-76` `openPayee(nonce, ct, aad, keyVersion)` 按**行上的版本**选钥；`crypto.go:150-165` `piiKeyForVersion`。
- **旧密钥缺失时的行为**：`crypto.go:157-162` 打一条直指配置的 `SysError`，返回**独立的** `errPIIKeyMissingVersion`（`errors.go:94-95`，`http.StatusInternalServerError`，文案"密钥版本未配置，请联系管理员"），与 `errPayeeUndecryptable`（`errors.go:97`，400"请联系用户重新提供"）刻意分开。**不会让管理员误以为单据损坏去找用户重新要银行卡号。**
- 调用链接上了：`payee.go:145` `resolvePayee` 传 `row.KeyVersion`；`api_admin.go:294` `handleAdminRevealPayee` 传 `payee.KeyVersion`，失败仍 `recordPiiAccess(..., "view_plain_failed")` 留痕。`sealPayee` 现在返回它实际用的版本号（`crypto.go:36-59`），`payee.go:60/159` 直接采用，杜绝"两次分开读 config 恰好落在热更新两侧、把 v1 密文标成 v2"。
- 配置侧闭环：`validate.go:195-206` 校验版本号 > 0、格式 32 字节、**且不得包含当前启用版本**；`defaults.go:76 intDefault(&w.PIIKeyVersion, 1)` 在 validate 之前跑（`config.go:389-390`），所以未配版本时 `retired[1]` 会被正确拒绝 —— 我特意查过这条，没有漏。`activeKeyVersion` 把 0/负数归一到 1（`crypto.go:170-175`），与列默认值、`applyDefaults` 三者一致。
- `version <= 0` 的历史行/脏数据按当前钥试，解不开只回落到 `errPayeeUndecryptable`，AAD + GCM 标签保证不会误解出别人的数据。

**回归测试**：`crypto_test.go:124-189` 走**真实 `config.Load()`**（临时 YAML），完整模拟"新钥 v2 启用、旧钥进 retired[1]"，断言老密文仍可解、新密文用新钥且旧钥解不开；`TestOpenPayee_MissingRetiredKeyIsAnOpsError` 用 `assert.NotErrorIs(err, errPayeeUndecryptable)` 把两类错误钉开。回滚必挂。

---

# 汇总

| 缺陷 | 判定 | 核心证据 |
|---|---|---|
| B1 | 真堵住（新增场景），**遗留数据有残留** | `module.go:43` + `bootstrap.go:65` / `api_admin.go:479-507` |
| B2 | 真堵住，pre 阶段未被改坏 | `fee.go:170-203` ↔ `service/quota.go:414` |
| B3 | 真堵住，deferred 无人工出口 | `counter.go:91,96-98,204-237` / `tasks.go:82-85` |
| B4 | 真堵住，空值处理正确 | `execute.go:249-254` / `service.go:99-105,351-364` |
| B5 | 真堵住 | `clawback.go:177-218` / `api_admin.go:205,227` |
| B6 | 真堵住，三处 `msg[:512]` 全改 | `audit.go:128-148` / `execute.go:358` / `compensate.go:173,189` |
| B7 | 真堵住，回滚闸门取到正确状态 | `execute.go:356-382` / `service.go:286` / `credit.go:73` |
| B8 | 真堵住，缺钥是显式 500 | `crypto.go:72-76,150-165` / `validate.go:195-206` |

**需要跟进的三件事**

1. **B1 遗留数据（唯一有实际后果的残留）**：为 `qy_fund_orders.kind='violation_fee' AND status=Success` 但 `qy_violation_record.fee_status IN ('charged','truncated')` 的行做一次性修复（或在 `violation/tasks.go` 加一个对账任务）。在修之前，管理员对这类记录点"撤销+退款"会拿到 `refunded_quota=<金额>` 的 200 与一条 `records.revoke` 成功审计，而资金侧与 `fee_status` 纹丝不动，点几次写几条。
2. **B3 的 `deferred` 死胡同**：要么补一个管理端"执行/放弃 deferred 封禁"的接口（`tasks.go:82-85` 的注释已经这么承诺了），要么把注释改成实话。
3. **测试盲区**：B1 的 Resolver 注册、B3 的 `bumpCounter → st.Reached`、B5 的 `manualClawback`/`adminClawback` 接线，这三处**回滚后测试仍然全绿**。前两处各补一行断言即可（`resolverRegistry` 可导出一个只读查询；`Reached` 可从 `bumpCounter` 抽出赋值单测）。

**另外（与 B 级无关，但会影响 CI 可信度）**：`qianye/modules/violation` 的 `TestMatchTypes/keyword_...` 偶发失败（5 轮里挂 1 轮）。根因是 `rules.go:405` 的超时预算检查**排在第一条规则求值之前**，而 `service.AcSearch` 首次构建 AC 自动机在冷启动/高负载下可能超过 20ms 的 `ScanTimeoutMs`，`scan` 直接返回 `&verdict{Timeout:true}`（`rules.go:425`，`Rule=nil`、`Terms=nil`）。这不是这批修复引入的（`git diff qianye/modules/violation/rules.go` 只加了 D4 的 `maxFeeMultiple` 校验），但含义不轻：**线上第一次 prompt 扫描可能在评估任何一条规则之前就静默放行**。
