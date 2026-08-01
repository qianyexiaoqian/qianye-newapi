# 需求 3-B · 支付密码(裁决 1)

口径来源:`qianye/docs/design-12-batch6-decisions.md` §裁决 1。与本文冲突一律以它为准。

---

## 0. 保护范围与已知缺口

**保护范围:仅余额划转。** 验密点只有一处 —— 划转的**执行入口**
(`qianye/modules/transfer` 的 `handleCreate`),也就是真正动钱那一次提交。
不是"进入划转页",不是"选中联系人"。

**联系人不构成任何豁免。** 选中联系人只做一件事:把收款人字段填好。
`paypass.Require` 的签名里没有任何可以表达豁免的入参,想加豁免必须先改签名。

**已知缺口(不要用"已保护资金操作"糊过去):**

| 出钱路径 | 本轮是否接入支付密码 |
| --- | --- |
| 余额划转 | ✅ 已接入 |
| **佣金提现**(`qianye/modules/withdraw`) | ❌ **未接入** |
| 充值 / 兑换码 | ❌ 未接入 |

也就是说:一个拿到用户会话的攻击者仍然可以走**提现**把钱转出去,支付密码只挡住了划转。
接入提现只需要在 withdraw 的创建入口加同一行 `paypass.Require`,但那属于 withdraw 模块的
改动预算,本轮不做。

另有两条**部署级**已知限制:

- 邮箱找回的验证码存在上游 `common/verification.go` 的**进程内内存 map** 里
  (与上游注册验证码、找回登录密码同一套)。多节点部署时发码与验码必须落在同一个节点,
  否则验码必然失败。这是复用上游实现带来的既有性质,不是本模块新引入的。
- 验证码比对复用上游 `common.VerifyCodeWithKey`,它是**普通字符串比较**而非恒定时间比较。
  代价被 10 分钟有效期、一次性消费、`CriticalRateLimit` 与 8 位十六进制(43 亿)共同约束住。

---

## 1. 表结构

`qy_pay_passwords`(扩展库,MySQL;测试跑 SQLite)

| 列 | 类型 | 说明 |
| --- | --- | --- |
| `user_id` | int,**主键** | 一个用户至多一份支付密码,天然唯一 |
| `algo` | varchar(16) | 恒为 `bcrypt`。存下来是为了将来能做渐进式重哈希 |
| `hash` | varchar(128) | bcrypt 摘要。结构体上带 `json:"-"` |
| `fail_count` | int | 连续输错次数,由**数据库侧原子自增**维护 |
| `locked_until` | bigint | 锁定截止 unix 秒,0 = 未锁定 |
| `set_at` | bigint | 首次设置时间 |
| `changed_at` | bigint | 最近一次修改(改密 / 找回 / 管理员重置) |
| `updated_at` | bigint | 行更新时间 |

设计要点:

- **不塞进主库 `users`**:锁定状态是高频写字段,混进 users 行会和上游的余额扣减抢同一把行锁;
  而且新功能数据一律进独立库。
- **管理员重置 = 清空 `hash` 但保留行**:保住 `set_at` / `changed_at` 这段历史,
  审计才能回答"这个账号的支付密码被重置过几次"。`is_set` 的判据因此是 `hash != ''`,
  不是"行存在"。

---

## 2. 安全设计

### 2.1 慢哈希:沿用上游登录密码的 bcrypt

上游 `common/crypto.go` 用 `bcrypt.GenerateFromPassword(pwd, bcrypt.DefaultCost)`,
登录密码就存在这套里。本模块沿用同一套,理由:

1. bcrypt 满足硬要求:自带盐、自适应成本、比对恒定时间;绝不明文、绝不 MD5/SHA 系。
2. 换 argon2id 要引新依赖并自行调内存/并行度参数,配错(内存给小)反而弱于 bcrypt 默认成本。
   在没有调参与压测预算的这一轮,"跟一个已经在生产跑的正确实现"比"引入一个可能配错的更强算法"更稳。
3. 与登录密码同 cost,不会出现"登录密码很贵、支付密码很便宜"的木桶短板。

刻意**不直接调** `common.Password2Hash`:那是上游文件,未来合并上游时 cost 可能被静默改掉。
本模块直接调 bcrypt 并把 cost 写在 `hash.go` 里,同时用 `algo` 列记录算法。

### 2.2 响应时间不泄露账号状态

`verify` 的执行顺序是安全性的一部分:

```
① 读行  →  ② 锁定期已过则清零  →  ③ 无论什么状态都跑一次同 cost 的 bcrypt  →  ④ 才按状态分支
```

