# authz

我已通读 `qianye/` 全部 117 个 Go 文件、6 个上游新增导出文件,以及 9 个上游 hook 插入点的上下文。下面只列我实际读过并能给出具体触发路径的缺陷。

---

## 缺陷 1 — 审计写入按字节截断,会切断 UTF-8 字符导致审计行被数据库整条拒绝

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\service\audit\audit.go:120`**

```go
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max { return s }
	const mark = "...[truncated]"
	if max <= len(mark) { return s[:max] }
	return s[:max-len(mark)] + mark   // ← 裸字节切,不看 rune 边界
}
```

**触发场景(可复现)**:管理员在提现审核页填一条中英混排的中文拒绝理由,例如 `"W20260730001 收款人实名与账户不符,已多次联系用户核实未果,…"`,总长 ≤ 200 rune(通过 `withdraw/review.go:114` 的 `checkRunes(rawReason, 200)`),但 UTF-8 字节数 > 512。
→ `writeDecisionAudit`(review.go:284)把它放进 `audit.Entry.Reason`
→ `build()` 调用 `truncate(reason, 512)` → `s[:498]`。只要第 498 字节不是 rune 起始位(混排文本下概率约 2/3),结果就是一个尾部残缺的非法 UTF-8 串
→ 扩展库 DSN 强制 `charset=utf8mb4`(`db/db.go:normalizeDSN`),MySQL 默认 `STRICT_TRANS_TABLES` 下直接报 1366 `Incorrect string value`
→ `audit.Write` 只 `SysError` 一行,**这条"谁在什么时候拒了这笔提现、理由是什么"的审计记录彻底丢失**,而 `qy_audit_logs.reason` 是 `varchar(512)`(按字符计),原文本来完全放得下。

同一路径还覆盖 `withdraw.fail` / `withdraw.resolve.*`(同样 200 rune 上限)与 `qianye/controller/admin.go:180` 的 `fund.resolve`(理由完全无长度上限)。

对比佐证:本仓其他三处截断都是安全的 —— `transfer/service.go:364` 用 `utf8.RuneStart` 回退,`withdraw/mask.go:261` 按 rune 计数,`violation/rules.go:492` 的 `safeCut` 也回退到 rune 边界。只有 audit 这一处漏了,属于实现遗漏而非取舍。

**影响等级**:数据错误(资金操作审计静默缺失,与该文件顶部"审计静默丢失会让事故无法复盘"的自述直接矛盾)

**修复建议**:把 `truncate` 改成先回退到 rune 边界再拼 mark(直接复用 `violation/rules.go` 的 `safeCut` 逻辑),或改成按 rune 计数截断。顺带把 `qianye/service/twophase/execute.go:259-261` 的 `msg = msg[:512]` 一并改掉 —— 那是同一类裸字节切,落的也是 `varchar(512)` 的 `last_error`。

---

## 缺陷 2 — 密钥轮换会让全部历史收款信息永久不可解密,而配置与注释都宣称支持轮换

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\withdraw\crypto.go:128-138`**

```go
func piiKey() ([]byte, error) {
	raw := strings.TrimSpace(config.Get().Withdraw.PIIKey)  // ← 永远只读"当前"这一把
	...
}
```

`sealPayee` / `openPayee` 都无条件调用 `piiKey()`,**从不读取密文行上的 `KeyVersion`**;配置里也没有任何 version→key 的映射结构(`config.Withdraw` 只有单个 `PIIKey string` + `PIIKeyVersion int`,见 `config/config.go:169-175`)。

而 `model.go:117` 明确承诺:「KeyVersion 支持密钥轮换:解密时按版本选密钥,旧密文不会因为换钥而全部作废」,`qianye.example.yaml:166` 也提供了 `pii_key_version` 让运维去改。

**触发场景(可复现)**:运维按注释与配置字段的指引轮换 `withdraw.pii_key` 并把 `pii_key_version` 从 1 改成 2、重启。此后:
- `payee.go:142` `resolvePayee` 对任何已保存的收款方式解密失败 → `errPayeeUndecryptable` → 用户再也无法用已保存的收款方式提交法币提现;
- `api_admin.go:294` `handleAdminRevealPayee` 对**队列里全部待打款单**解密失败 → 管理员拿不到收款账号,这些单既打不了款,佣金又已经在 `frozen` 里,只能人工找用户重新提交。

