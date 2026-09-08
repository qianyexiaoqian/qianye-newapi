# 设计 15 · 星屑:独立货币、星屑商城、星屑转盘

> 状态:**待拍板的设计稿**(2026-09-04)。§1 是需要项目方拍板的清单;其余各节按"推荐方案"写,
> 拍板改了哪条就改哪一节。实施前必读 `decisions.md`(D-01/03/04/05/07/10)与
> `99-coherence-review.md`。
>
> 这份稿子经过一轮 8 个镜头的对抗审查(账本并发 / 跨库两阶段 / logs 重算 / lottery 切换 /
> 转盘公平性 / 守卫契约 / 双发口径 / 产品与既有拍板),64 条发现里 62 条 CONFIRMED,已全部折进正文,
> 每一处都带 file:line。审查底稿不进仓库。

## 0. 一句话

新增一种**只在扩展库里存在**的积分货币「星屑」(名称可改):消费站内余额次日按分组比例返、
邀请人在下线充值/用兑换码时按比例得、买套餐一次性返、娱乐活动中奖得、管理员手调得;
**只能**用于娱乐活动(抽奖 / 双色球 / 竞猜 / 转盘)与星屑商城(套餐 / 兑换码 / 实物),
不可划转、不可消费模型、不可兑回余额。娱乐活动从此不再动 `users.quota`。

站内余额的展示单位默认改成「星辉」(上游 CUSTOM 展示模式的默认值,不新增机制)。

---

## 1. 待拍板清单

每条都写了「推荐 / 备选 / 为什么 / 代价」。标 **★** 的不拍板就没法开工;其余按推荐执行,
项目方不反对即视为采用。与第一版稿相比**方向反转**的两条是 D-I(支付密码)与 D-J 里的
管理员参与,理由写在各自条目里。

| # | 事项 | 推荐 |
|---|---|---|
| ★ D-A | 星屑刻度 | **1 星屑 = 1 美元等值额度**(锚点是 `common.QuotaPerUnit`,不是展示汇率);日结时 floor 取整、余数结转 |
| ★ D-B | 消费返的数据源 | **LOG_DB `logs` 表整日重算**(T+1,SET 语义,可重跑),不抢 `QyOnConsumeLog` 单槽 |
| D-C | "一天"的定义 | 共用 `commission.day_offset_minutes`(D-03 已拍板的固定偏移);`dayline` 抽成共享包 |
| D-D | 消费返口径 | 只算 `type=2`,**不冲减退款**;排除违规扣费 / 渠道测试 / 订阅额度出资;透支、注销、封禁账号**暂缓**(best-effort 快照) |
| ★ D-E | 邀请返星屑 vs 佣金 | **并行叠加**;订阅付费单按 `trade_no` 精确识别、**不走**充值返(只走套餐返);关系级风控(拉黑 / 互邀 / 成熟期)与佣金**共用同一份判据** |
| ★ D-F | 套餐返的触发与基数 | 新增上游 hook `QyOnSubscriptionGranted`(四处各一行);基数 = `plan.PriceAmount`;来源闭集 `{order, balance, admin, redemption}`,默认 `order,balance` 返;`stardust`(商城自购)**硬排除** |
| ★ D-G | 上游注册奖(aff_quota) | 运维把 `QuotaForInviter` / `QuotaForInvitee` 置 0;星屑侧新增 `QyOnUserRegistered`(默认 0 = 关)并**沿用** `IsPaymentComplianceConfirmed` 合规门;存量 `aff_quota` 保留「转入余额」通道;邀请人数改 `COUNT(users.inviter_id)` |
| ★ D-H | 娱乐活动切换策略 | `stardust.enabled=true` ⇒ **新建活动一律星屑**;双色球活动的币种**由所绑系列继承**,不再允许新建额度系列,存量额度系列开完池子再关;哈希协议 **`lot-v3` 是一次完整的版本抬升**(六处分派点),不是"三个函数加一段" |
| ★ D-I | 支付密码 | **保留**,守在"星屑离开账号"的出口:星屑活动/转盘按整批总额过 `pay_password_threshold_stardust`;商城 `code` / `physical` 下单强制验密;码揭示两个 GET 挂验密中间件。理由:星屑经商城可变现(第三方卡密、实物),第一版稿"最多烧在平台活动里"的论证不成立 |
| ★ D-J | 转盘 | lottery 第四种定档方式 **`kind=draw, draw_mode=wheel`**(即时开奖 + 期次承诺揭示),复用奖档 / 派奖 / 文本奖 / 证据链;**管理员与创建者禁参与**(这是 D-10 的例外:即时开奖没有名单混入,持种子者能预构造必中的一转);库存耗尽 → 同事务封盘 |
| ★ D-K | 商城三类商品的履行 | 兑换码 = **预存密文码**(纯扩展库;运营口径:**不得**上架本站余额码);实物 = **人工发货**(D-04 形态);套餐 = **跨库两阶段**(twophase 新 Kind + 商城自带对账任务 + Resolver + PostCommit + root 裁决端点),且依赖 `two_phase.main_outbox_enabled=true` |
| D-L | 管理员手调星屑 | 提到**超管**(RootActionGate),与抽奖人工落账同档:手调是净增发 |
| D-M | 「星辉」 | **不改任何默认值**:星辉 = 运维在设置页把展示模式切 CUSTOM / 符号「星辉」/ 汇率 1(runbook ③)。备选:改 Go 一处默认,但它会翻转**所有没持久化过展示类型**的存量部署且不受 `stardust.enabled` 约束,与 D-N「逐位一致」冲突,详见 D-M 条目 |
| D-N | 版本 | `v2.0.0`(MAJOR,D-11:不留旧路径与旧数据);「升级注意」四条见 §14 |
| D-O | 硬约束 | 星屑 `available` **恒 ≥ 0、不过期、不可划转、不可兑回余额**;代价:星屑可经商城兑换码 / 实物 / 文本奖**变现出平台**,所以 D-I 必须守出口 |

### D-A · 刻度

**推荐**:1 星屑 = `stardust.quota_per_unit` 额度,默认取 `common.QuotaPerUnit`(500000 = $1)。
消费返按 decimal 全精度累计,结算时 floor 成整数星屑,余数结转到下一日
(`commission.computeSettlement` 的形状,`qianye/modules/commission/settle.go:50-88`)。
刻度冻结在每一行日桶上(`quota_per_unit` 列),运营改 `QuotaPerUnit` 不追溯。

**锚点是美元等值,不是展示汇率**:「星辉」是上游 CUSTOM 展示模式下的一个符号 + 汇率
(`web/src/lib/currency.ts:353`),`custom_currency_exchange_rate` 允许改成任意 ≥ 0.0001 的值。
汇率为 1 时"1 星屑 = 1 星辉"恰好成立;运营把汇率改成 7 之后,用户端会看到
「昨日消费 7 星辉 → 已发 1 星屑」。所以用户端文案写"按美元等值 1:1",管理端配置页
把 `quota_per_unit` 与当前展示汇率并排显示。

**备选**:星屑与额度同刻度(1 星屑 = 1 额度)。1:1 时是整数,但用户看到的是"1,234,567 星屑",
商城标价一份 $10 套餐要写 5,000,000。

**代价**:小额用户当天不足 1 星屑时当天拿 0,余数累积到够 1 再发;前端要把"待结算余数"
讲清楚,否则会被追问"我昨天消费了怎么没有"。

### D-B · 消费返数据源

两条路都在仓里有先例:佣金走 `QyOnConsumeLog` hook + `guard.HotAsync`
(`qianye/modules/commission/hook.go:41-67`),抽奖走 logs 低水位扫描 + 整日重算
(`qianye/modules/lottery/spend.go`)。

**推荐 logs 整日重算**:

- 需求本身就是"次日结算",实时捕获换不来任何用户可见的东西。
- 整日重算是 SET 语义、幂等、**可重跑**;hook 路径是有界队列满即丢
  (`qianye/guard/guard.go:255-265`),丢的是用户看得见的星屑,且不可追。
- 不碰 `model.QyOnConsumeLog` 单槽变量(已被 commission 独占,lottery 当初刻意回避,
  `qianye/modules/lottery/module.go:19-21`)。
- 1:1 返意味着**每个用户的每一笔**都是事件,hook 路径事件量是佣金的数倍。

**代价与前提(审查后订正)**:

- 排除口径在 SQL 里表达:违规扣费 / 渠道测试 / 订阅出资三条都只在 `logs.other`(TEXT)里。
  三条 `LIKE` 的模式是常量、不含用户输入,写法照 `model/log.go:554-556`
  (`logs.other LIKE ?` + `%"key":value%`,四种日志库都用裸 LIKE,不加 ESCAPE、不加 LOWER;
  ClickHouse 不接受 ESCAPE 的原因见 `buildLogLikeCondition` `model/log.go:44-61`)。
  查询**不读** `logs.group`,不涉及 `QyLogGroupCol`。
- 性能前提要写准:第一版稿引用的"447 万行单日 418ms"来自 `idx_qy_logs_daily_consume`
  覆盖索引,那是 **commission 私有**、由 `lease.Run("commission.logs_index")` 后台补建、
  只在 `commission.enabled=true` 时启动(`qianye/modules/commission/module.go:89-124`、
  `logs_index.go:390-403`)。上游只有 `idx_created_at_type` 能范围收窄不覆盖,同文件实测
  单日基线 3915ms,读 `other` 后再放大。所以:① 这条索引的补建要从 commission 的开关下
  拆出来(§4.6);② 结算 SQL 自带 `dailyConsumeQueryTimeout` 形状的 ctx 截止,索引缺失时
  超时报 `partial` 而不是拖住 LOG_DB;③ 它是租约节点一天一次的后台任务,分钟级预算可接受。
- 关闭消费日志(`LogConsumeEnabled=false`)的部署**没有消费返**;写进配置注释与健康面板。
- logs 保留期 ≥ 2 天即可(结算最迟在日界后 `settle_delay_minutes`,重试同日内);
  ClickHouse 的 `LOG_SQL_CLICKHOUSE_TTL_DAYS` 若 ≤ 1 会静默少算,健康面板给告警。
- `settle_delay_minutes` **兜的是 relay 节点写日志的时钟偏差与日志库副本 / ClickHouse 摄入延迟**,
  不是"任务补扣窗口":`logs.created_at` 是写入时刻(`model/log.go:382`、`:462`),任务的
  差额补扣与退款永远落在结算当日,`[dayStart(D), dayStart(D+1))` 在 D+1 开始那一刻就已封口。
  默认值降到 30。
- 任务首次扣费与 MJ 日志**不写** `billing_source`(`service/task_billing.go:39-56`、
  `service/log_info_generate.go:422-431`),订阅出资的这两类消费排不掉。佣金的
  `exclude_subscription_consume` 有同一个盲区,但佣金那边两道开关默认都关
  (`qianye.example.yaml:250`),星屑这里默认**开** —— 盲区从"可选功能里的"变成"默认账里的",
  这条要写进 YAML 注释,不要再写"两边一致"。

### D-D · 口径(审查后订正)

**退款不冲减**。第一版稿的"Σ(type=2) − Σ(type=6)"有两个真问题:① `type=6` 不只任务退款 ——
额度活动的流局退款(`qianye/modules/lottery/payout.go:252/576`)与违规退款
(`qianye/modules/violation/api_admin.go:1125/1182`)都经 `QyRecordLedgerLog` 写 `type=6`,
而它们的借方分别是 `LogTypeSystem`(参与费,`entry.go:796`)与已被 LIKE 排除的罚款行,
减进去就是从基数里扣一笔从没加过的钱;② 跨日退款被按日下夹 0 静默蒸发。
与佣金日消费报表(`api_daily_consume.go:211`)、令牌今日用量(`token_usage_api.go:36`)、
抽奖 `SpendDaily`(`spend.go:171/376`)三处现行口径对齐:**只算 `type=2`**,base 天然 ≥ 0。
代价:预扣多退少的任务会多返一点,与 `SpendDaily` 同一取舍。

可选(不在本期):`stardust.refund_clawback`,形状照 `commission.refund_clawback`,
打开时任务退款(`other` 含 `task_id`)按退款当日进桶、允许 `carry` 为负抵未来返还,
`available` 仍恒 ≥ 0。

**暂缓(held)是 best-effort 的策略闸门,不是账务判定**。`users.quota` 的读取是锁外一次快照:
`BATCH_UPDATE_ENABLED` 下可能滞后一个刷新窗口(默认 5s,`model/utils.go:33-38`),D-01 下
在途请求的补收 / 退还交错可使余额在秒级内瞬时为负。误标只影响发放时点不影响金额;
用户端把状态命名为「待复核(账户余额为负)」而不是「欠费」。注销(软删)与封禁账号同样暂缓
(`hold_reason = account_removed / account_disabled`),账号恢复即随下一次结算发放。

### D-E · 与佣金的关系(审查后订正)

同一笔"下线充值 100 元"会同时触发佣金(钱)与星屑(积分)。**推荐并行**,运营想要"只发一种"
把另一边比例置 0 即可。

**必须互斥的是同一事件的两条星屑路径**:订阅付费单在 `top_ups` 里也有一行
(`upsertSubscriptionTopUpTx`,`model/subscription.go:1559-1595`),它的 `trade_no` **就是**
`subscription_orders.trade_no`。星屑的充值扫描对 `trade_no` 在 `subscription_orders` 里存在的行
**跳过**(精确关联,不再用"provider 空且 Amount=0"这种实现细节做判据),订阅只走套餐返(§4.4)。

**关系级风控共用**:佣金的一次性计佣(`accrueOneShot`,`hook.go:247-297`)在充值口径之外还有两道门 ——
自邀拒绝(`:258`)与 `blockedInvitees`(管理端拉黑 + `ensureRelation` 对互邀环路自动 blocked,
`:261-267` → `accrual.go:369-475`)。星屑三条邀请路径都过**同一份**判据:commission 导出
`InviteeEligible(ctx, inviteeId) (inviterId int, ok bool)`,内部按 `accrueOneShot` 的顺序做
resolve → 自邀拒 → `ensureRelation`(保留互邀自动拉黑,**不依赖 `commission.enabled`**)→ blocked 拒。
否则"佣金拒了、星屑发了"就是双标,而星屑能换套餐,漏洞从积分升级成商品。
**不套** `min_invitee_age_hours`:它只作用于佣金的消费日聚合(`hook.go:141-146`),一次性事件在
佣金侧本来就不看成熟期,星屑与之同口径;要给星屑邀请奖加成熟期是独立拍板项,不借"同源"之名。

### D-F · 套餐返(审查后订正)

第一版稿打算扫 `user_subscriptions`,不可行:同组续期只 UPDATE 既有行的 `end_time` 并返回
**同一条**订阅(`applyUserGroupPurchaseRulesTx`,`model/subscription.go:1120-1160`),
`id` 低水位看不到续期、幂等键撞同一个 id;且该表没有状态跃迁与 `complete_time`,
`id > low` 单向游标在 order 路径的长事务下会永久漏行;`subscription_orders` 与 `user_subscriptions`
之间也没有任何引用列。

**改为新增 hook** `model.QyOnSubscriptionGranted(g model.QySubscriptionGrant)`,纯新增文件
`model/qy_stardust_export.go` 声明(默认 no-op),在四条购买路径的**事务提交之后**各插一行:
`CompleteSubscriptionOrder`(`:1543-1557`)、`PurchaseSubscriptionWithBalance`(`:1806-1821`)、
`AdminBindSubscription`(`:1704-1709`)、`Redeem` 的套餐码分支(`model/redemption.go:395-402`)。
载荷:`{UserId, PlanId, SubscriptionId, Source, Outcome (new|extended|unchanged), TradeNo, RedemptionId, PriceAmount, Money}`。
`Outcome=unchanged` 是「永久组已持有、订单成交但不发货」那条分支(`subscription.go:1116-1119`):
钱已收,照返;运营人工退款时同步手调星屑,写进代价。hook 体只做内存判定后 `guard.HotAsync`
(购买是低频事件,丢弃面可接受,记 skipped 计数)。
配 hookpoint 守卫(形状照 `model/qy_log_hookpoint_test.go`):四个调用点存在 + 默认实现 no-op。

基数 = `plan.PriceAmount`(售价,美元)→ 星屑 = `floor(price × bps / 10000)`(1 星屑 = $1 时);
`order` 来源用 `Money`(快照)优先。来源**闭集** `{order, balance, admin, redemption}`,
`PUT /admin/stardust/plan-rewards` 校验 `sources ⊆ 闭集`,未知值(含 `stardust`)一律 400;
商城自购(§6.3,`source="stardust"`)由 stardust 直接处理且**无条件不返**(否则
"花 N 星屑买套餐 → 返 N 星屑"是自供回路)。默认 `buyer_bps=10000, inviter_bps=0`。

幂等键按来源取唯一量:`order`/`balance` → `sub:<trade_no>`;`redemption` → `sub:rd<redemption_id>`;
`admin` → `sub:admin<subscription_id>:<granted_at>`(默认不返)。

### D-G · 上游注册奖

上游的邀请奖励只有注册那一刻的固定额(`model/user.go:704-714`、`761-770`),进
`users.aff_quota`,靠用户自己划转进余额。要"不再用通用余额做邀请奖励":

1. 运维在系统设置把 `QuotaForInviter` / `QuotaForInvitee` 置 0(`common/constants.go:124-126`)。
   副作用:`aff_count` 停止累计(它只在那个分支里 +1,`model/user.go:545-558`),推广页的
   「邀请人数」改成 `COUNT(users WHERE inviter_id = ?)` —— **不要**读 `qy_invite_relation`,
   那是首次计佣才懒建的快照,行数远少于真实绑定。
