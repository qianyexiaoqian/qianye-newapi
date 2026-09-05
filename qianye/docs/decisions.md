# 决策记录(ADR)

这份文件记的是**项目方拍过板的事**:什么时候、谁拍的、拍的是什么、为什么、
代价是什么。它不记设计细节(那在 `design-*.md`),也不记缺陷(那在 `98-audit-findings.md`)。

## 这份文件为什么存在

因为吃过亏。2026-08-10 项目方拍板「按上游语义办,接受高并发下的透支」,
可这个决定只活在一条 commit message 里。后来的人读代码时看见的是
「预扣不校验余额、余额能被扣成负数」,合理地判定它是缺陷,又改了一遍 ——
改完把有余额的用户 403 误拒了,只能整条撤回。

**一个拍过板的取舍,如果只有拍板的人知道,它就会被反复"修好"。**

所以:

> ### 读这份文件的规矩
>
> 1. 想改下面任何一条涉及的代码之前,**先读对应那一条**。
> 2. 每一条都写了「代价是什么」。看到代价还是想改,那是**新的一次拍板**,
>    要去问项目方,不是自己判断。
> 3. 改了之后回来更新这里,把新的拍板追加成新的一条,**不要删掉旧的** ——
>    旧的那条解释了当初为什么是那样。

---

## D-01 · 预扣是估算,结算无下限 —— 接受透支

| | |
|---|---|
| **时间** | 2026-08-19(2026-08-10 已拍过一次同向的,见 D-06) |
| **谁拍的** | 项目方 |
| **落点** | `relay/helper/price.go`(`preConsumeTokenEstimate`)、`service/funding_source.go`(`WalletFunding.Settle`)、`service/billing_session.go` |

### 拍的是什么

一次请求开始时扣下的「预扣额」是**估算**;请求跑完按真实用量结算,估不足的部分
**无条件补收**。补收走 `model.DecreaseUserQuota`,那是一条裸的
`UPDATE users SET quota = quota - ?` —— 不校验余额、不夹 0、不会因为余额不够而失败。

**所以余额可以被扣成负数。这是刻意的,不加下限。**

### 项目方原话

> 「我看很多 AI 平台都不会有上限的,高并发的情况下都会有透支的吧。
> 性能和准确,高并发环境下只能先考虑性能。」

### 为什么

要让「余额一定不会变负」成立,得做两件事:结算补收改成**会失败**的条件写,
以及把额度变动从批量更新队列里拿出来同步落库。两件都做过,两件都退回了 ——
理由见 D-06。简单说:**在高并发下,严格额度控制的代价是误拒真实付费用户**,
而误拒的损失比透支大。

### 代价(说清楚,别以为是白捡的)

- 余额会被扣成负数。实测:8 路并发打一个只够 1 次的钱包 → 8/8 全过,余额 −140000。
- 欠款靠「用户回来充值」自动抵掉;**弃号跑掉的欠款是净损失**,追不回。
- 单笔也可能透支:客户端不写 `max_tokens` 是 OpenAI 协议的默认用法,
  而模型输出远超兜底值时,预扣那一笔就是不够的。

### 接受不等于不管 —— 配套的三件事(只观测,不改计费)

1. **每一笔**「预扣没兜住真实花费」在消费日志里留
   `other.admin_info.pre_consume_shortfall`(reserved / charged / shortfall)。
   嵌在 `admin_info` 下 = 只有管理员看得见。
   管理端日志详情弹窗直接渲染这一段,日志列表支持「只看预扣不足」筛选。
2. **`GET /api/qy/admin/overdraft`**:现在有多少账号余额为负、合计欠多少、
   最深的是谁。这是运营决定「要不要追欠费 / 封号」的依据。
   只读 —— 处置动作仍在上游用户管理页。
3. **负余额账号下一次请求的文案与「余额不足」分开**:
   「账户已透支, 当前欠费 $X, 请充值补足欠款后再继续调用」。
   这两件对用户不是一回事 —— 欠费的人充值 1 块钱余额仍然是负的,
   不说清楚他会以为是系统坏了。错误码仍是 `insufficient_user_quota`
   (换码会让所有既有客户端集成在这一档上改变行为)。

### 可以改什么、不可以改什么

- ✅ 让**估算更准**(`preConsumeTokenEstimate` 本身)。
- ✅ 让**透支更可见**(上面那三件)。
- ❌ 在结算侧加余额下限 / 把补收改成会失败的操作。
- ❌ 把额度变动从批量更新队列里拿出来。

---

## D-02 · 佣金费率按「上线自己的」用户分组算

| | |
|---|---|
| **时间** | 拍板 2026-08-18,落地 2026-08-19(`2c6a47eaa`) |
| **谁拍的** | 项目方 |
| **落点** | `qianye/modules/commission/grouprate.go` |

### 拍的是什么

返佣费率(topup / consume / redemption 三档)与法币折算比例,一律按
**上线(推广人)自己的用户分组**解析,没配就走兜底档。**与下线的分组无关。**

### 项目方原话

> 「佣金返利最终计算不是按照用户所在的用户分组返利的吗,如果没配置就是兜底,
> 和他的下级没有什么影响的吧。」

### 为什么

改之前费率取的是**下线**账号的分组快照,而法币折算取的是上线分组 —— 两个口径不一致。
更糟的是:下线自己买个套餐换了分组,会**静默改掉上线的返佣比例**。推广人完全不知道
自己的费率被别人改了,而账本每一行仍然自洽、没有任何告警会响。

