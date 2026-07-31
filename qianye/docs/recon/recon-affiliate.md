# 现有邀请码/返利体系

## 一、User 结构体中的邀请相关字段

`C:\Users\Administrator\Desktop\qianye\qianye-newapi\model\user.go:79-113`

```go
AffCode          string `json:"aff_code" gorm:"type:varchar(32);column:aff_code;uniqueIndex"`        // :99
AffCount         int    `json:"aff_count" gorm:"type:int;default:0;column:aff_count"`               // :100
AffQuota         int    `json:"aff_quota" gorm:"type:int;default:0;column:aff_quota"`               // :101 邀请剩余额度
AffHistoryQuota  int    `json:"aff_history_quota" gorm:"type:int;default:0;column:aff_history"`     // :102 邀请历史额度（列名是 aff_history，不是 aff_history_quota）
InviterId        int    `json:"inviter_id" gorm:"type:int;column:inviter_id;index"`                 // :103
```

注意两个坑：
- `AffHistoryQuota` 的**数据库列名是 `aff_history`**，与 json 名不一致。
- `AffCode` 有 `uniqueIndex`，长度 varchar(32)，但生成时只用 4 位随机串（碰撞风险随用户量上升）。
- 这些字段都在**主库**的 `users` 表上。新功能若要独立库，需要自己维护 `user_id -> inviter_id` 的映射快照，或跨库读主库 `users`（只读）。

---

## 二、邀请码的生成与绑定

**生成**（两处，均为 `common.GetRandomString(4)`，4 位）：
- `model/user.go:598` — `User.Insert(inviterId int) error`（密码注册路径）
- `model/user.go:662` — `User.InsertWithTx(tx *gorm.DB, inviterId int) error`（OAuth 注册路径）
- `controller/user.go:470-479` — `GetAffCode(c *gin.Context)` 惰性补生成（老用户 AffCode 为空时）

**绑定（注册时 aff 参数链路）**：

前端：
1. `web/src/routes/__root.tsx:54-59` — 全站根组件读取 `?aff=` 存入 localStorage（key `'aff'`）
2. `web/src/features/auth/lib/storage.ts:39` `getAffiliateCode()` / `:53` `saveAffiliateCode(code)`
3. `web/src/features/auth/sign-up/components/sign-up-form.tsx:134-139`（再存一次）、`:168` `aff_code: getAffiliateCode()` 放进注册请求体
4. OAuth 路径：`web/src/features/auth/api.ts:145` — `const aff = intent === 'login' ? getAffiliateCode() : ''`

后端（密码注册）`controller/user.go:206 Register`：
```go
// controller/user.go:263-271
affCode := user.AffCode // this code is the inviter's code, not the user's own code
inviterId, _ := model.GetUserIdByAffCode(affCode)
cleanUser := model.User{
    Username: user.Username, Password: user.Password,
    DisplayName: user.Username, InviterId: inviterId,
    Role: common.RoleCommonUser,
}
...
if err := cleanUser.Insert(inviterId); err != nil { ... }   // :275
```

后端（OAuth 注册）`controller/oauth.go:369-373`，随后 `InsertWithTx(tx, inviterId)`（:380 / :406）→ 事务提交后 `user.FinalizeOAuthUserCreation(inviterId)`（:401）。

查询函数：`model/user.go:467` `func GetUserIdByAffCode(affCode string) (int, error)`

---

## 三、现有返利逻辑 —— 只有「注册即送固定额度」，没有任何比例返佣

**唯一触发点**：`model/user.go:617-647` `func (user *User) finishInsert(inviterId int)`（OAuth 版本是 `:676-703` `FinalizeOAuthUserCreation`，逻辑重复了一遍）：

```go
// model/user.go:636-646
if inviterId != 0 && operation_setting.IsPaymentComplianceConfirmed() {
    if common.QuotaForInvitee > 0 {
        _ = IncreaseUserQuota(user.Id, common.QuotaForInvitee, true)
        RecordLog(user.Id, LogTypeSystem, fmt.Sprintf("使用邀请码赠送 %s", ...))
    }
    if common.QuotaForInviter > 0 {
        RecordLog(inviterId, LogTypeSystem, fmt.Sprintf("邀请用户赠送 %s", ...))
        _ = inviteUser(inviterId)
    }
}
```