**影响等级**:数据错误(可导致在途提现单集体不可结算)

**修复建议**:二选一,不要留中间态。(a) 真正实现轮换:配置改成 `pii_keys: {1: "...", 2: "..."}` + `pii_key_active_version`,`openPayee` 按行上的 `KeyVersion` 选钥,`sealPayee` 用 active version;(b) 明确不支持轮换:删掉 `pii_key_version` 配置项与 `KeyVersion` 列,把 `model.go:117` 的注释改成"密钥一经启用不得更换",与 `api_admin.go:265` 已有的"旧密文解不开是可预期运维状态"表述统一。

---

## 缺陷 3 — `QueueStats` 无同步读取 `queue`,与 worker 首次初始化构成数据竞争

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\guard\guard.go:186`**

```go
func QueueStats() map[string]any {
	return map[string]any{
		"capacity": cap(queue),   // ← 未经 queueOnce,直接读包级变量
		"pending":  len(queue),
		...
```

`queue` 只在 `startWorkers()`(guard.go:162-172)的 `queueOnce.Do` 内被赋值,而 `startWorkers` 只由 `HotAsync` 调用。`QueueStats` 完全绕开了这个 once。

**触发场景(可复现)**:进程刚启动、还没有任何 relay 请求时,管理员打开健康面板打到 `GET /api/qy/admin/health`(`qianye/controller/admin.go:27` → `guard.QueueStats()`),与此同时第一个 relay 请求触发 `guard.HotAsync`(违规检测 / 可用率采样 / 消费返佣任一)执行 `queue = make(chan hotJob, size)`。两个 goroutine 对同一个包级 chan 变量一读一写且无 happens-before → 数据竞争。`availability/api.go:267` 与 `commission/api_admin.go` 的 `adminHealth` 是同样的入口。

实践后果通常是读到 nil(面板显示 capacity=0),但按 Go 内存模型这是未定义行为,`-race` 下必然报警 —— 而任务背景里已注明 `-race` 从未在本机跑过。

**影响等级**:代码质量

**修复建议**:在 `QueueStats()` 开头加 `startWorkers()`,或把 `queue` 改成 `atomic.Pointer[chan hotJob]` / 用 `queueOnce.Do` 之外的显式初始化(例如在 `qianye.Init()` 里一次性建好队列,与其它 hook 安装同批完成)。

---

## 缺陷 4 — `commission` 的用户名脱敏对单字符名原样回显

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\commission\mask.go:45`**

```go
case n <= 2:
	return string(r[0]) + "**"   // n==1 时 r[0] 就是全部原文
```

**触发场景(可复现)**:下线的用户名是单个字符(`model/user.go:81` 的 `validate:"max=20"` 没有下限,单字符用户名可注册),或邮箱本地部分是单字符(`resolveInviter` 在 username 为空时回落到 email,`inviter.go:101-104`)。邀请人调用 `GET /api/qy/commission/invitees` → `masked_name` 字段返回 `"王**"` / `"a**@***.com"`,**星号之外的部分就是完整原文**,脱敏为零。

同一份代码库里 `transfer/validate.go:174` 的 `maskUsername` 对 `n==1` 返回 `"*"`,说明"单字符必须整体遮蔽"才是本项目的既定口径;commission 这一处是遗漏,不是取舍。`mask_test.go:104` 把 `{"单个中文", "王", "王**"}` 固化成了期望值,所以这条不会被现有测试发现。

**影响等级**:信息泄漏(范围有限:仅泄漏 1 字符用户名/邮箱本地部分,不涉及 user_id、邮箱域名以外的完整地址)

**修复建议**:`maskCore` 增加 `case n == 1: return "**"`,并同步修正 `mask_test.go` 对应用例。

---

## 缺陷 5 — 人工裁决理由无长度上限,超长时写库失败且错误信息不指向原因

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\controller\admin.go:169`**

```go
if req.Reason == "" {            // :135 只校验非空
	badRequest(...); return
}
...
"last_error": "人工裁决: " + req.Reason,   // :169 直写 varchar(512)
```

`qy_fund_orders.last_error` 是 `varchar(512)`(`qianye/model/fund_order.go:43`),前缀 `"人工裁决: "` 占 6 字符,因此 `req.Reason` 超过 506 字符即溢出。

**触发场景(可复现)**:管理员在裁决一笔 uncertain 资金单时粘贴一段较长的排查过程(对账截图说明、与用户的沟通记录),长度 > 506 字符 → MySQL 严格模式返回 1406 `Data too long for column 'last_error'` → `serverError(c, res.Error)` 只回 `"处理失败,请稍后重试"`,不提示是理由太长。管理员会原样重试,**这笔资金单永远停在 uncertain 无法收敛**。

**影响等级**:代码质量(管理端可用性;资金单卡在中间态)

**修复建议**:与提现审核对齐,用 `checkRunes(req.Reason, 200)` 之类的显式上限做前置校验并返回明确的 `qy_reason_too_long`;写库前再用 rune 安全的 `truncate` 兜底。

---

## 已核对、未发现问题的项(供复核时缩小范围)

1. **接口权限**:`qianye/router.go:37-46` 的 `user`/`admin` 两个组分别独立 `Group()`,gin 的 `combineHandlers` 每次新分配 handler 链,不存在中间件串味;所有 admin 接口都在 `/api/qy/admin` 下且只挂 `AdminAuth`。逐个核对了 7 个模块的 `RegisterUserRoutes`,无管理接口误挂用户组。
2. **数据归属**:全部用户态 handler 的身份都取自 `c.GetInt("id")`(见 `withdraw/api_user.go:25/77/91/130/153/166/200/218`、`commission/api_user.go:25/105/183`、`violation/api_user.go:63/96/145`、`transfer/handler.go:102/120/140/207`),没有任何一处读请求参数里的 `user_id`。`loadUserWithdrawal`(review.go:318)、`deletePayeeAccount`、`resolvePayee`、`userCreateAppeal` 的 `WHERE id = ? AND user_id = ?` 都把归属放进了 SQL 条件而不是取回后再比。
3. **AES-GCM**:`sealPayee`(crypto.go:49-54)每次 `crypto/rand.Read` 生成新 nonce,无复用;AAD 绑定了业务标识,密文搬运会解密失败;`piiKey` 强制 32 字节,密钥缺失时 `sealPayee` 直接报错,不存在明文回落。管理端明文接口强制事由 ≥4 字符、双写 `qy_pii_audits` + 全局审计,且失败访问也留痕。上游 `beginAdminAudit`(middleware/audit.go:107-110)跳过 GET,响应体不会被落库,明文不会二次泄漏。
4. **分组裁剪**:`groupvis/filter.go:43` 对 `EnableGroup` 一律新分配切片,没有原地改写 `model.GetPricing()` 的共享缓存;`filterGroupKeys` 返回值恒非 nil,空切片在 `pkg/perf_metrics/metrics.go:257` 与 `model/perf_metric.go:104-107` 都被正确解读为"全部过滤"而非"不过滤";`?group=` 探测被 `filterPerfGroups` 整条剔除。匿名访客经 `GetUserUsableGroups("")` 只拿到运营主动配置的公开分组。另核对了同样匿名可达的 `controller/group.go:26` `GetUserGroups`,它本身已按白名单过滤,不构成遗漏的泄漏面。
5. **违规证据**:`Payload` 只有 `adminGetEvidence`(AdminAuth + 审计)可读;入库前已 `stripInlineBinary` + `redact`(邮箱/手机/身份证/银行卡/API key);用户端走白名单 `userRecordView`,不含 `matched_terms`/`match_snippet`/`rule_id`/`ip`;计费日志里的 `qy_matched_terms` 放在 `other.admin_info`,被 `model/log.go:123` 对普通用户剥离。
6. **错误信息**:`transfer/errors.go:277-301`、`withdraw/errors.go`、各模块 `internalError` 均把未识别错误降级为固定文案;`resolveExisting` 的 `order.LastError` 在 `respondErr` 里被映射成 `errTransferFailed`,不外泄。单号用 `twophase.NewOrderNo`(crypto/rand)生成,不可枚举。
7. **限流**:所有动钱接口都挂了 `CriticalRateLimit`;`/transfer/preview` 挂 `SearchRateLimit`(按用户 10 次/分),且 `classifyIdentifier` 拒绝通配符与用户名模糊搜索、只允许 ID 精确匹配或完整邮箱全等,并写 `qy_transfer_lookup_logs` 留痕。枚举成本与留痕程度都在合理范围。