**费率是推广人自己的等级属性,不该由被推广的人决定。**

### 代价

- 消费日聚合的幂等键里含分组,**上线换组当天要落两行**(前一段归旧费率)。
- 历史 `accrual.rate_group` 是逐行冻结值,**改口径不动存量** ——
  所以老账和新账的费率来源不同,对账时要看行上的冻结值,不能拿当前配置反算。

---

## D-03 · 佣金一日一结算,日界可配

| | |
|---|---|
| **时间** | 2026-08-18(`83f322160`) |
| **谁拍的** | 项目方 |
| **落点** | `qianye/modules/commission/`(`dayline.go`、`qy_commission_settle_run` 表) |

### 拍的是什么

项目方要的是「每天发一次」,不是「每 300 秒发一批」。所以:

- `settle_interval_seconds` 的语义从「结算周期」变成**调度心跳**。每次心跳只回答
  「今天这一次跑过了没有」,没跑过就抢占并**排空整个队列**。默认值刻意没动(300),
  存量部署重启后只经历语义变化,不同时经历数值变化。
- 「今天跑过了没有」落在扩展库的 `qy_commission_settle_run`,一天一行。
- **日界**收进 `commission.day_offset_minutes`,默认 `0` = UTC = 改动前行为。
- **到账日 = 消费日 + holding_days + 1**。那个 `+1` 不是四舍五入,
  是「桶要整天结束才封板」。`holding_days: 0` 也是次日到账。

### 为什么

把周期调大解决不了问题:那样第 501 个人就要等到明天,而且**延后的是谁完全取决于
排序键**,没有任何信号会响。所以改成「一天一次、一次排空」,并且用键集游标分页 ——
两路选人 SQL 都是 `ORDER BY ... LIMIT`,这一轮没发出去的人会原样停在队首,
后面的人一整天一次都轮不到。

日界默认不能换成本地时区:`bucket_date` 已按 UTC 落了几百万行的账,而且它进了
消费幂等键;也不能用 `time.Local` —— 计佣落在任意 relay 节点、结算由租约选主,
各节点 TZ 不同会让同一笔消费进两个桶。

### 代价

- 佣金到账**最长要等将近两天**(当天消费 → 次日结算 → holding_days)。
  前端必须把到账日算出来给用户看,否则会被反复追问。
- 到账日的计算规则只有后端一份(由 `payout_day_offset` 下发),
  **前端不许复刻** —— 两边各算一遍就是界面上写着一个会被追问的错数字。

---

## D-04 · 提现只做佣金扣除,金额由管理员手动发放

| | |
|---|---|
| **时间** | 2026-08-19 |
| **谁拍的** | 项目方 |
| **落点** | `qianye/modules/withdraw/`、`qianye/docs/design-03-withdraw.md` 顶部 |

### 拍的是什么

> 「用户佣金提现只做佣金扣除处理,金额由管理员手动去增加给用户或打款给用户。」

**系统不再自动给任何人一分钱。**

| 步骤 | 谁做 | 系统做什么 |
|---|---|---|
| 申请 | 用户 | 佣金**立刻**离开可用池(available → frozen),落单据 |
| 审核通过 | 管理员 | 单据进「待发放」队列。**不动任何一张账** |
| 驳回(待审) | 管理员 | 佣金原样退回 available |
| 发放 | 管理员**自己** | 去主库加额度 / 线下打款 —— 本模块不参与 |
| 标记已发放 | 管理员 | 校验凭证 + 实发金额复核,佣金 frozen → withdrawn |
| 标记发放失败 | 管理员 | 佣金原样退回 available |

### 为什么

自动兑现要跨两个库(扩展库的佣金账本 + 主库的用户余额),而跨库两阶段的失败面
(资金单、outbox 探针、`paying`/`hold` 中间态、补偿与对账任务)大到不值得 ——
换来的只是省掉管理员点一次按钮。这条口径一拍,**那整条链路被删掉了**。

### 代价 / 不许变的三条

- 发放是人工的,**到账不及时是常态**。用户端必须说清楚「审核通过 ≠ 到账」,
  否则会被当成系统吞钱。
- **单据金额仍然只有一个来源**(佣金账本,与冻结共用同一把行锁同一事务)。
  手动发放不代表数字可以是错的:管理员照着一个错数字打款,和系统自动打错款,损失一样。
- **申请即扣除仍然是硬要求**。管理员是照着单据发钱的,同一笔佣金只要还能再发一单,
  就会被发第二次钱。

---

## D-05 · 扩展功能的数据存进独立库,用独立 YAML 配

| | |
|---|---|
| **时间** | 2026-07-30(2026-08-19 有一条修订) |
| **谁拍的** | 项目方 |
| **落点** | `qianye/db/`、`qianye/config/`、`qianye/docs/design-00-foundation.md` |

### 拍的是什么

fork 新增的全部功能(划转、佣金、提现、违规、抽奖、工单……)**数据不进主库**,
存进一个独立的数据库,连接串与全部开关走独立 YAML(`./data/qianye.yaml`,
env `QIANYE_CONFIG` 覆盖)。配置缺失 → 扩展**静默禁用**,主程序行为与上游逐字节一致。

明确禁止的三件事:

- 不注册进 `setting/config.GlobalConfig`(那会持久化到主库 `options` 表)。
- 不用 `init()` 加载配置(早于 `main.go` 的 `godotenv.Load`)。
- 不调 `common.SetMainDatabaseType` / `SetLogDatabaseType`(全局单例,会破坏上游的列名引号判断)。