`model/user.go:492-501`：
```go
func inviteUser(inviterId int) (err error) {
    user, err := GetUserById(inviterId, true)
    ...
    user.AffCount++
    user.AffQuota        += common.QuotaForInviter      // 注意：加的是 AffQuota，不是 Quota
    user.AffHistoryQuota += common.QuotaForInviter
    return DB.Save(user).Error
}
```

关键语义差异：
- **被邀请人**（invitee）：`QuotaForInvitee` 直接进 `Quota`（可用余额）。
- **邀请人**（inviter）：`QuotaForInviter` 进 `AffQuota`（待提取池），需要用户手动「转移到余额」。

**设置项**：
- 常量声明 `common/constants.go:124-126` — `var QuotaForNewUser/QuotaForInviter/QuotaForInvitee = 0`（默认全 0，即默认关闭）
- OptionMap 注册 `model/option.go:133-135`；热更新赋值 `model/option.go:524-529`
- 前端表单 `web/src/features/system-settings/general/quota-settings-section.tsx:56-57, 187, 216`；类型 `web/src/features/system-settings/types.ts:249-252`

**结论：现有机制 = 注册一次性固定额度，与充值额、消费额完全无关。「按比例返佣」的能力项目里完全不存在，需要全新建。**

---

## 四、提现 / 佣金功能 —— 确认不存在

对 `withdraw` / `Withdraw` / `commission` / `Commission` / `提现` / `佣金` / `返佣` / `rebate` 全库搜索：**0 命中**。

现有的、最接近「提现」的东西是**站内额度划转**：

`model/user.go:503-538`
```go
func (user *User) TransferAffQuotaToQuota(quota int) error
// 最小额度 = common.QuotaPerUnit（= 500000，约 $1）
// 事务 + lockForUpdate；AffQuota -= quota; Quota += quota
```
- Controller：`controller/user.go:435-461` `TransferAffQuota(c *gin.Context)`，请求体 `TransferAffQuotaRequest{ Quota int \`json:"quota" binding:"required"\` }`（`:435-437`）；入口先过 `requirePaymentCompliance(c)`（`controller/payment_compliance.go:22`）
- 路由：`router/api-router.go:113` `selfRoute.POST("/aff_transfer", controller.TransferAffQuota)`

**没有任何提现单、审核流、结算单、佣金记录表。全部要新建。**

---

## 五、前端：邀请码/邀请链接在哪

不在「个人设置」页，在 **钱包页（/wallet）**：

- 路由文件 `web/src/routes/_authenticated/wallet/index.tsx:28` → `createFileRoute('/_authenticated/wallet/')`
- 页面 `web/src/features/wallet/index.tsx`，卡片挂载于 `:342-350`
- 组件 `web/src/features/wallet/components/affiliate-rewards-card.tsx` — `AffiliateRewardsCard({ user, affiliateLink, onTransfer, complianceConfirmed, loading })`
  - 显示三个指标（`:84-99`）：`Pending`=`aff_quota`、`Total Earned`=`aff_history_quota`、`Invites`=`aff_count`
  - 邀请链接只读输入框 + 复制按钮（`:102-114`）
  - `aff_quota > 0` 时显示「Transfer to Balance」按钮（`:63`, `:115-124`）
- Hook `web/src/features/wallet/hooks/use-affiliate.ts:33` `useAffiliate()`
- 链接生成 `web/src/features/wallet/lib/affiliate.ts:26`：`${window.location.origin}/sign-up?aff=${affCode}`
- API `web/src/features/wallet/api.ts:187` `GET /api/user/aff`、`:195-200` `POST /api/user/aff_transfer`
- 划转弹窗 `web/src/features/wallet/index.tsx:368-374` `<TransferDialog availableQuota={user?.aff_quota ?? 0} />`

**用户当前能看到的邀请信息仅 4 项**：邀请码/链接、待转额度、累计获得、邀请人数。看不到「谁通过我注册的」「每笔返佣明细」「返佣比例」。

其他只读展示位：
- `web/src/features/usage-logs/components/dialogs/user-info-dialog.tsx:139-165`（管理员看用户 aff_code / aff_quota）
- `web/src/features/users/components/users-columns.tsx:228, 262-285`（用户列表显示 inviter_id / "No Inviter"）

---

## 六、充值完成回调链路（充值后返佣的候选触发点）