③ 排在 ④ 前面不是风格问题。反过来写(先判断"没设过密码就早返回")会让"未设置"在几百微秒内
返回、"密码错误"要等几十毫秒 —— 响应时间成了一个不需要任何权限就能读的账号状态探针。
`hash.go` 的 `dummyHash`(进程启动后惰性生成的随机密码摘要)存在的全部意义,
就是让 ③ 在没有真实哈希时也付出同样的时间。

**残余时间差(如实写明):**"密码错误"比另外两条路径多两条短语句(自增 + 回读),
约一次索引写的量级,相对 bcrypt 的几十毫秒是两个数量级以下的噪声。
本接口全程要求登录态,调用方就是账号本人,而本人本来就能从 `/pay-password` 读到这三种状态。

`qy_pay_pwd_not_set` / `qy_pay_pwd_locked` / `qy_pay_pwd_wrong` **对本人可分辨是产品硬要求**:
"首次使用强制设置"需要前端识别出"未设置"才能把用户引到设置页。

### 2.3 错误计数防并发绕过

反面写法(读-改-写)在任何隔离级别下都会丢计数:N 个协程读到同一个旧值、各自 +1 写回,计数只涨 1。
攻击者保持并发就永远到不了阈值。

实现是三条各自原子的语句(`counter.go`),不需要事务也不需要行锁:

```
① UPDATE ... SET fail_count = fail_count + 1                  数据库侧自增,N 并发就是 +N
② SELECT fail_count                                            读回自己落在第几
③ UPDATE ... SET locked_until = ? WHERE locked_until < ?       只延长、不缩短
```

**为什么不用一条 `CASE WHEN` 搞定:** MySQL 的 UPDATE 赋值从左到右求值,右边表达式看到的是
**已被前一个赋值改过**的 `fail_count`;SQLite 与 PostgreSQL 一律用旧值。同一条 SQL
在生产(MySQL)与测试(SQLite)上差一个计数 —— 那正是"测试全绿、线上错一次"的经典形状。

锁定期内的失败**不再计数**:让它继续累加等于给了一条"持续打请求把锁越续越长"的骚扰路径。

### 2.4 fail-closed 与不可取消

- 扩展库不可用 → 验密返回 503,**绝不放行**。回落成放行等于把整套支付密码变成
  "打挂扩展库就能关掉的开关"。
- `Require` 用 `guard.ColdContext(context.Background())` 而不是请求 ctx:
  用请求 ctx 会让**客户端主动断连**取消掉失败计数的写入 —— 攻击者每次发完立刻断开,
  就能无限试密码而计数一次都不涨。
- **没有"关闭支付密码校验"的开关**:一个能关掉验密的配置项本身就是那条绕过路径。
  功能总闸是 `transfer.enabled`,划转整体关掉时这条路径根本不会被执行到。

### 2.5 强度规则

字节长度 ∈ [6, 64](上界给 bcrypt 的 72 字节静默截断留余量,按字节而非字符卡);
不得从头到尾同一个字符;纯数字时不得连续递增/递减。
真正兜住暴力破解的是锁定策略 + bcrypt 成本,强度校验只挡最廉价的那批。

---

## 3. 配置

阈值与锁定时长从 `qy_settings`(**scope = `transfer`**)读:

| 键 | 默认 | 区间 |
| --- | --- | --- |
| `pay_pwd_max_attempts` | 5 | 1 ~ 100 |
| `pay_pwd_lock_minutes` | 30 | 1 ~ 10080(7 天) |

键的**定义与写入侧**在 `qianye/modules/transfer/settings.go`(管理端配置页、白名单、区间校验);
本模块只是**消费方**。没有做成同一份的原因是包依赖方向:`transfer → paypass`(执行入口要调
`Require`),`paypass` 再 import `transfer` 就成环。
两份定义的一致性由 `qianye/modules/paypass/settings_contract_test.go` 盯着 ——
任一侧改了 scope / 键名 / 默认值 / 区间,测试立刻变红。

越界或写坏的值**丢弃回落默认**,不做钳取:被写坏的 `999999` 钳成 100 之后阈值依然高得
等于没有锁定,而回落到 5 只是"运营的微调没生效",损失有界且可解释。

读不到扩展库时同样回落默认值 —— 回落成"不限次数"的话,打挂扩展库就能关掉锁定策略。

---

## 4. API 契约

前缀 `/api/qy`。用户端已挂 `UserAuth`,管理端已挂 `AdminAuth`。

### 4.1 用户端(feature flag:`transfer`)