### 为什么

当时的首要目标是**合并上游时不冲突**:表不进主库就不会和上游的迁移撞车,
配置不进 `options` 表就不会和上游的设置页撞车。

### 代价

- **跨库一致性要自己做**。凡是同时动主库余额和扩展库账本的动作,都得自己写
  两阶段/补偿/对账 —— D-04 那条口径删掉自动兑现,一半原因就是这个代价太贵。
- 运维多一个库要备份、要监控、要迁移。
- 扩展库不可用时哪些接口该 503、哪些该照常返回,得逐个端点表态
  (`requireCore` 的有无就是这个表态)。

### 2026-08-19 的修订

原始决定写死的是 **MySQL**(`qianye/db/db.go` 硬编码 `mysql.Open`,
`qianye/config/validate.go` 按 DSN 前缀拒绝其他方言,迁移互斥用 MySQL 的 `GET_LOCK`)。
项目方本轮的原话是「你觉得有必要就尝试」,因此扩展库正在放开到 PostgreSQL。

关键事实:**扩展库的 Go 测试本来就跑在 `glebarez/sqlite` 上** ——
GORM 那一层早就是方言中立的,真正 MySQL-only 的面只有上面那三处。

同一次修订还放宽了另一条更早的约束:「改动预算 ≤10 个上游文件 / ≤40 行」
**早已作废**(2026-08-13 实测:57 个上游非测试 `.go` 被改,+1671/−357)。
项目方原话:「现在也不考虑和上游合并冲突了,毕竟改了很多东西,以后只保证中间件
和一些新平台的账号兼容就差不多了。」**不要再拿那个预算当红线。**

---

## D-06 · 撤回「预扣 TOCTOU + 批量队列」那一整轮改动

| | |
|---|---|
| **时间** | 2026-08-10(`5a834f8dd`) |
| **谁拍的** | 项目方 |
| **落点** | `model/user.go`、`model/token.go`、`model/utils.go`、`service/billing_session.go` |

这一条是 D-01 的**前传**。单独留着,是因为它记录了「不知情地改掉一个取舍」
会付出什么 —— 那正是这份文件存在的理由。

### 发生了什么

上一轮把预扣改成了「条件原子 UPDATE + 两个哨兵错误」,并把
`users.quota` / `tokens.remain_quota` 从批量更新队列里拿出来同步落库。
目标是消灭预扣的 TOCTOU 窗口,让额度成为硬上限。

项目方看完之后决定**按上游语义办**,整轮撤回:

> 「年损失在 2000 刀余额都是可以接受的」

上游 issue **#5690** 报过同一批问题,维护者关成 **NOT_PLANNED**:

> 「这是高并发下必须做的取舍……如果你需要严格控制额度,请自行二开」

*(issue 编号与维护者结论来自项目方转述与该次 commit message,本仓内无原文副本。)*

### 为什么必须整条撤而不能撤一半

**预扣同步直写 + 退款走队列是不对称的**,而这正是那一轮引入的误拒 blocker:
有钱的用户被 403。A/B 实测 —— 撤回前第二笔报
「剩余 $0.006000, 需要 $0.054000」,而用户真实余额是 **29972**;
撤回后两笔都 200,flush 之后 29944 与两条 `logs.quota=28` 逐位相符。

### 代价

- 透支按预期回来了,量级已量化:8 路并发打只够 1 次的钱包 → 8/8 全过、余额 −140000。
- 顺序请求下两个上限仍然精确到 1(27000 过 / 26999 拒)—— 也就是说,
  **只有并发才会击穿**,单线程客户端不受影响。
- 给 key 设余额算硬上限,且与资金来源无关(套餐 5,000,000 出资的请求照样被
  `remain_quota=100` 拦死),但有两条结构性旁路:`TrustQuota` 以上的限额 key
  单次请求就能被扣成负数;playground 完全免疫。

### 明确**不在**撤回范围的(与并发无关,撤掉换不回任何性能)

预扣补乘 `CompletionRatio`、订阅结算钳位与钱包补收、realtime Reserve、
扣费顺序写死套餐优先、钱包出资按分组成员资格。逐条实测确认仍然生效。

---

## D-07 · 文本奖兑换码强制加密,按 MAJOR 记

| | |
|---|---|
| **时间** | 2026-08-27 |
| **谁拍的** | 项目方 |
| **落点** | `qianye/config/validate.go`(`validateLottery`)、`qianye/modules/lottery/text_prize.go`(`sealPrizeSecret` / `activePrizeSecretKey`)、`qianye/modules/lottery/prize_secret_backfill.go`、`qianye/version/baseline.txt` |

### 拍的是什么

`lottery.prize_secret_key` 从**可留空**改成**必填**:`lottery.enabled: true` 却没配
这把钥匙的部署,升级之后**拒绝启动**(FATAL,不是降级)。二开版本号因此从
`v0.2.0` 进到 `v1.0.0`(MAJOR)。

原话:

> 「要像提现的 pii_key 那样强制的话,得按 MAJOR 记。加密一下。」

### 为什么

留空的语义是**明文直存**(`key_version=0`、`nonce=NULL`)。
`qy_lot_payout.secret_cipher` 存的是发给中奖者的实际兑换码,与提现的收款账号
(`withdraw.pii_key`)、AI 审核渠道的 api_key(`violation.ai_review_key`)同级,
而后两者从第一天起就是 AES-256-GCM 密文。