`TopUp` 模型：`model/topup.go:14-25`（**GORM 表名 `top_ups`**，无 TableName 覆盖；AutoMigrate 在 `model/main.go:274`）
```go
type TopUp struct {
    Id int; UserId int `gorm:"index"`; Amount int64; Money float64
    TradeNo string `gorm:"unique;type:varchar(255);index"`
    PaymentMethod string; PaymentProvider string
    CreateTime int64; CompleteTime int64; Status string
}
```
Provider 常量 `model/topup.go:35-42`：`epay/stripe/creem/waffo/waffo_pancake/balance`；状态常量 `common/constants.go:251-254`：`pending/success/failed/expired`。

**六条充值成功路径（全部要覆盖才算完整）**：

| # | 支付方式 | 完成函数 | file:line | 用户加额度的写法 | 设 CompleteTime |
|---|---|---|---|---|---|
| 1 | 易支付 | `EpayNotify` | `controller/topup.go:310`，成功分支 `:385-408`，加额度 `:401`，日志 `:407` | `model.IncreaseUserQuota(topUp.UserId, quotaToAdd, true)` | **否**（坑） |
| 2 | Stripe | `Recharge` | `model/topup.go:109`，加额度 `:144`，日志 `:157` | `tx.Model(&User{}).Updates(... gorm.Expr("quota + ?"))` | 是 `:136` |
| 3 | Creem | `RechargeCreem` | `model/topup.go:392`，加额度 `:449`，日志 `:462` | 同上 | 是 `:419` |
| 4 | Waffo | `RechargeWaffo` | `model/topup.go:467`，加额度 `:511`，日志 `:524` | 同上 | 是 `:505` |
| 5 | Waffo Pancake | `RechargeWaffoPancake` | `model/topup.go:530`，加额度 `:572`，日志 `:585` | 同上 | 是 `:566` |
| 6 | 管理员补单 | `ManualCompleteTopUp` | `model/topup.go:320`，加额度 `:374`，日志 `:389` | 同上 | 是 `:367` |

Webhook 路由注册：`router/api-router.go:58-63`（stripe/creem/waffo/waffo-pancake）、`:78-79`（epay notify GET+POST）。

**旁支「充值」**（是否计入返佣需产品决策）：
- 兑换码：`model/redemption.go:184` `RecordLog(userId, LogTypeTopup, "通过兑换码充值 ...")`
- 订阅购买：`model/subscription.go:638`、`model/subscription.go:833`（`PurchaseSubscriptionWithBalance`，`:746`）；订阅易支付回调 `controller/subscription_payment_epay.go:118` `SubscriptionEpayNotify`

⚠️ **重要坑**：`model.RecordTopupLog`（`model/log.go:254`）签名是
`RecordTopupLog(userId int, content string, callerIp string, paymentMethod string, callbackPaymentMethod string)`
—— **不带金额参数，写入的 Log 行 `Quota` 字段恒为 0**，金额只在 `Content` 字符串里。所以**不能**把 `RecordTopupLog` 当作充值返佣的统一 hook（拿不到结构化金额）。

---

## 七、消费结算收尾（消费后返佣的候选触发点）

调用链（文本/音频主链路）：
`service/text_quota.go:451` → `service.SettleBilling(ctx, relayInfo, summary.Quota)`（`service/billing.go:51`）→ `relayInfo.Billing.Settle()` 或回退 `service.PostConsumeQuota`（`service/quota.go:411`）；
随后 `service/text_quota.go:526` → `model.RecordConsumeLog(...)`。

```go
// service/billing.go:51
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) error

// service/quota.go:411
func PostConsumeQuota(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int, sendEmail bool) (err error)
```

**覆盖面对比（关键）**：

| 候选 hook | file:line | 覆盖的消费路径 |
|---|---|---|
| `SettleBilling` | `service/billing.go:51` | 仅 text/audio（`text_quota.go:451`）——**漏** task、mj、wss、违规费 |
| `PostConsumeQuota` | `service/quota.go:411` | 无 BillingSession 的回退路径 + mj（`relay/mjproxy_handler.go:237,544`）+ 违规费（`service/violation_fee.go:126`）——**漏** BillingSession 主路径 |
| **`model.RecordConsumeLog`** | **`model/log.go:343`** | **全覆盖**：`service/text_quota.go:526`、`service/quota.go:245`（PostWssConsumeQuota）、`service/quota.go:368`、`service/task_billing.go:55`、`relay/mjproxy_handler.go:245,551`、`service/violation_fee.go:150` |