| 方法 | 路径 | 限流 | 说明 |
| --- | --- | --- | --- |
| GET | `/pay-password` | — | 状态查询 |
| POST | `/pay-password` | `CriticalRateLimit` | 首次设置 |
| PUT | `/pay-password` | `CriticalRateLimit` | 修改(需旧密码) |
| POST | `/pay-password/recover/code` | `CriticalRateLimit` | 往**已绑定邮箱**发验证码 |
| POST | `/pay-password/recover/reset` | `CriticalRateLimit` | 凭验证码重设并解锁 |

只有 GET 不挂关键操作限流:它是纯读、只读自己的状态,前端每次打开划转页都要调。

**GET `/pay-password` 响应 `data`:**

```json
{
  "is_set": true, "locked": false, "locked_until": 0,
  "fail_count": 0, "remaining_attempts": 5,
  "max_attempts": 5, "lock_minutes": 30,
  "set_at": 1785000000, "changed_at": 1785000000
}
```

**POST `/pay-password`** `{"password": "..."}` → `{"is_set": true}`
**PUT `/pay-password`** `{"old_password": "...", "password": "..."}` → `{"is_set": true}`
**POST `/pay-password/recover/code`** 无入参 → `{"email_masked": "a***e@example.com", "expires_in_minutes": 10}`
**POST `/pay-password/recover/reset`** `{"code": "...", "password": "..."}` → `{"is_set": true}`

发码接口**刻意不接受任何入参**。裁决 1 的红线:未绑定邮箱时只提示去绑定,
绝不在这条路径上代为绑定 —— 允许在这里填一个新邮箱再往那个邮箱发码,
等于给了一条"绕过原邮箱改绑"的路,拿到会话的攻击者把找回邮箱换成自己的,
支付密码当场归零。验证码同时绑定 `(userId, email)`,因为上游 `users.email` 并非强唯一。

### 4.2 管理端(feature flag:`core`)

用 `FlagCore` 而不是 `FlagTransfer`:关停划转恰恰是出事时的第一个动作,
那之后才轮到处理用户的锁定申诉 —— 管理端跟着一起 404 的话,运营在最需要它的时候没有工具。

| 方法 | 路径 | 限流 | 审计 |
| --- | --- | --- | --- |
| GET | `/admin/pay-password/:user_id` | — | — |
| POST | `/admin/pay-password/:user_id/unlock` | `CriticalRateLimit` | `pay_password.unlock` |
| POST | `/admin/pay-password/:user_id/reset` | `CriticalRateLimit` | `pay_password.reset` |

两个写动作**成功与失败都写** `qianye/service/audit`:category `admin`、actor `admin`、
带 `target_user_id`、操作原因、以及操作前后的状态快照(白名单字段,绝不含 hash)。
`reset` 强制填原因(破坏性动作),`unlock` 不强制(恢复性动作,强制只会让人填"1")。

目标用户不存在时返回 404 而不是静默"成功":静默成功会让管理员输错一位数字后
以为解锁了张三,而审计里还留着一条成功记录,事后复盘被这条假记录带偏。

**管理员重置不代设新密码** —— 代设意味着管理员在某一刻知道用户的支付密码,
这条路径一旦存在,"支付密码只有本人知道"的前提就不成立,事后也无法自证不是管理员动的钱。

### 4.3 错误码

| code | HTTP | 含义 |
| --- | --- | --- |
| `qy_pay_pwd_not_set` | 403 | 未设置 → 前端引导去设置(首次使用强制设置) |
| `qy_pay_pwd_required` | 403 | 未提交密码 |
| `qy_pay_pwd_wrong` | 403 | 密码错误(已计入锁定) |
| `qy_pay_pwd_locked` | 403 | 已锁定 |
| `qy_pay_pwd_already_set` | 409 | 已设置,请走修改 |
| `qy_pay_pwd_weak` | 400 | 强度不足 |
| `qy_pay_pwd_same_as_old` | 400 | 新旧相同 |
| `qy_pay_pwd_email_unbound` | 400 | 未绑定邮箱 |
| `qy_pay_pwd_code_invalid` | 400 | 验证码无效/过期 |
| `qy_pay_pwd_mail_unavailable` | 503 | 邮件服务不可用 |
| `qy_pay_pwd_user_not_found` | 404 | 管理端目标用户不存在 |

---

## 5. 接入点(给集成者)

### 5.1 后端:划转执行入口一行

`qianye/modules/transfer/handler.go` 的 `createRequest` 加一个字段:

```go
type createRequest struct {
	ToUserId int    `json:"to_user_id"`
	Amount   int64  `json:"amount"`
	Remark   string `json:"remark"`
	ClientRequestId string `json:"client_request_id"`
	Confirm         bool   `json:"confirm"`
	// PayPassword 是划转唯一的验密入参。刻意不参与幂等指纹:
	// 它不是资金要素,重试时重新输一次密码不该被判成"另一笔"。
	PayPassword string `json:"pay_password"`
}
```

`handleCreate` 在绑定请求体之后、调 `create` 之前加一行:

```go
func handleCreate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagTransfer) {
		return
	}
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	// 支付密码:唯一的验密点(裁决 1)。失败时 Require 已写好响应并 Abort。
	// 位置在 create 之前:它必须是"动钱前"的闸门,而不是失败后的补救。
	// 绝不能按收款人是不是联系人分流 —— 联系人只负责把收款人字段填好。
	if !paypass.Require(c, c.GetInt("id"), req.PayPassword) {
		return
	}
	resp, err := create(c, c.GetInt("id"), req)
	...
}
```

import 加 `"github.com/QuantumNous/new-api/qianye/modules/paypass"`。

注意事项:

- `POST /transfer` 路由上**已经**挂了 `middleware.CriticalRateLimit()`,不需要再加。
- `Require` 返回 `bool` 并自行写响应 + `Abort`,形状与同一个 handler 里已有的
  `guard.RequireAPI(c, guard.FlagTransfer)` 一致。它不返回 `error`,
  是为了不让 paypass 的错误类型穿过 transfer 的 `respondErr`
  (那里认不出来,会降级成 500 + "处理失败")。
- 另有 `paypass.Middleware()` 可用(从请求头 `X-Qy-Pay-Password` 取密码),
  供将来接入其他出钱路径。划转走函数调用是因为它的密码随请求体一起提交。

### 5.2 前端:确认弹窗里一格输入

`TransferForm` 的 `QyConfirmDialog` 的 `details` 插槽里插入:

```tsx
<QyPayPasswordField
  value={payPassword}
  onChange={setPayPassword}
  onBlockedChange={setPayPasswordBlocked}
/>
```

并把 `pay_password: payPassword` 加进 `qyCreateTransfer` 的入参、
把 `payPasswordBlocked` 并进确认按钮的禁用条件。

组件位置:`web/src/features/qy/components/qy-pay-password-field.tsx`。
它自己处理三种状态(未设置 → 引导去设置页;已锁定 → 显示解锁时间 + 找回入口;
状态读不到 → 提示服务不可用),调用方只管拿值。
它的 props 里**没有收款人、也没有联系人** —— 压根不知道钱要转给谁,也就没有能力分流。

管理页:`/qy/pay-password`(`web/src/features/qy/pages/pay-password/`),
设置 / 修改 / 邮箱找回都在那里。

---

## 6. 回归防线

| 不变量 | 守它的测试 |
| --- | --- |
| 联系人不构成豁免(行为) | `no_exemption_test.go` `TestContactIsNotAnExemption` 等三条 |
| 联系人不构成豁免(调用点结构) | `no_exemption_test.go` `TestNoContactBasedPayPasswordBranchInRepo`(AST,全仓扫) |
| 并发试密不丢计数 | `concurrency_test.go` `TestConcurrentWrongAttemptsAreAllCounted` |
| 加锁只延长不缩短 | `concurrency_test.go` `TestNoteFailureNeverShortensAnExistingLock` |
| 三条失败路径都付慢哈希成本 | `verify_test.go` `TestVerifyAlwaysPaysSlowHashCost` |
| 锁到期给完整新窗口 | `verify_test.go` `TestVerifyResetsCounterAfterLockExpires` |
| 扩展库不可用时 fail-closed | `verify_test.go` `TestVerifyFailsClosedWhenExtDBUnavailable` |
| 配置键真的接上了(反断链) | `settings_test.go` + `TestVerifyLocksAtThresholdAndRejectsEvenCorrectPassword` |
| 两份配置定义不漂移 | `settings_contract_test.go` |
| 管理端两个动作都写审计 | `api_admin_test.go` |
| 写接口挂了关键操作限流 | `api_test.go` `TestCriticalRateLimitGuardsWriteEndpoints` |
| 找回路径不代为绑定邮箱 | `api_test.go` `TestRecoverRefusesAndNeverBindsEmail` + 前端 `__tests__/pay-password.test.ts` |

结构锁的**已知边界**(如实写明,别把它当完备防线):它抓的是
`if <条件含 contact> { <分支提到支付密码> }` 这个形状。把豁免写成
`if isContact { return }` 之后再调 `Require`(early-return 形状),
或者把变量改名成不含 `contact` 的词,它都看不见 —— 那两种情况靠行为锁与代码评审。