列名叫 cipher、内容却是明文,意味着**库备份 / 只读报表账号 / 离线 dump** 三条路上
兑换码全部裸奔;在线侧那一整套控制(`json:"-"` 不下发、列表只回掩码、reveal
强制事由 + 双写审计)对这三条路一条都不起作用。

此前不强制的理由是「不让现存部署在升级那一刻起不来」。项目方判定:一次一次性的
运维动作(生成一串 32 字节随机数)换掉一个默认不安全的形状,值得。

### 代价

- **会有部署起不来。** 判据明确、错误文案点名配置键并给出两条出路(生成密钥 /
  把 `lottery.enabled` 置 false),但它确实是破坏性变更 —— 这就是它记 MAJOR 的原因。
- 密钥**没有也不能有默认值**:硬编码常量等于全站共用一把钥匙(等于没加密),
  每次启动随机生成则重启之后全部历史密文不可读。所以 `applyDefaults` 补不了它,
  运维必须自己填一行。健康面板「整段缺失」的修复片段因此会**当场生成一把随机密钥**
  一并给出,否则照着修复指引粘回去的配置反而起不来。
- 升级**不丢数据**:v0 明文行的读路径永久保留,并由启动后的一次性回填就地转成
  密文(先封装、立刻解回来逐字节比对通过才写库;CAS 恒为 `secret_key_version = 0`,
  只前进不后退)。演示站上实测转换 4 行出款 + 2 行履历,reveal 逐字节回读一致。

### 想改回去之前

「没配密钥就回落成明文」是一行代码的事,而且**不会有任何测试变红**,除非那条
断言还在:`qianye/config/lottery_caps_test.go` 的
`TestValidateLotteryRequiresThePrizeSecretKey` 与
`qianye/modules/lottery/prize_secret_crypto_test.go` 的
`TestPrizeSecretRefusesToWriteWithoutAKey` 就是为此存在的。

---

## D-08 · 分组限流按「用户分组」算,并发上限按节点计

| | |
|---|---|
| **时间** | 2026-08-28 |
| **谁拍的** | 项目方 |
| **落点** | `middleware/qy_rate_limit_export.go`、`middleware/model-rate-limit.go`(一行 hook)、`setting/rate_limit.go`、`router/relay-router.go`、`web/src/features/system-settings/request-limits/` |

### 拍的是什么

原话:

> 「给用户组分配不同的 rpm(并发数),当前 key 的限速策略似乎是跟随全站?
> 你看一下改成根据用户组来限制并发。当前我在速率限制这里,无法添加用户组,
> 当前用户组和原项目是拆开了的,这一点你要进行一次适配。」

两件事:

1. **`ModelRequestRateLimitGroup` 这张表按用户分组查**,不再按令牌上的模型分组查。
2. **新增一张按用户分组的并发上限表** `ModelRequestConcurrencyGroup`,与 RPM 分开存。

### 为什么口径原来是错的

上游 `middleware/model-rate-limit.go` 写的是「令牌分组优先、用户分组兜底」。在上游那套里
`users.group` 同时兼作模型分组,两者同名,这段代码看不出区别。本 fork 把两者拆成了
`qy_user_groups` 与 `qy_model_groups` 两张表、`GetUserGroupOptions` 与 `GetModelGroupOptions`
两个接口,于是它塌成了「模型分组优先」:

- 令牌选了模型分组 → 查的是模型分组名,而运营填的是用户分组名,那一行**静默失效**;
- 令牌是 auto → `ContextKeyTokenGroup` 字面量就是 `"auto"`,**全站 auto 令牌共用一个桶**;
- 只有「令牌没选分组」这一档,兜底分支才碰巧查到用户分组。

而这一页自己的文案从上游起就写的是 "Configure rate limiting rules for a specific user group."
—— 实现与它声明的意图从来不一致,fork 的分组拆分只是把它暴露了出来。

### 代价

- **回落那一次查询是刻意留的**:用户分组没配时,仍按原来那个键再查一次。
  没有它,升级前配好的表会在升级那一刻集体失效 —— 限流是安全设施,静默失效比配错更糟。
- **并发上限按节点计数,不是跨节点的**。同一页上的 RPM 在 Redis 可用时是跨节点的,
  这一条不是,差别写在配置页的提示文案里(`How many requests ... Counted per node.`)。
  理由:并发计数要求每一次 acquire 都有配对的 release,节点崩在请求中途时 Redis 里的
  占位没人归还,那个分组的额度会**永久**少掉几个且无任何迹象;要做对需要带时间戳的
  成员 + TTL 清扫 + Lua 原子化,而本机与目标部署都没有 Redis,那套东西一行都测不到。
  单节点部署下它就是字面意思;多节点下配 N 等于每节点 N。
- **仍有覆盖盲区**:`/suno`、`/mj`、`/kling`、`/jimeng`、`/video` 这几条路由从上游起
  就没挂 `ModelRequestRateLimit`,本轮也只在 `/v1` 与 `/v1beta` 两处挂了并发闸。
  这些端点上按分组的限流与并发都**不生效**。

### 顺带修掉的一处真 data race

`setting/rate_limit.go` 的 `UpdateModelRequestRateLimitGroupByJSONString` 在**写** map 时
拿的是 `RLock()`,而热路径 `GetGroupRateLimit` 同样用 `RLock` 并发读 —— 两个读锁互不排斥。
Go 运行时对并发读写 map 是**直接 fatal**,不是"偶尔读到旧值"。现场是「管理员按下保存
的那一瞬 × 线上每一个 relay 请求」。
回归测试 `middleware.TestRateLimitTableUpdateIsRaceFree` 已验证:把锁改回 `RLock` 立即红。