```go
// model/log.go:343
func RecordConsumeLog(c *gin.Context, userId int, params RecordConsumeLogParams)
// params 结构体 model/log.go:328-341，含 Quota int / ModelName / TokenId / Group / ChannelId / Other
// ⚠️ :344-346 开头有 `if !common.LogConsumeEnabled { return }` 提前返回
```

底层落库：`model/log.go:101-104` `func createLog(log *Log) error { ... return LOG_DB.Create(log).Error }`（写的是 `LOG_DB`，可能是独立库甚至 ClickHouse；全局变量见 `model/main.go:53 var DB`、`:55 var LOG_DB`）。
`Log` 结构体 `model/log.go:59-81`（有 `UserId/Type/Quota/ModelName/Group/TokenId/CreatedAt/RequestId/Other`）；日志类型常量 `model/log.go:84-93`（`LogTypeTopup=1`, `LogTypeConsume=2`）。

---

## 八、【扩展点建议】

### 8.1 结论先行：两种口径的最佳 hook 点

**A. 充值后返佣 —— 最佳 hook：`top_ups` 表的 status→success 变更**

推荐方案（按侵入度从低到高）：

1. **零改动方案（推荐）：异步轮询 `top_ups` 表**
   新模块用自己的连接（或复用 `model.DB` 只读）周期扫描
   `SELECT * FROM top_ups WHERE status='success' AND id > :cursor`，
   在**独立 MySQL 库**里建 `commission_record` 表，以 `trade_no` 做唯一索引保证幂等。
   - ⚠️ **不要用 `complete_time` 做游标**：易支付路径（`controller/topup.go:385-408`）从不写 `CompleteTime`，该字段恒为 0。
   - ⚠️ 也不能只用 `id > cursor`：订单先 `pending` 插入、后续才转 `success`，纯 id 游标会漏单。可行做法：`id > 低水位` + 近 N 天窗口重扫 + 新库唯一索引去重；低水位取「新库中尚未见到 success 的最小 topup id」。
   - 优点：**原项目 0 行改动**，天然规避「回调事务未提交就返佣」的问题。缺点：秒级延迟。

2. **准零改动：GORM 全局 callback**
   在新模块 init 里 `model.DB.Callback().Update().After("gorm:update").Register("qy:topup", fn)`，在 fn 内判断 `tx.Statement.Table == "top_ups"`。
   已核对：**全部 6 条路径**对 TopUp 行的写都是 `tx.Save(topUp)` / `DB.Save(topUp)`（含 `model/topup.go:82 UpdatePendingTopUpStatus`），`Statement.Dest` 恒为 `*model.TopUp`，可稳定取到 UserId/Amount/Money/TradeNo/Status。
   - ⚠️ callback 在**外层事务提交前**触发，务必只投 outbox/channel，异步落库，不要同步写新库。

3. **侵入式最小集（6 个文件位点，各 1 行）** —— 若要求实时性 + 强一致：
   - `controller/topup.go:407` 之后（epay）
   - `model/topup.go:157` 之后（stripe `Recharge`）
   - `model/topup.go:389` 之后（`ManualCompleteTopUp`）
   - `model/topup.go:462` 之后（`RechargeCreem`）
   - `model/topup.go:524` 之后（`RechargeWaffo`）
   - `model/topup.go:585` 之后（`RechargeWaffoPancake`）
   统一调用 `affiliate.OnTopUpSuccess(topUp *model.TopUp)`。全部在事务外、日志记录点旁边，插入安全。
   可选再加：`model/redemption.go:184`（兑换码）、`model/subscription.go:638` 和 `:833`（订阅）。

   > 注意：**不要**把 hook 加在 `model.RecordTopupLog` 里 —— 它不接收金额，`Log.Quota` 恒为 0（见第六节末尾），且 Pancake 路径（`model/topup.go:585`）走的是 `RecordLog` 而非 `RecordTopupLog`，覆盖不全。

**B. 消费后返佣 —— 最佳 hook：`model.RecordConsumeLog`，`model/log.go:343`**

这是**唯一一个覆盖全部消费路径的单点**（text / audio / wss / task / midjourney / 违规费，共 6 个调用处，见第七节表格）。改动量 = **1 个文件 1 行**：