2. 星屑侧新增 `QyOnUserRegistered(userId, inviterId)`,在 `finishInsert`(`:704` 那个 `if` 块**之后**)
   与 `FinalizeOAuthUserCreation`(`:761` 块之后)各插一行**无条件**调用 —— 上游只多一行、不多一个分支,
   守卫最简单;`inviterId == 0` 与合规判定都在 stardust 侧做。四条建号路径:Register / 微信经 `finishInsert`,
   OAuth 经 `FinalizeOAuthUserCreation`,管理员 `CreateUser` 经 `FinishInsert(0)` 也到达同一行、`inviterId=0` 不发,
   root 账号 bootstrap 不经任何一处。配 hookpoint 守卫(`qianye/stardust_hookpoint_guard_test.go`,
   形状照 `subscription_hookpoint_guard_test.go:39-139`:两处各恰好一次调用 + 默认实现函数体为空)。
   **合规门(需拍板)**:推荐星屑侧三个邀请类正值(`invite_topup_bps` / `invite_redeem_bps` / `invite_register_stardust`)
   与上游同门 —— 有效值解析处在 `!operation_setting.IsPaymentComplianceConfirmed()` 时按 0 处理,
   `PUT /admin/stardust/config` 对这三个键收到正值时镜像 `controller/option.go:164` 的判据直接拒绝,
   健康面板提示「合规未确认,邀请类星屑按 0 生效」。理由:星屑能在商城换套餐,有真实价值。
   顺带记进 `decisions.md`:fork 已落地的 commission(可提现的真钱)与 withdraw 对这道门**零引用**,
   `design-02 §12.6` / `design-03` 曾建议沿用而实现没做 —— 两个模块的口径要在同一条目里写清,
   不能星屑过门、佣金不过门却没人知道。
3. 存量 `aff_quota` **不折算、不清零**,`/api/user/aff_transfer` 保留。上游推广卡**并不**自己
   按存量隐藏 —— `affiliate-rewards-card.tsx` 的 `hasRewards` 只控制「转入余额」按钮,三个数字
   (待使用收益 / 累计收益 / 邀请人数)是无条件渲染的,所以 `QuotaForInviter` 置 0 之后每个人的
   「我的推广」顶部仍会挂一张 `0.00 星辉` 的"邀请返利"卡,与"邀请返在本站是星屑"直接打架。
   门开在 qy 宿主 `pages/affiliate/components/referral-program-card.tsx`:`aff_quota <= 0` 时整张卡
   (含转账弹窗)不渲染,上游文件一行不改;邀请链接与下线数在本页别处已各有落点,不会跟着消失。「邀请人数」的落点:改 commission
   `getSummary` 的 `invitee_count`(`api_user.go:37-42`)从 `qy_invite_relation` 换成
   `model.DB.Model(&model.User{}).Where("inviter_id = ?", uid).Count`(GORM 自动带软删过滤,与管理端
   `api_admin_users.go:233-238` 同口径),前端零改动;新口径通常 ≥ 旧 `aff_count`(含从未消费的下线),
   被解绑的不再计入时可能 < 旧值(`aff_count` 不回退),上线说明写明。

### D-H · 娱乐活动切换(审查后订正)

现状:参与费从 `users.quota` 扣(`qianye/modules/lottery/entry.go:731-777`)、派奖加回
`users.quota`(`payout.go:206-238`),全部走跨库两阶段。切成星屑后**同库**,两阶段整套对新活动
不再需要,但它同时承担着**幂等重放**(§5.2)与**退款权威金额**(§5.3)两个职责,要给替代物。

**活动币种**:`qy_lot_activity.currency`(存量回填 `quota`)。`stardust.enabled=true` 后
创建 / 发布接口拒绝 `currency<>'stardust'`。

**系列(双色球)**:`qy_lot_series.currency`,活动币种**由所绑系列继承**而不是请求体决定;
`stardust.enabled` 时 `handleCreateSeries` 拒绝新建额度系列。这样存量额度系列能继续开期把
滚存池子发完 —— 第一版稿"新建活动一律星屑"会把一个 `pool_quota>0` 的额度系列永久钉死
(`pool_quota` 唯一减少路径是 publish 时的 `claimSeriesPool`,`series.go:188-230`;
`settleSeriesPool` 每期把未派出部分滚回,`:242-267`),唯一能按的是 `handleCloseSeries`
「滚存作废」(`:539-543`)。旧 twophase 分支的存活期与"最后一个额度系列关闭"绑定。

**收尾判据**:健康面板与 runbook 同时看三个数:额度活动在途 0、资金单未决 0、
`qy_lot_series WHERE currency='quota' AND status='open'` 为 0。

**备选**:一刀切 —— 关闭全部额度系列并公示滚存作废、取消并全额退款在途额度活动。

**哈希协议 `lot-v3`**:见 §5.4。

### D-I · 支付密码(方向反转)

第一版稿写"不要,盗号者最多把积分烧在平台自己的活动里"。这句在稿子自己的形态里不成立:
商城兑换码可以是任意第三方卡密(一次揭示)、实物寄到盗号者填的地址、转盘与抽奖的文本奖
同样是兑换码 —— 星屑**可变现出平台**。而现存代码给抽奖加验密的理由正是同一威胁模型
(`entry.go:192-195`),D-07 又把文本奖兑换码定为与提现收款账号同级的密文。
`UserCriticalRateLimit` 是节流不是授权:默认 20 次 / 20 分钟按**请求**计
(`middleware/rate-limit.go:204-213`、`common/init.go:129-131`),抽奖一次请求最多 999 注,
且 `CRITICAL_RATE_LIMIT_ENABLE=false` 时整体失效。

**推荐**:
- 星屑活动 / 转盘:新键 `lottery.pay_password_threshold_stardust`(0 = 关,默认给非 0 推荐值),
  按整批总额判(沿用 `api_user.go:945-958`"多注按总额"的规则)。
- `POST /mall/orders` 对 `kind=code|physical` **强制** `paypass.Require`;`plan` 可选(订阅落在
  本人账号上,带不走)。
- 码揭示两个 GET(`/mall/orders/:no/code`、现存 `/lottery/my/prizes/:payout_no`)挂
  `paypass.Middleware()`(`gate.go:58-70` 已提供请求头形态),**并且**登记 `sensitiveReads`
  留痕 —— 后者只是台账不是闸门,两者都要。
- `guard.FlagPayPassword` 的 OR 链加 `Mall.Enabled`(`guard.go:114-119`)。漏了这条的后果不是
  "前端不渲染密码框":支付密码的五个设 / 改 / 找回接口全部 `RequireAPI(FlagPayPassword)`
  (`paypass/api_user.go`),只开 mall 不开 lottery/transfer/withdraw 的部署里用户根本无法
  设密码,每一单死在 `qy_pay_pwd_not_set`。

**若项目方仍要免密**:那是新的拍板,D-O 与 `decisions.md` 必须把代价写成事实:
「星屑可经商城兑换码 / 实物 / 文本奖变现,盗号损失上限 = 星屑余额 + 当时可取的码库存;
仅有的防线是按请求计的限流,可被一个环境变量整体关掉」。

### D-J · 转盘(管理员参与方向反转)

需求原话:"管理员设定全服池子奖品中奖率、中奖数量,用户可以使用星屑参加抽奖(全站有抽奖公式,保证公平性)"。

现有 `draw_mode=prob` 已经是"按概率定档、超募均分",但它是**批次**模型(封盘 → 冻结名单 →
揭示种子 → 统一摇号)。转盘的产品直觉是"点一下立刻看到结果"。