## D-09 · 「API信息」并入 qy 地址簿,控制台卡片按用户分组过滤

**拍板(2026-08-29,项目方选定):** 站内一度有两张 API 地址表 ——
上游 `console_setting.api_info`(系统设置「API信息」,控制台首页卡片的数据源,
经公开的 `/api/status` 对所有访客一视同仁地下发)与 qy 地址簿
`qy_api_addresses`(密钥页「复制链接信息 / CC Switch」的数据源,按用户分组过滤)。
项目方要求「API 地址可以绑定可见用户组,不绑定就兜底全员可见」,在给出的三个
方案(上游表原地加分组 / 并成一张 / 只用地址簿)里选了**并成一张**。

### 怎么并

- 地址簿补上上游表唯一的增量字段 `color`(14 色白名单,`normalizeColor`,
  空串 = 前端默认色);`route`/`description`/顺序在地址簿本来就有对应物
  (`name`/`remark`/`sort_order`)。
- 控制台「API信息」卡片(`api-info-panel.tsx`)与 overview 首屏的示例端点改读
  `/api/qy/api-addresses`(UserAuth,按分组过滤),与「复制链接信息」同一份
  react-query 缓存。卡片显隐从 `api_info_enabled` 改判「这个用户看得到几条」,
  0 条整块不占位。
- 系统设置「API信息」段换成路牌(`QyApiInfoMovedSection`,零输入控件,
  与 `qy-restricted-notice` 同一条办法):深链接接得住,左侧菜单滤掉。
- 存量三条线路手工迁入地址簿后,`console_setting.api_info` 清成 `[]` ——
  `/api/status` 是公开载荷,留着旧 JSON 等于把已经按分组管控的线路清单
  继续对所有访客公开。

### 刻意不做的

- **不动 `/api/status` 的后端代码**:`api_info_enabled`/`api_info` 字段照常
  下发(空表就是 `[]`),上游的校验、迁移、KV 读写一行不改 —— 改动预算花在
  qy 侧,上游文件只动了三处前端接线(卡片、overview、section 注册表)。
- **不做自动数据迁移**:一次性的三行数据,手工迁完即弃;写一段"启动时发现
  上游表非空就搬家"的魔法,换来的是每个新部署都多一条永远不会再触发的路径。

---

## D-10 · 管理员/创建者禁参与按玩法分野:抽奖放开,竞猜照旧

| | |
|---|---|
| **时间** | 2026-08-29(双色球)/ 2026-08-30(扩展到全部抽奖) |
| **谁拍的** | 项目方 |
| **落点** | `qianye/modules/lottery/eligibility.go`(`Evaluate` 的硬规则)、`web/src/features/qy/pages/lottery/components/lottery-rules-list.tsx`(规则页公示行) |

### 拍的是什么

两天两句原话,方向一致:

> 「对于双色球的活动,这种随机性强的,管理员也可以参加,
> 但是这种竞猜固定的,就管理员活动发起者不可以参加。」(08-29)

> 「随机性强的管理员都能参加。」(08-30,确认豁免覆盖 rank / prob,
> 不只双色球)

此前的硬规则是**全部玩法**一律禁止管理员(`role >= admin`)与活动创建者参与。
现在按 kind 分野:

- **抽奖(kind=draw,含 rank / prob / ball 三种定档方式):管理员与创建者
  可以照常参与。**
- **竞猜(kind=guess):照旧禁止** —— 结果是管理员按外部事实手工录入的
  (`handleSetGuessResult`),让能定答案的人下场对赌,任何密码学都拦不住
  "先买后判"。这条没得商量。

### 为什么抽奖敢放开

三种定档方式用同一套机制:开奖结果由 `FinalSeed(seed, roster_hash)` 推导,
封盘冻结名单之前没人算得出结果;票号(`entry_no`)由服务端 `crypto/rand`
在提交那一刻生成并进名单原像,连持有种子的人也无法预先构造一张"会中"的票
—— 随机性对全场一视同仁。竞猜没有这层结构:答案是人定的,谁定答案谁就不能下注。

### 代价

- **观感风险仍然存在**:协议上算不出结果 ≠ 用户不质疑。一个管理员中了
  头奖,公示的证据链能自证清白,但解释成本是真实的。对冲手段是
  规则页**原样公示**这条分野(`qy_lot_rule_admin_allowed`),而不是让用户
  自己在名单里发现管理员。
- **数据库写权限不在防御范围内**(与整套 commit-reveal 的边界一致):
  能改库的人能改掉任何东西,协议保证的只是不可抵赖地被检出。
- 竞猜的答案录入机制**没有变**:本来就是封盘后由管理员带公开依据录入
  (创建时不需要、也没有地方填答案),逾期未录入自动流局全额退款。

## D-10 · 地址簿的「展示位置」在服务端过滤,总量上限放到 100

**拍板(2026-08-30,项目方要求):** ① 适用分组要在表行内常显,不能只有打开
编辑弹窗才看得见;② 除分组外再加「在哪个位置可见」的自定义 —— 控制台
「API信息」卡片与「复制链接信息 / CC Switch」两个入口可各自勾选;③ 密钥页
复制条的桌面端改成一条线路一张卡(参照项目方给的截图),保留展示上限,
移动端维持原形态;④ 表总量上限从 30 放到 100。

### 关键取舍

