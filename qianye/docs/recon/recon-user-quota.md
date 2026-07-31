# 用户模型与额度(余额)体系

# 领域勘察报告：用户模型 / 额度（余额）体系

> 所有路径均为绝对路径根 `C:\Users\Administrator\Desktop\qianye\qianye-newapi\`，下文用相对路径书写。

---

## 一、User 结构体（已有）

`model/user.go:79-113`，表名默认 `users`（GORM 复数化，无自定义 `TableName()`）。

```go
type User struct {
	Id               int                        `json:"id"`
	Username         string                     `json:"username" gorm:"unique;index" validate:"max=20"`
	Password         string                     `json:"password" gorm:"not null;" validate:"min=8,max=20"`
	OriginalPassword string                     `json:"original_password" gorm:"-:all"`
	DisplayName      string                     `json:"display_name" gorm:"index" validate:"max=20"`
	Role             int                        `json:"role" gorm:"type:int;default:1"`   // admin, common
	Status           int                        `json:"status" gorm:"type:int;default:1"` // enabled, disabled
	Email            string                     `json:"email" gorm:"index" validate:"max=50"`
	GitHubId         string                     `json:"github_id" gorm:"column:github_id;index"`
	DiscordId        string                     `json:"discord_id" gorm:"column:discord_id;index"`
	OidcId           string                     `json:"oidc_id" gorm:"column:oidc_id;index"`
	WeChatId         string                     `json:"wechat_id" gorm:"column:wechat_id;index"`
	TelegramId       string                     `json:"telegram_id" gorm:"column:telegram_id;index"`
	VerificationCode string                     `json:"verification_code" gorm:"-:all"`
	AccessToken      *string                    `json:"-" gorm:"type:char(32);column:access_token;uniqueIndex"`
	Quota            int                        `json:"quota" gorm:"type:int;default:0"`
	UsedQuota        int                        `json:"used_quota" gorm:"type:int;default:0;column:used_quota"`
	RequestCount     int                        `json:"request_count" gorm:"type:int;default:0;"`
	Group            string                     `json:"group" gorm:"type:varchar(64);default:'default'"`
	AffCode          string                     `json:"aff_code" gorm:"type:varchar(32);column:aff_code;uniqueIndex"`
	AffCount         int                        `json:"aff_count" gorm:"type:int;default:0;column:aff_count"`
	AffQuota         int                        `json:"aff_quota" gorm:"type:int;default:0;column:aff_quota"`
	AffHistoryQuota  int                        `json:"aff_history_quota" gorm:"type:int;default:0;column:aff_history"`
	InviterId        int                        `json:"inviter_id" gorm:"type:int;column:inviter_id;index"`
	DeletedAt        gorm.DeletedAt             `gorm:"index"`          // 软删除
	LinuxDOId        string                     `json:"linux_do_id" gorm:"column:linux_do_id;index"`
	Setting          string                     `json:"setting" gorm:"type:text;column:setting"`
	Remark           string                     `json:"remark,omitempty" gorm:"type:varchar(255)" validate:"max=255"`
	StripeCustomer   string                     `json:"stripe_customer" gorm:"type:varchar(64);column:stripe_customer;index"`
	CreatedAt        int64                      `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	LastLoginAt      int64                      `json:"last_login_at" gorm:"default:0;column:last_login_at"`
	AuthVersion      int64                      `json:"-" gorm:"type:bigint;not null;default:1;column:auth_version"`
	AdminPermissions map[string]map[string]bool `json:"admin_permissions,omitempty" gorm:"-:all"`
}
```

关键点：
- **`users` 表有软删除**（`DeletedAt gorm.DeletedAt`）。默认查询自动过滤已删除用户；`Unscoped()` 才能看到。
- `AuthVersion` 是鉴权版本号，任何改动 password/role/status/group 都会 `+1` 并触发 Redis fence + 会话吊销。**划转余额绝不能碰它**（否则会把对方踢下线）。
- 缓存投影结构体 `UserBase`（`model/user_cache.go:16-27`）只含 `Id/Group/Email/Quota/Status/Role/Username/Setting/AuthVersion/CacheSchema`，`userCacheSchemaVersion = 2`（`model/user_cache.go:14`）。

---

## 二、额度增减核心函数（已有）

### 2.1 对外 API（`model/user.go`）

```go
// model/user.go:1232
func IncreaseUserQuota(id int, quota int, db bool) (err error)
// model/user.go:1249
func increaseUserQuota(id int, quota int) (err error)
// model/user.go:1257
func DecreaseUserQuota(id int, quota int, db bool) (err error)
// model/user.go:1274
func decreaseUserQuota(id int, quota int) (err error)
// model/user.go:1282
func DeltaUpdateUserQuota(id int, delta int) (err error)   // delta>0 走 Increase，<0 走 Decrease
```

`IncreaseUserQuota` 实现（`model/user.go:1232-1247`）：

```go
if quota < 0 { return errors.New("quota 不能为负数！") }
gopool.Go(func() {                       // ① 异步、best-effort 更新 Redis
	err := cacheIncrUserQuota(id, int64(quota))
	...
})
if !db && common.BatchUpdateEnabled {    // ② db=false 且开启批量时，只进内存队列
	addNewRecord(BatchUpdateTypeUserQuota, id, quota)
	return nil
}
return increaseUserQuota(id, quota)      // ③ DB.Model(&User{}).Where("id=?").Update("quota", gorm.Expr("quota + ?", quota))
```

**重要结论（直接影响划转实现）：**
1. **没有事务**。`increaseUserQuota` / `decreaseUserQuota` 都是单条裸 UPDATE，用 `gorm.Expr("quota ± ?")` 保证单行原子。
2. **没有 `lockForUpdate`**，也**没有余额是否足够的校验** —— `DecreaseUserQuota` 可以把 quota 扣成负数。
3. **缓存更新是异步 goroutine + 忽略错误**，且 `RedisHIncrBy`（`common/redis.go:275-300`）在 **key 不存在/无 TTL 时直接静默返回 nil 不做任何事**，靠后续 `GetUserCache` 从 DB 回填。
4. `db=false` + `common.BatchUpdateEnabled=true` 时**延迟落库**（`model/utils.go:42` `addNewRecord`，`model/utils.go:52` `batchUpdate`，最终走 `updateUserQuotaUsedQuotaAndRequestCount` `model/user.go:1336`）。**划转必须传 `db=true`**，否则中间态不可控。

### 2.2 其他额度相关函数

```go
// model/user.go:1129  优先 Redis，miss 落 DB，并异步回写缓存
func GetUserQuota(id int, fromDB bool) (quota int, err error)
// model/user.go:1156
func GetUserUsedQuota(id int) (quota int, err error)
// model/user.go:1309 / 1318 / 1336 / 1353 / 1364
func UpdateUserUsedQuotaAndRequestCount(id int, quota int)
func updateUserUsedQuotaAndRequestCount(id int, quota int, count int)
func updateUserQuotaUsedQuotaAndRequestCount(id int, quota, usedQuota, requestCount int)
func updateUserUsedQuota(id int, quota int)
func updateUserRequestCount(id int, count int)
```

注意 `User.Update` / `UpdateWithTx`（`model/user.go:705` / `:725`）在 `model/user.go:751` 显式 **`Omit("quota","used_quota","request_count","auth_version")`** —— 所以走 `user.Update()` 保存用户对象**不会**改动额度。这是一个很好的安全属性：划转必须显式走 SQL 表达式更新。

### 2.3 缓存层（`model/user_cache.go` + `model/user_auth_cache.go`）

```go
// model/user_cache.go:50
func getUserCacheKey(userId int) string     // "user:%d"
// model/user_cache.go:147   —— 就是你问的 "cacheUpdateUserQuota"
func cacheIncrUserQuota(userId int, delta int64) error   // RedisHIncrBy(key,"Quota",delta)
// model/user_cache.go:154
func cacheDecrUserQuota(userId int, delta int64) error   // = cacheIncrUserQuota(-delta)
// model/user_cache.go:208
func updateUserQuotaCache(userId int, quota int) error   // RedisHSetField(key,"Quota",绝对值)
// model/user_cache.go:63 / 72
func invalidateUserCache(userId int) error
func InvalidateUserCache(userId int) error               // 导出版，供 controller 用
// model/user_cache.go:94
func GetUserCache(userId int) (*UserBase, error)
// model/user_cache.go:76 / 86
func populateUserCache(user User) error   // writeUserCache(.., includeQuota=true)
func updateUserCache(user User) error     // writeUserCache(.., includeQuota=false) —— 刻意不覆盖 Quota
// model/user_auth_cache.go:48
func writeUserCache(user *UserBase, includeQuota bool) error   // Lua 脚本 + auth 版本围栏
```

`updateUserCache` 的注释（`model/user_cache.go:83-85`）明确写道：Quota 由原子增量路径维护，**禁止用整体用户快照覆盖**。这条规则新功能必须遵守。

---

## 三、用户查询函数（已有）

```go
// model/user.go:453  —— selectAll=false 时 Omit("password","access_token")
func GetUserById(id int, selectAll bool) (*User, error)
// model/user.go:467
func GetUserIdByAffCode(affCode string) (int, error)
// model/user.go:1372  优先 Redis
func GetUsernameById(id int, fromDB bool) (username string, err error)
// model/user.go:1033
func GetUniqueUserByEmail(email string) (*User, error)   // 0 条→ErrEmailNotFound，>1 条→ErrEmailAmbiguous
// model/user.go:240
func CheckUserExistOrDeleted(username string, email string) (bool, error)
// model/user.go:384
func SearchUsers(keyword string, group string, role *int, status *int, startIdx, num int, sortOptions ...UserSortOptions) ([]*User, int64, error)
// model/user.go:349
func GetAllUsers(pageInfo *common.PageInfo, sortOptions ...UserSortOptions) ([]*User, int64, error)
// model/user.go:961/969/977/993/1001/1009/1017/1405
func (user *User) FillUserById() / FillUserByEmail() / FillUserByGitHubId() / ...
// model/user.go:1099
func IsAdmin(userId int) bool   // role >= common.RoleAdminUser
// model/user.go:1298
func GetRootUser() (user *User)
```

**❗ 项目中不存在 `GetUserByUsername`。** 唯一按用户名精确查询的地方是 `model/user.go:621`（`DB.Where("username = ?", ...)` 内联）和 `controller/user.go:286`。划转功能若要「按用户名转账」，**需要新建**一个 `GetUserByUsername(username string) (*User, error)`（建议放在你的新包里，用 `model.DB` 查，不改原文件）。

---

## 四、额度单位与换算（已有）

| 项 | 值 | 位置 |
|---|---|---|
| Go 类型 | `int`（不是 int64） | `model/user.go:95` |
| DB 列类型 | `type:int` → MySQL `INT` **有符号 32 位** | `model/user.go:95-96` |
| 上下界常量 | `common.MaxQuota = math.MaxInt32`、`common.MinQuota = math.MinInt32` | `common/quota_math.go:14-17` |
| 单位换算 | `var QuotaPerUnit = 500 * 1000.0` （= 500000 quota = $1） | `common/constants.go:22` |
| 汇率 | `var USDExchangeRate = 7.3` | `setting/operation_setting/payment_setting_old.go:18` |
| 展示类型常量 | `QuotaDisplayTypeUSD/CNY/TOKENS/CUSTOM` | `setting/operation_setting/general_setting.go:7-10` |
| 后端格式化 | `logger.LogQuota(quota int) string`（带"额度"字样）、`logger.FormatQuota(quota int) string` | `logger/logger.go:122` / `:149` |
| 安全转换 | `common.QuotaFromFloat/QuotaRound/QuotaFromDecimal` + `*Checked/*Strict` 变体，全部 clamp 到 int32 | `common/quota_math.go:98-148` |
| 前端换算 | `formatQuota(quota)`、`parseQuotaFromDollars(amount)`、`quotaUnitsToDollars(units)` | `web/src/lib/format.ts:72` / `:83` / `:105` |
| 前端底层 | `formatQuotaWithCurrency(quota, opts)`、`getCurrencyDisplay()`；`config.quotaPerUnit` 默认 500000 | `web/src/lib/currency.ts:496` / `:130-205` |
| 前端拿 quotaPerUnit | 后端 `/api/status` 返回 `"quota_per_unit": common.QuotaPerUnit` | `controller/misc.go:76` |
| 日志行 quota 列 | `Log.Quota int gorm:"default:0"` 同样 int32 | `model/log.go:68` |

**划转必须做溢出防护**：接收方 `quota + amount` 可能超过 `MaxInt32`（2147483647 ≈ $4294.97）。现有代码没有任何接收侧上限检查（`increaseUserQuota` 直接 `quota + ?`），MySQL 非严格模式下会截断、严格模式下会报错。新功能应在事务内校验 `receiver.Quota + amount <= common.MaxQuota`。

---

## 五、额度变动会写什么日志（已有）

日志表 `Log`（`model/log.go:59-81`），写入走 **`LOG_DB`**（`model/log.go:103` `createLog` → `LOG_DB.Create(log)`），`LOG_DB` 可以是独立库/ClickHouse（`model/main.go:212` `InitLogDB`）。

类型常量（`model/log.go:84-93`，注释明确「don't use iota」）：

```go
LogTypeUnknown = 0
LogTypeTopup   = 1   // 充值/兑换码/余额购买订阅 都用这个
LogTypeConsume = 2
LogTypeManage  = 3   // 管理/审计
LogTypeSystem  = 4   // 注册赠送、邀请赠送
LogTypeError   = 5
LogTypeRefund  = 6
LogTypeLogin   = 7
```

写日志的函数签名：

```go
// model/log.go:144
func RecordLog(userId int, logType int, content string)
// model/log.go:163
func RecordLogWithAdminInfo(userId int, logType int, content string, adminInfo map[string]interface{})
// model/log.go:229  写 Other.op = {action, params}，供前端 i18n 渲染
func RecordOperationAuditLog(logUserId int, content string, ip string, action string,
	params map[string]interface{}, adminInfo map[string]interface{}, auditInfo map[string]interface{})
// model/log.go:254  会把 server_ip/node_name/caller_ip/payment_method 写进 Other.admin_info
func RecordTopupLog(userId int, content string, callerIp string, paymentMethod string, callbackPaymentMethod string)
```

普通用户查日志时 `formatUserLogs`（`model/log.go:116`）会剥掉 `Other` 里的 `admin_info` / `audit_info` / `stream_status`。

现有额度变动的日志实例：
- 兑换码：`model/redemption.go:184` `RecordLog(userId, LogTypeTopup, "通过兑换码充值 %s，兑换码ID %d")`
- 在线充值：`controller/topup.go:407` `RecordTopupLog(...)`
- 余额购订阅：`model/subscription.go:833` `RecordLog(userId, LogTypeTopup, ...)`
- 管理员加/减额度：`controller/user.go:1181/1193` `recordManageAuditFor(c, user.Id, "user.quota_add"/"user.quota_subtract", {"quota": logger.LogQuota(req.Value)})`（`controller/audit.go:99`）
- 注册/邀请赠送：`model/user.go:634/639/643` `RecordLog(..., LogTypeSystem, ...)`

---

## 六、现有"转账类"操作先例（完整链路）

### 6.1 兑换码 Redeem（**有并发保护、无缓存同步 —— 有坑，别照抄**）

链路：`router/api-router.go:103` `selfRoute.POST("/topup", middleware.CriticalRateLimit(), controller.TopUp)`
→ `controller/user.go:1362 TopUp` → 进程内 per-user `getTopUpLock(id).TryLock()`（`controller/user.go:1348`）
→ `model/redemption.go:137 Redeem(key string, userId int) (quota int, err error)`

```go
common.RandomSleep()                                   // common/utils.go:297，随机 0-3000ms
err = DB.Transaction(func(tx *gorm.DB) error {
	lockForUpdate(tx).Where(keyCol+" = ?", key).First(redemption)   // SELECT ... FOR UPDATE
	// 状态/过期校验
	result := tx.Model(&Redemption{}).
		Where("id = ? AND status = ?", redemption.Id, common.RedemptionCodeStatusEnabled).
		Updates(map[string]interface{}{"redeemed_time":..., "status": Used, "used_user_id": userId})
	if result.RowsAffected == 0 { return errors.New("该兑换码已被使用") }   // CAS 兜底（SQLite 无行锁）
	return tx.Model(&User{}).Where("id = ?", userId).
		Update("quota", gorm.Expr("quota + ?", redemption.Quota)).Error
})
RecordLog(userId, LogTypeTopup, ...)
```

**⚠️ 坑：`Redeem` 事务提交后没有任何 Redis 缓存同步**（既没 `cacheIncrUserQuota` 也没 `invalidateUserCache`），`controller.TopUp` 也没有。开启 Redis 时兑换后余额在缓存 TTL（`common.RedisKeyCacheSeconds()`，默认兜底 60s，见 `model/user_cache.go:54`）内是陈旧的。**新功能不要复制这个遗漏。**

### 6.2 在线充值 Recharge / EpayNotify（`model/topup.go:109`、`controller/topup.go:373-408`）

`lockForUpdate` 锁 `top_ups` 行 + 状态机（`pending → success`）→ `model.IncreaseUserQuota(userId, quotaToAdd, true)`（`db=true` 强制落库、并异步同步缓存）→ `RecordTopupLog`。

### 6.3 余额购买订阅（**最佳模板，事务 + 行锁 + 余额校验 + 提交后缓存同步**）

`model/subscription.go:746 PurchaseSubscriptionWithBalance(userId int, planId int) error`：

```go
err := DB.Transaction(func(tx *gorm.DB) error {
	...
	var user User
	if err := lockForUpdate(tx).Where("id = ?", userId).First(&user).Error; err != nil { return err }  // :776
	if requiredQuota > 0 && user.Quota < requiredQuota { return errors.New("余额不足") }               // :779
	if requiredQuota > 0 {
		tx.Model(&User{}).Where("id = ?", userId).Update("quota", gorm.Expr("quota - ?", requiredQuota))  // :783
	}
	... 建订单 ...
})
if chargedQuota > 0 {
	if err := cacheDecrUserQuota(userId, int64(chargedQuota)); err != nil { common.SysLog(...) }   // :825 提交后同步缓存
}
RecordLog(userId, LogTypeTopup, msg)   // :833
```

### 6.4 邀请额度转自身额度（**同一用户内部划转，签名最接近你要做的事**）

`model/user.go:503`：

```go
func (user *User) TransferAffQuotaToQuota(quota int) error
```
- 最小额度校验 `float64(quota) < common.QuotaPerUnit`（即至少 $1）
- `tx := DB.Begin()` + `defer tx.Rollback()`
- `lockForUpdate(tx).First(&user, user.Id)`
- `user.AffQuota -= quota; user.Quota += quota` 然后 **`tx.Save(user)`**
- **⚠️ 同样没有 Redis 缓存同步**，且用 `Save` 全字段写回（有覆盖风险）。别照抄。

Controller：`controller/user.go:439 TransferAffQuota`，路由 `router/api-router.go:113` `selfRoute.POST("/aff_transfer", controller.TransferAffQuota)`。

### 6.5 签到（跨库事务 + 提交后缓存增量）

`model/checkin.go:95 userCheckinWithTransaction` —— 事务内 `tx.Model(&User{}).Update("quota", gorm.Expr("quota + ?", quotaAwarded))`，提交后 `go func(){ _ = cacheIncrUserQuota(userId, int64(quotaAwarded)) }()`（`model/checkin.go:117-119`）。另有 SQLite 分支 `model/checkin.go:125`。

### 6.6 管理员直接改额度

`controller/user.go:1170-1214`，`ManageUser` 的 `action == "add_quota"`，三种 `Mode`：
- `"add"` → `model.IncreaseUserQuota(user.Id, req.Value, true)`
- `"subtract"` → `model.DecreaseUserQuota(user.Id, req.Value, true)`
- `"override"` → `model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("quota", req.Value)`（**注意：override 分支完全没有缓存同步**）

---

## 七、权限校验（已有）

角色常量 `common/constants.go:178-183`：

```go
const (
	RoleGuestUser  = 0
	RoleCommonUser = 1
	RoleAdminUser  = 10
	RoleRootUser   = 100
)
func IsValidateRole(role int) bool   // common/constants.go:185
```

用户状态 `common/constants.go:225-228`：`UserStatusEnabled = 1`、`UserStatusDisabled = 2`。

中间件（`middleware/auth.go`）：

```go
func UserAuth()  func(c *gin.Context)   // :92  → authHelper(c, common.RoleCommonUser)
func AdminAuth() func(c *gin.Context)   // :98  → authHelper(c, common.RoleAdminUser)
func RootAuth()  func(c *gin.Context)   // :104 → authHelper(c, common.RoleRootUser)
func TryUserAuth() / TokenAuth() / TokenOrUserAuth() / TokenAuthReadOnly()
func RequirePermission(permission authz.Permission) func(c *gin.Context)   // :226 Casbin 细粒度
```

`authHelper`（`middleware/auth.go:45-76`）逻辑：解析凭证 → `user.Status != common.UserStatusEnabled` 拒绝 → `user.Role < minRole` 拒绝 → `setDashboardAuthContext`。
**并且 `minRole >= common.RoleAdminUser` 时自动挂 `beginAdminAudit(c)` / `finishAdminAudit`（`middleware/auth.go:68-75`）—— 任何挂 `AdminAuth()/RootAuth()` 的新写接口会自动留审计，无需手工埋点。**

Context 键（`middleware/auth.go:194-207`）：`"id"`（int）、`"username"`、`"role"`（int）、`"group"`、`"user_group"`、`"session_id"`、`"auth_version"`；另有 `constant.ContextKeyUserQuota = "user_quota"`（`constant/context_key.go:48`）等。

管理员越权保护辅助函数：`controller/user.go:371 func canManageTargetRole(myRole int, targetRole int) bool { return myRole == common.RoleRootUser || myRole > targetRole }`。

限流中间件：`middleware.CriticalRateLimit()`（默认 20 次/20 分钟，`common/constants.go:206-209`）、`middleware.SearchRateLimit()`（每用户 10 次/60s，`common/constants.go:217-220`）。

---

## 八、重点回答：实现"用户A向用户B划转余额"

### 8.1 必须复用的现有函数

| 用途 | 函数 | 位置 |
|---|---|---|
| 行级锁 | `lockForUpdate(tx *gorm.DB) *gorm.DB` | `model/locking.go:20` |
| 主库句柄 | `model.DB *gorm.DB` | `model/main.go:53` |
| 判断数据库类型 | `common.UsingMainDatabase(common.DatabaseTypeMySQL/SQLite/PostgreSQL)` | `model/locking.go:21` 用法 |
| 查用户 | `model.GetUserById(id int, selectAll bool) (*User, error)` | `model/user.go:453` |
| 查用户名 | `model.GetUsernameById(id int, fromDB bool) (string, error)` | `model/user.go:1372` |
| 缓存增量 | `cacheIncrUserQuota` / `cacheDecrUserQuota`（**未导出**） | `model/user_cache.go:147/154` |
| 缓存失效（导出） | `model.InvalidateUserCache(userId int) error` | `model/user_cache.go:72` |
| 读额度 | `model.GetUserQuota(id int, fromDB bool) (int, error)` | `model/user.go:1129` |
| 日志 | `model.RecordLog(userId, model.LogTypeTopup, content)` | `model/log.go:144` |
| 额度格式化 | `logger.LogQuota(quota int) string` / `logger.FormatQuota` | `logger/logger.go:122/149` |
| 溢出防护 | `common.MaxQuota`、`common.QuotaFromDecimal*` | `common/quota_math.go:15/138` |
| 单位 | `common.QuotaPerUnit`（500000 = $1） | `common/constants.go:22` |
| HTTP 响应 | `common.ApiError(c, err)` / `ApiErrorI18n(c, key, args...)` / `ApiSuccessI18n(c, key, data, args...)` | `common/gin.go:199/223/232` |
| i18n key | `i18n/keys.go`（如 `MsgUserTransferSuccess = "user.transfer_success"`，`i18n/keys.go:105`） | `i18n/keys.go` |

### 8.2 并发安全方案（推荐，模板 = `PurchaseSubscriptionWithBalance`）

```go
// 新建文件，例如 <newpkg>/transfer.go
func TransferQuota(fromUserId, toUserId, amount int, remark string) error {
    if fromUserId == toUserId { return ErrSelfTransfer }
    if amount <= 0 { return ErrInvalidAmount }

    return model.DB.Transaction(func(tx *gorm.DB) error {
        // ① 固定顺序加锁，避免 A→B / B→A 并发死锁
        first, second := fromUserId, toUserId
        if first > second { first, second = second, first }
        var u1, u2 model.User
        if err := lockForUpdate(tx).Where("id = ?", first).First(&u1).Error;  err != nil { return err }
        if err := lockForUpdate(tx).Where("id = ?", second).First(&u2).Error; err != nil { return err }

        sender, receiver := &u1, &u2
        if sender.Id != fromUserId { sender, receiver = &u2, &u1 }

        // ② 业务校验：状态、余额、接收方溢出
        if sender.Status != common.UserStatusEnabled || receiver.Status != common.UserStatusEnabled { ... }
        if sender.Quota < amount { return ErrInsufficientQuota }
        if receiver.Quota > common.MaxQuota-amount { return ErrReceiverQuotaOverflow }

        // ③ 用 CAS 条件 UPDATE（SQLite 无 FOR UPDATE 时的兜底，参考 Redeem 的 RowsAffected 检查）
        r := tx.Model(&model.User{}).Where("id = ? AND quota >= ?", fromUserId, amount).
              Update("quota", gorm.Expr("quota - ?", amount))
        if r.Error != nil { return r.Error }
        if r.RowsAffected == 0 { return ErrInsufficientQuota }

        if err := tx.Model(&model.User{}).Where("id = ?", toUserId).
              Update("quota", gorm.Expr("quota + ?", amount)).Error; err != nil { return err }

        // ④ 流水表写入 —— 这里要用你的独立库（见 8.4）
        return nil
    })
}
```

要点解释：
1. **两把行锁必须按 id 升序加**，否则 A→B 与 B→A 并发会死锁（这是原项目从没遇到过的新问题，没有现成先例）。
2. **不要用 `tx.Save(user)`**（`TransferAffQuotaToQuota` 那种写法会全字段覆盖，可能把并发的其他字段改动写回）。用 `Update("quota", gorm.Expr(...))`。
3. **`WHERE quota >= ?` + `RowsAffected == 0`** 是 SQLite 场景的必需兜底 —— `lockForUpdate` 在 SQLite 上是 no-op（`model/locking.go:21-23`）。
4. **不要用 `IncreaseUserQuota/DecreaseUserQuota`** 完成转账主逻辑：它们不带事务、不校验余额、且 `db=false` 时可能进批量队列。
5. **绝不能触碰 `auth_version`**，也不要调用 `user.Update()`/`user.Edit()`（会触发 `IncrementUserAuthVersionWithTx` → 会话吊销）。

### 8.3 缓存一致性方案

事务**提交成功之后**（不是事务内）执行，二选一：

**方案 A（推荐，与 `subscription.go:825` / `checkin.go:118` 一致）—— 增量同步：**
```go
_ = cacheDecrUserQuota(fromUserId, int64(amount))   // 未导出
_ = cacheIncrUserQuota(toUserId,   int64(amount))
```
但 `cacheIncrUserQuota`/`cacheDecrUserQuota` **是包私有的（`model` 包内）**。如果新代码在独立包里，需要**在 `model` 包新增一个导出薄封装**（新文件，不改原文件），例如 `model/quota_transfer_cache.go`：
```go
func CacheApplyUserQuotaDelta(userId int, delta int64) error { return cacheIncrUserQuota(userId, delta) }
```
这样"改动原有文件"= 0，只是往 `model` 包加一个新文件。

**方案 B（更保守）—— 失效：** 调用已导出的 `model.InvalidateUserCache(fromUserId)` / `(toUserId)`。代价是下一次读会回源 DB 并重建整个 hash（`GetUserCache` → `populateUserCache`，`model/user_cache.go:104-119`），一致性最强。**建议在划转这种低频高价值操作上用方案 B。**

**绝对不要**在划转后调用 `updateUserCache(user)` / `model.PublishUserAuthCache` 去写整个用户快照 —— `model/user_cache.go:83-85` 的设计约定是 Quota 只能由增量路径维护。

**多实例部署注意**：`common.BatchUpdateEnabled`（`common/constants.go:159`）为 true 时，其他节点的消费扣费可能滞后落库；划转用 `DB.Transaction` 直写是正确的（不受批量队列影响），但要接受"划转瞬间的余额快照可能不含尚未 flush 的消费"。这与现有 `PurchaseSubscriptionWithBalance` 的语义完全一致，可接受。

### 8.4 数据落到独立 MySQL 的取舍（重要）

用户 `users.quota` 列**必然在原项目主库**（`model.DB`）。所以：
- **余额加减的两条 UPDATE 只能在主库事务里做**，无法搬到新库。
- **划转流水表（transfer_records）应该建在你的新库**。

于是"扣款 + 加款"（主库事务）与"写流水"（新库）是**跨库两阶段**，无法用一个事务覆盖。推荐落地形式：

1. 在**新库**先插入一条 `status = pending` 的流水（拿到 `transfer_no` 唯一单号）；
2. 在**主库事务**里做 双向 quota 更新（`WHERE quota >= ?` CAS）；
3. 主库事务成功 → 新库流水置 `success`；失败 → 置 `failed`；
4. 提交后同步 Redis 缓存 + `model.RecordLog(fromUserId/toUserId, model.LogTypeTopup, ...)`（日志走 `LOG_DB`，与主库解耦，无需改动）；
5. 补偿：新库对长期停留在 `pending` 的记录做对账任务（可复用 `model/system_task.go` 的 `SystemTaskLock` 思路，但建议在新包自建，避免耦合）。

`transfer_no` 唯一索引 + 前端幂等 token，可防重复提交。也可参考 `controller/user.go:1348 getTopUpLock(userID)` 的进程内 per-user TryLock，做一层轻量防抖（注意它只在单实例内有效）。

---

## 九、【扩展点建议】

### 9.1 需要改动的原有文件 —— 最小集合（共 1~2 处）

**① `router/api-router.go`（必改，1 行）**

在 `SetApiRouter` 的 `{ ... }` 块内追加一行注册调用即可，把你所有新功能全部挂上去：

```go
// router/api-router.go:234 附近，与 registerChannelRoutes(apiRouter) / registerAuthzRoutes(apiRouter) 同级
registerQianyeRoutes(apiRouter)   // ← 唯一需要新增的一行
```

`registerQianyeRoutes(apiRouter *gin.RouterGroup)` 定义在**新文件** `router/qianye-router.go`（同包，无需改任何已有文件的函数体）。这是本项目现成的模式：`registerChannelRoutes` 定义在 `router/channel-router.go`、`registerAuthzRoutes` 定义在 `router/authz-router.go`。**改动一处 = 挂载全部新后端 API**，冲突面 = 1 行。

新文件内部形如：
```go
func registerQianyeRoutes(apiRouter *gin.RouterGroup) {
	g := apiRouter.Group("/qianye")
	g.Use(middleware.UserAuth())
	{
		g.POST("/transfer", middleware.CriticalRateLimit(), qianyectl.CreateTransfer)
		g.GET("/transfer/self", qianyectl.ListSelfTransfers)
		g.GET("/transfer/lookup", middleware.SearchRateLimit(), qianyectl.LookupReceiver) // 按用户名/ID 查收款人
	}
	admin := apiRouter.Group("/qianye/admin")
	admin.Use(middleware.AdminAuth())   // 自动带审计（middleware/auth.go:68-75）
	{
		admin.GET("/transfer", qianyectl.AdminListTransfers)
	}
}
```

**② `main.go` 或 `model` 初始化点（必改，1~2 行）** —— 用于初始化独立 YAML 配置 + 独立 MySQL 连接。理想做法是**利用 Go 的 `init()` + 空导入**，做到 0 行业务改动：在 `router/qianye-router.go`（新文件）里加一行 `import _ "github.com/QuantumNous/new-api/qianye"`，包的 `init()` 里读 YAML 并 `gorm.Open` 自己的 DSN + `AutoMigrate` 自己的表。这样连 `main.go` 都不用改（参照 `router/api-router.go:8` 已有的 `_ "github.com/QuantumNous/new-api/oauth"` 空导入注册模式）。若需要控制启动顺序（在 `model.InitDB()` 之后），再退而求其次在 `main.go` 加一行显式 `qianye.Init()`。

**③ 可选：`model` 包内新增文件（0 行原文件改动）**

若需要导出私有缓存函数，新建 `model/qianye_export.go`（新文件，同包）：
```go
package model
func CacheApplyUserQuotaDelta(userId int, delta int64) error { return cacheIncrUserQuota(userId, delta) }
func LockUserForUpdate(tx *gorm.DB) *gorm.DB { return lockForUpdate(tx) }
```
这是**在不修改任何现有文件的前提下**打通 `model` 包私有能力的唯一干净手段。合并上游时不会冲突（纯新增文件）。

### 9.2 不需要改的（已有可直接复用）

- **鉴权**：`middleware.UserAuth() / AdminAuth() / RootAuth()` 直接用，context 里已有 `"id"` / `"role"` / `"username"` / `"group"`。
- **审计**：挂 `AdminAuth()` 的接口自动留痕，无需埋点。用户侧敏感操作可调 `controller/audit.go:113 recordUserSecurityAudit`（同包）或直接 `model.RecordOperationAuditLog`。
- **日志**：`model.RecordLog(userId, model.LogTypeTopup, content)` 写到 `LOG_DB`，前端"日志"页自动展示，无需改前端日志组件。
- **额度显示**：后端返回原始 `int` quota，前端统一用 `formatQuota()`（`web/src/lib/format.ts:72`）渲染，无需新增换算逻辑。
- **限流**：`middleware.CriticalRateLimit()` / `middleware.SearchRateLimit()` 直接挂。

### 9.3 本领域的风险清单（写方案时务必覆盖）

1. `quota` 是 **int32**，接收侧必须防溢出（`common.MaxQuota`）；现有代码全无此校验。
2. `IncreaseUserQuota/DecreaseUserQuota` **不是事务安全的**，且 `DecreaseUserQuota` 允许扣成负数 —— 划转不能用它们。
3. Redis 缓存同步在项目里是 **best-effort、异步、失败仅记日志**；`RedisHIncrBy` 在 hash 不存在时静默 no-op。划转后建议直接 `model.InvalidateUserCache` 两个用户，最稳。
4. `users` 表**有软删除**，查收款人必须确认 `deleted_at IS NULL`（默认查询已过滤，但别误用 `Unscoped()`）。
5. 双方加锁必须**按 id 升序**，否则 A↔B 互转并发死锁。
6. SQLite 场景 `lockForUpdate` 是 no-op，必须靠 `WHERE quota >= ?` + `RowsAffected` 兜底。
7. 划转路径**禁止**调用 `user.Update()` / `user.Edit()` / `IncrementUserAuthVersionWithTx` / `updateUserCache(user)`，会触发会话吊销或覆盖缓存 Quota。
8. `Redeem`（`model/redemption.go:137`）和 `TransferAffQuotaToQuota`（`model/user.go:503`）都缺缓存同步，是**反面教材**，不要作为模板。正面模板是 `PurchaseSubscriptionWithBalance`（`model/subscription.go:746`）。