**推荐**:lottery 第四种定档方式 **`kind=draw, draw_mode=wheel`**,**即时开奖 + 期次承诺揭示**(§7)。
选 `draw_mode` 而不是新 `kind`,是 `model.go:115-121` 自己的明文指引("加一列 draw_mode 则整条生命周期
一行都不用改"):创建入口 `api_admin.go:1447` 只放行 draw/guess、spec 构建 `:1564-1587` 只在 KindDraw 下、
三处奖档漂移检测(`lifecycle.go:524/1074/1310`)、`fillProofSpec`(`api_proof.go:356`)、
`api_user.go:515` 的 spec 展示、`acceptAmount`(`entry.go:474`)、`CommitHash` 原像里的 kind 分量,
全部随 `kind=draw` 自动覆盖。奖档、派奖计划行、文本奖履行队列与加密、匿名证据链、管理端列表 / 红点 /
审计分类原样复用;概率轴 `Bands` / `RollPpm` 是现成纯函数(`commit.go:532-586`)。
仍需逐条加 `draw_mode` 分支的清单见 §7.5。

**管理员与创建者禁参与 —— 这是 D-10 的例外,不是"D-10 不变"**。D-10 放开抽奖的理由是
协议层的(`decisions.md` D-10:结果由 `FinalSeed(seed, roster_hash)` 推导、票号由 `crypto/rand`
进名单原像,"连持有种子的人也无法预先构造一张会中的票"),边界只把 DB 写权限排除在外。
转盘票面 `HMAC(seed, act_no‖seq‖client_seed)` 没有名单混入、没有服务端随机量,凡是能读到
种子的人对下一个 `seq` 离线枚举 `client_seed` 就能挑出必中的一转,证据链重算照样 PASS ——
这正是 `commit.go:351-357` 的 `FinalSeed` 注释与 `lifecycle.go:18-32` 选时攻击铁律要防的形状。
单人即时开奖没有任何 commit-reveal 形状能对抗"既知服务端秘密又下场的人"(第一版审查里
"每 seq 一份秘密 + 哈希链预承诺"也挡不住生成链的人),唯一真实防御是**策略排除 + 缩小种子暴露面**。
残余风险照 D-10 的格式写进 `decisions.md`:能读扩展库 / 跑服务端代码的人仍可用小号 grinding,
协议只保证不可抵赖地被检出。

**代价**:`draw_mode` 分派点仍有十来处要加分支(§7.5);大厅选择夹被测试钉死为 4 张
(`pages-table.test.ts:324`),转盘做成**独立页面**、不进大厅 lane;规则页公示行现有文案
"开奖结果由随机种子与封盘冻结的名单共同决定,任何人都无法预知"(`zh.json:1695`)对转盘是假的,
wheel 要单独一条 i18n 键,只声称"可复算"不声称"不可预知"。

**备选**:独立模块 `wheel`。解耦但要复制文本奖那套加密与 D-07 密钥。

### D-K · 商城履行(审查后订正)

| 商品 | 履行 | 动主库? |
|---|---|---|
| 兑换码 | 管理员把码**预存**进商城库存(AES-GCM 密文),下单即从库存取一枚,用户在订单页揭示(验密) | 否 |
| 实物 | 下单扣星屑 → 待发货 → 管理员填单号发货 / 标记失败退星屑 | 否 |
| 套餐 | 扩展库扣星屑 + 落单 → 主库发订阅 → 扩展库标完成;**商城自带对账任务 + Resolver + PostCommit + root 裁决端点** | **是**,twophase 新 Kind |

D-04 删掉提现自动兑现的理由是"跨库失败面大到不值得,换来的只是省管理员点一次按钮"。
商城套餐是**用户自助**、高频、金额固定,人工发放会把商城做成工单系统。lottery 的派奖已经在
跑同一套两阶段。**但"资金单侧零改动"不等于"商城侧零改动"**:twophase 把"Failed 之后要不要退"
明确外包给模块对账任务(`compensate.go:322-323/602-603` 注释),transfer / lottery / violation
三个模块各自带了 `reconcile`,violation 当年就是缺 Resolver 才卡在 `charged`。§6.3 把这三件套
写全。**这条要项目方点头**,因为它与 D-04 的精神相反。

**兑换码预存的口径**:码内容平台不解析、不能识别。运营口径:**不得上架本站余额兑换码**
(那是"星屑 → 码 → `users.quota`"隔了一跳,绕过 D-O 的"不可兑回");写进 D-O 代价与管理端上传页文案。
不走"主库生成 `redemptions` 行":那是第二条跨库路径,且 `redemptions.user_id` 是发码人、
`Quota` 列对套餐码是 100 的死数据(`model/redemption.go:44-49`)。

**备选**:套餐也走人工(扣星屑 → 管理员在上游"绑定套餐" → 标记完成)。

### D-M · 星辉(审查后订正)

今天的"货币单位"是「展示模式 + 符号 + 汇率」(`setting/operation_setting/general_setting.go:13-33`)。
CUSTOM 模式渲染成 `星辉 10.50`,汇率 1 时 1 星辉 = $1。机制已在,差的只是默认值 —— 但
第一版稿写的"三处默认"里,前端 store 默认只覆盖 `/api/status` 到达前的一瞬,section-registry 的
`?? '¤'` 只在 options 响应缺键时生效而 `GlobalConfig.Register` 后每个键都会导出,两处是死代码。
**真正生效的只有 Go 一处**(`general_setting.go:30-31`)。

改这一处的连带(全新安装才吃到;已保存过设置的站点以主库 `options` 为准):
① 消费日志 / 兑换码日志 content 变成「星辉0.001234 额度」(`logger/log.go:141`,符号紧贴数字再加"额度");
② 模型定价页与用量日志列走 `formatBillingCurrencyFromUSD`,印「星辉 0.15」;
③ `getCurrencyLabel` 的 8 个调用点全部变「星辉」,其中 `channels-columns.tsx:405` 是**上游渠道余额**(美元);
④ OpenAI 兼容的 `/dashboard/billing` 在 CUSTOM 下走 USD 分支、不吃自定义汇率,只在汇率恰为 1 时与页面一致。

**审查后再校正一层**:`pricing-section.tsx:118-131` 只保存改过的字段,`InitOptionMap` 只填内存不落库,
所以改 Go 默认的生效范围是**所有从没改过展示类型的部署**(不只全新安装),而且不受 `stardust.enabled`
闸门约束 —— 与 D-N / §14「段缺失或 `enabled:false` 时逐位一致」直接冲突;持久化的旧键
`DisplayInCurrencyEnabled` 还会在加载时把它同步回 USD/TOKENS(`model/option.go:409-418`),
`AllOption` 无 ORDER BY,两行并存时谁赢看行序 —— 在存量库上连"可靠生效"都做不到。

**推荐(零代码)**:不改默认;runbook 第 ③ 步手动切,新部署也照做;演示站已保存过设置,本来就要手动切。
**备选**:只改 `general_setting.go:30-31` 一处,§14 升级注意必须逐条写明:生效范围、`DisplayInCurrencyEnabled`
行序问题、上面四条可见连带,并给 D-N 开例外或改 MAJOR。
另:D-A 的「1 星屑 = 1 星辉」只在 `custom_currency_exchange_rate ≡ 1` 时成立,配置页给提示。

**不覆盖的**:i18n 里几十处"额度 / Quota"字样。那是一次跨 7 个 locale 文件的上游文案改造,不在本设计范围。

---

## 2. 货币定义与不变量

| 项 | 取值 |
|---|---|
| 单位名 | `qy_settings(scope=stardust, k=name)`,默认「星屑」;随 `GET /api/qy/config` 匿名下发 |
| 刻度 | 整数;1 星屑 = `quota_per_unit` 额度(冻结在每行日桶上,默认取 `common.QuotaPerUnit`) |
| 精度 | 账本 `bigint`;日桶累计 `decimal(30,10)`;余数结转 `decimal(30,10)`;比例 bps 整数 |
| 上界 | 单笔与余额 ≤ `common.MaxQuota`(2^43)。星屑数值比额度小五个数量级,沿用同一算术上界;`Credit` 走 headroom CAS 保证(§3.3);所有换算走 `common.QuotaFromDecimalChecked` |
| 下界 | **`available` 恒 ≥ 0**。扣减一律条件 UPDATE 断言 `RowsAffected == 1`。星屑不是钱,没有 D-01 的透支理由;`carry` 落 `[0,1)` |
| 不可划转 | 没有任何"用户 → 用户"的接口;账本 kind 枚举里没有 transfer |
| 不可兑回 | 没有任何"星屑 → users.quota"的路径;商城不允许上架"余额"商品;运营不得预存本站余额码 |
| 不过期 | 本期不做;将来要做也是一条独立的 `expire` kind |

---

## 3. 账本(扩展库,模块 `stardust`)

### 3.1 表

**`qy_sd_balance`** —— 每人一行,唯一加锁点(`db.LockForUpdate`)。

| 列 | 类型 | 说明 |
|---|---|---|
| `user_id` | int PK | 主库 `users.id` 软引用 |
| `available` | bigint | 可用星屑,恒 ≥ 0 |
| `total_earned` | bigint | 累计获得(**不含**手调) |
| `total_spent` | bigint | 累计花掉(毛额,不因退款回冲) |
| `total_refunded` | bigint | 累计退回 |
| `total_adjusted` | bigint | 累计手调净额(带符号) |
| `carry` | decimal(30,10) default 0.0000000000 | 消费返结算余数,`[0,1)` |
| `hold_reason` | varchar(32) | 暂缓原因(`overdraft` / `account_removed` / `account_disabled`),空 = 正常;该用户 held 桶归零时清空 |
| `updated_at` | bigint | |

**一个 kind 只落一列**(§3.2 表),恒等式因此闭合:

- I0:`available = total_earned − total_spent + total_refunded + total_adjusted`
- I1(最强,账本只追加):per-user `available == Σ qy_sd_ledger.amount`,并链式核对最后一行的 `balance_after`
- I2:per-user `Σ qy_sd_accrual.gross WHERE status='settled'` == `Σ ledger.amount WHERE kind='consume_rebate'` + `carry`

三条都进 `GET /admin/stardust/ledger-check`,输出照 commission 的形状
(`drifted_users / worst_user_id / worst_drift`,`api_admin.go:1374-1474`,沿用 `maxLedgerCheckUsers` 上界)。

**`qy_sd_ledger`** —— 流水,只追加,每一次余额变动一行。

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | bigint PK | |
| `ledger_no` | varchar(32) uk `uk_qy_sdl_no` | `SD` + UTC 时间 + 序列 + 随机(`twophase.NewOrderNo` 形状) |
| `user_id` | int idx | |
| `kind` | varchar(32) | 见 3.2 |
| `amount` | bigint | 带符号 |
| `balance_after` | bigint | 写入时的余额快照 |
| `idem_scope` / `idem_key` | varchar(32) / varchar(96) uk `uk_qy_sdl_idem` | 幂等键,>96 字节 sha256 折叠(`commission/accrual.go:117-123`) |
| `ref_type` / `ref_no` | varchar(32) / varchar(64) idx | 关联单据:`lot_entry`/票号、`lot_payout`/出款号、`mall_order`/订单号、`sd_settle`/`run_date`、`topup`/充值单号 … |
| `act_no` | varchar(32) idx | `lot_*` 行冗余活动号(其余空串):活动删除后仍能按活动归拢流水;`qy_sd_ledger` **永不随活动删除** |
| `peer_user_id` | int | 邀请返时的下线 id;其余 0 |
| `rate_bps` | int | 冻结的比例(适用的 kind) |
| `rate_group` | varchar(64) | 冻结的分组名(groupns 残留登记为 **keep**,同 `accrual.rate_group`;该列名不在守卫扫描集合内,登记只为文档) |
| `base_quota` | bigint | 基数(消费额 / 充值额 / 售价换算额) |
| `remark` | varchar(255) | 手调事由等 |
| `operator_id` | int | 手调的操作人;系统 0 |
| `created_at` | bigint | |

**`qy_sd_accrual`** —— 消费返日桶。

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | bigint PK | |
| `user_id` + `bucket_date` | uk `uk_qy_sda_bucket` | `bucket_date` varchar(8) `YYYYMMDD`,日界见 D-C |
| `user_group` | varchar(64) | 结算那一刻读到的 `users.group`(§4.1);残留登记 **keep** |
| `rate_bps` / `quota_per_unit` | int / bigint | 冻结 |
| `base_quota` | bigint | Σ(type=2),天然 ≥ 0 |
| `gross` | decimal(30,10) default 0.0000000000 | `base × rate_bps / 10000 / quota_per_unit` |
| `status` | varchar(16) | `computed` → `settled` / `held` |
| `ledger_id` | bigint | 结算后指向流水行;`net=0` 的桶 `settled` 且 `ledger_id=0` |
| `hold_reason` | varchar(32) | held 时的原因 |
| `computed_at` / `settled_at` | bigint | |

**`qy_sd_group_rate`** —— 按用户分组的比例(空 = 未配置,回落全站默认)。

| 列 | 类型 | 说明 |
|---|---|---|
| `user_group` | varchar(64) PK | 归一化小写(`groupname.Normalize`) |
| `consume_bps` / `invite_topup_bps` / `invite_redeem_bps` | int null | 指针三态:null = 未配 |
| `enabled` | bool(不写 default 标签) | false = 整行回落全站 |
| `operator_id` / `updated_at` | | |

分组名做键 ⇒ 模块内必须调 `groupns.RegisterResidue`(`usergroup_residue_coverage_test.go:116-123`
会因 `qy_sd_accrual.user_group` 命中而强制),形状**照 `commission/residue.go`**:`Probe` 报 `clean`,
`Sweep` 里按 `rename` 分支 `Update("user_group", Normalize(to))` / `Delete`,绝不并入迁移目标
(`rewrite` 的定义是"删除时也跟着改成迁移目标",与本表要的相反)。第一版稿"`qy_commission_group_rate`
改名后是孤儿"是误读:那张表的 Sweep 同样 Update 改键(`residue.go:107-129`)。
改名撞键:`PUT /admin/stardust/group-rates` **只接受已登记于 `qy_user_groups` 的分组名**
(commission 的 `adminPutGroupRate` 不查,那是它的既有隐患,本表不重蹈),让"费率行挂在未登记名字上"
不可能出现;Sweep 里 Update 撞 PK 返回带表名的错误,groupns 报 StageCleanup 半成状态
(用户已迁走,不可能整体回退)。`stardust` 追加进守卫的 `knownKeyedModules`。

**`qy_sd_plan_reward`** —— 套餐上的星屑返还定义(扩展库 per-plan 附表;不给主库加列,
`qianye/modules/subscription/model.go:13-16`)。

| 列 | 类型 | 说明 |
|---|---|---|
| `plan_id` | int PK | 主库 `subscription_plans.id` 软引用 |
| `buyer_bps` / `inviter_bps` | int | 0 = 不返;10000 = 按售价 1:1 |
| `sources` | varchar(64) | 逗号分隔,**闭集** `{order, balance, admin, redemption}` 的子集,默认 `order,balance` |
| `updated_at` / `operator_id` | | |

套餐删除时级联删本表行(挂进 `qianye/modules/subscription/delete.go:314` 的 cascadePlan)。

**`qy_sd_settle_run`** —— 一日一结算的运行记录,形状照 `qy_commission_settle_run`
(`settle_daily.go:47-100`),唯一索引 `run_date`;不与佣金共用表。

游标进共享 `qy_kv`:`stardust.topup_low_water` / `stardust.topup_late_water`。
**键名与 commission 的不同**,两模块各自推进,任一方失败不钉住对方。

### 3.2 流水 kind 与落列

| kind | 符号 | 累计列 | 幂等键 | 触发 |
|---|---|---|---|---|
| `consume_rebate` | + | `total_earned` | `sdsettle:<run_date>:<user_id>`;管理端重跑 `sdrerun:<D>:<user_id>:<发起时刻>` | 日结(每人每次运行一行) |
| `invite_topup` | + | `total_earned` | `topup:<trade_no>` | 充值扫描 |
| `invite_redeem` | + | `total_earned` | `redeem:<redemption_id>` | 兑换码 hook |
| `invite_register` | + | `total_earned` | `register:<invitee_id>` | 注册 hook(默认关) |
| `plan_buyer` / `plan_inviter` | + | `total_earned` | `sub:<trade_no>` 等(D-F) | 订阅 hook |
| `lot_stake` | − | `total_spent` | `idem_scope='lot_stake'`, `idem_key=Entry.IdemKey`(§5.2) | 报名 / 投注 / 转一次 |
| `lot_prize` / `lot_refund` | + | `total_earned` / `total_refunded` | `lotpay:<payout_no>` | 派奖 / 退款 |
| `mall_order` | − | `total_spent` | `mall:<order_no>` | 下单 |
| `mall_refund` | + | `total_refunded` | `mallrf:<order_no>` | 发货失败 / 取消 / 两阶段失败 |
| `manual` | ± | `total_adjusted` | `manual:<operator>:<client_request_id>` | 管理员 |

### 3.3 写账的唯一入口

包内导出两个函数,**所有**模块(lottery / mall)都只经它们动账:

```
stardust.Credit(tx, Posting{UserId, Kind, Amount>0, IdemScope, IdemKey, Ref..., ...}) (inserted bool, err)
stardust.Debit (tx, Posting{...Amount>0...})                                     (inserted bool, err)
```

协议(审查后订正,第一版稿把 `db.UpsertHead` 当成 DoNothing 引用了,那是 DO UPDATE 的头部):

1. `lockBalance(tx, userId)`:seed 一行再 `db.LockForUpdate`(`commission/settle.go:636-647` 形状)。
2. 锁内快速失败:`Debit` 先比 `bal.Available < amount` → `ErrInsufficient`;`Credit` 先比
   `bal.Available > MaxQuota − amount` → `ErrOverflow`。此时什么都没写。
3. `tx.Clauses(clause.OnConflict{Columns: [idem_scope, idem_key], DoNothing: true}).Create(&ledger)`,
   `inserted = RowsAffected == 1`(三方言下 DoNothing 的 inserted 判定精确;DO UPDATE 命中时
   MySQL 返回 2、PG 返回 1,不能用,`accrual.go:275-305`)。`inserted=false` 表示重放,**不再动余额**。
4. 仅 `inserted` 时做条件 UPDATE:`Debit` `SET available = available − ? WHERE user_id = ? AND available >= ?`;
   `Credit` `SET available = available + ? WHERE user_id = ? AND available <= ? − ?`(`common.MaxQuota`);
   同一语句按 §3.2 表更新对应累计列;`RowsAffected ≠ 1` 返回错误。
5. **契约**(写进函数注释、`stardust/doc.go` 与 AST 守卫):本函数返回任何非 nil error 时调用方
   必须让整个扩展库事务回滚;绝不能吞掉 `ErrInsufficient` 后继续提交 —— 否则第 3 步的幂等行
   残留,重试被判成重放而余额未动。
6. 金额校验:`0 < amount ≤ common.MaxQuota`,超出直接错(不截断)。

**锁序(合库后第一次出现,不是既有顺序)**:今天余额锁在主库事务②、活动行锁在扩展库事务③,
现有代码里没有任何一个事务同时持有两把锁。合库后规则写成可守卫的形式:

- 同一事务里**活动行锁必须先于余额行锁**(报名:`reserveEntry` → `Debit`)。
- 持余额行锁的事务只允许在活动 `status ∉ {published}` 时再碰活动行;派奖 worker 的事务里
  `addPaidPayoutToActivityTotals` 那条 UPDATE **挪到 `Credit` 之前**(A→U),从结构上消灭反向顺序。
- 转盘的星屑奖在转动事务里**直接以 `paid` 落行**(§7.2),永远不进 `DrivePayouts` 的扫描集。
- AST 守卫:lottery 包里不得出现"先 `Debit` 后 `reserveEntry`"、"`Credit` 之后再 UPDATE Activity"、
  "进入 `reserveEntry` 前没有按 `idem_key` 的 `qy_lot_entry` 读"。
- 并发测试(必须跑在真 MySQL,glebarez/sqlite 没有行锁):同一用户两路 goroutine 连转同一转盘 +
  `DrivePayouts` 循环,断言零 1213、且 `draw_mode=wheel` 活动没有 `planned` 行。

---

## 4. 获得途径

### 4.1 ① 消费站内余额 → 次日结算

**数据源**:LOG_DB `logs`(`model.QyLogDB`),`type = 2`,`created_at ∈ [dayKeyStart(D), dayKeyStart(D)+86400)`
(固定偏移下一天恒 86400 秒,`dayline` 已有按日键取起点的入口),按 `user_id` 聚合 `SUM(quota)`。
ClickHouse 日志库同一条 SQL 可跑(不依赖自增 id、不 JOIN)。只借 `spend.go:365-406` 的
DELETE+INSERT 整日重算**形状**,**不借它的日界**(`dayBucket/dayRange` 是 `time.Local`,抄过来会撞
`serverday` 单实现守卫且与 D-C 冲突)。

**排除**(与佣金 `hardExcluded` / `subscriptionConsumeExcluded` 同一批键名,`commission/hook.go:73-111`):

- `other LIKE '%"violation_fee":true%'`
- `other LIKE '%"channel_test":true%'`,兜底 `token_name = '模型测试' AND token_id = 0`(常量 `model.ChannelTestTokenName`)
- `other LIKE '%"billing_source":"subscription"%'`(开关 `stardust.exclude_subscription_consume`,默认 **true**)

**调度**(三个量用同一把时钟写死,`settleTargetDay` 纯函数 + 表驱动测试):

1. 每次心跳先过门槛:`now < dayStart(now) + settle_delay_minutes×60` 则直接返回(不抢占、不建行)。
   `delay=0` 退化为 commission 的"日界后第一次心跳开跑"。
2. `run_date = dayKey(now)`(与 `settle_daily.go:110` 同源;⓪ 重置路径 `created_at < dayStart(now)`
   可原样照抄而不会误触发);`claimDailyRun(run_date, now)`。
3. 目标日 `D = dayKey(dayStart(now) − 1)`(`api_daily_consume.go:154` 的"昨日"写法)。
   **一次运行只结 D 这一天;聚合窗口永不含 run_date 当天**;D 之前仍为 `computed` 的桶
   由该日的 partial 重试或管理端 rerun 处理。

**步骤**:

1. **重算**:`DELETE FROM qy_sd_accrual WHERE bucket_date = D AND status = 'computed'` +
   聚合插入 `clause.OnConflict{Columns: (user_id, bucket_date), DoNothing: true}`。
   `(U, D)` 已存在(只可能是 `held` / `settled`)即跳过不重算;partial 重试与 rerun 走同一条路。
   **已 held 的桶 `gross` 冻结于首次计算值,rerun 不追溯** —— 这是 D-D 的既定代价。
2. **候选集 = 两路并集**:`bucket_date = D AND status = 'computed'` ∪ `status = 'held'`(任意日期),
   按 `user_id` 去重。第一版稿只看当日会让"D 日被暂缓、之后停止消费"的用户永远发不出去,
   与 `commission/settle.go:89-95` 点名的饥饿形状相同(它为此有第二路 carry-only 选人)。
3. **每个用户**:事务开始之前(锁外、不进事务)读一次主库
   `Unscoped().Select("id","group","quota","status","deleted_at")`(照 `api_daily_consume.go:485-497`,
   多参数 Select 让 GORM 按方言引号);缩短快照到落账的间隔,分组也在同一次读里取。
   - 不存在 / `deleted_at` 非空 → 该用户全部桶 `held(account_removed)`;`status ≠ enabled` → `held(account_disabled)`;
     `quota < 0` → `held(overdraft)`。三种都不 Credit,`balance.hold_reason` 同步。
   - 否则:分组走 `qy_sd_group_rate.consume_bps` → 回落 `stardust.consume_bps`;
     **决定 D 日档位的是这一刻读到的 `users.group`**(`partial` 重试或 rerun 时为重跑那一刻)。
     在此之前的任何换组 —— 包括 D+1 凌晨的套餐到期降级(`ExpireDueSubscriptions` 每分钟一批)——
     都作用于 D 日全天。与佣金"消费时刻冻结、换组落两行"(D-02)口径不同,原因是 D-B 放弃了 hook;
     `logs.group` 是计价分组不可用(`hook.go:154-158`)。原样写进管理端说明。
4. **发放**(一个扩展库事务):`lockBalance` → `computeSettlement(carry, Σgross)`(`settle.go:50-88`,
   `Σgross ≥ 0` 所以只走正分支)→ `net > 0` 时 `Credit(kind=consume_rebate, idem=sdsettle:<run_date>:<user_id>, ref_no=run_date)` →
   本批所有桶逐行 `UPDATE ... SET status='settled', ledger_id=?, settled_at=? WHERE id=? AND status IN ('computed','held')`,
   **任一行 `RowsAffected==0` 即 return error 整笔回滚**(`absorbAccruals` 同规矩,`settle.go:711-738`)→ 余数回写。
   `net=0`(不足 1 星屑)不写流水、桶仍 `settled` 且 `ledger_id=0`。
5. **收口**:全部成功才 `done`;单人失败计数、当天 `partial`、最多重试 5 次 + 管理端 `rerun`
   (`settle_daily.go:259-281` 形状)。

**告警**:`ledger-check` 加 `held 行数 / 最老 held 桶的 bucket_date`,超过 `stardust.held_alert_days`
(默认 7)进健康面板红点 —— 否则与佣金一样"没有任何告警会响"。

**重跑**:`POST /admin/stardust/settle/rerun {day}` 的 `day` 是桶日 D;只对 `status IN ('computed','held')`
的桶生效(held 桶重评仍读当时快照,余额 ≥ 0 即发放),已 `settled` 的一律不动。重跑的流水幂等键是 `sdrerun:<D>:<user_id>:<发起时刻>`,与日结的 `sdsettle:<run_date>:<user_id>` 分开:同一天先重跑老日子 D′ 再跑当天日结是两笔合法发放,共用一把键会让第二笔被判成重放(结算里"键已存在"是错误分支)而整笔回滚。

**用户端看到的**:「昨日消费 X 星辉 → 已发 Y 星屑」「累计余数 0.37」「待复核(账户余额为负)」;
下次结算时刻 = `nextDayStart(now) + settle_delay_minutes×60`,只由后端下发(D-03),前端不复刻。
任务补扣按补扣当日返、按补扣当日分组定档。

**明日预计到账**(`GET /stardust/forecast`,2026-09-06 补):昨日摘要在一整个白天里是个不会动的数,
而"我今天花了这么多到底会返多少"此前界面上没有答案。这一条按**今天到现在**的消费估下一次结算会记入多少:
口径与日结逐字相同(同一批排除项、`grossOf` 的截断、`floor(carry + Σgross)`),两条线各自取整再相加
(消费返按我自己的消费、下线消费返按我名下绑定中未拉黑的下线,后者与 `/invite/summary` 的
`pending_today_base_quota` 共用 `pendingTodayInviteeConsume`);账号处于三种暂缓之一时两条都估 0 并写明原因。
`settle_at` 恒等于 `nextDayStart(now) + delay` —— 它回答的是**今天这一桶**什么时候结,与
`/stardust/me` 的 `next_settle_at`(下一次结算在什么时候)不是同一个问题。

**它只读,不写任何库、不建任何桶**。估算要扫 LOG_DB(下线那一段最多 500 个 `user_id`),而余额是星屑宿主的
第一张标签,所以缓存在进程内一小时(`forecastTTL`),用户可 `?refresh=1` 请它提前重算,但两次**真正的**重算
之间至少隔 `forecastManualMinSecs`(60 秒)—— 否则那颗按钮就是一个人人可按的 LOG_DB 压测器。窗口内的强制刷新
原样退回缓存那一份,响应里的 `refresh_after` 让界面把按钮按住。缓存是 per-node 的(同 `invite/inviter.go` 的
判断),`computed_at` 下发给界面写成"数据截至",不假装它是实时的;运营改全站比例或分组档时
(`invalidateSettings` / `invalidateGroupRates`)整个缓存清空,避免"改了比例、页面上一小时还是旧数"。

### 4.2 ② 娱乐活动中奖

§5 / §7。派奖走 `Credit(kind=lot_prize)`。

### 4.3 ③ 邀请返:下线充值 / 用兑换码

**充值**:上游没有充值成功 hook,只能扫 `top_ups`。stardust **不共用游标**(键名不同),
但**共用判定函数**:commission 导出 `ExcludedTopUp(t, excludeManual bool)` / `TopUpBaseQuota` /
`InviteeEligible`。`excludeManual` 由 stardust 传自己的键 `stardust.exclude_manual_topup`(默认 true,
与 D-F「admin 来源默认不返」同向),不再静默跟随 commission 的 `exclude_redemption_and_manual`
(那是 YAML-only、stardust 配置页不下发的键)。**订阅单排除是 stardust 专属**,不塞进共用函数
(D-E 要求 commission 对同一行照常计佣):每批先取 `trade_no` 集合,`SELECT trade_no FROM subscription_orders WHERE trade_no IN (?)`
一次查出,命中即跳过;**前向扫描与迟付回收两趟都要过这一层**。原来的 `provider=='' && Amount==0`
三元组降级为日志级兜底(不一致时 warn)。

比例:`qy_sd_group_rate.invite_topup_bps`(按**上线**分组,D-02)→ 回落 `stardust.invite_topup_bps`;
基数:`TopUpBaseQuota` 的额度数;星屑 = `floor(base × bps / 10000 / quota_per_unit)`,
一单一行、余数**不**结转。**首启不补历史**(`bootstrapCursor` 形状)。

**兑换码**:`model.QyOnRedeemSuccess` 单槽已被 commission 占,而 `commission.onRedeemSuccess`
前三行就在 `!cm.Enabled` / `ExcludeRedemptionAndManual` 上 return(`hook.go:190-196`),
"在末尾调 stardust"永远到不了。**改法**:commission 新增包级 hook 变量
`var AfterRedeemSuccess = func(userId, redemptionId, quota int) {}`(默认 no-op),在
`onRedeemSuccess` **第一行**、两道早退之前无条件调用;`stardust.InstallHooks` 里
`commission.AfterRedeemSuccess = OnRedeemSuccess`(仓内同形先例:`planentitlement.go:118`
`groupns.PlanUnlockFundingState = UnlockFundingState`,由 `bootstrap.go:100` 统一赋值、顺序无关)。
配一条与 `qy_log_hookpoint_test` 同形的 AST 位置守卫:调用必须先于 `!cm.Enabled` 分支。
stardust 侧自判 `stardust.enabled` / `invite_redeem_bps > 0` / `quota > 0`,经自己的
`guard.HotAsync("stardust.redeem")` 投递。只对余额码触发(套餐码走 4.4)。基数 = 码面额。
回归测试:`commission.enabled=false + stardust.enabled=true` 兑换余额码后 `qy_sd_ledger` 必须有
`invite_redeem` 行;`exclude_redemption_and_manual=true` 下同样成立且 `qy_commission_accrual` 无行。

**依赖方向**:`stardust → commission`(导出函数);`commission` **不** import `stardust`。
第一版稿两个方向同时写就是 Go 编译期 import 环(爆点在 S2,方向必须在 S0 定下)。
`ResolveInviter` 绑死在 commission 的缓存 / singleflight / cachesync / 配置段上抽不动,反向只需一个
func 变量。S0 加一条 import 图守卫:模块之间不得成环,且 `commission` 不得 import `stardust`(`stardust → commission` 单向允许)。

### 4.4 ④ 买套餐一次性返

触发、基数、来源、幂等键见 D-F。实现:`stardust.InstallHooks` 赋值 `model.QyOnSubscriptionGranted`,
hook 体只做内存判定后 `guard.HotAsync`(购买是低频事件,丢弃面可接受,记 skipped 计数)。
`Renewed=true` 的续期同样返(付了钱);`source ∉ 该套餐的 sources` 不返;`source='stardust'`
不经此 hook(商城自己处理,且无条件不返)。

前端:套餐抽屉按 `plan-entitlement` 的注入模式加「星屑返还」字段组(买家 bps / 上线 bps / 来源多选),
主体保存后单独 `PUT /admin/stardust/plan-rewards/:id`,失败单独 toast
(`subscriptions-mutate-drawer.tsx:279-300` 的注释要更新为"第四次无事务写入")。

### 4.5 ⑤ 管理员手调

`POST /api/qy/admin/stardust/adjust {user_id, delta, reason, client_request_id}`:

- `RootActionGate(RootActionStardustAdjust)` 先于 `CriticalRateLimit`(登记 `rootGateSite{file, fn, recv, routes}`
  进 `root_action_guard_test.go`,`wantChecked` 15 → 16;`middleware/root_action.go` 加常量);
- `guard.ActorMayActOnCtx`(自营 / 越级 fail-closed,登记 `actor_gate_guard_test`);
- `reason ≥ 4` 字符;`client_request_id` 必填;
- **事务内 `lockBalance` 后先查 `(manual, key)` 是否命中**:命中则比 `user_id` 与 `delta`,一致返回原单
  (审计写「幂等重放,账本未再变动」),不一致 409 `qy_idem_key_conflict`;未命中才做
  「负数不得扣到 0 以下 / `|delta| ≤ max_manual_adjust`」校验;`Credit/Debit` 返回 `inserted=false`
  (刚查过不存在却冲突 = 并发重放)同样 409。顺序"幂等先于上下界"的理由见 `api_admin_adjust.go:250-253`;
- 成功与失败各写一条 `qy_audit_logs`(category `stardust`,金额取实际落账值)。

### 4.6 invite 共享设施(原 commission,D-14 之后)

D-14 把 commission 瘦身改名成 `invite`:现金佣金账本、结算、冲正、法币折算全部删除(**D-15 又把账本 /
结算 / 余额恢复到 `modules/commission`、按「星辉」口径,D-16 再把记账单位改成星屑**,与本节的星屑邀请
奖励并行且同币种;方向 commission → invite,D-16 起再加一条 commission → stardust),留下的是
被 stardust 复用的几样东西 —— 邀请关系(绑定 / 换绑 / 解绑 / 拉黑、互邀自动拉黑、`InviteeEligible` /
`InviteMatch` 判定)、邀请人缓存与跨节点失效通道(`cachesync`,表 `qy_invite_cache_invalidation`)、
日界 `dayline`(`invite.day_offset_minutes`,星屑日桶 / 日结 / 下线日消费报表共用的唯一"一天")、
`AfterRedeemSuccess` 转发槽、`ExcludedTopUp` / `TopUpBaseQuota`、logs 覆盖索引的后台补建、
以及只读 logs 的下线日消费报表(管理端 `/admin/invite/daily-consume*`)。

依赖方向不变:**stardust → invite**,invite 不 import stardust(`module_import_guard_test`)。
invite 反过来要的几个数(关系列表上的累计星屑、下线日消费报表里的日桶基数与毛额)走 `export.go`
的 func 变量 `PairRewardTotals` / `InviteAccrualByInvitee` / `InviteAccrualByDay`,由 stardust 在
`InstallHooks` 里赋值。

共享设施的启动不再挂在任何模块开关之下的问题按原 S0 的改法落地:`invite.StartTasks` 在
`SharedInfraWanted()`(= `invite.enabled || stardust.enabled`)时调 `StartSharedInfra()`
(`sync.Once` 包住 `startCacheSync` + `startLogsIndexMaintenance`)。`InviteeEligible` 在
`invite.enabled=false` 时对所有人回"不合格"且不建快照 —— 这是邀请功能的总闸:关掉它,
四条邀请返(充值 / 兑换码 / 注册 / 下线消费)与「我的推广」整页一起消失,消费返不受影响。

### 4.7 ⑥ 下线消费返(D-14 新增)

**是什么**:下线 D 日的站内消费基数(与 4.1 消费返**同一份** logs 聚合、同一批排除项、同一把日界)
按**邀请人自己**的分组档 `invite_consume_bps`(分组表 `qy_sd_group_rate.invite_consume_bps` →
回落全站 `stardust.invite_consume_bps`,默认 0)折成星屑,一日一结,与消费返在**同一次** run 里、
消费返排空之后跑(`settle_invite.go`)。受支付合规门约束,与其余邀请类同档。

**账本形状**:`qy_sd_invite_accrual` 一天一对 `(inviter_id, invitee_id)` 一桶
(uk `(inviter_id, invitee_id, bucket_date)`),记 `base_quota / rate_group / bps / quota_per_unit / gross /
status(computed|settled|held) / hold_reason / ledger_no`。档位与 gross 在**重算时**就冻结
(一个邀请人名下几十个下线共用一次分组解析),不在结算事务里。

**结算**:每个邀请人一个扩展库事务 —— `LockBalance` → FOR UPDATE 重读候选桶(D 的 computed ∪ 任意日期的 held)
→ `floor(invite_carry + Σgross)` → `Credit(kind=invite_consume, idem=sdinvite:<run_date>:<inviter_id>, ref_no=run_date,
base_quota=Σ基数, rate_bps/rate_group=最新一桶的)` → 逐桶 CAS 成 settled 并写同一个 `ledger_no` → 余数回写
`qy_sd_balance.invite_carry`。**`invite_carry` 与消费返的 `carry` 各自结转**,体检里对应 I3
(Σ settled gross == Σ invite_consume 流水 + invite_carry),两条恒等式各自闭合。

**暂缓 / 重跑**:暂缓判据与消费返逐字相同,只是看的是**邀请人**的主库快照(钱发给谁就看谁);
held 桶 gross 冻结于首次计算值,管理端 `settle/rerun` 用 `sdinviterr:<D>:<inviter_id>:<nonce>` 补发。
`bps=0`、下线被拉黑、没有上线、合规未确认:**不写行**。

**不做的**:不追溯 D-14 之前的消费;不给多级;下线自己的消费返与上线的状态无关(507 透支不影响 506)。
**与推广佣金并行**(D-15/D-16):同一天同一个下线的消费,这里按邀请人分组档折成星屑当天日结发出去,
`modules/commission` 按佣金费率也折成星屑、过持有期、攒够门槛再自动入账。两边各记各的账、互不抵扣。

⚠ D-16 之后两边发的是**同一种钱**,而 `invite_consume_bps` 与 `commission.consume_rate_bps` 打的是
**同一笔基数**:两边同时为正,同一笔下线消费会给上线落两笔星屑。这是运营的配置选择(即时小额 +
延迟大额两条并存完全可能是故意的),代码不拒绝也不清零,只在 `/admin/commission/health` 的
`rate_overlap` 段逐档标出来。充值档(`invite_topup_bps` ↔ `topup_rate_bps`)与兑换码档
(`invite_redeem_bps` ↔ `redemption_rate_bps`)同理。详见 `decisions.md` D-16。
用户端看它走「我的推广」(`/api/qy/invite/summary|invitees|records|invitee-daily`,住 stardust 包),
管理端看日桶走 `/api/qy/admin/invite/invite-accruals`。

---

## 5. 娱乐活动切星屑(lottery)

### 5.1 币种

`qy_lot_activity.currency` / `qy_lot_series.currency` / `qy_lot_payout.currency`(三张表都加,
`varchar(16)`,存量回填 `quota`,读侧空串 = `quota`)。活动 DTO 新增 `currency`;
`stake_quota` / `pool_quota` / `amount_quota` 等**字段名不改**(进证据链哈希、也是前端契约),
语义由 `currency` 决定。**对 `currency=stardust` 的行这是改语义**(任何按 `QuotaPerUnit`
换算的消费方都会印错),按 v0.2.0 的先例记成「升级注意」(§14),不进 MAJOR。

### 5.2 报名 / 投注(`ChargeEntry`)

按 `currency` 分叉;`quota` 原路径原样保留。`stardust` 分支**一个扩展库事务**,但要给
twophase 承担的两条契约(原样重放返回原票、换参重放 409,`execute.go:253-313`)找到新落点 ——
第一版稿的 `lot:<entry_no>` 幂等键在结构上永远命中不了(`entry_no` 每次请求 `crypto/rand` 现摇,
`entry.go:293`),客户端超时重试会撞 `uk_qy_lot_entry_idem` 得到 500,用户换 crid 再买一注,
正是 `entry.go:1034-1052` 注释里修过一次的资损形状。

**幂等**:
(a) `qy_lot_entry` 加 `fingerprint varchar(64) not null default ''`,口径照 `fundingFacts` / `twophase.Digest`
    (kind + scope + user_id + amount + act_no + opt_no + pick,**不含** entry_no);存量行空串 = 不比对。
(b) 事务前按 `uk(act_id, idem_key)` 读票:命中且 `status=success` → 比指纹,一致返回原票
    (回执与今天 `settledEntryNo→reloadEntry` 同形),不一致 409 `qy_lot_idem_conflict`(既有码);
    命中但非 success(封盘 excluded 等终态)照 `ChargeEntry :375-384` 的 switch。
(c) 未命中才进事务:`reserveEntry`(活动行条件 UPDATE 取锁 + `checkCaps` + 插票 + 推链,原样)→
    `stardust.Debit(kind=lot_stake, idem_scope='lot_stake', idem_key=Entry.IdemKey)`(与票同键,
    不加前缀:`act_no(27)+':'+crid(64)+'#998'` 已顶满 96;`IsCollationNeutralIdemKey` 仍只对 crid 段断言)→
    票直接 `success`、`pool_quota += amount`。`Entry.OrderNo` 填 `ledger_no`。
(d) `tx.Create(e)` 撞 uk 时用 `db.IsDuplicateKey` 判成并发重放,事务回滚后回到 (b),不落 500
    (`withdraw/create.go:117-121` 形状)。
(e) `Debit` 余额不足 → 整笔回滚、票行不落库。**星屑币种(单事务)与额度币种(twophase)的差异**:
    失败尝试不再占 `seq`、链不再记录失败的试探(`model.go:436-437`、`entry.go:870-871` 写明的
    "失败条目永久占 seq"意图对星屑路径不成立);`lifecycle.go:1182-1192` 的 `COUNT==entry_seq==MAX(seq)`
    不变量不受影响;`checkCaps` 里 `max_attempts_per_user` 在星屑路径上不再有失败尝试可数
    (`entry.go:668-671` 的理由不再适用),规则页与管理端说明照实写明或对星屑活动隐藏该项。
    星屑活动同样**不使用** `entry_close_grace_seconds`(判 `now < close_at`):grace 的用途是给两阶段
    pending 单留收敛窗口(`config.go:834-835`),星屑路径没有 pending;`close_at` 进承诺原像,
    任何更早的截止都不影响验证。
(f) `entry_idempotency_test` / `entry_replay_db_test` / `idemkey_case_db_test` 各加 `currency=stardust` 用例。

**资格判定**:`Evaluate` 里除门槛外还有 `stakeQuota > 0 && s.Quota < stakeQuota → MissStake`
(`eligibility.go:339-341`),星屑活动上它会拿 `users.quota` 比星屑票价。
`Subject` 加 `Stardust int64`,`LoadSubject` 对 `currency=stardust` 顺带读 `qy_sd_balance.available`
(不存在视为 0;读失败 fail-closed);`Evaluate` 增加 `currency` 参数,`MissStake` 在 stardust 上比
`Subject.Stardust`;`min_quota / min_used_quota / recent_spend_quota` 仍比 users 字段(账号质量信号)。
`ChargeEntry`、`handleGetEligibility`、§7.2 `spins` 三个调用点都传币种;前端 `display.ts:165`
的 `'stake'` 单位按币种切换。

**扣钱前提的落点**:`debitMainQuota` 在主库行锁内复检 `status / group / MinQuota`(`entry.go:740-763`,
`eligibility.go:37-38` 称之为"唯一例外")。星屑路径不锁主库:"够不够扣"由 `Debit` 的 CAS 承担;
三项不在锁内复检,接受 `LoadSubject`(锁外最新读)→ `Debit` 的窗口;同步改 `eligibility.go:37-38`
头注释与 `Rules.Normalize:161` 的"恒真"前提,管理端 `min_quota` 文案在星屑活动上从
"报名瞬间持有"改成"受理时刻持有"。

多注仍是 N 次串行,每注只有一个事务,`entry_batch_max_ms` 预算逻辑不变。

### 5.3 派奖 / 退款(`DrivePayouts`)

计划行照旧,但 `PlanPayouts` 签名改传 `*Activity`,落行时把 `currency` 冗余进
`qy_lot_payout.currency` —— worker 扫的是 payout 表本身(`payout.go:111-142`),分叉键必须在行上。

`stardust` 分支:单事务 `UPDATE qy_lot_activity` 合计(先,A)→ `Credit(kind=lot_prize|lot_refund, idem=lotpay:<payout_no>)`
(后,U)→ `markPayoutPaid`。`markPayoutPaid` 只用于 worker 腿(quota 与 stardust 的 `DrivePayouts` 路径),
转盘的当场派奖不走它(§7.2)。

**失败终态**:星屑分支的 `held` **只来自重试耗尽**(复用 `holdPayout`,保持 `finishIfDone`
把 held 当终态放行、`markPayoutPaid` 接受 held→paid、reconcile 红点持续可见三处语义不变),
没有"主库不可判定"那一档。处置 = 管理端现有 `POST .../payouts/:payout_no/retry`,走 `RetryPayout`
的「资金单不存在 → 原样重排、attempts 归零」分支(`payout.go:482-540`),零改动。
人工仲裁端点**不改**:星屑出款命中现有 `errAdjudicateNoOrder` / `errAdjudicateNotHeld`(409),
不新增 404 分支。

**退款权威金额**:`refundAmountOf` 改收 `*Activity`(`planFullRefund` 与 `settleGuessResult`
本就持有 act),按 `act.Currency` 分叉;stardust 分支按 `Entry.OrderNo`(= `ledger_no`)读
`qy_sd_ledger`,比较与取较小值用 **`|amount|`**(`lot_stake` 行是负数);读不到同样返回
`(0,false)` 并 `noteRefundDrift`。

**收款人口径**:`Credit` 不查 `users.status` / 软删,照发(星屑不可流出,disabled 期间也花不掉);
管理端账本页对主库已注销的 `user_id` 标记可见。这是与 `creditMainQuota:209-223`(转 held 交人)
不同的口径,写明。

### 5.4 协议与证据链:`lot-v3` 是一次完整的版本抬升

第一版稿写"三个哈希函数原像各加一段 currency",不够:包内算法版本的分派不是三处而是**六处**,
且全部写成 `algo == AlgoV2 → v2,否则 → v1` —— 一个 `lot-v3` 活动若只改三处,`prizeSpecLineOf`
(`api_admin.go:1779-1786`)会用 **v1 的四分量奖档行**算 `spec_hash`,概率表 / 奖品类型 /
公开说明 / 球赛字段全部掉出承诺;`checkSpecIntegrity`(`lifecycle.go:523-542`)用同一函数,
"发布后改一个 win_ppm 等于点名挑中奖者"在 v3 活动上零告警通过。

**改法**:
① 分派点一次改齐:`SpecHashFor`(`commit.go:114`)、`prizeSpecLineOf`(`api_admin.go:1779`)、
   `CommitHashFor`(`:266`)、`ChainNextFor`(`:299`)、`RosterHashFor`(`:344`)、
   `checkAlgoPublishable` 白名单(`api_admin.go:585-610`)、建草稿的 `Algo: AlgoV2`(`:1534`);
   验证器侧 `verify.py` 的 `SUPPORTED_ALGOS / spec_lines / commit / chain_next / roster_hash`,
   `verify.ts` 的 `isKnownAlgo / qyLotSpecLines / qyLotSpecHash / commit / chain / roster`。
   `== AlgoV2` 改成版本序判定(`algoRank(algo) >= 2` 或显式 `case AlgoV2, AlgoV3`),
   加包内 AST 守卫禁止再出现 `== AlgoV2` / `!= AlgoV2` 字面比较。
② `currency` **只进 `CommitHashV3` 原像**(活动级常量,紧随 `DrawMode` 之后);chain / roster / spec
   只换域前缀(`qylot-*-v3`),不加分量 —— `chain_0 = commit_hash`、roster 原像含 `commit_hash`
   (`api_admin.go:481`、`commit.go:328/339`),币种经 `commit_hash` 已传递到链与名单。
③ `PrizeSpecLineV3` 分量与 V2 完全相同;转盘的 `count` 就是初始库存,已在承诺内;
   `stock_left` 是运行时计数,**明确不进** spec 行。
④ 先补 golden vector 再放开发布:`fairness_v3_test.go`(四组)、扩展 `TestPrizeSpecLineFollowsTheActivityAlgo`
   与 `TestOnlyVerifiableDrawModesArePublishable`;`verify.py` / `verify.ts` 各补 v3 向量并通过后,
   `checkAlgoPublishable` 才加 `case AlgoV3`。注意它的玩法白名单只在 `act.Kind == KindDraw` 分支内
   (`:591-607`),`draw_mode=wheel` 要在这张白名单里显式加一项(§7.5)。

`proofDocument` 加 `currency`;匿名证据链里的 `order_no` 对星屑活动下发 `ledger_no` —— 与今天下发
资金单号同类(`api_proof.go:59-61`,每人私有的资金记录号,持有它进不了别人的账本),该路径不产生
扣费失败的非 success 条目,字段只用于退款交叉复核。

### 5.5 闸门与文案

| 现有键(额度) | 星屑活动对应 | 落点 |
|---|---|---|
| `max_stake_quota` | `max_stake_stardust`(0 = 不限) | **YAML-only**,与 `max_stake_quota` 同档(`settings.go:46-57`、`entry.go:503-506` 明写"不允许在线改");配置页放 `yaml_readonly` |
| `max_total_prize_quota` | `max_total_prize_stardust` | qy_settings 可改 |
| `large_prize_alert_quota` | `large_prize_alert_stardust` | qy_settings 可改;二次确认回显精确星屑数 |
| `pay_password_threshold_quota` | `pay_password_threshold_stardust` | qy_settings 可改(D-I) |
| 系列 `issue_cap` 夹值(`series.go:386`) | 按系列币种取 `max_total_prize_quota / _stardust` | |

三个 `*_stardust` 键**显式加进 `validate.go:204-216` 的表驱动清单走 `checkQuotaCap`**:名字不含
`quota` 不代表不受 `MaxQuota` 约束(§2),配得比 `MaxQuota` 大就是一道永不触发的闸门。
新键不以 `_quota` 结尾:前端配置页按后缀把 `_quota` 键当美元录入(`admin-lottery-config/index.tsx:100`)。

**刻度文案是一张清单不是一处**:`quotaText` / `quotaColumnCeilingText` / `tierBudgetShort` /
`netIssueConfirmRequired` / `prizeCapExceeded` / `api_admin.go:1764` 的 SysError /
`api_admin_config.go:298` 的跨字段文案,统一改成带币种参数;`quotaColumnCeilingText`
("全站额度换算的整数上界")与 `netIssueConfirmRequired`("对用户余额的净增发")要按币种换**句子**
而非只换数字;`yaml_readonly` 加 `system_max_stardust`;`caps_test` / `advice_test` 补币种感知用例。

### 5.6 D-10 分野

抽奖类(rank / prob / ball)管理员可参与,竞猜照旧禁止,**转盘禁止**(D-J)。

### 5.7 收尾旧路径与生命周期

`InstallResolvers` 对 `KindLotteryEntry / KindLotteryPayout` 继续注册;删旧分支的前提是 D-H 的三个数归零。

**`stardust.enabled` 关回 false**(两种结局必须选一个,推荐 b):
(a) D-07 形状拒绝启动 —— `validate.go` 看不到库,判据只能放在 lottery 的 `Init/StartTasks` 里查
`qy_lot_activity WHERE currency='stardust' AND status<>'finished'`;
(b) **不 FATAL,只关入口不关账**:`/api/qy/stardust/*`、商城、前端页 404;`Credit/Debit` 是包内函数、
**不查 `stardust.enabled`**,在途星屑活动的扣 / 派照走进一本用户暂时看不见的账;`moduleGates` 的
`stardust` 行 `Effect` 明写这一点(仓库规矩:每个开关都要写"关掉之后发生什么",`sections.go:133-139`);
lottery 健康面板加「星屑活动在途 N / stardust 已关」红项(与 D-H 的"额度活动在途 0"对称)。
`reload` 对 `stardust.enabled` true→false 不阻断(与全局 `enabled` 不同):阻断会让运维在事故时连入口都关不掉。

**删除活动**:`qy_sd_ledger` 永不随活动删除(与 `api_admin_retire.go:70-76` 对资金单的口径同);
`buildDeleteEvidence` 加 `currency`,并按 `act_no` 列算出 Σ `lot_stake` / Σ `lot_prize` / Σ `lot_refund`
三个数写进证据(`purgeActivityRows` 之后仍可按 `act_no` 归拢)。

---

## 6. 星屑商城(模块 `mall`)

### 6.1 表

**`qy_ml_product`**:`product_no`(uk)、`kind`(`plan|code|physical`)、`title`、`description`、
`cover_ref`(复用 lottery 封面存储形状)、`price`(星屑,**所有 kind 都要求 `1 ≤ price ≤ MaxQuota`**,
与 §3.3 及 `twophase.validateAmount` 同源)、`stock`(−1 = 不限;`code` 类由库存表算)、`sold`、
`per_user_limit`、`sale_start_at / sale_end_at`、`enabled`、`sort_order`、`plan_id`、时间戳。

**`qy_ml_code_stock`**:`id`、`product_id` idx、`code_cipher`(`qymodel.Blob`)、`key_version`、`nonce`、
`status`(`unused|issued|revoked|taken`)、`order_id`、`created_at / issued_at`、`taken_at / taken_by`。
`taken` 是**管理员提卡**:明文交到人手上,那一枚从此不计入可售库存、也不会再发给任何用户。
做成独立一态而不是复用 `issued`,是因为 `issued` 的语义带着 `order_id`(履行证据),而提卡没有单;
留在 `unused` 更不行 —— 同一枚码会被再卖给一个用户。`taken_at / taken_by` 与审计双写:
审计可被保留期清理,库存行不会,而运营对着库存表问的正是"这一页里哪几枚被提走了、分别是谁"。
密钥 `mall.secret_key`(必填,与 `withdraw.pii_key` 同规格,登记 `SecretKeys`)。
管理端批量粘贴上传(≤ 500 条/次),入库即密文,明文只出现在上传请求体里
(登记 `credentialBodyRoutes`,键格式 `POST /api/qy/admin/mall/products/:no/codes`)。
可选的机器闸门:上传时对每条明文点查一次主库 `redemptions.key`(char(32) 唯一索引),命中即 400 ——
让"不得上架本站余额码"(D-K)不只靠运营口径。
`code_cipher` 只经 `sealCode/openCode` —— **mall 包内新写一条 AST 守卫**(`secret_guard_test` 只扫 lottery 目录)。

**`qy_ml_order`**:`order_no`(uk)、`user_id`、`product_id`、`kind / price / title`(快照)、`status`、
`idem_key` uk(`<user_id>:<NormalizeIdemClientKey(crid)>`)、`ledger_no`、`refund_ledger_no`、
`fund_order_no`、`trade_no`(`kind=plan`,= `"SUBSD" + order_no`,LocalDetail 阶段即定)、
`user_subscription_id` + `sub_renewed`(`kind=plan`,LocalCommit 从闭包回填;Resolver 按 `trade_no`
读主库 `subscription_orders.provider_payload` 回填)、`expect_action` + `expect_superseded`
(用户确认过的顶替后果,§6.3)、`code_stock_id`、`address_cipher / contact_cipher`(`json:"-"`)、
`tracking_no`、`ship_note`、`fail_reason`、`created_at / fulfilled_at / updated_at / address_pruned_at`。

**`qy_ml_order_event`**:单据时间线(`withdraw` 的 Event 形状)。

### 6.2 下单(一个扩展库事务,`kind=code|physical`)

`POST /mall/orders {product_no, client_request_id, pay_password, address?, contact?}`

1. 幂等重放先于一切(`create.go:42-151` 的顺序;命中比 `product_no`,不一致 409)。
2. `paypass.Require`(ShouldBindJSON 之后、开事务之前、事务之外)。
3. 锁商品行(条件 UPDATE:`enabled=1 AND 在售窗 AND (stock=-1 OR sold<stock)`,`sold+1`)。
4. 每人限购:`COUNT(orders WHERE user_id, product_id, status<>'failed')` 在商品行锁内数。
5. `stardust.Debit(kind=mall_order, idem=mall:<order_no>)`,不足 → 409 `qy_sd_insufficient`。
6. `code`:`SELECT id ... WHERE product_id=? AND status='unused' ORDER BY id LIMIT 1` 后
   `UPDATE ... SET status='issued', order_id=? WHERE id=? AND status='unused'`,`RowsAffected=0` 重选
   (最多 3 次;商品行锁已把同商品下单串行化);订单直接 `done`。MySQL 5.7 没有 `SKIP LOCKED`,不用它。
7. `physical`:订单 `paid`,地址加密落列。
8. 提交。前端 `invalidate(qyKeys.all)`;不动主库。

### 6.3 下单(`kind=plan`,跨库两阶段)

**资金单要素**:`Kind=mall_plan`(`KindCode MP`)、`IdemScope=mall`、`IdemKey=<user_id>:<crid 折叠>`
(与 `qy_ml_order.idem_key` 同源,让 LocalDetail 撞 `qy_ml_order` 唯一键与资金单撞 uk 指向同一张原单)、
`RefType="mall_order"`、`RefId=order_no`(Resolver/PostCommit 按它找订单)、
`Fingerprint = Digest(product_no)` 且 **RefId 置空后再算**(`entry.go:420-422` 形状;`order_no` 每次
现生成,不得进指纹);`AmountQuota`(售价快照)留在指纹里,代价"重试期间改价 → 409",与 lottery 的
`StakeQuota` 同口径。**`FundOrder.AmountQuota / qy_fund_outbox.amount / 审计 AmountQuota 装的是星屑数**,
写进 `fund_order.go` 的 Kind 注释。Execute 之前先照 §6.2 第 1 步做一次锁外重放。

**预检**(锁外、体验层):`plan.Enabled`、`PlanSaleWindowError`、`QyGateSubscriptionSeat(nil, plan, uid, "precheck")`、
`CountUserSubscriptionsByPlan`、`PreviewUserGroupPurchase`。用户确认的 `action` 与排序后的
`superseded_groups` 随下单请求回传(`expect_action` / `expect_superseded`)。

**`LocalDetail`**(扩展库事务①):验密已在事务外;锁商品行 + 限购 + `Debit` + 订单 `paid` + `fund_order_no` + `trade_no`。

**`MainApply`**(主库事务②)—— `CreateUserSubscriptionFromPlanTx` **不检查** `Enabled` 与发售窗
(`model/subscription.go:1341-1451` 只有限购 / 名额 / 顶替规则),必须自查:
1. `tx.Where("id = ?", planId).First(&plan)` 直读主库(不走 `getSubscriptionPlanByIdTx`,它先查 300s 缓存)+ `NormalizeDefaults()`;
2. `!plan.Enabled` 拒;`PlanSaleWindowError(plan, now)` 拒;
3. `model.QyLockForUpdate(tx)` 锁用户行;`u.Status != enabled` 拒(纵深防御;软删由 `First` 的 `DeletedAt` 过滤覆盖);
4. **复核顶替集合**(只对 `plan.NoQuota && UpgradeGroup != ""`):用 `QyLockForUpdate` 重跑与 Preview
   同判据的 actives 查询,算出 `action / superseded` 与 `expect_*` 不一致 → 业务错误
   `qy_ml_plan_state_changed`(前端「套餐状态已变化,请重新确认」)。商城是第一条**能在事务内安全拒绝**
   的购买路径(非 paid),2026-08-14"跨组顶替要用户确认"的拍板在这里由执行侧复核保证;
   上游四条路径的 TOCTOU 窗口保持原样;
5. `CreateUserSubscriptionFromPlanTx(tx, uid, plan, "stardust")`(`"stardust"` 不在 `isPaidSubscriptionSource`,
   限购 / 名额 / 同组永久三条会在事务内拒绝 → 回滚 → Failed → 退星屑);
6. 写全 `subscription_orders`:`PlanId, Money=0, TradeNo, PaymentMethod=PaymentProvider="stardust", Status=success, CreateTime=CompleteTime=now, ProviderPayload="mall_order_no=<order_no>;stardust=<price>;user_subscription_id=<sub.Id>;renewed=<0|1>"`。
   本仓没有任何列出 `subscription_orders` 的接口或页面,这一行只供对账;用户可见性靠下一步。
   任何一条业务错误 → `phaseBody → Failed → MainNotApplied → 退星屑`。

**`AfterCommit` / `PostCommit`**(同一个函数 `postMallPlanFromOrder(order)`,只用 `order.UserId`):
**无条件** `model.QyRefreshSubscriptionUserGroupCache(order.UserId, "mall plan purchase")`(它内部已先调
`QyOnUserGroupChanged` 再刷缓存,`subscription.go:1453-1463`,**不要**再并列写钩子)+
`planentitlement.InvalidateUser(order.UserId)` + `model.RecordLog(uid, LogTypeTopup, "星屑兑换订阅成功 …")`。
不判「升组时」:补偿路径拿不到 `PrevUserGroup`,分组没变时刷新是语义空操作。
已知残余:业务线程认领后、刷新前进程崩溃,补偿任务只会无条件 `InvalidateUserCache`(Redis),
commission 的进程内分组缓存要等 `InviterCacheSecs` 自然过期。

**`LocalCommit`**(扩展库事务③):订单 `paid → done`,回填 `user_subscription_id / sub_renewed`。

**注册**:`RegisterResolver(KindMallPlan)`(幂等 `finalizePlanOrder`)与 `RegisterPostCommit(KindMallPlan)`。

**失败与对账**(第一版稿缺这一整段,是 blocker):
- 请求线程内:只在 `Status==Failed && ProbeMainSide==MainNotApplied` 时 `Credit(mall_refund, idem=mallrf:<order_no>)`
  (`transfer/service.go:454-483` 判据);退款与 held 标记一律走 `guard.ColdContext(context.WithoutCancel(ctx))`
  (`lottery/entry.go:946-968` 的教训:Failed 常常就是调用方预算耗尽触发的)。
- `Failed + MainApplied/MainUnknown`(探针关闭 / 报错 / 行缺失)与 `InDoubt / Uncertain` 一样 → 订单 `held`,绝不退。
- **`lease.Run("mall.reconcile")`**(照 `transfer/reconcile.go:84-141` 的 `syncStuckOrders`):扫
  `kind='plan' AND status IN ('paid','held') AND created_at < now − pending_grace`,按 `fund_order_no` 读资金单:
  `Success` → `finalizePlanOrder`(CAS `paid|held → done`,同时兜住 `execute.go:219`"主库已生效但扩展库回写失败"
  与 `settleGuard` 失败的 paid 单);`Failed` → 再 `ProbeMainSide`,`MainNotApplied` → 退星屑 + `failed`,否则 `held` + 告警;
  `Pending/InDoubt/Uncertain` → 只把展示状态改 `held`;`Reversed` → SysError 交人工。
- 人工出口两条:① 现有资金单对账台对 `Uncertain` 判 success/failed —— **success 由 Resolver、failed 由 mall.reconcile 收尾**
  (`ResolveManually(target=Failed)` 只改资金单状态,`compensate.go:622-640`);② `mall_plan` 挂一个 root 端点
  (照 `payout_adjudicate.go` 形状调 `twophase.AdjudicateFailedAsApplied`)处理 `Failed + MainApplied/MainUnknown` 的 held 单。
- `settleGuard` 复核(`Execute` 返回 nil ≠ 已落定)。

**单位**:`compensate.go` 的积压告警 `SUM(amount_quota)` 按 kind 分组(至少把 `mall_plan` 单独 SUM),
文案「合计 %d 额度 + %d 星屑」;`markUncertain`(`:233-235`)与 `in_doubt`(`execute.go:524-526`)
两处 SysError 按 `order.Kind` 带单位;前端 `admin-fund-orders/index.tsx:248`、`fund-order-resolve-dialog.tsx:118`
按 `row.kind === 'mall_plan'` 切 `QySdAmount`,`fund-audit-tab.tsx:365-368` 按 `log.action` 前缀
`fund.mall_plan.` 或 `log.category === 'stardust'` 切(审计行没有 kind 列);i18n 加 `qy_cfg_fund_kind_mall_plan`。

**依赖 outbox**:`ProbeMainSide` 在 `two_phase.main_outbox_enabled=false` 时恒 `MainUnknown`
(`probe.go:59-63`),届时 `paid→failed(退)` 不可达、每一笔失败都成 held。`validateMall` 在
`mall.enabled && !OutboxEnabled()` 时 SysError 告警(不 FATAL,商城可能只卖 code/physical);
商品创建 / 启用接口对 `kind=plan` 拒绝(`qy_ml_plan_needs_outbox`);存量 plan 商品标"暂不可售";
健康面板 `two_phase` 段增加 `outbox_enabled`(全仓缺口,顺手补)。

### 6.4 履行与退款

| kind | 状态机 | 用户动作 | 管理员动作 |
|---|---|---|---|
| code | `done` | 订单页揭示码(`GET /mall/orders/:no/code`,**验密** + `sensitiveReads`,一次一条) | 撤回(码标 `revoked`、退星屑、写事由) |
| physical | `paid → shipped → done` / `paid → cancelled` / `paid|shipped → failed` | `paid` 时可取消(全额退);查看物流 | 发货(必填 `tracking_no`)、标记失败(必填事由,退星屑)、揭示地址(`sensitiveReads`,写审计) |
| plan | `paid → done`(LocalCommit / Resolver / reconcile-Success);`paid → failed`(线程内或 reconcile,仅探针开启时可达);`paid → held`(资金单 InDoubt/Uncertain;Failed 但探针≠NotApplied;Execute 返回 nil 但未落定);`held → done`;`held → failed`(reconcile 再探针 NotApplied 才退) | 订单页展示 `user_subscription_id`、是否续期、到期时间;超过宽限期的 paid/held 显示「处理中 / 待核对」 | 资金单对账台;root 裁决端点 |

退款一律 `Credit(kind=mall_refund, idem=mallrf:<order_no>)`,同一订单至多一次。

地址 PII:`mall.address_retention_days`(默认 90,下限 30)后由 `lease.Run("mall.prune")` 清空
`address_cipher / contact_cipher` 并记 `address_pruned_at`。

### 6.5 不做的

- 不上架"余额 / 额度"商品;运营不得预存本站余额码(D-K)。
- 不做购物车、不做数量 >1(一单一件)。
- 不做自动生成主库 `redemptions` 行。
- 星屑买套餐**不返星屑**(买家与上线都不返,D-F)。

---

## 7. 星屑转盘(lottery `kind=draw, draw_mode=wheel`)

### 7.1 模型

一个转盘 = 一场 `kind=draw`、`draw_mode='wheel'`(新常量,`normalizeDrawMode:1626` 放行;**不能留空** ——
空串会落进 `pickWinnersByMode` 的 rank 分支,`lifecycle.go:411-421`)的活动:`open_at` 开放、`close_at` 关闭、
`draw_at = close_at + reveal_delay`(揭示种子);`min_entries_to_hold` **强制 0**(非 0 直接 400);
`currency` 只能是 `stardust`。奖档 = `qy_lot_prize` 行,新增 `stock_left`(`count` 是发布时库存,进承诺;
`stock_left` 是唯一在线可变列,**不进** `prizeSpecLineOf` 原像);`prize_type` 沿用 `quota`(语义随
`currency`,即星屑数,与 §5.1 同口径)与 `text`(人工履行),不新造 `stardust` 类型。

**「谢谢参与」不是运营填的奖档,而是服务端派生的独立 `prize_type=none` 行**:`count/amount` 恒 0、
无 `stock_left`、不进 `PlanPayouts`,其 `win_ppm = PpmDen − Σ其余档` 由 `buildPrizes` 的 wheel 分支在末尾
算出并作为固定一行进 spec 原像;发布期断言 wheel 的 `ppmSum == PpmDen`,让 `Bands` 的"留空区间"
(`commit.go:562-563/594`)在结构上不可能出现;允许派生值为 0(全中转盘)。
现有创建期校验与它正面相撞,所以转盘的奖档校验是**单独一套**:`normalizeWinPpm` 加 wheel 分支 ——
真实档 `win_ppm ∈ (0, PpmDen]`、`count ≥ 1`(硬库存)、`amount ≥ 1`;去掉 prob 的
"`count × amount ≥ entriesCap` 超募均分"规则(`api_admin.go:1720-1727`,对硬库存无意义:每人拿定额,
永远不摊薄);保留 `count ≤ MaxTotalEntriesHard`、`Σ(count × amount) ≤ max_total_prize_stardust` 与
`netIssueOverflowGuard`;`api_admin.go:1683-1685`(Count>0)与 `:1705-1712`(非文本档 Amount>0)对 `none` 档豁免;
`normalizePrizeType` 接受 `none`。`WorstCaseTextGrants` 对 wheel 只统计 text 真实档的 `count`。

`spin_cost` = 活动的 `stake_quota`(星屑);每人次数上限 / 冷却 = 现有 `Rules`。

### 7.2 一次转动(一个扩展库事务)

`POST /lottery/activities/:act_no/spins {client_request_id, client_seed?, pay_password?}`
(`client_seed` 可选,≤ 64 字节、仅 `[0-9a-zA-Z_-]`,不合规 400;空串按空分量进原像与 proof;
前端默认随机生成一份并持久展示在回执里;`Entry` 加 `client_seed varchar(64)` 列并进 lot-v3 链原像,与 `Pick` 同形)

1. **幂等重放**:键 = `act_no:crid`,指纹 = `user_id, amount, client_seed`;命中返回原转动结果
   `{tier, amount, ppm, seq, chain_head}`,**不得再摇一次**;换参 409。
2. `LoadSubject` + `Evaluate(currency=stardust)`(含 `MissAdmin / MissCreator`,D-J)。
3. 验密(阈值 `pay_password_threshold_stardust`)。
4. 活动行条件 UPDATE 取锁(`status='published' AND open_at<=now AND now<close_at`,`entry_seq+1`;
   **不用** `entry_close_grace_seconds`,转盘没有 pending 阶段;`close_at` 进承诺,验证者按它核对每一转)。
5. **奖档完整性**:锁内重算 `spec_hash`(≤12 行)与活动承诺比对,不一致 → 拒绝转动 + `FlagSpecDrift`。
   批次模型只在开奖那一瞬校验一次;转盘的钱是逐转付出的,发布后改一行 `win_ppm` 会立刻改变
   后续每一转,必须每转校验。`auditSpecDrift` 的 kind 过滤也要含 wheel。
6. `checkCaps`(每人上限 / 冷却 / 邀请人 / IP 原样)。
7. `stardust.Debit(kind=lot_stake, idem=Entry.IdemKey)`。
8. **票面**:`ticket = HMAC(seed, "qylot-wheel-v3" ‖ act_no ‖ seq ‖ client_seed)`。
   原像里**没有** `user_ref`(第一版稿有):`ref_salt` 与种子同行、永不公开、不进承诺,验证者与用户本人
   都无法核验那一分量;身份绑定由链环(含 `user_ref`)承担即可。原像里也**没有任何服务端当场生成的量**:
   批次玩法里 `entry_no` 随机是安全的(结果由封盘后的名单哈希决定),即时开奖里若原像含服务端随机数,
   服务端就能"多摇几次挑一个"。`seq` 在活动行锁内单调,`seq` 顺序 = 提交顺序;因为星屑路径失败
   即整体回滚(§5.2 e),`seq` 从 1 起**连续无缺口**,验证者必须显式检查这一点(否则服务端可跳号挑结果)。
9. `ppm = RollPpm(ticket)` → `Bands` 落档:落到 `none` 档不做任何 UPDATE;落到真实档才
   `UPDATE qy_lot_prize SET stock_left=stock_left-1 WHERE id=? AND stock_left>0`,`RowsAffected=0` ⇒
   记 `exhausted_tier=k`,`result_tier` 落到 `none`。
10. 插票(`status=success`,`result_tier / ppm / exhausted_tier / client_seed`)→ 推链(原像加 `tier ‖ ppm ‖ exhausted_tier`)。
11. 仅 `result_tier` 为真实档时落 payout 行:quota(星屑)档 → 先生成 `payout_no`,`Credit(kind=lot_prize, idem=lotpay:<payout_no>)`,
    再**直接插入** `qy_lot_payout` 行 `status=paid, settled_at=now, order_no=<ledger_no>`(**不经 `PlanPayouts`、
    不经 planned/paying、不调 `markPayoutPaid`** —— 它的 CAS 刻意不含 `planned`,`payout.go:314-319`,
    "先 planned 再标 paid"会静默 no-op 并把行留给 worker);text 档 → 插 `status=granted` 行并在**同一事务**
    `UPDATE qy_lot_activity SET text_grant_count = text_grant_count + 1`(批次模型里它是开奖一次写入的期望值,
    `finishIfDone:920-934` 与 `auditTextPrizes:1389-1392` 靠它复核,转盘必须逐转累加,不能在封盘时用 COUNT 回填);
    `none` / 耗尽 → 不落 payout 行。`payout_quota` 逐转累加只服务进行中的实时显示,`finishIfDone` 收尾时以
    `SUM(paid)` 整体覆盖为权威(`lifecycle.go:973-991`)。
12. **库存耗尽**:所有 stardust/text 档 `stock_left=0` → 同事务内做与 `lockActivity` 相同的 CAS
    `published→locked` + `roster_hash`(`close_at / draw_at` 不动,原像不变)。不做"耗尽后继续按未中开出"。
13. 提交,返回 `{tier, amount, ppm, seq, chain_head}`。前端 `invalidate(qyKeys.all)`。

`seed` 的读取:`loadSeedForSpin` 是包内**第三个触碰 `Seed` 字段的函数**(现有两个:`handleCreateActivity`
的创建点 `api_admin.go:188-195`、`loadSeedForReveal`;`model.go:337-339` 说的 `newSeed` 不存在),也是
**唯一的热路径读点**(现有生产调用点四个全是冷路径或揭示后:publish / 开奖 worker / proof / 删除审计),
这是在扩大暴露面,是 D-J 禁止管理员参与的另一个理由。`model.go:339` 说的 `seed_guard_test.go`
**并不存在**(目录下只有 `secret_guard_test.go` / `tamper_guard_db_test.go`),本期**新建**一条 AST 守卫
(照 `secret_guard_test.go` 形状):① 元素非空的 `Seed{...}` 复合字面量只允许在 `handleCreateActivity`
(零元素 `&Seed{}` 作 Model/AutoMigrate/删除清单放行);② `.Seed` 选择器且接收者是 `lottery.Seed` 类型
只允许在 `loadSeedForReveal / loadSeedForSpin`(用 go/types 判接收者类型,或把 `api_proof.go:162` 的
`proofDoc.Seed` 改名 `RevealedSeed` 后退化为纯名字匹配,否则 `api_proof.go:300` 会误报);③ `loadSeedForSpin`
的调用者只能是转动事务函数,返回值只能作为票面推导的实参,所在文件其余位置不得出现 `seed` 标识符。
同 PR 修正 `model.go:337-339` 与 `admin_support.go:115`(`computeCommit` 是测试专用包装)两处漂移的注释。

### 7.3 生命周期(与批次玩法分开写)

| 阶段 | 转盘的实现 |
|---|---|
| 封盘 `lockActivity`(按 `draw_mode` 分派) | CAS `published→locked` + `locked_at` + `roster_hash/roster_count`(名单承诺保留);**跳过** `MinEntriesToHold` 的 shortfall 分支(`lifecycle.go:115-127`),不写 outcome |
| 揭示 `revealActivity`(`runReveal` 的 `kind=draw` 扫描已覆盖,函数内按 `draw_mode` 分派) | 承诺哈希校验 + roster 漂移校验 + `checkSpecIntegrity`(spec 行用 `count` 不用 `stock_left`)→ CAS `locked→settling` + `outcome=drawn` + `seed.revealed_at` + `payout_quota = SUM(payout WHERE kind=prize AND status=paid)` + `text_grant_count` 复核;**不抽签、不 `PlanPayouts`、不 `checkQuotaBudget`** —— 现有分支会无条件重写合计并再抽一批(`:343-374`),`pickWinnersByMode` 对 wheel 永远不能被走到 |
| 结算 / 完成 | `runSettle` / `finishIfDone` 无需改(open payouts=0、非全退 outcome) |
| 取消 `handleCancelActivity` | 对 wheel **只允许「提前封盘」**(`published→locked`,同一条 `lockActivity` 事务冻结名单,揭示时刻取 `max(draw_at, locked_at+reveal_delay)`),绝不写 `OutcomeCancelled`、绝不进 `planFullRefund`;真正作废只允许 `roster_count==0` |
| 流局 / 退款 | `planFullRefund` / `refundExcluded` 入口对 `draw_mode=wheel` 直接返回(结构性挡住);`isFullRefundOutcome` 的五种 outcome 对 wheel 不可达,加守卫测试。否则本金已花、奖已当场 Credit,再退一遍本金是双付 |

### 7.4 公示与验证

匿名 `proof` 端点对 `wheel` 输出:承诺哈希、种子(揭示后)、奖档与初始库存 `count`(含派生的 `none` 行)、
**按 `seq` 排序的全部转动**(`seq, user_ref, client_seed, ppm, tier, exhausted_tier, chain`)。验证者从种子逐条
重算 `ppm` 与落档、检查 `seq` 从 1 起连续、按顺序重放库存递减(跳过 `none` 档),即可证明每一次
"库存耗尽落空"都是真的。**有效中奖率随库存下降**是固有性质,页面上实时公示"各档剩余 / 初始"。

**诚实列出的两条"不保证"**(与 `api_proof.go:40-42` 的写法同形):① `user_ref` 与真人的对应关系不可被
外部证明(它不是随机量的输入,用户可用回执自查同一活动内自己的 `user_ref` 是否一致);② 并发转动时
哪个请求拿到 `seq N` 与 `N+1` 由服务端串行化决定,理论上存在一次二选一的重排空间 —— 批次玩法靠
`FinalSeed(seed, roster_hash)` 消除了它,转盘做不到。

### 7.5 `draw_mode` 分派点清单

随 `kind=draw` **自动覆盖、不必动**的:创建入口 `api_admin.go:1447`、spec 构建 `:1564-1587`、
`checkSpecIntegrity` / `auditSpecDrift` / `reconcileActivity` 的三处漂移检测(`lifecycle.go:524/1074/1310`)、
`fillProofSpec`、`api_user.go:515`、`acceptAmount`、`CommitHash` 的 kind 分量、`api_admin_retire`(它不按 kind 分支)。

**仍需逐条加 wheel 分支**的:`normalizeDrawMode`(`:1626`)、`checkAlgoPublishable` 的 draw_mode 白名单
(`:591-597`,先补 `verify.py` / `verify.ts` 的 wheel 分支再放开)、`normalizeWinPpm` / `buildPrizes`(§7.1)、
`normalizePrizeType`(`none`)、`runLock` / `runReveal` / `revealActivity` / `pickWinnersByMode`(§7.3,wheel 只揭示不摇号)、
`handleCancelActivity` / `planFullRefund` / `refundExcluded`(§7.3)、`playOf / playSettingKey / playShown`
(玩法键 **`show_play_wheel`**,与 `show_play_draw_*` 同形;`Plays` 4→5,`play_visibility_db_test.go:347` 及
`editableKeys / settingBounds{0,1} / settingsSnapshot / assignSetting / mergeOverrides` 各加一支)、
`hallLanes` / `playFilterClause`(转盘**不进**大厅 lane:`/qy/wheel` 独立页自己按 `draw_mode=wheel` 拉列表;
改写 `TestHallLanesPartitionEveryPlay` 的"划分"断言并写明理由;`playFilterClause` 显式排除 wheel、
`playOf` 不得把 wheel 归到 `draw_rank`)、`Evaluate` 的 play 参数(D-J 硬规则)、前端 `QyLotPlays` /
`normalizeQyConfig` / 三处 `QyFeatures` 测试字面量加 wheel。

### 7.6 与批次玩法的差异

| | 批次(rank/prob/ball) | 转盘 |
|---|---|---|
| 结果时刻 | 封盘后统一 | 当场 |
| 随机源 | `FinalSeed(seed, roster_hash)` | `HMAC(seed, act_no‖seq‖client_seed)` |
| 管理员参与 | 可(D-10) | **禁**(D-J) |
| 奖档校验 | 开奖时一次 | 每转一次 |
| 管理端提前截止 | 无 | 「取消」= 提前封盘 |
| 派奖 | worker | 同事务、直接 paid |
| 流局退款 | 有 | 结构性不可达 |
| 失败尝试占 seq | 是 | 否 |

---

## 8. 名称与显示

### 8.1 星屑名

- 存 `qy_settings(scope=stardust, k=name)`,YAML `stardust.name` 是基线;白名单键、越界(空白 / >16 rune)
  丢弃回落,形状照 `restricted_notice.go:57-62`。
- 匿名下发:`GET /api/qy/config` 新增 `stardust: {enabled, name, show_entry}` 与 `mall: {enabled, show_entry}`;
  `qyctl.QyStardustConfig` hook 变量由模块 `InstallHooks` 接管(`lottery/module.go:57-64` 形状)。
- 前端 `normalizeQyConfig` 补默认(缺键 ⇒ `name` 用 i18n 兜底「星屑」,`show_entry` **默认显示**);
  三处手写 `QyFeatures` 全量字面量的测试同步(`route-entry-guard.test.ts:44`、`system-settings.test.ts:47/132`、
  `pages-table.test.ts:355/378`)。
- 跨节点失效:照 transfer 的单行版本表 + 2s 比对(`transfer/settings.go:175-187,355-400`)。

### 8.2 星屑展示组件

新建 `QySdAmount`(整数 + 单位名)与 `QySdInput`(右侧单位取 `useQyConfig().stardust.name`)。
**绝不**走 `formatQuotaWithCurrency`。替换清单**含管理端**(第一版稿只列了用户端 13 处):

- 用户端:`lottery-entry-dialog.tsx` 7 处、`activity-card` 2、`records` 2、`detail` 2、`display.ts:165`。
- 管理端 lottery(8 个文件,60 处):`lottery-create-wizard.tsx`(**`stake_quota` / 奖档 `amount_quota` 用
  `QyAmountInput` 按 USD 录入,运营给星屑活动填「1」会存成 500000**,必须按活动币种切 `QySdInput`)、
  `lottery-entries-tab` / `lottery-payouts-tab` / `lottery-payout-adjudicate-dialog` / `lottery-publish-dialog` /
  `lottery-series-panel` / `detail.tsx` / `index.tsx`;`advice.test.ts` / `advice-wiring.test.ts` 补币种用例。
- 资金单页三处(§6.3)。

### 8.3 星辉

D-M。另外 `qy/lib/quota-usd.ts` 固定按 USD 录入配置(刻意忽略展示类型),不动。

---

## 9. 配置

### 9.1 YAML(`qianye.example.yaml`)

```yaml
stardust:
  enabled: false
  show_entry: true                    # 前端是否渲染星屑入口(qy_settings 可覆盖)
  name: "星屑"                         # 单位名基线(qy_settings 可覆盖)
  quota_per_unit: 0                   # 0 = 取 common.QuotaPerUnit(500000 = $1);冻结进每行日桶
  consume_bps: 10000                  # 消费返默认比例,1:1(qy_settings 可覆盖;分组表可覆盖)
  # 订阅额度出资的消费不返。任务首次扣费与 MJ 日志不带 billing_source,这两类订阅消费排不掉;
  # 佣金侧的同名开关默认关、这里默认开 —— 盲区在这里是"默认账里的"。
  exclude_subscription_consume: true
  # 日界之后多久开始结算昨日。兜的是 relay 节点写日志的时钟偏差与日志库副本/ClickHouse 摄入延迟;
  # logs.created_at 是写入时刻,任务补扣/退款永远落在结算当日,这个窗口覆盖不了它们。
  settle_delay_minutes: 30
  settle_interval_seconds: 300        # 调度心跳(语义同 commission.settle_interval_seconds)
  held_alert_days: 7                  # 暂缓桶积龄告警
  invite_topup_bps: 0                 # 下线充值返上线,0 = 关;受 IsPaymentComplianceConfirmed 约束(D-G)
  invite_redeem_bps: 0                # 不受 commission.enabled / commission.exclude_redemption_and_manual 影响
  exclude_manual_topup: true          # 管理员补单不返(stardust 自己的键,不跟随 commission 的同名语义)
  invite_register_stardust: 0         # 注册奖(整数星屑),0 = 关
  topup_scan_interval_seconds: 60
  max_manual_adjust: 100000           # 单次手调上限(星屑)
mall:
  enabled: false
  show_entry: true
  secret_key: ""                      # 必填(enabled 时),AES-256-GCM,加密兑换码库存与收货地址
  secret_key_version: 1
  secret_keys_retired: {}
  address_retention_days: 90          # 下限 30
  max_products: 200
  code_upload_max: 500
  # 套餐商品依赖 two_phase.main_outbox_enabled=true;关闭则套餐商品不可上架、在途失败单全部转人工
```

lottery 段新增:`max_stake_stardust`(YAML-only)/ `max_total_prize_stardust` / `large_prize_alert_stardust` /
`pay_password_threshold_stardust`(默认给非 0 推荐值)/ `wheel_max_tiers`(默认 12)。

日界:**不新增键**,读 `commission.day_offset_minutes`(D-C)。`selfcheck` 的 `fieldConsumers` 是
**一项一文件**(`selfcheck.go:47-53`),不是"加第二个消费文件":`dayline` 抽到 `qianye/service/dayline`
后,把 `selfcheck.go:143` 那条改指新包文件,且新包文件必须**自己引用** `Config.Commission.DayOffsetMinutes`
(不能只接收偏移量参数,`selfcheck_test.go:100` 会解析文件确认引用)。`serverday` 单实现守卫
只抓 `time.Date(…,0,0,0,0,非UTC)`,`dayline.go` 全是 unix 秒算术,抽走不触碰它;**不要**照抄
`lottery/spend.go` 的 `dayRange`(那是本地时区 `time.Date`,会红且与 D-C 冲突)。

守卫:每个新叶子登记 `fieldConsumers`;三个 `*_stardust` 额度键显式走 `checkQuotaCap`;
`secret_key` 走 `checkAESKey` + `SecretKeys`;`enabled` 登记 `moduleGates`(`stardust` / `mall` 两行);
示例 YAML 写出 `stardust.enabled` / `mall.enabled`;`*_bps=0` 与 `settle_delay_minutes=0` 允许(`explicit_zero_test`)。

### 9.2 qy_settings 白名单

| scope | keys |
|---|---|
| `stardust` | `name`, `show_entry`, `consume_bps`, `invite_topup_bps`, `invite_redeem_bps`, `invite_register_stardust`, `held_alert_days` |
| `mall` | `show_entry` |
| `lottery`(追加) | `max_total_prize_stardust`, `large_prize_alert_stardust`, `pay_password_threshold_stardust`, `show_play_wheel`(0/1 区间进 `settingBounds`) |

`max_stake_stardust` **不在**白名单(§5.5)。`GET/PUT /admin/stardust/config` 下发五段,照
`lottery/api_admin_config.go:123-206`。

---

## 10. 接口清单

用户(`/api/qy`,UserAuth;写接口 `CriticalRateLimit` + `UserCriticalRateLimit`;限流是节流不是授权):

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/stardust/me` | 余额、余数、暂缓状态、昨日结算摘要、下次结算时刻(后端算) |
| GET | `/stardust/ledger` | 流水分页(`httpq.Paginate`),按 kind 筛 |
| GET | `/stardust/accruals` | 待结算 / 暂缓的日桶 |
| GET | `/stardust/forecast` | 明日预计到账(§4.1;`?refresh=1` 手动重算,服务端缓存一小时 + 60 秒节流) |
| GET | `/mall/products` / `/mall/products/:no` | 在售商品;`kind=plan` 附 Preview 结果与名额预检 |
| POST | `/mall/orders` | 下单(**验密** for code/physical) |
| GET | `/mall/orders` / `/mall/orders/:no` | 我的订单(不含码、不含地址明文) |
| GET | `/mall/orders/:no/code` | 揭示兑换码(**验密中间件** + `sensitiveReads`) |
| POST | `/mall/orders/:no/cancel` | 实物 `paid` 态取消 |
| POST | `/lottery/activities/:act_no/spins` | 转一次(§7.2) |
| GET | `/lottery/activities/:act_no/spins/me` | 我的转动记录 |

管理(`/api/qy/admin`,AdminAuth):

| 方法 | 路径 | 闸门 |
|---|---|---|
| GET/PUT | `/stardust/config` | 审计 `WriteConfigUpdate` |
| GET/PUT/DELETE | `/stardust/group-rates(/:group)` | 审计;残留登记 |
| GET/PUT | `/stardust/plan-rewards/:plan_id` | 审计;`sources` 闭集校验 |
| POST | `/stardust/adjust` | **RootActionGate** + ActorMayActOnCtx + 幂等 + 审计 ×2 |
| GET | `/stardust/balances` / `/stardust/ledger` / `/stardust/accruals` | 分页 |
| GET | `/stardust/ledger-check` | I0/I1/I2 + held 积龄 |
| POST | `/stardust/settle/rerun` | 审计;`{day}` 是桶日;只对 computed/held 桶生效 |
| GET/POST/PUT/DELETE | `/mall/products(/:no)` | 审计;`kind=plan` 需 outbox |
| POST | `/mall/products/:no/codes` | 批量上传(`credentialBodyRoutes`) |
| GET | `/mall/products/:no/codes` | 码库存分页(`?status=`);**不回明文**,只回状态 / 时间 / 去向 |
| POST | `/mall/products/:no/codes/:id/take` | 管理员提卡:**验密中间件** + 审计;解出明文并标 `taken` |
| DELETE | `/mall/products/:no/codes/:id` | 删一枚 `unused` 的码;`issued/revoked/taken` 是证据,409 |
| GET | `/mall/orders`;POST `/mall/orders/:no/ship` `/fail` `/revoke-code`;GET `/mall/orders/:no/address` | 审计;地址揭示 `sensitiveReads` |
| POST | `/mall/orders/:no/adjudicate` | **RootActionGate**(`Failed + MainApplied/MainUnknown` 的 held 单) |

公开(`PublicRouter`):`/lottery/public/:act_no/proof` 对 `wheel` 输出 §7.4;`/mall/covers/:ref`。

**受限账号**:`router/restricted_user_routes_test.go` 要求**每一条**路由被显式归类,`/api/qy/`(非 admin)
是混合子树、拒绝侧一律精确列举:上表全部用户路由逐条加进 `restrictedDeniedSessionRoutes`,
`GET /api/qy/mall/covers/:ref` 加进 `restrictedAnonymousRoutes`;前端 `QY_RESTRICTED_URLS` 不加。
`sensitiveReads` / `credentialBodyRoutes` 的键是「METHOD 完整路由模板」。

全部新路由 → `QY_ROUTE_MANIFEST_UPDATE=1 go test ./qianye/ -run TestQyRouteManifestIsCurrent -count=1`。

---

## 11. 前端

| 页面 | url | 落点 |
|---|---|---|
| 星屑(宿主:余额 / 流水 / 待结算) | `/qy/stardust`、`/qy/stardust-ledger`、`/qy/stardust-accruals` | **新组 `qy-stardust`** |
| 星屑商城(宿主:商品 / 我的订单) | `/qy/mall`、`/qy/mall-orders` | `qy-stardust` |
| 转盘 | `/qy/wheel` | `qy-stardust` |
| 星屑配置 | `/qy/admin/stardust-config` | `QY_SETTINGS_GROUP` |
| 星屑账本(余额 / 流水 / 日桶 / 手调) | `/qy/admin/stardust` | 新管理组 `qy-stardust-ops`(2 行) |
| 商城管理(商品 / 订单 / 码库存) | `/qy/admin/mall` | `qy-stardust-ops` |
| 转盘期次 | 走 `/qy/admin/lottery` 建活动向导,`draw_mode=wheel` | 已有 |

导航组:`pages-table.test.ts:217` 的上限是 7,但 `pages.ts` 头注释与 tickets 行注释写的意图是 ≤6;
`qy-settlement` 现 5 行,再塞 2 行就到 7。所以用户页开新组 `qy-stardust`(3 行,`qy-growth` 保持 4),
管理页开新组 `qy-stardust-ops`(2 行,同组成员角色必须齐),两个既有组都不动。

登记量(第一版稿写"各加一行",实际是):`QY_PAGES` +9 行、`page-order.ts` 末尾追加 9 个 GATE 号、
9 对 `titleKey / qy_sg_code_*`、3 个 redirect 桩(`stardust-ledger` / `stardust-accruals` / `mall-orders`,
宿主 = 第一张标签,与 `/qy/affiliate` 同形)、`QY_TAB_GROUPS` 新增 2 个宿主并同步 `qy-page-tabs.test.ts`
的 `HOST_SOURCES`(+2)与 `pages-table.test.ts` 的冻结快照(选择夹 5→7、`HOSTED_URLS` 14→17 即 +3、
`ROOT_ADMIN_URLS` +2、`SETTINGS_URLS` +1)、两个新导航组、
`QY_ERROR_CODE_I18N` 登记新错误码(`qy_sd_insufficient` / `qy_ml_sold_out` / `qy_ml_limit` / `qy_ml_off_sale` /
`qy_ml_plan_state_changed` / `qy_ml_plan_needs_outbox` / `qy_lot_wheel_closed` / `qy_lot_tier_exhausted` / `qy_idem_key_conflict` …)、
`qyKeys` 新键、`QyFeatures` 加 `stardust` / `mall` 并同步三处测试字面量、`QyAuditCategory` 联合类型与
`fund-audit-tab` 的 `CATEGORIES` 手写清单加 `stardust` / `mall`(Category 常量在 `qianye/model/audit_log.go`)。

主题:转盘的多色扇区与旋转动效撞 Midnight Signal 的"一支色相 / 辉光 ≤2 / 零投影"(`design-14 §4`)。
扇区只用四支语义色 + 两档图表色(族外允许项),不加辉光,`prefers-reduced-motion` 下不旋转直接显示结果;
"真·霓虹转盘"需要单独的主题豁免拍板。

**2026-09-05 补(项目方:「娱乐功能文字画太多,让人观感很差」,要求加动画与 SVG)**——
上面那条"只用六支色"在**色相**上一字未改,另加了两件不属于色相的东西,口径写在
`web/src/styles/qy-visual.css` 的头注释里:

1. **光**。盘面与号码球各盖一层白/黑的低透明度渐变(左上高光、右下暗面)。它不是色相
   (合成在任何一支扇区色或任何一张管理员配的封面之上都成立,换主题预设时不用跟着改)、
   不是辉光(那两处配额仍归后台页头与落地页首屏)、也不是投影(零投影管的是 `box-shadow`)。
   同一条口径下还有大厅封面顶部那层压暗 `.qy-art-scrim` —— 徽章压在管理员随手配的图上,
   底图是亮是暗无从预知。
2. **「谢谢参与」那一格换色**:`--muted` → `color-mix(in oklch, var(--foreground) 22%, var(--background))`。
   `--muted` 在浅色下是 `oklch(0.97)`、深色下是 `oklch(0.305)`,两边都紧贴 `--card`,而这一格
   通常是盘上最大的一块(落空概率常在 50% 以上) —— 盘面因此被挖掉一个与背景同色的大缺口。
   取值仍是纯中性,不占那"一支色相"的配额。`wheel/lib/__tests__/spin.test.ts` 钉着它不许退回 `--muted`。

动效一律 CSS 驱动(`transform` / `opacity` / `stroke-dashoffset`),`prefers-reduced-motion: reduce`
下全部退化成静态终态。奖档表(抽奖 / 双色球 / 转盘三份)从 `StaticDataTable` 改成"一档一行"的清单,
原文一个字没删,只是换了位置与字号 —— 字数上限仍由 `lottery/__tests__/text-budget.test.tsx` 守。

金额组件:§8.2。报名 / 转动 / 商城 code/physical 成功后 `invalidate(qyKeys.all)`;商城套餐(动主库)
才 `useQyAfterMoneyChange`。

---

## 12. 地基契约核对表

| 守卫 | 本设计的动作 |
|---|---|
| `modules_test`(目录名 = Name) | `stardust` / `mall` 两个新目录;`wheel` 在 lottery 内 |
| `module_section_guard` / `sections_test` | `moduleGates` 加两行;示例 YAML 写出 |
| `selfcheck_test`(fieldConsumers,一项一文件) | 每个新叶子登记;`commission.day_offset_minutes` 改指新 dayline 包文件(它自己引用字段) |
| `quota_cap_coverage_test` | 三个 `*_stardust` 额度键显式进 `checkQuotaCap` 清单 |
| `explicit_zero_test` | `*_bps=0` / `settle_delay_minutes=0` / `quota_per_unit=0` 允许 |
| `route_manifest_test` | 重生成 |
| `restricted_user_routes_test` | 用户路由逐条进 `restrictedDeniedSessionRoutes`,封面进 `restrictedAnonymousRoutes` |
| `audit_coverage_guard_test` | 配置写 / 手调 / 商品上下架 / 发货 / 撤码 / 地址揭示 / 码揭示 / rerun / 裁决 全部登记;新 Category `stardust` `mall`(`qianye/model/audit_log.go`) |
| `actor_gate_guard_test` | 手调、裁决登记 |
| `root_action_guard_test` | `RootActionStardustAdjust` / `RootActionMallAdjudicate` 常量 + `rootGateSite` 各一条;`wantChecked` 15 → 17 |
| `usergroup_residue_coverage_test` | `qy_sd_group_rate.user_group`(Probe clean / Sweep 按 rename Update|Delete)、`qy_sd_accrual.user_group`(keep)、`qy_sd_ledger.rate_group`(keep,不在扫描列集合内);`stardust` 追加进 `knownKeyedModules` |
| `schema_index_name_test` / `schema_crossdb_test` / `migrate_idempotency_test` | 索引名 `idx_qy_sd_*` / `uk_qy_sd_*` / `idx_qy_ml_*`;decimal default 写满 scale;bool 不写 default;二进制列 `qymodel.Blob`;`QY_TEST_MYSQL_MIGRATE_DSN` 至少在演示机跑一次 |
| `json_array_guard_test` / `httpq_guard_test` / `ctx_guard_test` | 照规矩 |
| hookpoint 守卫(`subscription_hookpoint_guard_test.go:39-139` 形状) | 新建 `qianye/stardust_hookpoint_guard_test.go`:`QyOnUserRegistered`(两处各恰好一次)、`QyOnSubscriptionGranted`(四处)、`commission.AfterRedeemSuccess` 调用先于 `!cm.Enabled` 分支;默认实现函数体为空 |
| import 图守卫(新,`qianye/module_import_guard_test.go`) | `qianye/modules/*` 之间不得成环;`commission` 不得 import `stardust`(`stardust → commission` 单向允许) |
| 新写的 AST 守卫 | `lottery/seed_guard_test.go`(§7.2 三条);`mall/secret_guard_test.go`(`code_cipher` 只经 `sealCode/openCode`,`address/contact_cipher` 只经 `sealAddress/openAddress`,`mall.prune` 的置空更新显式豁免);lottery 锁序 / 幂等预读;`Credit/Debit` 错误分支只能 return;lottery 包内禁 `== AlgoV2` 字面比较;`isFullRefundOutcome` 对 wheel 不可达 |
| `single hook slot`(`qy_log_hookpoint_test`) | 不改 `QyOnConsumeLog`;`QyOnRedeemSuccess` 仍由 commission 独占,`commission.AfterRedeemSuccess` 是第二级转发槽,由 `stardust.InstallHooks` 接管 |
| `seed / secret / fund_guard`(lottery) | `loadSeedForSpin` 进白名单(守卫本期新建);资金铁律不变 |
| `paypass/feature_gate_test` | `FlagPayPassword` OR 链加 `Mall.Enabled` |
| `play_visibility_db_test` / `hall_lane_db_test` | `Plays` 4→5;转盘不进大厅 lane 的断言改写 |
| `serverday` 单实现守卫 | 不触碰(dayline 无 `time.Date`);不抄 `spend.go:dayRange` |
| commission 共享设施守卫(新) | `Commission.Enabled=false && Stardust.Enabled=true` ⇒ `cacheSyncOn=true` |
| 并发测试(真 MySQL) | 转盘连转 + worker 零 1213;星屑报名并发同 crid 只扣一次 |
| 前端 `pages-table` / `page-order` / `route-entry-guard` / `i18n-key-coverage` / `route-contract` / `qy-page-tabs` | §11 |

### 12.1 实施偏差记录(与上文字面不同、有意为之)

| 处 | 设计文写的 | 实际落地 | 为什么 |
|---|---|---|---|
| 日结重跑幂等键 | `sdsettle:<run_date>:<user_id>` 一把键 | 日结 `sdsettle:<run_date>:<uid>`,重跑 `sdrerun:<D>:<uid>:<发起时刻>` | 同一天"先重跑老日子、再跑当天日结"是两笔合法发放;共用键会把第二笔判成重放并整笔回滚,桶永远结不了 |
| 三个 hook 的预过滤 | 读 `effective()` | 读进程内快照 `cachedSettings()`,快照缺失时放行到 worker 再判 | hook 体必须 O(1) 无 I/O;`effective()` 会回库 |
| `ledger-check` 响应 | 只有数值 | 多 `ok` / `error` 两字段 | 体检查询本身失败(扩展库不可用)要和"账平"区分开 |
| 手调错误码 | 只列 `qy_sd_adjust_too_large` 等 | 另有 `qy_sd_user_not_found`(400)、`qy_self_dealing` / `qy_target_not_manageable`(403) | 复用既有 actor 闸门与目标可管理性判定的错误形状 |
| `DELETE /admin/stardust/group-rates/:group` | 无 body 约定 | `{user_group, deleted}` | 前端要知道删的是哪一档、有没有真删到 |
| `PUT /admin/stardust/config` | 返回保存值 | 返回 `{effective}`(合并 YAML 基线 + 覆盖后的生效值) | 前端表单要回显生效值而不是覆盖值 |
| `DELETE /admin/stardust/plan-rewards/:plan_id` | 无 body 约定 | 返回 `exists=false` 的默认视图 | 抽屉删完直接用响应回填,不再多一次 GET |
| qy_settings 跨节点失效 | 未明说 | 未做:单节点进程内 60s 缓存 | 演示站单节点;多节点部署改配置最迟 60s 生效,写进 runbook |
| 版本号 | `v1.1.0`(MINOR) | `v2.0.0`(MAJOR) | D-11:不留旧路径、不迁旧数据,`lot-v3` 与 `currency` 列的兼容分支全部不存在 |
| 商城发货完结 | shipped→done 无独立入口 | `POST /admin/mall/orders/:no/ship` 请求体可选 `done:true`:发货即完结,或对已 shipped 的单只做完结 | 少一条端点;人工发货的运营多数一步到位 |
| 退款与库存 | 未明说 | 实物 / 套餐退款 `sold−1`;兑换码撤回不放回(码已暴露) | 限购本就不数 failed / cancelled,库存要跟着回来 |
| 主库拒绝的错误码 | 未列 | `CreateUserSubscriptionFromPlanTx` 的中文拒绝句按"购买上限 / 永久拥有 → `qy_ml_limit`、名额 → `qy_ml_sold_out`"归类,其余按内部错误(仍退星屑) | 上游只给句子不给码 |
| 封面 | 无配置项 | 固定 ≤4 MiB(imagestore 8 MiB 硬顶)、每管理员待挂 10 张 | 与 lottery 封面同一口径 |
| 转盘换参重放的错误码 | 契约 §7 写 `qy_idem_key_conflict` | `qy_lot_idem_conflict`(与报名同一个码) | 前端已认这一个,少一条映射 |
| 转盘 proof 的 NDJSON | `spins` 与 entries 同形分页 | JSON 形态 `spins[]` 与 entries 同页同序;NDJSON 头里 `spins` 为空,每条 entry 行带 `spin` 子对象 | 流式一行一票,转动结果跟着票走,不另开一路 |
| 转盘「取消」 | 只写"绝不 planFullRefund" | 有转动 → 提前封盘,响应 `{status:"locked", early_locked:true}`;一转都没有 → 正常 cancelled;已封盘再点 → `qy_lot_wheel_no_cancel` | 已转出去的结果不可撤,取消只能是"不再收新转" |
| `spins/me` 分页 | 未明说 | `{items,total,p,page_size}`,参数 `p` / `page_size`,按 seq 倒序 | 沿用本模块既有分页形状 |
| 列表接口的 wheel 行 | 只说按 draw_mode 过滤 | 额外带 `tiers[]`(含 `stock_left`),`draw_mode` 不能与 `lane` 同给(400 `qy_lot_bad_draw_mode`) | 卡片要画"剩余 / 初始",不能为每张卡再打一次详情 |
| 转盘并发用例 | 真 MySQL 零 1213 | SQLite 单连接下只验"两路都落定、seq 连续、恰好扣两次";真行锁竞争留给演示机的 `QY_TEST_MYSQL_MIGRATE_DSN` 一轮 | 本机没有 MySQL |
| 转盘的时刻 | 与批次玩法同用四个时刻 | 运营只填「开始 / 结束」(项目方 2026-09-04:像抽卡卡池);`draw_at` 不填时后端按「结束 + reveal_delay_seconds」派生,`settle_deadline` 恒 0;结束后不再受理转动,种子自动公开,复算入口在活动页 | 转盘没有"封盘摇号"这一步,封盘 / 开奖两个概念对它只是噪音 |
| 转盘承诺原像 | 与批次玩法一样含 open_at / close_at / draw_at | `CommitHashV2` 对 wheel 省略这三个分量;已发布转盘可 `PUT /admin/lottery/activities/:act_no/schedule` 改开始 / 结束(draw_at 重新派生),有「立即开始」;验证器 py / ts 同步 | 转盘票面 = HMAC(seed, act_no, seq, client_seed),公平性与排期无关;排期只是卡池上下架时间(项目方 2026-09-04) |
| 奖品种类 | quota(星屑)/ text(人工填码) | 新增 `product`:奖档引用商城商品(plan / code / physical),中奖即建一张 0 星屑的商城订单(`source=lottery`,幂等键 `lotprize:<payout_no>`);套餐立即生效且订阅 source=`lottery` 不触发套餐返;码当场分配,不足则单据等码(上传码时补齐);实物由中奖者补填一次地址后人工发货 | 项目方 2026-09-04:「转盘不要局限于星屑,增加一些套餐、兑换码、实物」 |
| 奖品套餐的落地 | 走 twophase(KindMallPlan) | 不走 `twophase.Execute`(它要求金额 > 0,0 元奖品单会污染资金单积压合计):扩展库落单后直接 `applyPlanOnMain`,以主库 `subscription_orders.trade_no` 唯一索引收敛,`mall.reconcile` 兜底补发 | 奖品单没有"扣了星屑要不要退"的问题,两阶段防的那件事在这里不存在 |
| 奖档原像 | `PrizeSpecLineV2` 10 位 | 加第 11 位 `product_no`;Go / Python / TS 黄金值同步;v2.0.0 之后、本次之前发布的活动 spec_hash 失配(演示站清库) | 公示奖档要能对上是哪件商品 |
| 活动标题 / 说明 | 发布后不可改 | `PUT /admin/lottery/activities/:act_no/basics`:草稿 / 进行中 / 已封盘可改,结算或结束后 409;标题不进承诺原像 | 项目方 2026-09-05:「星屑转盘应当可以命名」 |
| 建活动向导的关闭 | 未明说 | `QyResponsiveDialog` 加 `dismissible`,向导传 false:点空白与 Esc 不关,只有 × 与取消能关 | 项目方原话「误触旁边空白导致窗口关闭,丢失很多编辑的信息」 |
| 侧栏分组 | 「星屑」「星屑运营」两个新组 | 并入「娱乐」「娱乐运营」:抽奖竞猜从「推广」、抽奖活动从「结算」挪入 | 项目方 2026-09-04:星屑板块与娱乐板块合并 |

---

## 13. 与既有拍板的关系

| 拍板 | 关系 |
|---|---|
| D-01 透支 | 不碰结算侧。星屑侧只做"透支账号暂缓发放"(best-effort 快照),不加任何余额下限 |
| D-02 费率按上线分组 | 邀请返星屑同口径;**消费返按结算时刻的用户自己分组**,与佣金"消费时刻冻结、换组落两行"不同(D-B 放弃 hook) |
| D-03 一日一结算 / 日界 | 共用日界与调度形状;到账时刻只后端一份 |
| D-04 提现人工发放 | 实物商品同形;**套餐商城自动发放与它相反**,需要 D-K 拍板,且商城自带对账三件套 |
| D-05 独立库 | 星屑 / 商城全部 `qy_` 表;名称进 `qy_settings`;唯一跨库写是套餐商城(两阶段) |
| D-07 密钥必填 | `mall.secret_key` 同规格;转盘文本奖沿用 `lottery.prize_secret_key` |
| D-10 管理员参与 | 批次抽奖不变;**转盘是例外**(D-J),竞猜照旧 |
| 2026-08-14 用户组商品 | 商城套餐先 `PreviewUserGroupPurchase` 让用户确认,**并在 MainApply 内复核确认集合**(上游四条路径保持原窗口) |
| design-12 裁决 1(支付密码逐条明说) | §10 接口表逐条标注验密与否 |

---

## 14. 版本与迁移

- `qy_version` → `v2.0.0`(MAJOR,D-11):**不留旧路径、不迁旧数据**。lottery 只认星屑(`CurrencyStardust` 是常量,
  没有 `currency` 列,没有额度活动的兼容分支);twophase 整体退出 lottery;`lot-v2` 协议不抬升,金额数字直接是星屑。
  `stardust` / `mall` 段缺失或 `enabled: false` 时账本不写、商城 404;`lottery.enabled: true` 而 `stardust.enabled: false`
  的部署在报名那一刻拿到 `qy_sd_*` 错误 —— 星屑是抽奖唯一的钱,关掉它等于关掉抽奖的收付。
- **升级注意**(v1.x → v2.0.0):
  1. YAML 是严格解析(`KnownFields(true)`):`lottery.max_stake_quota / max_total_prize_quota / large_prize_alert_quota /
     pay_password_threshold_quota` 四键改名 `*_stardust`(数值语义从额度变成星屑,**不能照抄旧值**),
     `lottery.excluded_manual_after_seconds` 删除 —— 老 YAML 里还写着它们的部署起不来,改完键再起;
  2. `mall.enabled: true` 要求 `mall.secret_key`(缺则 FATAL);套餐商品要求 `two_phase.main_outbox_enabled`;
  3. 删除的接口与错误码:`POST /admin/lottery/activities/:act_no/payouts/:payout_no/adjudicate`、
     `RootActionLotteryPayoutAdjudicate`、`qy_lot_in_progress / not_settled / entry_excluded / user_unavailable /
     quota_overflow / entry_in_flight / delete_entry_open / payout_needs_manual`;`qy_lot_entry` 只剩 `success` 并加
     `fingerprint`(无 `fail_code`)、`qy_lot_payout` 去 `epoch`、活动 DTO 去 `pending_count`、管理端配置 `system_max_stardust`;
  4. `qy_lot_*` 存量数据不迁移:额度活动、twophase 资金单形状的票与出款按 D-11 整表清空后再启用
     (当前版本未上线,演示库直接重建);`lottery-verify.py` / `verify.ts` 加了 wheel 分支,旧版本验不了转盘。
- 迁移:全部新表 `qy_sd_*`、`qy_ml_*`;`qy_lot_prize.stock_left`;`qy_lot_entry.fingerprint / client_seed / result_tier /
  ppm / exhausted_tier`(后四列随转盘)。
- 运维 runbook(演示站):① 填 `stardust` / `mall` 段与密钥(`data/qianye-prod.yaml` 已填,`enabled: true`)→ ② 重建前端 +
  重编二进制 + 重启(前端是 go:embed 进二进制的)→ ③ 系统设置把 `QuotaForInviter/Invitee` 置 0、展示模式切 CUSTOM/星辉/1
  → ④ 管理端配分组比例与套餐返 → ⑤ 多节点部署注意 qy_settings 覆盖最迟 60s 生效(§12.1)。

---

## 15. 实施阶段

| 阶段 | 内容 | 依赖 | 占比 |
|---|---|---|---|
| S0 | 抽 `dayline` 到共享包(改 `selfcheck.go:143` 路径);导出 `commission.ExcludedTopUp(t, excludeManual) / TopUpBaseQuota / InviteeEligible`;新增 `commission.AfterRedeemSuccess` 变量 + 位置守卫;commission 共享设施脱离开关(§4.6);`QyOnUserRegistered` / `QyOnSubscriptionGranted` 两个 hook + hookpoint 守卫;import 图守卫;新建 `seed` 守卫 | — | 8% |
| S1 | `stardust` 模块:表、`Credit/Debit`、配置段、qy_settings、手调、体检、用户端三接口、`/api/qy/config` 段、前端 `/qy/stardust` 宿主与配置页、`QySdAmount` | S0 | 18% |
| S2 | 获得途径:日结(logs 重算 + 两路候选 + 暂缓)、充值 / 兑换码 / 注册返、套餐返 hook + 附表 + 套餐抽屉字段 | S1 | 18% |
| S3 | lottery 切星屑:`currency` 三表、幂等重放、资格判定、两条资金路径分叉、`lot-v3` 完整抬升、闸门新键、验密阈值、文案清单、验证器、前端用户端 + 管理端金额组件替换 | S1 | 22% |
| S4 | 转盘:`draw_mode=wheel` 全部分派点(§7.5)、派生 `none` 档、`stock_left`、`spins`、生命周期、证据链、`seed` 守卫、前端 `/qy/wheel` | S3 | 12% |
| S5 | 商城:`mall` 模块、码库存加密、实物人工发货、验密、**套餐两阶段 + reconcile + Resolver + 裁决最后做**、前端 `/qy/mall` 与管理页、资金单页单位 | S1 | 17% |
| S6 | `decisions.md` 追加(含合规门口径与 commission 现状)、`README` 索引、`baseline.txt` v2.0.0、runbook(含星辉手动切换步骤) | 全部 | 5% |

S2 / S3 / S5 三条线可并行;S4 依赖 S3。

---

## 16. 明确不做的

- 星屑过期、星屑划转、星屑兑回余额、多级邀请(`levels=1`)、退款冲减(`refund_clawback` 留作可选项)。
- 把上游"额度"文案全站改成"星辉"(D-M 只改一处默认)。
- 上游注册奖的自动折算 / 清零(D-G)。
- 商城购物车、多件、优惠券、自动生成主库兑换码、本站余额码上架。
- 转盘的主题豁免、转盘管理员参与(D-J)。
- 消费时刻的分组快照(§4.1 按结算时刻)。

---

## D-16 · 推广佣金也改记星屑(2026-09-05,与 D-15 同日)

拍板与完整后果在 `decisions.md` D-16。这里只补本文档口径上受影响的三处。

### 1) 折算口径:佣金与消费返共用同一条式子

佣金的 gross 从"额度"变成"星屑",算法与 §4.7 的下线消费返逐字同形:

```
gross = base_quota × rate_bps / 10000 / quota_per_unit
```

刻度与费率、分组一样**逐行冻结**(`qy_commission_accrual.quota_per_unit`),与
`qy_sd_accrual.quota_per_unit` / `qy_sd_invite_accrual.quota_per_unit` 同一条纪律、
同一个来源(`stardust.QuotaPerUnit()`)。冲正取**原单**冻结的刻度,不取当刻的。

`base_quota` 仍然是额度 —— 它是分母(下线实际花掉多少),不是返给谁的钱。用户端与
管理端因此同屏出现两种单位:基数印 `$…`,佣金印 `… 星屑`。这不是遗漏,是"花了多少、
返了多少"这条唯一能自己验算的式子必须两边都看得见。

### 2) 跨库两阶段入账整层退役

§8.3「星辉」那一节描述的是**站内余额的展示名**,仍然成立(`users.quota` 的 CUSTOM 符号)。
但 D-15 建立在它之上的那条**佣金入账链路**没有了:

| D-15(佣金记星辉) | D-16(佣金记星屑) |
| --- | --- |
| 扩展库冻结 → 主库 `IncreaseUserQuota` → 扩展库销账 | 一个扩展库事务:`stardust.Credit` + `available → credited` |
| 两阶段资金单 `KindCommissionCredit`、主库探针、人工裁决、对账循环 | 无 |
| `qy_commission_freeze` 表、`balance.frozen_quota` 列 | 已删 |
| credit 状态 pending / done / failed / held | 只剩 `done` |
| `credit.fund_order_no` 指向资金单 | `credit.ledger_no` 指向 `qy_sd_ledger` 那一行 |

理由是一句话:那一整层回答的是"主库到底动没动",而佣金账本与星屑账本同库之后
这个问题不存在了。新增依赖 `commission → stardust`(无环:commission → stardust → invite)。

星屑账本新增 kind `commission_credit`(落 `total_earned`),幂等键
`(idem_scope=commission_credit, idem_key=credit_no)`。

### 3) D-G ③ 的订正

原文写着"上游推广卡本来就只在 `aff_quota>0` 时显示"——读错了源码,`hasRewards` 只控制
「转入余额」按钮。已在 §D-G 就地订正,门开在 qy 宿主一侧。