- **位置过滤在服务端**(`?surface=console|picker`,`Address.Surfaces` 空串 =
  所有位置,与 UserGroups 同口径):让每个前端消费方自己滤是同一条规则的
  N 份拷贝,新增消费方忘了过滤时没有任何东西会红。不带参数 = 不过滤,
  旧版前端升级期间看到全量,与从前一致;未知位置名 400,拼写错误不该
  静默变成"到处可见"。react-query 键按位置分开(`apiAddresses(surface)`),
  两个位置拿到的是不同子集,混一个键会让先到的那份冒充另一个位置的清单。
- **弹窗里「两个都勾 = 存空串」**:全集与空串今天等价,但存空串让将来新增
  第三个位置时这些行自动跟上;一个都不勾被提交按钮摁住 —— 那是「停用」
  该干的事。
- **桌面卡片有展示上限**(`QY_AA_BAR_MAX_CARDS`,前端常量):表上限 100,
  全铺出来会把密钥列表推到两屏之外;截掉的条数写出来,完整清单在
  「复制链接信息」的选择窗里仍然全量可选。桌面/移动用 useIsMobile 二选一
  渲染而不是 CSS 显隐:两份同文案的按钮同时在 DOM 里,读屏与测试都分不清
  该按哪一个。

---

## D-11 · 星屑:娱乐与邀请返利整体换成独立积分货币,不保留旧数据

| | |
|---|---|
| **时间** | 2026-09-04 |
| **谁拍的** | 项目方 |
| **落点** | `qianye/docs/design-15-stardust.md`(整份)、`qianye/modules/stardust`、`qianye/modules/mall`、`qianye/modules/lottery`(资金路径整体换底) |

### 拍的是什么

> 「其他关键,这些相当于重构,放弃以前的,不需要以前的数据,当前版本未上线,一切都可以更改,开始做吧。」

设计稿 §1 的 15 项拍板里,除 D-I / D-J 两条另有明示(见 D-12、D-13)外,**其余全部按推荐执行**,
并且比推荐更进一步:**不保留额度活动的旧路径,不保留旧数据**。于是 lottery 不再有
`currency` 双路径、不再有在途额度活动收尾、不再有跨库两阶段(资金单 / outbox 探针 /
补偿 / 代次 / held-from-main / 人工仲裁)—— 参与费与派奖全部走扩展库星屑账本,同库单事务。

### 为什么

娱乐竞猜 / 双色球 / 抽奖与邀请奖励直接动 `users.quota`,是一条"平台凭空造钱、没有回收路径"
的资金面(`qianye/modules/lottery/caps.go` 头注释);把它们换到一种**不可划转、不可兑回
余额、不能消费模型**的积分上,风险面从"资损"缩到"积分通胀"。当前版本未上线,没有存量
用户账,所以不做兼容。

### 代价

- 与第一版设计稿相比少了整整一层复杂度,但 lottery 里以资金单为锚点的几处判据
  (退款权威金额、幂等重放、held 的来源)必须换成账本行的锚点,见 design-15 §5.2 / §5.3。
- 已实施部分不再有"回退到额度活动"的路径。要回退只能整体回滚代码。
- 上游注册奖(`QuotaForInviter / QuotaForInvitee`)由运维置 0 关闭,存量 `aff_quota`
  保留划转通道,不折算。

---

## D-12 · 星屑路径保留支付密码

| | |
|---|---|
| **时间** | 2026-09-04 |
| **谁拍的** | 项目方 |
| **落点** | `qianye/modules/lottery/api_user.go`(阈值 `lottery.pay_password_threshold_stardust`)、`qianye/modules/mall`(下单与码揭示)、`qianye/guard/guard.go` 的 `FlagPayPassword` OR 链 |

### 拍的是什么

> 「D-I 支付密码保留」

星屑经商城能换成第三方卡密、实物与文本奖兑换码,是**可变现出平台**的。支付密码守在
星屑离开账号的出口:星屑活动 / 转盘按整批总额过阈值;商城 `code` / `physical` 下单强制验密;
码揭示接口挂验密中间件;`FlagPayPassword` 的 OR 链加 `mall.enabled`。

### 代价

- 与 design-12 裁决 1 的规矩同:接了验密的路径要出现在 `guard.featureOn(FlagPayPassword)` 里,
  否则只开商城的部署里用户连密码都设不了。
- 第一版稿"星屑最多烧在平台活动里"的论证被审查判为不成立,本条是对它的纠正。

---

## D-13 · 转盘不禁止管理员参与

| | |
|---|---|
| **时间** | 2026-09-04 |
| **谁拍的** | 项目方 |
| **落点** | `qianye/modules/lottery/eligibility.go`(硬规则只对竞猜生效,转盘按抽奖类放开) |

### 拍的是什么

> 「D-J 转盘不用禁止,每次抽奖都是服务器随机数据计算在服务器又不是用户本地。」

转盘(`kind=draw, draw_mode=wheel`)与 rank / prob / ball 三种抽奖同档:管理员与创建者可以参与,
竞猜照旧禁止(D-10 不变)。

### 为什么可以,以及不保证什么

转盘的随机源是服务端在发布时承诺的种子 + 活动内单调序号 + 用户自选的 client_seed
(`HMAC(seed, act_no ‖ seq ‖ client_seed)`),结果由服务端算、公示后第三方可逐转复算,
用户本地改不了任何东西。**但它与批次抽奖有一处结构性差别**:批次抽奖的结果由封盘后
冻结的名单哈希混入(`FinalSeed(seed, roster_hash)`),连持有种子的人也无法预构造一张会中的票;
转盘是即时开奖,没有名单可冻结,**能读到种子的人**(扩展库读权限、服务端进程、备份)
可以对自己的下一个 `seq` 离线枚举 `client_seed` 挑出必中的一转,而证据链重算照样 PASS。