```go
// model/log.go:343，建议插在函数体第一行（在 :344 的 LogConsumeEnabled 提前 return 之前）
func RecordConsumeLog(c *gin.Context, userId int, params RecordConsumeLogParams) {
    affiliatehook.OnConsume(userId, params.Quota, params.ModelName, params.Group)  // ← 新增
    if !common.LogConsumeEnabled { return }
    ...
```
必须插在 `:344` 之前，否则管理员关闭消费日志时返佣静默失效。

零改动替代：`model.LOG_DB.Callback().Create().After("gorm:create").Register(...)` 监听 `logs` 表 `Type==LogTypeConsume`。但同样受 `LogConsumeEnabled` 门禁影响，且 LOG_DB 可能是 ClickHouse（批量插入语义不同），**不推荐**，还是走上面那 1 行更稳。

如果只需覆盖普通对话（不含 mj/task），也可用 `service/billing.go:51 SettleBilling` 或 `service/text_quota.go:451`——但覆盖不全，不推荐。

### 8.2 需要改动的原项目文件「最小集」

如果采用「A=轮询 或 GORM callback」+「B=RecordConsumeLog 1 行」，最小集是：

| 文件 | 改动 | 必要性 |
|---|---|---|
| `model/log.go:343` | +1 行 hook 调用 | 消费返佣必需（除非接受 LOG_DB callback 的缺陷） |
| `router/main.go:15-34` `SetRouter` | +1 行 `SetAffiliateRouter(router)`（新文件 `router/affiliate-router.go` 放同包） | 新 API 挂载必需 |
| `main.go:284 InitResources()` 或 `main.go` 主流程 | +1~2 行：初始化独立 MySQL（读 YAML）、启动轮询 goroutine | 必需 |
| （可选）`model/topup.go` + `controller/topup.go` | 6 处各 +1 行 | 仅当放弃轮询、要求实时充值返佣 |

前端最小集：
- **新增路由零改动**：TanStack 是**文件路由**，新建 `web/src/routes/_authenticated/affiliate/index.tsx` 会自动进 `routeTree.gen.ts`，**不需要改任何现有文件**。
- 侧边栏入口需要改 `web/src/hooks/use-sidebar-data.ts`（+ 可能 `web/src/hooks/use-sidebar-config.ts`、`web/src/components/layout/types.ts`）——各 +1 项。
- 若只想「零前端改动」，可把新功能入口挂在 `web/src/features/wallet/index.tsx:342-350` 的 `AffiliateRewardsCard` 旁边（但那要改现有文件），或直接让用户走 URL `/affiliate`。

### 8.3 给架构师的额外提醒

1. **邀请关系只有一层**：`users.inviter_id` 是单层父指针，没有 `path`/`level`。多级分销要么递归查 `users` 表，要么在新库里自建关系闭包表（注册时或首次返佣时回填）。
2. **返佣的「钱」放哪**：现有 `AffQuota` 在主库 `users` 表。若新功能的佣金必须进独立库，则不要复用 `AffQuota`，应在新库建独立余额；「佣金→平台额度」的兑换动作再调 `model.IncreaseUserQuota(id, quota, true)`（`model/user.go:1232`）写回主库，形成清晰的跨库边界（新库扣 → 主库加，用新库的流水表做幂等/对账）。
3. **合规开关**：现有所有邀请奖励都被 `operation_setting.IsPaymentComplianceConfirmed()`（`setting/operation_setting/payment_setting.go:33`）门禁；`controller/user.go:440` 的 `requirePaymentCompliance` 同理。新功能建议沿用同一门禁语义，否则会出现「注册奖励关着、返佣开着」的不一致。
4. **配置模式参考**：`setting/operation_setting/checkin_setting.go` 是最干净的新增配置范例（struct + `init()` 里 `config.GlobalConfig.Register("checkin_setting", &x)`）。但用户要求走**独立 YAML**，所以建议新模块自带 loader，不进 `config.GlobalConfig`，避免与上游 option 表耦合。
5. **签到功能（`controller/checkin.go` / `model/checkin.go` / `setting/operation_setting/checkin_setting.go` / `router/api-router.go:124-125`）是本项目「新增一个独立功能模块」的最佳模仿模板**，可直接照抄其文件分层。