项目方判定这属于 D-10 已经排除在防御范围之外的那一档(能改库 / 能跑服务端代码的人),
接受。协议保证的是"服务端按公示公式算了票、不可抵赖地被检出",不保证"内部人不可能中奖"。

### 代价

- 规则页对转盘**不能**复用"开奖结果由随机种子与封盘冻结的名单共同决定,任何人都无法预知"
  那句文案(对转盘是假的),要单独一条只声称"可复算"。
- `loadSeedForSpin` 是种子第一个在揭示前、由普通用户触发的读点,暴露面比现状大;
  由新建的 `seed_guard_test` 钉住它是唯一的热路径读点。
- 一个管理员中了转盘头奖,公示的证据链能自证"按公式算的",但解释成本是真实的。

---

## D-14 · 佣金 + 提现整体退场,邀请收益全部改成星屑

| | |
|---|---|
| **时间** | 2026-09-05 |
| **谁拍的** | 项目方 |
| **落点** | `qianye/modules/invite`(原 `commission` 瘦身改名)、`qianye/modules/stardust`(`settle_invite.go` / `api_invite_user.go`)、`qianye/modules/withdraw` 整个删除 |

### 拍的是什么

> 「佣金重构成星屑版本,不需要兼顾旧的,全部改造;当前没有上线。」

推广人因下线得到的**一切**都是星屑:下线充值返 / 兑换码返 / 注册奖 / 套餐返(D-11 已有),
再加一条新的「下线消费返」(design-15 §4.7,按邀请人自己的分组档,一日一结、与消费返同一次 run)。
现金佣金账本(`qy_commission_*`)、提现申请 / 审核 / 收款账号 / 打款凭证(`qy_withdrawal_*`、`qy_pii_audits`)
整体删除,不迁移、不保留读路径。

### 后果

- **没有现金形态的推广收益**。README 里的 D1「站内额度兑换 + 线下法币打款」作废;design-02 / design-03 只作历史。
- **提现模块删除**:`/api/qy/withdraw/*`、`/api/qy/admin/withdraw/*` 全部消失,`RootActionWithdrawPayeeReveal` 与
  `SecurityProofScopeWithdrawPayeeRead` 一并删掉;支付密码的派生开关改为 `transfer || lottery || mall`。
- **日界迁到 `invite` 段**:`invite.day_offset_minutes` 是星屑日桶、日结与下线日消费报表共用的唯一"一天";
  `invite.inviter_cache_seconds` 同迁。`commission:` / `withdraw:` 两段被 `adoptRetiredCommission` 整段吸收并告警,
  里面写什么都不生效(与 `group_pricing` 同一套处置)。
- 邀请关系(绑定 / 换绑 / 解绑 / 拉黑)、互邀自动拉黑、跨节点缓存失效、下线日消费报表留在 `invite`,
  管理端路由从 `/admin/commission/*` 搬到 `/admin/invite/*`,审计 category 改为 `invite`。
- 用户端「我的推广」四条接口(`/api/qy/invite/summary|invitees|records|invitee-daily`)住在 stardust 包:
  它们读的全是星屑账本,而 `invite` 不能 import `stardust`(方向是 stardust → invite)。
- 演示库里 14 张旧表要手工 DROP(见 `retired_tables.md` 的 D-14 段);`qy_sd_balance` 新增 `invite_carry`,
  `qy_sd_group_rate` 新增 `invite_consume_bps`,新表 `qy_sd_invite_accrual`。

## D-15 · 推广佣金以「星辉」结算,自动入账,与星屑邀请奖励并行

| | |
|---|---|
| **时间** | 2026-09-05 |
| **谁拍的** | 项目方 |
| **落点** | `qianye/modules/commission`(从 git HEAD 恢复"钱"的部分并改成星辉口径,新增 `autocredit.go`)、`qianye/modules/invite`(第二转发槽 / 缓存失效登记 / `UserGroupOf`)、`qianye/model/fund_order.go`(`KindCommissionCredit`)、`qianye/config`(`commission:` 段回来)|

### 拍的是什么

> 「邀请模块,你得重新设计一下,以前是直接返还余额,现在是折算成星辉。推广模块;佣金账本、结算、佣金余额,这些你得改成星辉才行。不再直接结算成现金,规避非法集资,拉人头等闲话。」

追问后两条结论:

1. 星屑侧的邀请奖励(充值返 / 兑换码返 / 注册奖 / 套餐返 / 下线消费返)**全部保留,两条线并行** ——
   同一笔下线消费两边各按各的比例、各记各的账。
2. 佣金余额**到期自动入账**星辉(= 站内余额 `users.quota` 的展示名):没有申请、没有审核、没有现金。

### 后果

- D-14 撤回一半:「邀请人收益只有星屑」不再成立,佣金账本回来,记的是星辉(额度整数);
  `withdraw` 模块与提现表**仍然永久删除**,任何页面不出现「元 / 现金 / 提现 / 打款」。
- 账本链路:计佣(消费日桶 / 充值 / 兑换码,费率按上线自己的分组)→ 持有期满一日一结算进
  `available_quota` → 后台任务 `commission.credit` 对 `available >= min_credit_quota`(默认 1 星辉)的
  余额行按 `max_per_order_quota` 分批开两阶段资金单 `commission_credit`(单号 `CC`):扩展库
  available → frozen → 主库 `users.quota += q` → 扩展库 frozen → credited。Failed 且探针确认主库未动
  才退回 available;其余一律 held,由 `/admin/fund-orders/:no/resolve` 裁决后经 Resolver / 对账收敛。
  一个人同一时刻只允许一张在途单。
- 六张 `qy_commission_*` 表移出退役清单、AutoMigrate 重建(`balance.withdrawn_quota` 改名 `credited_quota`,
  法币列全删,**不迁旧数据**);新表 `qy_commission_credit`。`qy_commission_fiat_rate` 与提现表照旧退役。
- 配置 `commission:` 段回来,费率改成万分比整数(`*_rate_bps`),新增 `credit_interval_seconds` /
  `min_credit_quota`;日界读 `invite.day_offset_minutes`;`guard.FlagCommission` / features 键 `commission` /
  `wallet.show_commission_entry` 回来。
- 依赖方向:`commission → invite`,`stardust → invite`,commission 与 stardust 互不 import。兑换码事件经
  invite 的单槽转发到两个并列槽(`AfterRedeemSuccess` / `AfterRedeemSuccessCommission`),消费 / 任务
  计费两个上游 hook 由 commission 占用(星屑按日整日重算 logs,不用它们)。
- 接口路径与 D-14 之前一致,去掉提现 / 法币 / 关系 / 日消费(后两者在 `/admin/invite/*`):用户端
  `summary`(加 `credited_quota` / `next_credit_at` / `min_credit_quota`)、`records`、新 `credits`;
  管理端 `config` / `group-rates` / `balances` / `balances/adjust` / `records` / `users` / `settle` /
  `settle/rerun` / `clawback` / 新 `credits` / `health`。审计 category `commission`,自动入账每批一条系统审计。

## D-16 · 删掉上游自带的敏感词过滤,词表能力归违规规则一处

| | |
|---|---|
| **时间** | 2026-09-05 |
| **谁拍的** | 项目方 |
| **落点** | 删除 `setting/sensitive.go`、`service/sensitive.go`、`relaykit/dto/sensitive.go`、`web/src/features/system-settings/request-limits/sensitive-words-section.tsx`;`controller/relay.go`、`model/option.go`、`relaykit/types/error.go`、`qianye/modules/availability/outcome.go`、`web/src/features/system-settings/security/*` 各去掉对应几行 |

### 拍的是什么

> 「newapi 自带的敏感词过滤,既然我们已经有违规规则、AI 内容审核了,就把原项目的移除。
> 同时把这 2 个菜单移动到「安全与限制」下:违规规则 / AI 内容审核。」

两件事一起拍,而且必须一起做:上游那一栏原来就长在「系统设置 → 安全与限制」里,
搬进来的两页正好接管它的位置。

### 为什么

上游的敏感词过滤与我们的违规规则管的是同一件事(什么内容不许过),而且是**弱的那一份**:

- 一张全局词表,不分用户分组、不分模型、不分渠道;违规规则是按分组 / 模型分组作用域配的。
- 只有"拦下来"一种处置;违规规则有计数、按类型定阈值、到阈值封号、违规扣费。
- 命中之后只回一个 `sensitive_words_detected`,不落违规台账,运营在后台看不见发生过什么。

两套词表并存的真实代价不是多跑一次 AC 自动机,是**运营改了一处以为改完了**。
上游那张表还带一个出厂默认词 `test_sensitive`,而 `CheckSensitiveEnabled` 默认是 `true`——
新部署站点在什么都没配的情况下就带着一条谁也不知道的拦截规则在跑。

### 代价(说清楚,别以为是白捡的)

- **上游 option 键位彻底消失**:`CheckSensitiveEnabled`、`CheckSensitiveOnPromptEnabled`、
  `StopOnSensitiveEnabled`、`SensitiveWords`、`StreamCacheQueueLength` 不再被读写。
  数据库 `options` 表里的旧行不删也不迁,它们进 `OptionMap` 之后没有任何消费者。
  已经在上游那张表里配过词的站点,**升级后那些词不会自动变成违规规则**,要手工重录一次。
- **`sensitive_words_detected` 这个错误码没了**。API 调用方如果按这个 code 分支处理,
  升级后收到的是违规模块自己的错误。可用率分类里 `client_error` 那一档同步去掉这一项
  (design-06 §分类表)。
- **`CombineText` 的构建条件变窄**:此前"敏感词检测默认开着"顺带保证了它恒被构建,
  现在只剩 `CountToken`。违规模块的 `promptText` 早就有重建兜底(design-07 §6.1),
  行为不变,但那条重建路径会更常走到,`/health` 的 `combine_text_rebuilt_ratio` 会上升。
- `StreamCacheQueueLength` 一并删掉:它属于同一个功能(流式响应替换敏感词)的残留,
  relay 链路本来就没有任何消费者(recon-relay-pipeline.md 已记录)。

### 菜单落点

违规规则、AI 内容审核两页在页面表里写 `settingsSection: 'security'`
(`web/src/features/qy/lib/pages.ts`),挂到上游「安全与限制」折叠项末尾,不再出现在
「扩展设置」里。**「违规类型」没有跟着搬** —— 项目方点名的只有两页,它留在扩展设置。
role<100 的管理员打不开设置抽屉,那一档照旧由根侧栏的兜底折叠项接住(`nav.ts`),
本次改动对它不生效。
