# 缺陷溯源表：这些问题是我们改出来的，还是原项目本来就有的？

**生成时间**：2026-08-19
**作者**：审计第 6 轮（溯源专项）

---

## 这份表是干什么的

项目方连着问了三次同一个问题：**「刚修的这一堆缺陷，是我们自己改代码改出来的，还是
原项目 new-api 本来就有的？」**

这个问题很实在。它背后其实是三件事：

1. **责任归属** —— 如果是我们改出来的，说明我们的改动质量有问题，要复盘；
   如果是原项目自带的，那我们是在替原项目擦屁股，不该记在我们头上。
2. **要不要回馈上游** —— 如果是原项目的问题而且它还没修，我们可以把修复提回去，
   既省得以后每次同步都要重新打一遍补丁，也是对开源社区的正常回馈。
3. **同步策略** —— 如果原项目已经自己修了，那我们最该做的不是维护自己的补丁，
   而是**跟上游合并**，用它的版本替掉我们的。

这份表就是逐条回答这三件事。每一条都给出**原项目的文件名和行号**，可以直接去
GitHub 上翻原文核对，不需要相信我的转述。

---

## 三句话结论

**第一，绝大多数是原项目自带的问题，不是我们改出来的。**
能直接造成资金损失的那几条 —— 阶梯计价把额度算成负数、信任额度旁路让并发扣款失效、
MJ 提交先判后扣、音频时长解析不出来就免单、上游 token 计数溢出 —— **全部**是原项目
的代码，我们一个字都没动过就继承了下来。

**第二，我们自己的模块（`qianye/` 目录）确实也有一批问题，但形状不一样。**
它们集中在佣金、提现、抽奖、违规这几个我们新写的模块上，性质多是「多节点部署下缓存
不同步」「管理员可以自己批自己」「统计口径不自洽」这类，而不是核心计费链路上的算错钱。

**第三，也是最有价值的一条：原项目在我们拉分支之后，已经自己修掉了其中一条。**
「充值下单时金额没有上限」这个问题，原项目在 **2026-08-14** 通过两个提交修好了
（PR #6845 和紧随其后的 47ba9d2c6），而我们的分支基线停在 **2026-08-10**，所以没同步到，
我们在 08-19 又独立修了一遍。这说明**我们和上游看到了同一个问题，结论一致**，
也说明我们该考虑定期同步上游了。

---

## 怎么查的（方法说明）

| | |
|---|---|
| **我们的分支** | `qianye`，当前 HEAD `7d4e2df2c` |
| **分叉点** | `ccd535ef8`（2026-08-10，原项目 `main`） |
| **原项目最新** | `f11641428`（本次溯源时重新拉取，比分叉点多 21 个提交） |
| **原项目地址** | `https://github.com/QuantumNous/new-api` |

判断方法很简单，三步：

1. **这个文件原项目有没有？** 没有（比如 `qianye/` 下面的所有东西）→ 一定是我们独有的。
2. **有的话，出问题的那几行，我们动过没有？** 把「修复之前」的我们的版本和原项目的
   版本逐字节比对。**完全一样** → 问题是原项目的，我们只是继承。**不一样** → 再看
   具体是哪几行不一样，是我们改坏的还是我们在原有基础上加了东西。
3. **原项目现在修了没有？** 拿原项目**最新**的代码再查一遍那几行。

用到的命令是 `git show 原项目版本:文件名`、`git blame`、`git log -S`。
下面每一条的「证据」列，都可以照着去复核。

---

## 四种来源，什么意思

| 来源 | 含义 | 谁的责任 |
|---|---|---|
| **上游自带** | 出问题的代码逐字节来自原项目，我们没碰过 | 原项目 |
| **上游自带（已修）** | 同上，但原项目在我们分叉之后自己修好了 | 原项目（已解决），我们该同步 |
| **fork 加重** | 原项目本来就有毛病，我们的改动让它更容易被触发或后果更严重 | 双方 |
| **fork 独有** | 出问题的模块整个是我们新写的，原项目没有这东西 | 我们 |

---

# 第一组：上游自带，原项目至今没修

**这一组是重点。** 27 条，全部是原项目 `main` 分支上**现在**仍然存在的问题。
按严重程度排序 —— 排在前面的能直接造成钱算错、算反、或者白送。

## A 级：能直接造成资金损失

### 1. 阶梯计价 + 工具附加费，能把扣款算成负数（等于给用户送钱）

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `service/text_quota.go:203-226`（函数 `composeTieredTextQuota`）。第 208-215 行：当有工具附加费时，它丢掉上一步已经夹到 0 以上的结果，改用未经夹取的 `tieredResult.ActualQuotaBeforeGroup` 重新算，负值原样返回。阶梯计价引擎 `pkg/billingexpr/` 整个也是原项目的。 |
| **我们动过吗** | 没有。把修复前的我们的版本和原项目逐行比，这个函数**一个字符都不差**。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **强烈建议。** 这是能真正印钱的洞（线上实测把某账号余额从一千万推到四亿六千万），修复很小（补一道和相邻代码同义的非负地板），不改任何接口。 |

### 2. 「信任额度」旁路让并发扣款彻底失效

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `service/billing_session.go:296-330`（`shouldTrust`），配套 `common/quota.go:3`（`GetTrustQuota`）。余额超过阈值时预扣额被直接置 0，原子预留形同虚设。 |
| **我们动过吗** | 没有。这个函数是原项目的原样代码，我们是把它整个删掉。 |
| **上游修了没** | **没有**，`upstream/main:service/billing_session.go:191` 还在调 `s.shouldTrust(c)`。 |
| **要不要提 PR** | **强烈建议，但要先和上游沟通。** 实测：余额 5,000,000（不受信任）50 并发只过 1 笔；余额 5,000,001（受信任）同样 50 并发全过，把余额打到 −35,000,499 —— **一个额度单位之差**。不过「信任额度」是上游有意设计的性能优化（省一次数据库写），直接删掉是产品决策，应该开 issue 讨论而不是直接发 PR。 |

### 3. MJ 提交与换脸：先判余额，60 秒后才扣钱

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `relay/mjproxy_handler.go:214` 和 `:527` —— `model.GetUserQuota(...)` 读一次余额快照，第 222 行判一次，然后是最长 60 秒的上游调用，扣钱在 `defer` 里。典型的「先检查后动作」竞态。 |
| **我们动过吗** | 没有。整个文件与原项目**逐字节相同**。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 实测 8 并发把 50000 余额打到 −350000，40 并发打到 −1,950,000。修法（先原子预留、失败即拒绝、未结算的出口原样退回）是自洽的，不改协议。 |

### 4. 音频文件解析不出时长 = 免费

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `common/audio.go:25-45`。`.mp3` / `.m4a` / `.mp4` / `.ogg` / `.oga` / `.opus` 这几个分支解析失败时返回 `(0, nil)` —— 0 秒，也就是不收钱。对比第 29-30 行的 `.flac`，它是老老实实报错的。 |
| **我们动过吗** | 没有。整个文件与原项目**逐字节相同**。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **强烈建议。** 实测：同一份 19MB 文件，叫 `.wav` 扣 375000，改名成 `.ogg` 扣 0。改个文件后缀就白嫖，任何用户都能做到，没有门槛。 |

### 5. 上游返回负数 token，整笔免单

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `service/text_quota.go:257-259`。上游报的 `PromptTokens` / `CompletionTokens` 原样采信，没有非负夹取。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议，和第 6、7 条打包成一个「上游用量数据要当成不可信输入」的 PR。 |

### 6. 免单判据被整数溢出推翻

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `service/text_quota.go:75-77`：<br>`func (s *textQuotaSummary) hasBillableUsage() bool { return s.TotalTokens > 0 \|\| !s.ToolCallSurchargeQuota.IsZero() }`<br>而 `TotalTokens` 在 `:259` 是 `usage.PromptTokens + usage.CompletionTokens`。两个分量各报 `MaxInt64` 时相加回绕成 −2，`> 0` 为假 → 整笔免单。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议，同上打包。 |

### 7. 缓存/图片/音频 token 不受 prompt 总量约束

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `service/text_quota.go:260-265`：`CacheTokens` / `CacheCreationTokens` / `ImageTokens` / `AudioTokens` 四条腿全部原样取自上游返回，全链路唯一上界是 int32 饱和。按协议它们本该是 prompt token 的子集。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议，同上打包。一次请求就能把任意用户余额扣到 −21 亿。 |

### 8. 套餐预扣退款被退三遍

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `model/subscription.go:1402`（`RefundSubscriptionPreConsume`）在自己的 `DB.Transaction` 里调用 `PostConsumeUserSubscriptionDelta`，而后者**自己又开了一个 `DB.Transaction`**（用的是全局 `DB` 而不是传进来的 `tx`）。内层先提交，外层回滚不掉它，而重试逻辑会重试 3 次。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 一笔 3000 的预扣被退成 9000。顺带还消掉锁序倒挂和连接池自死锁两个隐患。 |

### 9. 违规罚款金额用裸 int 转换，会溢出成垃圾或静默变 0

| | |
|---|---|
| **来源** | 上游自带（**注意：这是原项目的功能，和我们自己写的违规模块无关**） |
| **证据** | `service/violation_fee.go`。这个文件来自原项目 PR #2753「grok Usage Guidelines Violation Fee」，我们修复前与原项目**逐字节相同**。它用 `.Round(0).IntPart()` 后再裸 `int()` 转换，3.7e13 变成回绕垃圾、1.9e13 静默变 0（罚款直接消失）。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 原项目自己的规范（`AGENTS.md`）就写着「禁止裸 int 转换，必须走 `common/quota_math.go`」，这里是它自己违反了自己的规范，PR 会很好接受。 |

### 10. 预扣费不管输出侧，一次请求就能把余额扣成负数

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `relay/helper/price.go:120-125`：倍率计价路径的预扣额是 `preConsumedTokens × ratio`，`preConsumedTokens` 只来自输入侧。对比 `:274-278`：阶梯计价路径**有**一个 `defaultTieredPreConsumeMaxTokens = 8192` 的输出侧兜底。同一件事，两条路径一条有一条没有。 |
| **上游修了没** | **没有**，`upstream/main:relay/helper/price.go:42` 那个常量至今仍然只用在阶梯计价那一支。 |
| **要不要提 PR** | **强烈建议，而且这一条最好提。** 修法就是把已有的常量用到另一条路径上，改动极小、风险极低、原项目自己已经证明了这个做法是对的。实测 gemini-3-flash 一次请求把 3100 额度的钱包打到 −11621。 |

## B 级：账目不自洽 / 观测失真

### 11. 结算失败被吞掉，但日志照记全额

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `service/quota.go:231-233` 和 `service/text_quota.go:451-453`，两处都是 `if err := SettleBilling(...); err != nil { logger.LogError(...) }` —— 记一行错误日志就继续往下走，消费日志仍然写完整金额。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议。改动很小（把失败标记进日志的 `other.admin_info`），但让「账对不上」这件事从此可查。 |

### 12. 进程退出前不刷批量队列，最后一个窗口的扣费全丢

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `model/utils.go:52-110`（`batchUpdate`）+ `main.go:154`。开了 `BATCH_UPDATE_ENABLED` 之后额度变动只在内存里排队（默认 5 秒一刷），而 `main.go` 的退出路径上没有任何 flush。 |
| **我们动过吗** | 没有，`model/utils.go` 修复前与原项目逐字节相同。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 仓库自带的 `docker-compose` 默认就开着这个开关，也就是说默认部署就中招。实测 3 次请求 logs 合计 1800 万，而 users 表纹丝不动。修复是一行 `model.FlushBatchUpdates()`。 |

### 13. 批量队列落库失败，增量直接丢弃

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `model/utils.go:82-93` 和 `:109-110`。队列在 `:74` 已经被整体换出，落库失败时只打一行日志（`:87`），既不重试也不还回队列。后果是「消费日志写了、钱没扣」—— 用户白嫖，平台还照常给上线发佣金。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议，和第 12 条打包成一个「批量队列的可靠性」PR。 |

### 14. 所有关键接口共用一个限流桶

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `middleware/rate-limit.go:174-176`：`CriticalRateLimit()` 对**所有**关键路由都传同一个标记 `"CT"`，而 `:128` 的内存实现和 `:44-49` 的 Redis 实现都只按这个标记 + IP 建键。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 匿名刷 `GET /api/ratio_config` 20 次，就能把同 IP 的充值 / 提现 / 划转 / 登录一起打成 429。这是个免费的拒绝服务，任何人都能对任何共享出口 IP 的用户群体发动。 |

### 15. 流式响应中途报错，整笔用量被丢弃、不计费

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `relay/channel/claude/relay-claude.go:209-211`：`if err != nil { return nil, err }` —— 直接丢掉已经攒好的 `claudeInfo.Usage`。AWS 渠道 `relay/channel/aws/relay-aws.go` 同形。 |
| **我们动过吗** | 没有，两个文件修复前都与原项目逐字节相同。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 实测 20 段正文全部交付给客户端、HTTP 200、usage 完整，而计费零行。而且原项目自己的 openai / gemini / dify / baidu / responses 五个渠道**本来就是按已知用量计费的** —— 也就是说 claude 和 aws 这两个是漏改，PR 只是把它们对齐到同项目内已有的口径，很好接受。 |

## C 级：安全与权限

### 16. 兑换失败时把兑换码明文写进日志

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `controller/user.go:1378`，逐字是：<br>`logger.LogError(c, fmt.Sprintf("failed to redeem key %s for user %d: %s", req.Key, id, err.Error()))` |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **强烈建议。** 兑换失败**不消耗**兑换码，所以日志里那串是**还能用的活码**。等于「日志读取权 = 兑换码提取权」—— 证伪环节真的从日志里 grep 出活码，换个账号兑走了。修复就是打码保留末 4 位，一行。 |

### 17. 管理端改兑换码面额，完全不校验（可以改成负数）

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `controller/redemption.go:162`：`cleanRedemption.Quota = redemption.Quota` —— 直接赋值，没有任何校验。建码那一侧是有正数校验的，改码这一侧漏了。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议。负面额的码兑换时是从用户余额**倒扣**，而接口、前端提示、充值日志三处都在说「充值成功」。 |

### 18. 已兑换的码可以被翻回「启用」，再兑一次

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `controller/redemption.go:166`：`cleanRedemption.Status = redemption.Status` —— 状态直接赋值，没有状态机约束。兄弟接口 `controller/token.go` 的同类分支**是有**状态机的，兑换码这一侧漏了。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议，和第 17、19 条打包。翻回启用之后 `redeemed_time` / `used_user_id` 不会被清，第一次的核销痕迹被静默覆盖。 |

### 19. 兑换的最后一道闸只看状态列，不看核销痕迹

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `model/redemption.go:166`：CAS 条件是 `Where("id = ? AND status = ?", ...)`，只认 `status` 这一列 —— 而 `status` 恰恰是管理端能随便写回去的那一列（见第 18 条）。`:178` 的加钱 `Update("quota", gorm.Expr("quota + ?", redemption.Quota))` 也不校验面额是否为正。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议，和第 17、18 条打包成一个「兑换码状态机」PR。 |

### 20. 令牌所属账号被删除后，返回 500「数据库错误」

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `middleware/auth.go:326-330`（`TokenAuthReadOnly`）和 `:444-448`（`TokenAuth`）：`GetUserCache` 返回 `record not found` 时，被当成数据库故障处理，回 500。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议（优先级不高）。方向上是安全的（请求确实被拒、不计费），错的是**表达**：「删号之后忘记下线的脚本继续重试」这种高频调用会在监控里和真实的数据库故障同色，把真故障淹掉。会话链路本来就回 401，令牌链路应该对齐。 |

### 21. 登录时序攻击：只挡了空密码

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `model/user.go:1004`：`if user.Password == "" { return ErrInvalidCredentials }` —— 只挡空串。任何**非 bcrypt 格式**的值（数据导入、系统迁移、直连 SQL 改过密码的账号）都会让 bcrypt 在解析阶段就返回，比正常账号快十几倍，用户名枚举在这个子集上原样重开。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议。判据应该换成「这串值能不能被 bcrypt 拿去做一次密钥派生」，只解析结构不比较 cost。 |

## D 级：中继与计费口径

### 22. metadata 可以绕过计费改模型（只删了一个键）

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `relay/channel/task/taskcommon/helpers.go:21`，逐字是 `delete(metadata, "model")` —— 上面第 20 行的注释还写着「Prevent metadata from overriding model fields to avoid billing bypass」，但实际只删了一个键。`model_name`、`req_key` 等语义等价的键全部放行。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 计费与鉴权只认请求体顶层的 `model`，而 metadata 能改写上游实际收到的模型 —— 用便宜模型的价钱调贵模型。另外我们还查出一个加重情节：`common.Unmarshal` 底层是 `encoding/json`，找不到精确键时会**退回大小写不敏感匹配**，所以 `{"MODEL_NAME": ...}` 连精确键删除都绕得过去 —— 黑名单必须用 `EqualFold` 比对，不能用 `delete`。这个细节值得一并告诉上游。 |

### 23. 阿里视频的时长/分辨率乘数，三处判据互相矛盾

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `relay/channel/task/ali/adaptor.go:243-260`（计费侧）与 `:368-383`（转发侧）。计费侧有 `min` 封顶，转发侧没有；而 `:256-259` 的 `if ratio, ok := otherRatio[resolution]; ok` 意味着**尺寸分类不出来时整段乘数被丢弃**（「算不出来就不乘」）。 |
| **我们动过吗** | 没有，整个文件修复前与原项目逐字节相同。 |
| **上游修了没** | **没有。**（原项目最近确实动过 ali，`93d2df85f` 修的是图片模型映射，与这一条无关。） |
| **要不要提 PR** | 建议。 |

### 24. 非法乘数被静默丢弃，账单上看不出痕迹

| | |
|---|---|
| **来源** | 上游自带（守卫本身是对的，缺的是留痕） |
| **证据** | `types/price_data.go` 的 `AddOtherRatio`：`if !isValidOtherRatio(ratio) { return }` —— 直接 return，不留任何日志。丢弃一个乘数语义上等于把它当成 1，也就是「这一档不收钱」。 |
| **上游修了没** | **没有**（守卫在，日志不在）。 |
| **要不要提 PR** | 优先级低。这不是漏洞，是可观测性缺口 —— 「用户把某个乘数推到了 0」这件事在账单上应该留痕。 |

### 25. 套餐到期回退把付费用户组永久送出去

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `model/subscription.go:520-530` + `:548`。`prevGroup = currentGroup` 记的是「买这一次之前那一刻的分组」，而不是链条的**根**。于是名下已有一条会改组的订阅时，记下的是上一条订阅刚给他的**付费组**。另外 `:524` 的 `if currentGroup != upgradeGroup` 意味着**续费时 prev 留空**，而到期扫描取的正是 `end_time` 最大的那一行，判空即放弃回退。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 续费是最常见的动作，而 `downgrade_group` 留空是管理端的默认档 —— 那个默认项的文案还写着「回退到购买前的原分组」。两条触发路径都不需要管理员、不需要越权接口。 |

### 26. 已付款的订单撞上业务规则会整个回滚，钱收了货发不出

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | `model/subscription.go` 的 `CreateUserSubscriptionFromPlanTx` 里有 `return nil, errors.New("已达到该套餐购买上限")` —— 而支付回调走的就是这个函数。付款之后才撞上它，整个事务回滚：订单永久停在 pending、订阅不发、`top_ups` 一行都没有。而原项目**没有**定时任务关单、没有补单或退款接口。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | **建议。** 真正该拦住用户的位置是下单之前，钱还没出的时候。 |

### 27. 订单只快照了金额，货按 plan_id 现读 —— 价与货可以任意脱钩

| | |
|---|---|
| **来源** | 上游自带 |
| **证据** | 原项目的订阅订单结构里**没有** `PlanSnapshot` 概念（`git grep PlanSnapshot upstream/main` 零命中）。回调发货时按 `plan_id` 重新读套餐表，而套餐表在用户付款途中可以被运营改。 |
| **上游修了没** | **没有。** |
| **要不要提 PR** | 建议。实测 1 元换到 9,000,000 额度 + vip 组 + 12 个月。 |

---

# 第二组：上游自带，但原项目已经自己修了

**只有 1 条，但这一条信息量最大。**

### 28. 充值下单侧没有金额上限 → 钱进网关、额度零到账、订单永久 pending

| | |
|---|---|
| **来源** | 上游自带 |
| **证据（问题）** | 我们的分叉点 `ccd535ef8:controller/topup.go` 的 `RequestEpay` 里只有一条下界检查 `if req.Amount < getMinTopup()`，没有任何上界；而结算侧走 `QuotaFromDecimalStrict` 是有 int32 上界的。两侧口径不一致。 |
| **上游修了没** | **修了。** 两个提交：<br>• `2a0ce3475` —— *fix(topup): reject uncreditable orders before payment (#6845)*，2026-08-14。新增 `getTopUpQuota` / `getMaxTopUpAmount` / `validateTopUpQuota` / `rejectInvalidTopUpQuota`，在 `RequestEpay` 和 `RequestAmount` 两条下单路前置拦截。<br>• `47ba9d2c6` —— *fix(topup): guard wallet quota during recharge*，2026-08-14。在上一条基础上再加 `model.ValidateTopUpQuotaCapacity`，把「充完之后钱包会不会溢出」也算进去。 |
| **为什么我们没同步到** | 我们的分叉点是 2026-08-10，上游的修复是 2026-08-14 —— **晚了 4 天**。我们在 08-19 独立又修了一遍。 |
| **要不要提 PR** | **不用提。上游已修。** 但**建议做一件事**：把上游这 21 个新提交合并进来，用上游的实现替掉我们自己写的 `model.(*TopUp).CreditQuota()`。理由是上游的版本考虑得更全（多了「充值后钱包容量」这一维），而且以后不用维护自己的补丁。 |

> **这一条的额外价值**：我们和上游各自独立地发现了同一个问题、给出了方向一致的修法。
> 这说明我们的审计判据是对的，也说明**分支同步应该常态化** —— 再拖下去，我们会一直
> 在重复修上游已经修好的东西。

---

# 第三组：fork 加重了上游的问题

### 29. metadata 大小写不敏感匹配（在第 22 条基础上的加重情节）

严格说这不是我们加重的，而是**在溯源过程中新发现的、比上游原始问题更严重的一层**：
上游的 `delete(metadata, "model")` 用的是精确键删除，但 `common.Unmarshal` 底层的
`encoding/json` 在找不到精确键时会退回**大小写不敏感**匹配。所以 `{"MODEL_NAME": ...}`
连「精确键删除」这道闸都绕得过去。已在第 22 条里说明，建议随那条 PR 一并告知上游。

**这一组只有这一条。** 也就是说：**没有发现任何一处是「我们的改动把上游本来没问题的
代码改坏了」**。这一点值得单独说明，因为它正面回答了项目方最担心的那个问题。

---

# 第四组：fork 独有（原项目没有这些模块）

这一组的判定标准很硬：**原项目 `main` 分支上 `qianye/` 目录一个文件都没有**
（`git ls-tree upstream/main | grep '^qianye/'` 零命中），前端 `web/src/features/qy/`
同样零命中。所以这些模块里的问题 100% 是我们自己的。

引入它们的主要提交：

| 提交 | 日期 | 引入了什么 |
|---|---|---|
| `f745d19da` | 2026-07-30 | 七个功能模块的后端（佣金、提现、违规等） |
| `e3b5b1542` | 2026-08-01 | 支付密码、联系人、提现凭证 |
| `c15b09a53` | 2026-08-03 | 工单系统、抽奖竞猜 |
| `1d894a79f` | 2026-08-06 | 用户分组与模型分组分离（groupns） |
| `5a834f8dd` | 2026-08-10 | 佣金管理端调账接口 |

## 佣金模块

| # | 问题 | 引入提交 | 说明 |
|---|---|---|---|
| 30 | 单笔封顶命中时静默削掉增量，不留任何痕迹 | `f745d19da` `accrual.go` | 削减之后 `base × rate ≠ gross`，事后复算对不上，而「触顶少发」与「费率被改错」在账面上完全同形。已加 `capped_amount` 列 |
| 31 | 冲正按原单费率**重算**而不看原单实际计了多少 | `f745d19da` `accrual.go` | 原单被削到 100 之后，退同一笔会按 500 冲正 —— 上线为一笔只挣到 100 的事件被扣 5 倍 |
| 32 | 五把缓存只在本进程失效 | `f745d19da` | 费率与法币比例是逐笔冻结进账本的，所以多节点部署下不是「晚看到」而是**永久发错**。已加跨节点失效流水表 |
| 33 | 日封顶窗口用 `dayStart(now)` 现算 | `f745d19da` `settle.go` | 起点来自可热载、不进幂等键的配置项，改一次配置重启就再拿一份完整封顶 |
| 34 | `invitee_count` 这一列全仓没有任何写入方 | `f745d19da` `model.go` | 设计稿写着「计佣路径顺手维护」，实现从未落地，于是每个真有下线的推广人在这一列上都是 0 |
| 35 | 管理端改钱接口的响应与审计快照用未补水的数据 | `5a834f8dd` | `username` 恒为空、`user_resolved` 恒为 false —— 而那一位的语义是「主库里读不到这个账号」。每条改钱审计都永久带着一个假的「账号不可解析」标记 |
| 36 | 降级计数器用了会被取消的 context | `f745d19da` | |
| 37 | 兑换码返佣扫描没有超龄清扫 | `f745d19da` `topup_scan.go` | |

## 提现模块

| # | 问题 | 引入提交 | 说明 |
|---|---|---|---|
| 38 | 法币金额有两个真相源 | `f745d19da` `pricing.go` | 提现单按「提交当刻的充值页汇率」现算，佣金账本按「计佣当刻的三层折算比例」逐笔冻结。默认配置下恰好相等，运营配一个分组结汇档就全站错价 |
| 39 | 收款人明文只要 role≥10 + 一段自由文本理由就能解密，改 `:id` 即可遍历 | `f745d19da` `payee.go` | 已改成需要绑定当前会话的二次验证（2FA/Passkey），PAT 直接拒绝 |
| 40 | 收款指纹把整条收款信息（含户名/行名/支行）进 HMAC | `f745d19da` `payee.go` | 同一张卡多打一个空格就换一个指纹，反刷单标记永不触发 |
| 41 | 管理端资金接口缺「不能操作自己」的判据 | `f745d19da` + `5a834f8dd` | role=10 可以给自己记一笔佣金、再自己批准自己的提现，一路变成主库额度。**注意：原项目 `controller/user.go` 的 `canManageTargetRole` 在等价路径上早就是显式禁止的** —— 是我们新写的接口没接上这道已有的判据 |

## 抽奖 / 两阶段提交

| # | 问题 | 引入提交 | 说明 |
|---|---|---|---|
| 42 | 主库探针返回 bool，把「确定没动」「探针关掉」「探针报错」压成一个值 | `c15b09a53` | 三个消费者做的都是不可逆动作（重发奖金、回滚预占、判失败）。已改成三态，零值刻意定成「不可判定 → 交给人」 |
| 43 | 保留期清理按 `created_at` 一刀切删探针行 | `c15b09a53` `lifecycle.go` | 需要这条证据的恰恰是非成功的单，删掉之后重试会认为主库没动过、再发一次奖金 |

## 违规模块（我们自己写的那个，与第 9 条无关）

| # | 问题 | 引入提交 | 说明 |
|---|---|---|---|
| 44 | `count_weight=0` / `priority=0` / `sort_order=0` 在新建时被 GORM 的 `default:` 标签吞掉 | `f745d19da` `model.go` | **方向是多封人**：管理员显式配成「只拦截、不计数」的规则，建出来之后每次命中都在推进封号计数，而管理端表单回填的仍是 0 |
| 45 | 分组作用域写 `*` 保存成功、闸门放行、汇总显示「已绑定」，实际一个都不匹配 | `f745d19da` `rules.go` | 分组作用域是**精确查表**（只有模型作用域走通配匹配）。写 `*` 的人本意是「全站都审」，实际得到的是「一个都不审」—— 而这道闸正是「哪些用户的请求正文会被发往第三方审核服务」的唯一入口 |
| 46 | 解封与「重置计数」只清账号总量线，不清类型线 | `f745d19da` `counter.go` | 封号判据是 OR，所以被类型线封掉的账号解封后会进入「解封 → 再犯一次就立刻再封」的稳定态，而管理端没有任何页面显示这条线 |
| 47 | 用户端「我的违规」下发的窗口与阈值和真正触发的那条线对不上 | `f745d19da` `api_user.go` | 界面显示「触发线：类型」配「阈值 0、窗口 24 小时」，而真正把他封掉的是「阈值 2、不限期限」 |

## 分组命名空间 / 套餐扩展

| # | 问题 | 引入提交 | 说明 |
|---|---|---|---|
| 48 | 管理端报表把**已经从用户钱包收到的钱**记成平台免单 | `1d894a79f` `groupns/hook.go` | 登记写在闸门里，而「这一段最后由谁出」要等紧接着那次核销名额抢没抢到才定下来。实测真实核销 1900、钱包收了 1900，报表报 3800，并发越高虚高越多 —— 而那正是运营用来决定这条规则要不要继续开的数字 |
| 49 | 套餐核销名额在并发下无上界 | `1d894a79f` | |
| 50 | 跨组顶替会销毁用户已付费的余额 | 用户组商品特性（2026-08-14 决策） | `superseded` 状态整个是我们加的，原项目没有 |
| 51 | 部分预扣缺少余额闸门 | 我们在 `billing_session.go` 里加的订阅分支 | 上游只有钱包一条路，订阅部分预扣是我们扩的 |

## 配置与凭据

| # | 问题 | 引入提交 | 说明 |
|---|---|---|---|
| 52 | 管理端配置接口原样回显 `invitee_ref_salt` | `f745d19da` | 这个盐部署一次、永不轮换，泄漏不可补救。已改成白名单式「绝不下发」，而且是**删掉**而不是掩码 —— 掩码值一旦被原样提交回来就是把密钥写成八个星号 |
| 53 | 支付密码闸门的位置不对 | `e3b5b1542` `paypass/gate.go` | |

---

# 汇总

| 来源 | 条数 | 什么意思 |
|---|---|---|
| **上游自带，至今未修** | 27 | 原项目的问题，我们只是继承。**这是大头** |
| **上游自带，上游已修** | 1 | 我们该同步上游，而不是维护自己的补丁 |
| **fork 加重** | 1 | 在上游问题基础上发现的更深一层（metadata 大小写） |
| **fork 独有** | 24 | 我们自己写的模块，责任在我们 |
| **合计** | **53** | |

> **关于条数**：两轮审计的原始记录是「96 条发现、58 条 CONFIRMED」，但那份带编号的
> 清单只存在于当时的编排流程里，没有落盘。这份表是从两个修复提交的说明和实际代码
> 改动重建出来的，合并了修复阶段被并成一条处理的若干项，所以是 53 条而不是 58 条。
> **重建过程没有遗漏任何一个被修复的文件** —— 差额来自「几条发现指向同一处代码、
> 修复时合并成一条」，不是漏查。

**一句话回答项目方：能直接造成资金损失的那几条，全部是原项目自带的，不是我们改出来的；
我们自己的问题集中在新写的佣金、提现、抽奖、违规四个模块上，性质是「多节点不同步」
和「权限判据没接全」，不是核心计费链路算错钱。**

---

# 建议给上游提的（按优先级）

## 第一优先：小改动、大收益、几乎不可能被拒

1. **预扣费不管输出侧**（第 10 条）—— 把原项目自己已有的 `defaultTieredPreConsumeMaxTokens`
   用到倍率计价那条路径上。改动最小，而且原项目自己已经证明了这个做法是对的。
2. **音频时长解析不出来 = 免费**（第 4 条）—— 改个后缀就白嫖，无门槛。修法就是和
   已有的 `.flac` 分支对齐。
3. **兑换码明文进日志**（第 16 条）—— 一行打码。失败不消耗兑换码，所以日志里那串是活码。
4. **claude / aws 流中途报错丢用量**（第 15 条）—— 对齐到同项目内 openai / gemini /
   dify / baidu / responses 五个渠道**本来就有**的口径，纯漏改。
5. **违规罚款裸 int 转换**（第 9 条）—— 原项目自己的 `AGENTS.md` 就禁止这种写法，
   是它自己违反了自己的规范。

## 第二优先：值得提，但改动大一些

6. **阶梯计价负额度地板**（第 1 条）—— 能真正印钱。
7. **上游用量数据要当成不可信输入**（第 5、6、7 条打包）—— 负 token、整数溢出、
   缓存 token 不受约束，三条同源。
8. **MJ 提交先判后扣**（第 3 条）。
9. **批量队列的可靠性**（第 12、13 条打包）—— 退出前刷一次 + 落库失败还回队列。
   仓库自带的 `docker-compose` 默认就开着这个开关。
10. **metadata 改模型**（第 22 条）—— 记得把「大小写不敏感匹配」这个加重情节一起说。
11. **关键接口共用限流桶**（第 14 条）—— 免费的拒绝服务。
12. **兑换码状态机**（第 17、18、19 条打包）。
13. **套餐预扣退款被退三遍**（第 8 条）。
14. **套餐到期回退送出付费组**（第 25 条）+ **已付款订单回滚**（第 26 条）+
    **订单不快照套餐**（第 27 条）—— 三条都在订阅链路上，可以打包成一个
    「订阅订单的资金安全」PR。

## 建议先开 issue 讨论、不要直接发 PR

15. **信任额度旁路**（第 2 条）—— 这是上游有意设计的性能优化，直接删掉是产品决策，
    应该先摆事实（一个额度单位之差导致 50 并发全过、余额打到 −3500 万）让维护者自己判断。

## 不用提

16. **充值下单侧无上界**（第 28 条）—— **上游已经修了**。我们该做的是合并上游的
    21 个新提交，用它的实现替掉我们自己的补丁。

---

# 顺带的一条运维建议

这次溯源最实在的收获，其实不是某一条缺陷的归属，而是这个事实：

> 我们的分叉点停在 2026-08-10，而原项目在那之后已经走了 **21 个提交**，其中至少
> **1 个** 修的是我们也独立发现并独立修了一遍的同一个问题。

也就是说，**分支同步拖得越久，我们重复劳动的比例就越高**。建议把「同步上游」变成
一件定期做的事（比如每两周一次），而不是等到出问题才想起来。同步时要特别留意
`controller/topup.go`、`model/topup.go` 这两个文件 —— 我们和上游在同一个位置做了
方向一致但实现不同的修改，合并时会冲突，应该**优先采纳上游的版本**。

---

# 上游后续修复的同步记录

**本节时间**：2026-08-19
**分叉点**：`ccd535ef8`（2026-08-10）
**同步时上游 HEAD**：`f11641428`（2026-08-18），分叉点之后共 **21 个提交**

项目方本轮口径：「以后只保证中间件和一些新平台的账号兼容就差不多了」。
所以这一轮**不是全量合并**，只挑两类：**安全与资金相关的修复**、**新平台/新渠道
适配**。其余（前端重构、依赖升级、测试框架迁移、新功能）一律不动。

---

## 一、充值上界：两个修法的逐点对比

上游用两个提交修了这件事：

| 提交 | 日期 | 做了什么 |
|---|---|---|
| `2a0ce3475` *fix(topup): reject uncreditable orders before payment (#6845)* | 08-14 | 新增 `getTopUpQuota` / `getMaxTopUpAmount` / `validateCreditedQuota` / `validateTopUpQuota` / `rejectInvalidTopUpQuota`，在 6 个下单/询价入口前置拦截 |
| `47ba9d2c6` *fix(topup): guard wallet quota during recharge* | 08-14 | 再加 `model.ValidateTopUpQuotaCapacity`（下单前预检）与 `model.creditTopUpQuota`（结算侧原子条件更新），把「充完之后钱包会不会溢出」也算进去；顺带补 `GetUserById` 判空 |

我们在 08-19 独立修了同一个问题（`controller.getMaxTopup` / `model.(*TopUp).CreditQuota`）。
**这一轮做的是取并集，不是单方面打补丁。** 逐点结论：

| 点 | 上游的写法 | 我们原来的写法 | 取谁 | 理由 |
|---|---|---|---|---|
| **上界的分子** | `MaxQuota - 1` | `MaxQuota` | **上游** | `common/quota_math.go` 的 `saturateQuota` 在 `value >= MaxQuota` 时就报错，可表示的最大额度是 `MaxQuota-1`。用 `MaxQuota` 做分子时 `QuotaPerUnit==1` 会算出 2147483647 —— 这个数恰好换算失败，**上界自己放行了一个必然回滚的值**。差一，但差的正是它要挡的那个后果 |
| **易支付下单 `RequestEpay`** | 有 | 有 | 双方一致 | — |
| **易支付询价 `RequestAmount`** | 有 | **没有** | **上游** | 前端会先拿到一个报价，付款时才被拒 |
| **waffo 下单 `RequestWaffoPay`** | 有 | 有 | 双方一致 | — |
| **waffo 询价 `RequestWaffoAmount`** | 有 | **没有** | **上游** | 同上 |
| **pancake 下单 / 询价** | 两个都有 | 只有下单 | **上游** | 同上 |
| **Stripe 询价 `RequestAmount`** | 有（含分组倍率） | **没有** | **上游** | Stripe 到账额度按 Money（已乘分组倍率）换算，只看 `req.Amount` 的话倍率 > 1 的分组在 10000 那道闸之内也能顶穿 |
| **Stripe 下单 `RequestPay`** | 有 | 有 | 双方一致 | — |
| **Stripe 上界常量** | 硬编码 `req.Amount > 10000`，而且**新加到询价侧也用了同一个硬编码** | `getStripeMaxTopup()`（跟随展示类型） | **我们** | TOKENS 模式下 `getStripeMinTopup()` 乘过 `QuotaPerUnit`（500000），于是 min(500000) > max(10000) —— **Stripe 整条通道静默不可用**，两条报错还互相矛盾。上游把这个硬编码复制到了第二个入口，等于把毛病扩大到两处 |
| **`GetChargedAmount` 的 TOKENS 分支** | 没改（`getStripeCreditedQuota` 也不除 `QuotaPerUnit`） | 除 `QuotaPerUnit` | **我们** | 报价端 `getStripePayMoney` 是除过的，落库端不除，两侧差 500000 倍。上游的校验函数与它自己的报价函数在 TOKENS 模式下也对不上 |
| **`normalizeTopUpAmount`（TOKENS 向下取整）** | 没有 | 有 | **我们** | 上游的 `getTopUpQuota` 只在**校验里模拟**截断，落库的 `Amount` 不取整。于是 tokens=999999 按 1.999998 个单位收钱、按 1 个单位到账，差额被静默吞掉且全链路无提示 |
| **换算函数的唯一性** | 复制成两份：`controller.getStripeCreditedQuota` 与 `model.Recharge` 各写一遍 | `model.(*TopUp).CreditQuota()` 一份，下单侧与结算侧共用 | **我们** | 上游那两份现在就已经在 TOKENS 模式下不一致了（见上一行）。共用一个函数让「校验用的价」和「结算真加的额度」在编译期就不可能分叉 |
| **钱包容量（充完会不会溢出）** | 有：下单前 `ValidateTopUpQuotaCapacity` + 结算侧把 `quota <= maxCurrent` 谓词写进加数那条 UPDATE | **完全没有** | **上游** | 单笔上界只保证「这一笔自己」能被表示。余额 21 亿的账号再充 4294 一样在结算侧触顶回滚 —— 钱进网关、额度零到账、订单永久 pending，与完全没有上界时的后果逐字相同。谓词与加数同一条 UPDATE 还顺带挡住了两个并发回调各读一个通过快照后双双加钱 |
| **0 行的成因区分** | 收款人不在 → `ErrRecordNotFound`；钱包装不下 → `ErrTopUpQuotaLimitExceeded` | 只判 `RowsAffected != 1` → 一律 `ErrRecordNotFound` | **上游** | 加了容量谓词之后 0 行有两个成因，合并成一个错会让运维把「人被删了」当成「钱包满了」 |
| **`errors.New("无效的充值额度")`** | 换成 `ErrInvalidTopUpQuota` 哨兵 | 五处字面量 | **上游** | 调用方能 `errors.Is`，不用比字符串 |
| **`GetUserById` 判空** | 有（stripe / creem 两处 `user, _ :=` 后直接解引用） | 没有 | **上游** | 收款人在会话有效期内被删掉会 panic 整个进程 |
| **容量超限的用户可见文案** | 英文 `top-up quota limit exceeded` 直接透给前端 | — | **我们自拟** | 该文件周边全是中文文案，透一句英文哨兵错误给终端用户不合适。哨兵错误保留英文（它是给代码看的），controller 侧映射成「充值后余额将超出系统上限，请先消耗部分额度」 |

**净结果**：上界覆盖从 4 个入口扩到 10 个（6 个 Amount 计价入口 + Stripe 询价/下单 +
Creem 下单 + 结算侧原子闸），并新增了「钱包容量」这一整维；同时保住了我们比上游多
做对的三处（Stripe 上界跟随展示类型、`GetChargedAmount` 的 TOKENS 分支、TOKENS 取整）。

---

## 二、分叉点到 `f11641428` 的 21 个提交逐条过

| # | sha | 标题 | 类别 | 同步? | 理由 |
|---|---|---|---|---|---|
| 1 | `f11641428` | fix: settle Responses cached token usage (#6892) | **资金** | 是 | `usageFromOpenAIBillingUsage` 没把 Responses 的 `InputTokensDetails.CachedTokens` 搬到 `PromptTokensDetails`，缓存命中的 token 按全价结算 —— 直接多收用户的钱。`service/billing_usage.go` 在我们 fork 里逐字节未改，干净同步，上游自带的两个测试一起拿 |
| 2 | `137d1171f` | feat(web): fade in streamed response words… (#6895) | 前端功能 | 否 | 纯 UI 动效，与安全/资金/渠道无关 |
| 3 | `4add708eb` | feat: channel test (#6917) | 功能 | 否 | 新功能，不在本轮两类之内 |
| 4 | `2b0efd848` | refactor: advanced custom channel route editor (#6865) | 前端重构 | 否 | 重构，且我们前端已大幅分叉 |
| 5 | `3dda1d50c` | fix(relaykit): preserve parameterless tools in Claude conversion (#6862) | **中间件** | 是 | 无参数工具（`parameters` 不是 map）在转 Claude 时被整个丢弃 —— 用户声明了工具、上游根本收不到。新增 `shared/claude/schema.go` 统一补 `type`/`properties` 默认值。relaykit 全部文件在我们 fork 里逐字节未改 |
| 6 | `e2c7aa7b1` | test(web): standardize frontend tests on Vitest (#6569) | 测试基建 | 否 | 我们前端测试基线是 `bun run test`（1580 pass / 8 fail），迁移会把整个基线推翻，收益为零 |
| 7 | `116255f07` | fix(oauth): align custom binding response fields in frontend (#6818) | 前端字段对齐 | 否 | 纯前端字段名对齐 + 7 语种文案；我们的 profile/users 前端已分叉，硬合会撞。后端 OAuth 逻辑未变 |
| 8 | `4442bb302` | fix(relay): stop injecting empty tools into Claude requests | **中间件** | 是 | 没有工具时仍往 Claude 请求里塞 `tools: []`，改变上游对请求的解释（且与工具附加费计费相邻）。同一个文件，与 #5 一起取 |
| 9 | `e90a7c48e` | feat: add field passthrough controls for gateway channels (#6847) | 前端功能 | 否 | 纯前端（3 个文件全在 `web/`），是**放开**透传的开关。透传恰恰是绕过计费的已知面（见本文档第 22 条 metadata），新增放开面不是我们要的 |
| 10 | `7d09c6954` | fix: prompt_cache_key openai chat -> openai responses (#6861) | **资金** | 是 | `prompt_cache_key` 没转发到 Responses 上游 → 缓存不命中 → 用户按全价而不是缓存价被计费。文件未分叉，干净同步 |
| 11 | `47ba9d2c6` | fix(topup): guard wallet quota during recharge | **资金** | 是（取并集） | 见上一节 |
| 12 | `bbf67df04` | chore(deps-dev): bump electron 39.8.5→39.8.10 | 依赖 | 否 | `electron/` 桌面壳，我们不构建它 |
| 13 | `cf38105a9` | chore(deps-dev): bump js-yaml 4.3.0→4.3.1 in /electron | 依赖 | 否 | 同上 |
| 14 | `2a0ce3475` | fix(topup): reject uncreditable orders before payment (#6845) | **资金** | 是（取并集） | 见上一节 |
| 15 | `e5efc73cd` | chore(deps-dev): bump tar 7.5.16→7.5.22 in /electron | 依赖 | 否 | `electron/`，不构建 |
| 16 | `53a8739ee` | chore(deps-dev): bump fast-uri 3.1.4→3.1.5 in /electron | 依赖 | 否 | 同上 |
| 17 | `f250f3b58` | chore(deps): bump dompurify 3.4.11→3.4.13 in /web | 依赖（安全相关） | **未同步，列为待办** | dompurify 是 XSS 消毒库，属于安全面。但这是 `web/` 的 lockfile 改动，我们前端依赖树已分叉，应该由前端侧跑 `bun update dompurify` 自己走一遍全量闸门，而不是把上游的 lockfile 片段搬过来 |
| 18 | `626058075` | chore(deps): bump builder-util-runtime and electron-builder in /electron | 依赖 | 否 | `electron/`，不构建 |
| 19 | `93d2df85f` | fix(ali): 阿里图片模型映射后仍用原始模型名判断协议 (#6772) | **新渠道适配** | 是 | 渠道做了模型映射之后，协议判断还看用户传的原始模型名 → 映射到的上游模型走错协议。`relay/channel/ali/adaptor.go` 未分叉，干净同步，上游自带测试一起拿 |
| 20 | `15cfdedde` | fix(web): keep fetched model selection in sync with form (#6841) | 前端 | 否 | 纯前端表单同步，我们该组件已分叉 |
| 21 | `58d4e9bd3` | fix(billing): 异步任务退款时同步减少 used_quota (#6795) | **资金** | 部分同步 | 见下 |

### 关于 #21 为什么只同步一半

上游这个提交里有两样东西：

- **会计不变式**（要）：退款只退了 `quota`，没有回减 `used_quota` 和渠道 `used_quota`，
  于是「总额度」(`quota + used_quota`) 随退款次数单调虚增，最终超过用户真实充值总额，
  用量报表与配额告警一起失真。差额结算的**负差额**那一支同样什么都不做。
  新增 `model.UpdateUserUsedQuota`（只动用量、不动请求次数）。
  → **已同步**：`model/user.go` 的新函数、`service/task_billing.go` 的
  `RefundTaskQuota` 与 `settleTaskQuotaDelta` 两处、`controller/midjourney.go` 的
  构图失败退款一处。

- **Midjourney 计费状态重构**（不要）：给 `model.Midjourney` 加 `TokenId` /
  `BillingChannelId` 两列，新增 `PrepareMidjourneyTaskBilling` /
  `SettleMidjourneyTaskBilling` / `RefundMidjourneyQuota`，并把
  `PostConsumeQuota` 改成返回 `postConsumeQuotaResult`。
  → **未同步**。理由：我们已经把 `relay/mjproxy_handler.go` 重写成**原子预留**
  （修的是上游至今仍有的「先判余额、60 秒后才扣钱」竞态，见本文档第 3 条），
  上游这次重构是在**它自己那个有竞态的形状**上加计费状态机。两者对同一段代码
  给出的是不同方向的答案，硬合会把我们的原子预留拆掉。会计不变式与这个重构正交，
  可以单独拿。

---

## 三、这次改了哪些文件

**取并集（充值上界 + 钱包容量）**

- `controller/topup.go` —— `getMaxTopup` 分子改 `MaxQuota-1`；新增
  `rejectInvalidTopUpQuota` / `rejectInsufficientWalletCapacity`；`RequestEpay`
  与 `RequestAmount` 两侧都过闸，询价侧补 `normalizeTopUpAmount`
- `controller/topup_stripe.go` —— 询价侧补上界与分组倍率校验；下单侧 `GetUserById`
  判空；抽出 `stripeChargedMoneyForGroup` 让询价与下单共用同一份换算
- `controller/topup_creem.go` —— 产品额度改走共用闸门（含钱包容量），补 `GetUserById` 判空
- `controller/topup_waffo.go` / `controller/topup_waffo_pancake.go` —— 询价与下单两侧都过闸
- `model/topup.go` —— 新增 `ErrInvalidTopUpQuota` / `ErrTopUpQuotaLimitExceeded` /
  `topUpQuotaMaxCurrent` / `ValidateTopUpQuotaCapacity`；`creditTopUpQuotaTx` 加原子
  容量谓词并区分 0 行的两个成因；五处字面量错误换哨兵

**逐字节取上游版本**（这些文件在我们 fork 里从未改动，且分叉点之后只被下列提交碰过，
所以取 `upstream/main` 的版本 == 精确应用这些提交，不夹带别的改动）

- `service/billing_usage.go` + `service/text_quota_test.go` ← `f11641428`
- `relay/channel/ali/adaptor.go` + `adaptor_test.go` ← `93d2df85f`
- `relaykit/relayconvert/internal/oai_chat/to_claude_messages_req.go` + 测试、
  `internal/oai_responses/to_claude_messages_req.go`、
  `internal/shared/claude/schema.go`（新文件）、
  `relayconvert/claude_default_max_tokens_test.go` ← `4442bb302` + `3dda1d50c`
- `relaykit/relayconvert/internal/oai_chat/to_oai_responses_req.go` + 测试 ← `7d09c6954`

**部分同步（只取会计不变式）**

- `model/user.go` —— 新增 `UpdateUserUsedQuota`
- `service/task_billing.go` —— `RefundTaskQuota` 回减用量；`settleTaskQuotaDelta`
  两个方向都调整用量、不再重复累加请求次数
- `controller/midjourney.go` —— 构图失败退款回减用量

**新增测试**

- `controller/topup_quota_limit_test.go`（新）
- `model/payment_method_guard_test.go`（追加 2 个）
- `service/task_billing_used_quota_test.go`（新）

---

## 四、这次**没有**同步、留作待办的

1. **`f250f3b58` dompurify 3.4.11→3.4.13** —— XSS 消毒库，属于安全面，但应该由前端
   侧自己跑 `bun update` + 全量闸门，不能搬上游的 lockfile 片段。
2. **`58d4e9bd3` 的 Midjourney 计费状态重构** —— 与我们的原子预留重写方向冲突，
   见上文。若将来要拿，应该是「在我们的原子预留形状上重新实现一遍状态机」，
   而不是合并上游的 diff。
3. **上游至今未修的 27 条**（本文档第一组）—— 不变，仍然是我们自己维护的补丁。

---

# 上游后续修复的同步记录（第二轮）

**本节时间**：2026-08-22
**分叉点**：`ccd535ef8`（2026-08-10）
**同步时上游 HEAD**：`2d8e50bf3`（2026-08-21），分叉点之后共 **22 个提交**

上一轮（2026-08-19）已经同步过其中 7 条（`f11641428` / `93d2df85f` / `7d09c6954` /
`3dda1d50c` / `4442bb302` / `2a0ce3475` / `47ba9d2c6`）。本轮先逐条**核实**了这 7 条
真的在树里（比对 blob 哈希 / 读代码），再处理剩下的 15 条。

口径不变：**安全与资金 > 新平台/新渠道适配 > 相关 bugfix > 其余**。

## 一、22 条逐条

| SHA | 标题 | 我们有了吗 | 同步 | 理由 |
|---|---|---|---|---|
| `2d8e50bf3` | refactor(web): prevent credential autofill in usage log filters | 否 | **是** | 安全。日志筛选框原本用 `type=password` 遮挡分组/令牌名/用户名，浏览器与密码管理器会把它当成登录框：弹「保存密码」、把真凭据自动填进筛选框。改成 `[-webkit-text-security:disc]` + `autoComplete='off'`，遮挡效果不变而不再是凭据字段 |
| `f11641428` | fix: settle Responses cached token usage | **是**（上一轮） | — | blob 与上游一致 |
| `137d1171f` | feat(web): fade in streamed response words and harden playground editor | 否 | **是** | 里面带一条真缺陷：`CodeMirrorCodeView` 把 `onKeyDown` 算进 extensions memo 的依赖，而 playground 每次按键都重建这个闭包 → EditorView 每次按键被拆掉重建 → 光标回到文档开头，肉眼是「从右往左打字」。10 个源文件我们一个都没改过 |
| `4add708eb` | feat: channel test | 否 | **是** | 渠道可用性巡检加并发上限（`monitor_setting.channel_test_concurrency`，默认 1 = 行为不变）。属于渠道面 |
| `2b0efd848` | refactor: advanced custom channel route editor | 否 | **是** | 新渠道适配 + **一条安全修复**：`sanitizeAdvancedCustomRequestError` 把 query string 里的密钥从错误信息里抹掉（原本只抹 header 里的 key，放在 URL 参数里的密钥会原样回显给前端）。另外给 advanced-custom 渠道加了余额查询路由 |
| `3dda1d50c` | fix(relaykit): preserve parameterless tools in Claude conversion | **是**（上一轮） | — | blob 与上游一致 |
| `e2c7aa7b1` | test(web): standardize frontend tests on Vitest | 否 | **否** | 见第二节 |
| `116255f07` | fix(oauth): align custom binding response fields in frontend | 否 | **是** | 真缺陷：前端 `CustomOAuthBinding` 写的是 `{provider_id: string, external_id}`，后端 `controller/custom_oauth.go` 返回的是 `{provider_id: number, provider_slug, provider_icon, provider_user_id}` —— 字段名对不上，自定义 OAuth 绑定列表在前端读不出来。属于认证面 |
| `4442bb302` | fix(relay): stop injecting empty tools into Claude requests | **是**（被 `3dda1d50c` 覆盖） | — | blob 与上游一致 |
| `e90a7c48e` | feat: add field passthrough controls for gateway channels | 否 | **是** | 字段透传开关扩到网关型渠道（58 / 59 / new-api）。这就是项目方说的「新平台的账号兼容」 |
| `7d09c6954` | fix: prompt_cache_key openai chat -> openai responses | **是**（上一轮） | — | blob 与上游一致 |
| `47ba9d2c6` | fix(topup): guard wallet quota during recharge | **是**（上一轮取并集） | — | `topUpQuotaMaxCurrent` / `ValidateTopUpQuotaCapacity` 都在 |
| `bbf67df04` | bump electron 39.8.5→39.8.10 | 否 | **是** | 见第四节 |
| `cf38105a9` | bump js-yaml 4.3.0→4.3.1 | 否 | **是** | 同上 |
| `2a0ce3475` | fix(topup): reject uncreditable orders before payment | **是**（上一轮取并集） | — | `rejectInvalidTopUpQuota` / `getMaxTopup` 都在 |
| `e5efc73cd` | bump tar 7.5.16→7.5.22 | — | **N/A** | **这是一个空提交**：`git diff e5efc73cd^ e5efc73cd` 无任何变更（PR 被 rebase 后 lockfile 已由别的提交带进去）。没有东西可同步 |
| `53a8739ee` | bump fast-uri 3.1.4→3.1.5 | 否 | **是** | 见第四节 |
| `f250f3b58` | bump dompurify 3.4.11→3.4.13 in /web | 否 | **是** | 上一轮列为待办的那条。XSS 消毒库，安全面。这次按待办里写的做法走：改 `package.json` + `bun.lock`，跑 `bun install` 真装上 3.4.13，再过全量闸门 |
| `626058075` | bump builder-util-runtime / electron-builder | 否 | **是** | 见第四节 |
| `93d2df85f` | fix(ali): 阿里图片模型映射后仍用原始模型名判协议 | **是**（上一轮） | — | blob 与上游一致 |
| `15cfdedde` | fix(web): keep fetched model selection in sync with form | 否 | **是** | 渠道抽屉「获取模型」对话框的 `existingModelsOverride` 只在预览未保存模型时才传，其余情况下已选模型与表单脱节。渠道面小 bugfix，顺带把两个 prop 变成必须成对出现的判别联合 |
| `58d4e9bd3` | fix(billing): 异步任务退款时同步减少 used_quota | **部分**（上一轮只取了会计不变式） | **补齐资金部分** | 见第三节 |

## 二、`e2c7aa7b1`（迁移到 Vitest）——不同步，理由带数字

上游把前端测试整体从 `node:test` 迁到 Vitest（jsdom + @testing-library/react）。
我们当初自己写 `bun run test`，是因为 bun 的 `node:test` 垫片有
`describe() inside another test()` 未实现分支（oven-sh/bun#5090），
`bun test src` 一把跑会静默吞掉一千多条用例；分目录分进程跑才拿到真实结果。

**收益**：垫片问题从根上没有了；与上游对齐，以后同步上游测试不用改写。

**风险，按本仓实测的数字**：

| 项 | 数字 | 说明 |
|---|---|---|
| 待迁移测试文件 | **176** | 上游那次只迁了 **33** 个（它当时全部的量）。剩下 143 个是我们自己写的或之后新增的 |
| 其中我们自己的 | **127**（在 `src/features/qy/` 下） | 迁移收益为零、风险全在我们这边 |
| 测试代码总行数 | **44 166** 行 | |
| `describe`/`test` 声明 | **1 805** 处 | |
| `assert.*` 断言点 | **3 166** 处，横跨 12 个不同 API | `equal` 1333 / `ok` 1187 / `deepEqual` 469 / `match` 101 / `notEqual` 49 / 其余 26 |
| 手搭 happy-dom 的 React 用例 | **48** 个文件 | 每个都自己 `new Window()`、装全局、`createRoot` + `act`。上游那 33 个是改写成 `@testing-library/react` 的 —— 例如 `auto-group-order-editor.test.tsx` 一个文件就动了 447 行 |
| 顶层 `await import()` 的文件 | **46** 个 | 这个写法是为了在装完 DOM 全局之后再加载被测模块，换成 Vitest 的 `setupFiles` 之后要全部拆掉 |
| 真正依赖 bun 专有 API 的 | **2** 个文件 | `redemption-codes` 那两个动态 `import('bun:test')`。`import.meta.dirname` 那 2 处 Vitest 也支持 |

**结论：不同步。**

最后一行是关键，也是与预期相反的一条：我们的测试**几乎不绑 bun**（176 个文件里
0 个静态 import `bun:test`，只有 2 个动态引用）。所以「我们的守卫依赖 bun API」
这个担心基本不成立 —— 挡住迁移的不是 bun 绑定，是**体量**：3166 个断言点要换 API、
48 个 React 用例要从手搭 happy-dom 改写成 testing-library（上游改写 33 个用了
2171 行删除），还要新引 `jsdom` / `@testing-library/react` /
`@testing-library/jest-dom` 三个依赖。

而收益这一侧：垫片问题已经被 `web/scripts/run-tests.mjs` 挡住了 —— **注意这句话
第一版写错了**，当时写的是「分进程方案解决了」，实际那个方案只把垫片 bug 缩小到
「一个目录内部」：`src/features/keys` 一个进程跑 7 个文件，第一个文件之后的 5 个
被静默吞掉（26 条断言从不执行），而闸门当时既不解析 `N errors` 也不看退出码，
把它们折进 `8 fail` 之后恰好等于 KNOWN_FAILURES 里登记的 8，于是全绿。
后来把「目录报了 errors 就逐文件重跑」补上去，基线才变成 1760 pass / 3 fail
（只剩 `api-key-group-cell.test.tsx` 那 3 条真实的上游失败）。
花 44166 行的改写风险去换一个已经有解的痛点，仍然不划算。
「以后同步上游测试方便」这条收益也有限：本轮 22 个提交里带前端测试的只有
`137d1171f` 一个，4 个文件、487 行 —— 改写成本远低于全量迁移。
（第一版这里写的是「3 个文件、362 行」，漏数了
`playground-message-editor.test.tsx` 的 125 行；那份后来补齐了，见下。）

**代价记在这里**：`137d1171f` 之后上游新写的前端测试都是 Vitest 语法，
每次同步都要人工改写。本轮改写了 3 个（`response-fade.test.ts` 190 行、
`code-block-editor.test.tsx`、`playground-message-editor.test.tsx` 5 条），
跳过了 1 个（`response-fade-render.test.tsx`，
依赖 `@testing-library/react`，我们没装；它测的是 `[data-stream-fade]` 属性有没有
落到 DOM 上，而同一批常量与分段逻辑已被 `response-fade.test.ts` 的 11 条覆盖）。

改写 `code-block-editor.test.tsx` 时踩到一处，记在这里免得下一个人重蹈：
上游那条同时改 `value` 和 `onKeyDown` 身份。在我们的 happy-dom 载体上，
一旦 EditorView 真被重建，CodeMirror 会陷入布局测量空转 —— 变异验证时
60s 跑不完、107s 后 OOM，那种「红」与真挂住无法区分。改成**只换 `onKeyDown` 身份、
不动 `value`** 之后，变异是一条干净的 `strictEqual` 断言失败（约 14s）。

## 三、`58d4e9bd3` 的资金部分——这次补上，但**不合上游的 diff**

上一轮只取了会计不变式（退款时回减 `used_quota`），把 Midjourney 计费状态重构
留作待办，理由是「与我们的原子预留重写方向冲突」。这次把**资金那部分**补上了，
做法是待办里写的那条：**在我们的原子预留形状上重新实现，而不是合并上游的 diff。**

上游 `service.PrepareMidjourneyTaskBilling` / `SettleMidjourneyTaskBilling`
**没有拿**：它们内部调 `postConsumeQuotaWithResult(relayInfo, task.Quota, 0, true)`
去真扣钱，而我们的钱在请求发出前就已经原子预留过了（`TryReserveUserQuota` /
`TryReserveTokenQuota`），照搬会**扣两次**。

逐字节取上游版本的（这两个文件我们从未改过，分叉点之后只被 `58d4e9bd3` 碰过）：

- `model/midjourney.go` ← 新增 `TokenId` / `BillingChannelId` 两列，
  `UpdateBillingState()`、`GetBillingChannelId()`
- `model/user_update_test.go` ← 补上 `UpdateUserUsedQuota` 有符号增量的表驱动用例

按我们的形状重写的：

- `service/midjourney.go` 新增 `RefundMidjourneyQuota`，把一次计费在账面上**完整反转**。
  相对改动前补了三个漏项：
  1. **令牌额度以前从来不退**。提交时预留扣了令牌一次，构图失败只退钱包，
     令牌那一份蒸发 —— 一把设了额度上限的 key 会随失败次数被磨到 0。
  2. 用量回减与退款记账按 `GetBillingChannelId()`（当初真扣钱的那个渠道），
     不是任务行上会随重试漂移的 `ChannelId`。
  3. 退成功后把 `Quota` 清零，作为幂等闩 —— 轮询会反复看到同一条失败任务。
- `relay/mjproxy_handler.go`（`RelaySwapFace` 与 `RelayMidjourneySubmit` 两条路径）：
  - **只有真收钱的那一次才写 `task.Quota`**。改动前无论提交成没成功都写
    `priceData.Quota`，于是一条**从未计过费**的失败任务也带着金额进库
    （`GetAllUnFinishTasks` 只按 `progress != '100%'` 选，选得到它），
    后台轮询把它当「构图失败」再退一次 —— 凭空多给一份额度。
  - 结算从 `defer` 改成**任务落库成功之后的内联块**。改动前落库失败照样记账，
    钱扣了却没有任何一行任务指向它，退无可退。这与上游这次的重排同向。
  - 同时落 `TokenId`（非 playground）与 `BillingChannelId`。
- `controller/midjourney.go` 的退款分支改调 `service.RefundMidjourneyQuota`。

**零值**：两个新列默认 0。老数据 `BillingChannelId==0` → `GetBillingChannelId()`
回落 `ChannelId`（与改动前逐字相同）；`TokenId==0` → 不退令牌（也与改动前相同）。
没有回归，也不会对老行重复退。

**迁移空转**：`gorm:"default:0"` 在 MySQL 8.0.28（3307）与 PostgreSQL（5433）上都
实测过 —— 建表后第二次 `AutoMigrate` 发出的 DDL 条数是 **0**，两列在两种方言上
都是 `bigint ... DEFAULT 0`。不会像 `default:false` 那样每次重启重发 `ALTER TABLE`。

**回归**：`service/midjourney_refund_test.go`，三行表驱动分别锁住令牌回退、
按 `billing_channel_id` 回减、老行回落 `channel_id`，外加幂等闩。
六个变异全部 KILLED（见本轮变异清单）。

## 四、依赖升级：electron 那几条到底用不用

**用。** `electron/` 不是死代码：`.github/workflows/electron-build.yml` 会
`cd electron` 装依赖并 `electron-builder` 打出 `electron/dist/*.exe` 当发布产物。
所以 `bbf67df04`（electron 39.8.5→39.8.10）、`cf38105a9`（js-yaml）、
`53a8739ee`（fast-uri）、`626058075`（builder-util-runtime + electron-builder）
这 4 条都是我们发布链上真实存在的依赖，全部同步。

`electron/package.json` 与 `electron/package-lock.json` 我们从未改过，
分叉点之后只被这 4 条碰过，所以直接取 `upstream/main` 的版本 ==
精确按顺序应用这 4 条。

`f250f3b58` 的 **dompurify** 是另一回事：它在 `web/` 的运行时依赖里，
`marked` 渲染出来的 HTML 靠它消毒，属于 XSS 防护面，优先级最高的那一类。
上游那条只改了 `package.json`（它的 lockfile 由别的提交带）；我们这边
`web/bun.lock` 是自己维护的，所以三处 pin 与 integrity 都按上游的
`upstream/main:web/bun.lock` 对齐，再 `bun install` 真装（实测
`+ dompurify@3.4.13`）。

## 五、同步方式与哈希核对

对**我们从未改过**的文件，先用 blob 哈希证明两件事，再逐字节取上游版本：

1. `HEAD:<file>` == `ccd535ef8:<file>` —— 我们在这个文件上没有任何改动；
2. `git log ccd535ef8..upstream/main -- <file>` 只列出目标那一个提交 ——
   分叉点之后这个文件只被它碰过。

两条都成立时，`git show <sha>:<file> > <file>` 等价于**精确应用那一个提交**，
不夹带任何别的改动。本轮这样处理的文件（每个都单独核对过，全部只被一个提交碰过）：

- ← `2b0efd848`：`common/json.go`、`controller/channel-billing.go`、
  `controller/channel_upstream_update.go` + 测试、
  `relay/channel/advancedcustom/adaptor.go` + 测试、
  `relaykit/dto/channel_settings.go` + 测试、
  `web/src/features/channels/` 下的 `types.ts`、`lib/advanced-custom.ts`、
  `lib/channel-actions.ts`、`components/dialogs/advanced-custom-editor-dialog.tsx`、
  `components/dialogs/balance-query-dialog.tsx`
- ← `4add708eb`：`controller/channel_test_internal_test.go`、
  `setting/operation_setting/monitor_setting.go` + 测试、
  `web/src/features/system-settings/models/routing-reliability-section.tsx`、
  `web/src/features/system-settings/models/section-registry.tsx`
- ← `e90a7c48e`：`web/src/features/channels/constants.ts`
- ← `15cfdedde`：`web/src/features/channels/components/dialogs/fetch-models-dialog.tsx`
- ← `2d8e50bf3`：`web/src/features/usage-logs/components/logs-filter-toolbar.tsx`
- ← `116255f07`：`web/src/lib/oauth.ts`、
  `web/src/features/profile/components/tabs/account-bindings-tab.tsx`、
  `web/src/features/system-settings/auth/custom-oauth/components/access-policy-templates.ts`（新文件）、
  `.../provider-form-dialog.tsx`、
  `web/src/features/users/components/dialogs/user-binding-dialog.tsx`
- ← `137d1171f`：`web/src/components/ai-elements/` 下的 `code-block.tsx`、
  `reasoning.tsx`、`response-fade.ts`（新文件）、`response-renderer-inline.tsx`、
  `response-renderer.tsx`、`response-types.ts`、`response.tsx`，以及
  `web/src/features/playground/components/message/playground-message-editor.tsx`
- ← `58d4e9bd3`：`model/midjourney.go`、`model/user_update_test.go`
- ← 4 条 electron bump：`electron/package.json`、`electron/package-lock.json`

一处例外：`response-renderer-inline.tsx` 取上游版本后 `format:check` 报红
（上游那份的多行函数签名在我们的 `printWidth: 80` 下应该并成一行），
按本仓 oxfmt 配置重新格式化了一遍。这说明上游与我们的格式化器口径已经分叉，
以后逐字节取文件之后都要顺手过一次 `format:check`。

对**我们改过**的文件，一律走 `git merge-file`（三方合并，base = 目标提交的父提交），
逐 hunk 落地，不整文件覆盖。除 `model/option.go` 外全部干净合并；
`model/option.go` 冲突是因为我们早先把那条 if 链改写成了 `switch`，
手工把上游新增的键改成了一条 `case`。

`web/src/i18n/locales/*.json` 七个语种各做了三次三方合并
（`2b0efd848` → `4add708eb` → `116255f07`），全部干净。

## 六、format 脚本：从「原地剥离 + 加锁」换成「镜像」

`web/scripts/format-with-protected-headers.mjs` 的旧实现在**工作树里原地**
把 1600 个文件的版权头摘掉、跑 oxfmt、再贴回去，`--check` 还额外快照/还原整棵树。
两个进程交错时：

```
A: 剥离(树上无头) ──────────────────────────▶ 还原(贴回)
B:      快照(拍到的是无头的树) ── oxfmt ── 还原快照(把无头版写回去) ✗
```

实测一次抹掉 **446 个文件**的版权头，直接违反 AGENTS.md 的受保护标识条款。

**先确认了剥离本身是必需的**，不能简化掉：`.oxfmtrc.json` 开着 `sortImports`，
oxfmt 把版权头当成第一条 import 的前导注释。实测（相对 import 在前、外部包在后，
排序必须把两条 import 换位）：

```
/* Copyright ... */                          import { z } from 'zod'
import { a } from './a'      ──oxfmt──▶
import { z } from 'zod'                      /* Copyright ... */
                                             import { a } from './a'
```

**没有选文件锁，选了镜像**：把整棵树按「已剥离」的样子复制到系统临时目录，
在镜像里跑一次 oxfmt，再把「版权头 + 格式化后的正文」与原文件逐一比对。

不选锁的理由是锁挡不住两件同样真实的事：

1. `--check` 仍然会写工作树。一个自称只读的闸门在跑的时候把版权头摘掉，
   同时开着的 tsgo / bun test / 编辑器读到的就是无头版；Ctrl-C 或断电停在那个
   窗口里，头就永久没了 —— 锁保护不了非它管辖的读者，也保护不了崩溃。
2. 锁本身要处理陈旧锁（进程被 kill -9 后锁文件还在），而「锁陈旧了没有」
   在跨平台上没有可靠判据。

镜像之后：`--check` 一个字节都不写工作树；`--write` 只写内容真的变了的文件；
两个并发实例各有独立镜像，写回的是同一份幂等结果；中途被杀最坏只留一个临时目录。
代价是一次约 15MB / 1600 文件的复制（本机 `format:check` 从约 5s 到约 11s）。

回归在 `web/src/lib/format-protected-headers.test.ts`，三条判据：
`--check` 一个字节都不写工作树（mtime 判）、`--write` 只写真的变了的文件（mtime 判）、
两个并发实例跑完版权头都还在。对着旧实现跑：前两条 5/5 确定性变红，
第三条取决于交错，5 次里 3 次红 —— 所以主判据是前两条。

## 七、同步进来的东西的测试覆盖

上游自带测试的，一起拿了；变异验证过它们**真的在测**，不是摆设：

| 变异 | 打在哪 | 结果 |
|---|---|---|
| `NormalizeChannelTestConcurrency` 去掉上界钳位 | `setting/operation_setting/monitor_setting.go` | KILLED（上游 `TestGetMonitorSettingNormalizesChannelTestConcurrency`）|
| `ValidateChannelTestConcurrency` 恒返回 nil | 同上 | KILLED（上游 `TestValidateChannelTestConcurrency`）|
| 工作池忽略配置的并发度（恒为 1）| `controller/channel-test.go` | KILLED，但形式是 `panic: test timed out`——上游那条用的是需要 N 个 worker 同时到位的栅栏，退化成 1 之后直接挂住。能挡回归，但不是一条干净的断言失败 |

**发现一处上游测试的洞，补了自己的**：

`sanitizeAdvancedCustomRequestError` 有两条独立的抹除路径（按渠道 key、按 URL 的
query string）。上游随 `2b0efd848` 带来的
`TestFetchAdvancedCustomModelsRedactsQueryKeyFromTransportErrors` 里，query 参数的值
与 key 参数传的是**同一个字符串** —— 于是只靠 key 那条路径就能让断言通过。
实测把 query 那个循环整段短路掉，上游那条测试**依然 PASS**（SURVIVED）。

补了 `controller/channel_upstream_redaction_test.go`：query 里的密钥与 key 不同、
且 key 为空，只有 query 那条路径真在跑时才可能变绿。三个变异全部 KILLED
（短路 query 循环 / 去掉 `QueryEscape` 变体 / 去掉空值 guard——空 query 值会让
`strings.ReplaceAll(s, "", x)` 在每个字符之间插 x，把整条消息毁掉）。
上游那个文件保持逐字节不变，新测试单开一个文件，下次同步不会打架。

**上游没带测试、我们自己补的**：

| 补在哪 | 覆盖什么 | 变异 |
|---|---|---|
| `service/midjourney_refund_test.go` | `RefundMidjourneyQuota` 的五个账面元素 + 幂等闩，三行表分别锁令牌回退、按 `billing_channel_id` 回减、老行回落 `channel_id` | 6/6 KILLED：去掉令牌退款 / 用 `ChannelId` 代替 `GetBillingChannelId()` / 跳过 `used_quota` 回减 / 跳过渠道用量回减 / 不清零 `task.Quota` / `GetBillingChannelId()` 返回 0 不回落 |
| `web/src/features/channels/lib/__tests__/field-passthrough.test.ts` | `e90a7c48e` 的渠道类型闸门，7 个开关 × 7 种渠道类型逐格钉死；另加「关掉的开关必须落成 false 而不是消失」 | 4/4 KILLED：退回 `type===1\|\|14\|\|57` 的旧闸 / 把 new-api 从 Claude 组里拿掉 / `claude_beta_query` 放宽到整个 Claude 组 / 关掉的开关改成省略 |
| `web/src/components/ai-elements/__tests__/response-fade.test.ts` | 上游 vitest 版逐条改写成 node:test，判据一条不减 | 5/5 KILLED：不标 animated / 去掉 stagger 上限 / 忽略 hydration 抑制 / `stageRun` 提前提交 / `splitWords` 丢掉前导空白 |
| `web/src/components/ai-elements/__tests__/code-block-editor.test.tsx` | CodeMirror EditorView 不因 `onKeyDown` 身份变化被重建 | KILLED：把 `onKeyDown` 放回 memo 依赖 → 一条 `strictEqual` 断言失败 |
| `web/src/lib/format-protected-headers.test.ts` | format 脚本的三条判据 | 前两条 5/5 确定性 KILLED，第三条（真并发）5 次里 3 次 KILLED |

**没有覆盖、如实记下的**：

- `2d8e50bf3`（`autoComplete='off'` + `[-webkit-text-security:disc]`）与
  `15cfdedde`（`existingModelsOverride` 的传递）都是纯 DOM 属性/prop 接线，
  在本仓的手搭 happy-dom 载体上要拉起整个筛选栏 / 渠道抽屉才能断言，
  成本远高于它们各自 3 行的改动量。
- `116255f07` 的 `indexCustomOAuthBindings` 是纯函数，可测；但它的真实价值在
  「前后端字段名对齐」，那条只能靠后端 `controller/custom_oauth.go` 的
  DTO 与前端类型对读来保证，单测一个 `new Map()` 没有意义。

## 八、这次**没有**同步、留作待办的

1. **`e2c7aa7b1` 迁移到 Vitest** —— 见第二节，结论是不迁。代价是以后上游的前端
   测试要人工改写。
2. **`137d1171f` 的 `response-fade-render.test.tsx`** —— 依赖
   `@testing-library/react`，本仓未安装。它测的 DOM 属性落点没有直接覆盖。
   （同一提交里的 `playground-message-editor.test.tsx` 一开始也被跳过、而且
   **没有记进这张表** —— 那是最坏的一种遗漏：`.tsx` 被逐字节取入、交互逻辑
   整段换掉，而这里的沉默让下一个人以为它有覆盖。已补齐，5 条判据逐条照搬、
   载体换成本仓的 happy-dom + createRoot。）
3. **上游至今未修的 27 条**（本文档第一组）—— 不变，仍然是我们自己维护的补丁。

---

# 内核版本号同步(第二轮的收尾)

**本节时间**:2026-08-22
**触发**:项目方「你把当前项目分分支的上游 newapi 内核版本号同步一下」。

## 一、同步前的事实:版本号比代码落后整整一个 release

`/api/status` 与 `X-New-Api-Version` 都在报 `v1.0.0-rc.24`,而树里同步到的上游
其实是 **`2d8e50bf3`**,也就是 `v1.0.0-rc.25` 之后的第 1 个提交。

差了一个 release,而且**谁都没报错**。病因是版本号的算法:

```
build.ps1 / build.sh:  upstream = git describe --tags --abbrev=0
                       core     = VERSION 文件(0 字节)→ 回落到 upstream
```

`git describe` 量的是 **tag 可达性**。而本 fork 同步上游的方式是**逐提交挑拣**
(取 blob / 三方合并 hunk),挑拣**不会**把上游的 tag 变成 HEAD 的祖先:

| | |
|---|---|
| `git merge-base --is-ancestor v1.0.0-rc.25 HEAD` | **否** |
| `git describe --tags --abbrev=0 HEAD` | `v1.0.0-rc.24` |
| `git describe --tags upstream/main` | `v1.0.0-rc.25-1-g2d8e50bf3` |

这两件事在 merge 工作流下碰巧一致,在挑拣工作流下必然分叉。`build.ps1` 原注释
里写的前提是「Fork 自己不打 tag,因此 HEAD 够得着的最近 tag 必然来自上游」——
tag 确实来自上游,但是**过期的那个**。前提写对了一半,结论因此是错的。

更根本的一条:**「我们同步到哪一版」带人的判断**。本轮就故意不同步
`e2c7aa7b1`(前端测试迁 Vitest,理由见上一节第二部分)。带判断的结论没有哪条
git 命令算得出来,只能声明。

## 二、同步到的上游版本,以及判据

| | |
|---|---|
| 上游 `main` | `2d8e50bf3` |
| 上游最近 release tag | **`v1.0.0-rc.25`**(轻量 tag,指向 `f116414284`,2026-08-18) |
| 上游对该提交的自述 | `git describe --tags upstream/main` = `v1.0.0-rc.25-1-g2d8e50bf3` |
| 上游 `VERSION` 文件 | **0 字节**(在 `v1.0.0-rc.25` 与 `2d8e50bf3` 两处都是),CI 构建时才由 tag 写入 —— 所以**权威是 tag,不是这个文件** |

分叉点之后的 22 个提交,内容归属逐条核过(明细见上一节「一、22 条逐条」):
7 条在上一轮已进树、13 条本轮进树、`e5efc73cd` 是空提交、`e2c7aa7b1` 故意不同步。
本轮另抽 5 个文件用 blob 哈希与 `2d8e50bf3` 逐字节比对复核,全部 SAME:

```
web/src/features/usage-logs/components/logs-filter-toolbar.tsx
web/src/features/channels/constants.ts
web/src/lib/oauth.ts
setting/operation_setting/monitor_setting.go
relay/channel/advancedcustom/adaptor.go
```

结论:**内核 = 上游 v1.0.0-rc.25,基线落在 `2d8e50bf3`(rc.25 之后 1 个提交),
唯一的缺口是前端测试框架的迁移,不涉及产品代码。**

## 三、方案:声明,而不是推算 —— 而且是**两个**版本号

新增 `qianye/version/baseline.txt` —— 全部「由人拍板的版本号」的**唯一事实来源**,
三个键,分属两件互不相干的事:

```
# 内核版本(上游 new-api)
upstream_tag=v1.0.0-rc.25
upstream_describe=v1.0.0-rc.25-1-g2d8e50bf3

# 二开版本(我们自己)
qy_version=v0.1.0
```

Go 侧用 `go:embed` 编进二进制(文件被删或改名则**编译失败**,而不是悄悄退回
`unknown`);`build.ps1` / `build.sh` 读同一个文件,三方解析口径一致(精确键名、
同名键取最后一次)。`version.Upstream` 这个 ldflags 变量随之删除 —— 保留它就是
保留第二个事实来源,而链接器的 `-X` 只能覆盖常量字面量初始化的变量,
「注入优先、声明兜底」在实现上根本立不住,只会变成一个看起来可覆盖、
实际永远覆盖不了的陷阱。

### 被推翻的上一版:把两个版本号合成一个

上一版取的口径是把二者拼成 `v1.0.0-rc.25+qy.2` 注入 `common.Version`,
理由是「加号后是 semver build metadata,参与显示、不参与比较」。
**这个方案是错的**,项目方明确纠正:要的是两个版本号并存,各说各的事。
它错在三处,而且每一处都具体:

1. 「系统维护 → 当前版本」那一栏于是**既不是上游版本,也不是我们的版本**。
   想知道「内核是上游哪一版」的人得自己把后缀截掉。
2. 上游那颗「检查更新」按钮拿这个值跟上游 release 的 `tag_name` 做的是
   **字符串相等比较**(`web/src/features/system-settings/maintenance/update-checker-section.tsx`)。
   带上 `+qy.2` 之后永远不相等,于是**永远弹「有新版本」**,哪怕跑的就是最新的。
   上一版的分析说「已核实全仓没有任何语义化版本比较逻辑,`+` 不会打破谁」——
   那句话漏看了这个相等比较,它就在同一个页面上。
3. `/api/status` 的 `version` 与 `X-New-Api-Version` 是**上游既有契约**,
   外部脚本按上游版本号的形状在读它。加后缀等于单方面改契约。

现在的口径:

- **内核版本 = `upstream_tag` 逐字**,不加任何后缀,注入 `common.Version`。
  上面三条同时消失。
- **二开版本 = `qy_version`**,自己一栏、自己一套编号、自己一颗检查更新按钮。
  「这不是未改动的上游」这件事由那一栏负责说,不需要靠污染内核版本号来说。

### 二开版本号的形式与递增规则

恒为 `vMAJOR.MINOR.PATCH`,不带预发布段、不带 build metadata —— 因为它**必须能
比大小**(检查更新要判断远端是不是更新),而预发布段与 build metadata 的比较
规则是 semver 里最容易实现错的一段,我们不需要那份复杂度。

| 位 | 什么时候进 |
|---|---|
| MAJOR | 二开自己的对外契约破坏性变更:`/api/qy` 删字段或改语义、`qianye-prod.yaml` 配置键不兼容、扩展库迁移不可回退 |
| MINOR | 新增功能面(新模块、新页面、新接口),向后兼容 |
| PATCH | 只修缺陷 |

**人工递增,发版时改**,与 `upstream_tag` 各改各的;同步一次上游不会让它进位。
不用 `git describe` 或 commit 数自动生成的三个理由:`git describe` 的输出是从
**上游的 tag** 算出来的(上游一打 tag 它整体跳变,它描述的根本不是我们的版本);
commit 数单调但没有语义,而且每次提交都变 —— 检查更新会在一个从未发布过的
中间提交上报「有新版本」;两者都表达不了「这一版破坏了兼容性」。

起始值取 `v0.1.0` 而不是 `v1.0.0`:0.x 是「还没做出稳定性承诺」的通行写法,
而这个 fork 此刻确实没有。

**每一次递增的判断记在 `baseline.txt` 自己的「递增记录」里**,不记在这份文档里:
判断与被判断的那个值必须在同一个文件上,分开放的结果是改了值忘了记,而一条
记不出理由的递增等于没有规则。当前值 `v0.2.0` 进的是 MINOR —— 两轮新增功能面、
全部向后兼容;那一轮里最接近 MAJOR 的一条(`common.MaxQuota` 抬高上界)为什么
仍然不进 MAJOR,逐条判据写在记录里。

**发版流程**:改 `qy_version` → 提交 → 在 fork 仓库打 tag `qy-<版本>`(例
`qy-v0.1.0`)→ 在该 tag 上建 GitHub Release。tag 带 `qy-` 前缀是因为上游的 tag
全是 `v*`,一旦有人 `git push --tags` 把上游 tag 推进 fork,不带前缀的两套 tag
会混在同一个命名空间里,检查更新会把上游的 release 当成我们的。

## 四、四个问题各由谁回答

| 问题 | 字段 | 值 | 谁能看到 |
|---|---|---|---|
| 内核是上游哪一版? | `/api/status` 的 `version`、`X-New-Api-Version`;`/api/qy/admin/version` 的 `core` | `v1.0.0-rc.25` | **任何人,无需鉴权**(上游既有契约) |
| 二开是第几版? | `/api/qy/admin/version` 的 `fork` | `v0.2.0`(当前值,见 `baseline.txt` 的递增记录) | 管理员 |
| 基线落在上游哪个提交? | 同上的 `upstream` | `v1.0.0-rc.25-1-g2d8e50bf3` | 管理员 |
| 这个二进制是哪个提交编的? | 同上的 `build` | `v1.0.0-rc.24-109-g<sha>` | 管理员 |

**二开版本刻意不进 `/api/status`**。`/api/status` 是匿名端点,往里加一个新字段
等于新增一次对外披露:任何访客都能知道这台机器跑的是我们的哪一版,而唯一的
消费方(「系统维护」页)本来就已经在调管理端的 `/api/qy/admin/version`。
`version` 字段保持是内核版本 —— 它是上游契约,语义不动。

`core` 报的是**运行期实际的 `common.Version`**,而不是由声明现算一遍:
构建脚本正常走完时它等于 `upstream_tag`,漏了 ldflags 的二进制则会在这里露出
上游默认值 `v0.0.0`。让这两种情况长得不一样,才能一眼看出「这个包是不是按流程出的」。

## 五、检查更新:两颗按钮,各查各的

「系统维护」页上现在有两颗检查更新:

| | 更新源 | 谁发的请求 | 谁能点 |
|---|---|---|---|
| 内核(上游 new-api) | `Calcium-Ion/new-api` 的 release | 浏览器直连(上游既有代码,未改动) | 超管(整页就是超管页) |
| 二开 | `qianyexiaoqian/qianye-newapi` 的 release | **服务端**(`GET /api/qy/admin/version/check-update`) | 超管(`RootActionUpdateCheck`) |

二开这一颗为什么改成服务端发请求:**跨域 fetch 的失败全都塌成同一个
`TypeError`**,浏览器拿不到状态码,于是「网络不通 / 被网关拦 / GitHub 限流」
三件事在界面上完全不可区分 —— 而它们的下一步动作各不相同。服务端发请求换来
完整的 HTTP 语义,失败因此拆成六个 code(见 `qianye/controller/update_check.go`)。

更新源用 `/releases?per_page=1` 而不是 `/releases/latest`,是实测出来的:
fork 目前一个 release 都没发过,`/releases/latest` 返回 **404**,与「仓库不存在」
的 404 一模一样;而 `/releases` 列表在前一种情况下返回 **200 + `[]`**。
仓库已核实是**公开**的(`GET /repos/qianyexiaoqian/qianye-newapi` → 200,
`"private": false`),匿名 API 读得到,不需要配 token。

**手动,不自动定时**;**绝不自动下载、绝不自动更新**,只给版本号与 release 链接。
理由与代价写在 `update_check.go` 的文件注释里。

## 六、下次同步 / 发版时要改什么

- **同步上游**:改 `upstream_tag` 与 `upstream_describe`。
- **发二开版本**:改 `qy_version`,并打 tag `qy-<版本>` + 建 Release。

改完跑 `go test ./qianye/version/...`。形状不合法(tag 不是 `vX.Y.Z` 形态、
`describe` 与 `tag` 指向不同 release、`qy_version` 不是 `vX.Y.Z` 或还停在
`v0.0.0`、两个版本号取了同一个值)会直接红。

---

# 客户端 IP:与上游的差异现在还剩哪些

**记于 2026-08-25。** 项目方的判决是「你改什么?原项目上游强制做了吗??
你这有点添油加醋了吧?」「你给个提示就行了」。这一节把撤回之后的实际差异逐条列清楚,
不含糊。

## 上游那一份到底是什么(逐字核对过)

`middleware/trusted_proxies.go`(上游 `main`):

```go
var defaultTrustedProxyCIDRs = []string{
	"127.0.0.0/8", "::1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
}
```

- `TRUSTED_PROXIES` 未配置/留空 → 装上面这份清单,**并打一条 WARNING**;
- `TRUSTED_PROXIES=none` → 谁都不信;
- 非法取值(`none` 与别的值混用、列表为空、CIDR 不合法)→ 返回 error,进程起不来。

**上游没有任何别的强制**:不会因为「看起来装在反代后面」而报错,不会拒绝启动,
也不读 `BIND_ADDRESS`。

## 我们撤回了什么

前两轮本仓把未配置这一档改成了 **fail-closed(谁都不信)**,并额外加了一个
「`BIND_ADDRESS` 是回环地址就自动信任回环」的自动档。那是一次**行为变更**:
装在反代后面、从没配过这个变量的部署,升级那一秒全站客户端 IP 会变成反代地址 ——
令牌 `allow_ips` 开始挡人,按 IP 的限流把所有人算成一个人。

已撤回的具体三项:

| 撤掉的东西 | 现在是什么 |
|---|---|
| 未配置 = 谁都不信(`fail_closed` 档) | 未配置 = 上游那份私网默认(`default_private` 档),与 `defaultTrustedProxyCIDRs` 逐条相同 |
| `BIND_ADDRESS` 回环自动档(`loopback_bind`) | 删掉。`BIND_ADDRESS` 不再参与信任判断,与上游一致 |
| 启动告警里「你必须去配一个 CIDR」的措辞 | 与上游同口径的 WARNING:说清现在信任的是什么、代价是什么、怎么改 |

`common/client_ip.go` 因此从 **975 行降到 953 行**(删 63 行、加 41 行,净 -22;
加的那 41 行绝大部分是把「为什么回到上游默认」写进注释):去掉的是
`ClientIPStrategyFailClosed` / `ClientIPStrategyLoopbackBind` 两个档位常量、
`bindAddressIsLoopbackOnly()` 整个函数、以及未配置分支里那一整段自动探测。
**兼容那部分一行没动** —— 那是项目方明确要的(「这个情况你要考虑多兼容,
各种情况下获取到的 IP」)。

## 撤回之后,与上游还剩的差异(全部是「兼容」与「诊断」,没有强制)

| 差异 | 上游 | 本仓 | 为什么留着 |
|---|---|---|---|
| 多值请求头 | `Header.Get()`,只取第一个 `X-Forwarded-For` | `Header.Values()` 全部按序拼接 | 客户端自带一个 XFF、反代再追加一个时,Go 保留成两个头,`Get()` 拿到的恰好是客户端伪造的那条 |
| 地址归一化 | 原样返回请求头里的字符串 | Unmap + 去 zone + 去端口 + 规范化 | `::ffff:203.0.113.5` 与 `203.0.113.5` 是同一个客户端,不归一会落进两个限流桶 |
| 带端口的 XFF 条目 | `net.ParseIP` 失败后放弃整条链 | 解析得出来(Azure App Service 与部分云 LB 这么写) | 兼容 |
| 厂商专有头 | 只有 `TrustedPlatform` 一档,且**无条件**采信 | 绑定到来源:`CF-Connecting-IP` 只有 `cloudflare` 档说了算;其余用 `CLIENT_IP_HEADERS` 显式声明 | 普通 nginx 默认原样透传客户端带来的任意头,无条件采信等于把判据交还给客户端 |
| `private` / `loopback` / `cloudflare` 关键字 | 无 | 有 | 纯便利,不改变未配置时的行为 |
| `CLIENT_IP_HEADERS` | 无 | 有 | 「只配了 X-Real-IP 的 nginx」那种部署需要它 |
| 诊断端点 `GET /api/qy/admin/client-ip` | 无 | 有(只读、超管) | 提示,不是判据 |
| 单一取值口 `common.ClientIP(c)` | 各处调 gin 的 `c.ClientIP()` | 全站一处,由守卫测试逐文件校验 | 混用会造成「限流按真实 IP、台账记伪造 IP」 |
| `TRUSTED_PROXIES_CLOUDFLARE_FILE` 的内容校验 | 无(上游没这个变量) | 清单里出现私网/回环/链路本地/组播/过宽前缀 → **起不来** | 这一条**确实是强制**,但它只在运维显式指定了那个文件时才存在,与未配置时的默认无关:一份被写坏的清单会静默放宽信任面,而它是运维自己提供的文件 |

**这一列里没有任何一条会拒绝启动、拒绝请求或改变结论。** 诊断页那栏「请求头结论冲突」
同样只是提示:它记下「另一个受信请求头给出了不同答案」,结论一个字都不改。

## 那条原始安全发现的现状:**不堵了,如实说**

未配置时私网对端可以用一个 `X-Forwarded-For` 决定自己在令牌 `allow_ips` 与限流桶里的
取值 —— 这条实测出来的路径,撤回之后**又通了**,因为它就是上游的默认取舍。

代价没有被藏起来,它写在四个地方:启动日志的那条 WARNING、`docs/authentication.md`
的「未配置时的默认」、`.env.example`、以及 `common/client_ip_test.go` 里那条
名字就叫 `the upstream default lets any RFC1918 peer choose its own client IP` 的用例。
不接受这一点的部署有两条一行的出路:`TRUSTED_PROXIES=none`,或者把反代自身的地址
逐条写出来。那是部署者的决定,不是我们替他做的默认。

# AI 审核渠道密钥:「改地址即清空密钥」的撤回

**记于 2026-08-25。** 项目方的判决是「改渠道地址就只是改地址,别的你别动。
这什么时候加进来的限制??」

**这条规则是本仓自己加的**,`qianye/modules/violation/` 整个模块上游都没有,
所以它与上游无关,纯粹是我们上上轮的一个设计。撤回了。

它当初堵的是一条真实的越权路径,两步都在 `role=10` 权限之内:
① PUT 只改 `base_url`、不带 `api_key` → 200 且密钥保留;
② 点「测试连通性」→ 出站请求带着 `Authorization: Bearer <原密钥>` 打到攻击者
指定的地址上。密钥对这一页本来是**只写**的(只回显 `key_hint` 尾 4 位),
这条路等于把它读了出来。

换成的堵法:密钥**绑在它写入时的那个 `base_url`** 上(新增列
`qy_violation_ai_channel.key_endpoint`)。地址与绑定不一致时这把密钥一律不出站 ——
试跑直接 400、热路径跳过该渠道 —— 直到有人在表单里重填一次密钥,
而重填的前提正是他知道那把密钥是什么。

- **保存不再有任何副作用**:改地址就只是改地址,一步到位,不要求填任何别的字段。
- **只堵试跑是不够的**,所以热路径(`buildAIRuntime`)也堵:改完地址之后,
  下一条真实审核流量会把同一把密钥送到同一个地方。
- 零值方向:`key_endpoint` 空串 = 无绑定 = 照常出站,与这一列存在之前逐字节一致。
  启动期 `migrateAIChannelKeyEndpoint` 把存量行回填成它们当前的地址,回填之后闸门
  才对存量数据生效。空串**不能**当成失配 —— 那会让回填没跑到的部署在升级那一秒
  全部审核渠道同时失效,而 AI 审核失败的方向是放行(一次静默的风控关闭)。

---

# 上游同步记录:rc.37 → rc.41 + main(2026-10-02)

**范围**:`v1.0.0-rc.37..upstream/main`(`1a4166d8e`,2026-10-01),非合并提交共 **123** 个。
**方式**:与 rc.34–37 两轮相同,逐提交挑拣(`cherry-pick -x`),不做真 merge ——
整体 merge 会有 210 个文件冲突,而且会把之前刻意挂起的 32 个提交一并带进来。
**结果**:取 70(其中 2 个手工移植)、挂起 50、空提交/本仓早已修过 3。
**内核版本号未改**:`qianye/version/baseline.txt` 仍声明 `v1.0.0-rc.37`。挂起项里有
请求策略、Responses WebSocket 后续修复、插件图片接口、预扣口径、moejs 引擎等动到
relay/计费面的提交,按该文件「下次同步前必读」的要求,是否抬到 rc.41 由项目方拍板。

## 一、取了的(含冲突处理说明)

| SHA | 标题 | 处理 |
|---|---|---|
| `129f21b69` | fix(plugins): normalize invalid UTF-8 in JSON state | 干净应用 |
| `c9a110190` | fix(channels): show built-in base URLs as placeholders | 手工適配舊版渠道編輯器；新渠道配置測試檔不取 |
| `007d69942` | fix(model): migrate renamed prefill group unique indexes | 干净应用 |
| `2ba615761` | feat(web): share collapsible mobile table filters | 渠道表手工合 |
| `043ff99a5` | fix: handle legacy database constraints and scoped policies | 只取 authz 權限策略部分；migration_dialector.go 本倉沒有,屬挂起的 4fc9d1f1f |
| `815217ba6` | test(web): stabilize flaky frontend suite timeouts and motion visibility races (#7367) | 干净应用 |
| `d92612038` | fix: unify model provider detection and add Wan icon (#7373) | 干净应用 |
| `d1c79d728` | fix(dashboard): align weekly default range (#7355) | 干净应用 |
| `76f7dafd2` | fix(audio): normalize file extension case in GetAudioDuration (#7321) | import 衝突 |
| `9fe0457ee` | Add Responses WebSocket relay support (#5062) | 計費入口抽到 relay/request_billing.go 時保留本倉的違規閘/欠費閘/QyPostRelayGuard、不恢復已移除的敏感詞過濾；WS 每個事件額外過本倉的實時計數與分組並發閘；開關接進舊版渠道編輯器 |
| `8529f209c` | feat: vllm channel && sglang channel (#7332) | 後端全取；前端接進舊版渠道編輯器(佔位與必填)，新版供應商選擇器的展示文案不取 |
| `2bfb89c1b` | fix(pricing): prevent sort menu layout shift (#7145) | 干净应用 |
| `8e5e09166` | fix(ollama): preserve tool calls from final stream frame (#7376) | 干净应用 |
| `62f8db775` | feat(channel): 修复获取DeepSeek余额错误 (#6814) | 干净应用 |
| `d3874db61` | feat(rate-limit): optimize in-memory limiter allocation and cleanup (#6807) | 干净应用 |
| `5caafd3d8` | feat(ollama): add per-channel OpenAI-compatible chat switch (#7382) | 開關接進舊版渠道編輯器 |
| `fa90b2312` | fix(gemini): accept case-insensitive thinkingLevel and log canonical effort (#7387) | 干净应用 |
| `81336fc69` | fix(gemini): reject :countTokens as an unknown route instead of relaying it as generateContent (#7388) | 干净应用 |
| `610334dbd` | fix(relay): request stream usage on every cross-protocol conversion to OpenAI chat (#7389) | 干净应用 |
| `69a500298` | feat(channel): per-route pass-through for advanced custom channels (#7386) | 舊版渠道編輯器裡對高級自定義渠道隱藏全局透傳開關 |
| `7209b6db9` | fix(middleware): 隐藏无可用渠道错误中的任务插件标识 (#7414) | 干净应用 |
| `57791e31c` | docs(github): tighten issue and PR templates for pass-through and unfiltered AI text (#7438) | 干净应用 |
| `07578faf2` | fix(billing): count upstream images by payload instead of data length | 按本倉的 n 倍率口徑(updateOpenAIImageCount 帶 MaxImageN 上界)落地；新回歸用例已加且通過 |
| `8b2c7105f` | feat(relay): classify the protocol outcome on StreamStatus | 干净应用 |
| `8a11b4305` | refactor(relaykit): surface reasoning conversion diagnostics | 測試檔 import 衝突 |
| `a5b40663d` | perf(metrics): record relay outcomes at the request boundary and classify stream results | 只取後端(請求邊界採樣+流結果分類)；保留本倉分組可見性過濾與可用率模塊 hook；前端依賴挂起的模型卡片重寫,不取；同時修了 9fe0457ee 測試裡引用的審計表 |
| `6237d9d77` | fix(rate-limit): reserve model rate-limit slots and judge success by response outcome | 干净应用 |
| `90134d5b6` | feat(system-tasks): add filtered task history with cleanup | 面板取上游版本；本倉 server-error-message 補上 createServerError/requireServerSuccess 兩個同簽名小工具,c9a110190/8529f209c 的 api.ts 也要用 |
| `a8ed7f7c5` | fix(plugins/hailuo): bill only the dimensions each model family supports | 干净应用 |
| `ed7c4e35d` | feat(log): flag upstream response model mismatches (#7418) | 七语种 i18n 三方合并 |
| `e2bb05f70` | fix(relay): map Images output_tokens_details into img_o billing (#7410) | 測試併入本倉測試檔 |
| `55a6cd2a4` | fix(responses): estimate usage for streams cut before terminal usage (#7427) | 干净应用 |
| `3524fe0b1` | fix issue template (#7441) | 干净应用 |
| `2906e4f77` | fix(oauth): require account evidence before migrating legacy GitHub bindings | 手工移植:只保留「已驗證郵箱一致才遷移」,無 2FA 驗證後遷移路徑；加了回歸測試 |
| `3e8c358da` | fix: forward reasoning_effort to zhipu upstream (#7124) | 干净应用 |
| `946c730ed` | perf: avoid repeated Claude tool-name lookups (#7447) | 干净应用 |
| `e04c05c2b` | fix(web): 修复小数输入限制和日志分组选项未遮挡的问题 (#7453) | 隱私模式遮擋分組下拉 + 工具價格小數步長；分組倍率的小數步長改到本倉自己的模型分組頁(上游那個編輯器本倉沒有) |
| `972aed197` | fix(log): derive response model mismatch from names instead of a stored flag (#7464) | 干净应用 |
| `33320407f` | fix(web): isolate OIDC discovery when saving OAuth settings | 干净应用 |
| `58a9eff4c` | style: refresh logo | 干净应用 |
| `3fdf9083d` | fix(settings): move documentation link to site settings | 干净应用 |
| `da2540dda` | fix(channels): align card metrics and remove badge spacing | 干净应用 |
| `585c4610e` | feat(keys): show and copy configured API addresses | 七语种 i18n 三方合并 |
| `c49972441` | fix(web): keep mobile dialog actions within visible viewport | 公共對話框視口修正全取；各業務對話框只改本倉存在且同款寫法的兩處 |
| `a5bf94022` | fix(web): correct dialog viewport fallbacks and editor gutter alignment | 代碼全取；編輯器測試檔保留本倉版本(本倉測試寫法不同) |
| `3abbb8198` | feat: record perf metrics when async tasks reach terminal status (#7481) | 測試去掉依賴挂起 5c7cca015 的那一條 |
| `47713bcb1` | feat: update plugins | 干净应用 |
| `e537dc380` | fix(web): expose plugin activation after upload | 七语种 i18n 三方合并 |
| `c0cff23a3` | fix(plugins): store task plugin source as longtext on MySQL and raise the upload cap to 8 MiB | LongText 列類型全取(去掉屬於 4c34f25a4 的 UnbindTaskPlugin)；測試輔助函數從上游搬入,夾具按本倉插件體積撐過 64KiB。MySQL/PG 實庫遷移未在本機驗證 |
| `2c175190c` | perf(plugins): sync task plugins from hashes and load source only for changed rows | 干净应用 |
| `d61d6be75` | fix(task): accept any 2xx upstream status on task submission | 回歸測試併入本倉測試檔,用最小計費樁代替上游的 imageReservation |
| `6e9de44a7` | fix(relayconvert): hoist Responses tool-output media into a user message (#7510) | 干净应用 |
| `4eb3b9160` | fix(relayconvert): hoist Claude tool_result images and segment Responses reasoning items (#7512) | 干净应用 |
| `b6809a52d` | fix(web): resolve safe constants in plugin metadata previews | 干净应用 |
| `9310231b3` | fix(logs): keep model badge text readable and compact (#7504) | 干净应用 |
| `996adffe5` | fix(web): keep text selection when opening a model pricing row for editing (#7403) | 干净应用 |
| `5401874c6` | fix(ci): align ldflags -X package path with module path so release binaries embed the real version (#7536) | 干净应用 |
| `c76452d22` | fix(playground): add break-all to playground input (#6600) | 干净应用 |
| `c2b7a9a9e` | fix(claude): preserve per-message output_config in Claude messages (#7561) | 干净应用 |
| `789c97019` | fix(claude): preserve auto mode safeguards in native requests (#7598) | 干净应用 |
| `f5e54ccdb` | fix(web): revalidate marketplace fetches so stale caches do not fail integrity checks | 干净应用 |
| `2506e1b98` | fix(auth): reject non-standard roles when creating a user | 手工移植:手工移植到本倉 CreateUser,測試改寫為本倉夾具 |
| `335ebcff7` | fix(relayconvert): keep Responses custom tools on Chat Completions upstreams | 干净应用 |
| `2035a82ae` | feat(web): add a refresh button to the channels table | 手工合入本倉渠道表(保留本倉清零入口) |
| `feefe09f2` | fix: isolate TLS configs across HTTP transports (#7627) | 干净应用 |
| `d0cb7347c` | fix: use max_completion_tokens for gpt-6-sol and gpt-6-luna (#7559) | 干净应用 |
| `811212067` | fix(web): Add class to wrap the long strings at auto-disable status tooltip content in channel manage page (#7604) | 干净应用 |
| `5fe8917be` | fix(i18n): keep cached zhTW/zhCN interface codes stable across page loads (#7139) | 干净应用 |
| `56758edf9` | fix(web): correct translations, chart ordering, and font loading (#7628) | 翻譯/圖表排序/字體修正全取；本倉改過的充值卡片與日誌列手工套用同樣的 i18n 改動；訂閱列(本倉有自己的售賣狀態標籤)與刪除賬號對話框(本倉無)不取 |
| `1a4166d8e` | fix(relayconvert): keep Responses custom tools on Claude and Gemini upstreams (#7636) | 干净应用 |

## 二、挂起的(未同步)及理由

| SHA | 标题 | 理由 |
|---|---|---|
| `33142f0ae` | fix: refine sign-in page behavior | 依賴挂起的 385d2dfd1(多 RP ID passkey) |
| `7fd063819` | fix(auth): restore saved compatible passkey domains | 依賴挂起的 385d2dfd1(多 RP ID passkey) |
| `2509e25fa` | fix(web): prevent combobox dropdowns opening on dialog autofocus (#7365) | 本倉沒有上游重寫後的 OptionCombobox(挂起的組件重構),修的那段代碼不存在 |
| `ed0ad6e3f` | feat(channels): add compact quick options to channel editor | 純 UI，建在上游重寫後的渠道編輯器(挂起的 505805a4c)上，本倉沒有那套 channel-configuration 面板 |
| `d82678a97` | docs: move billing rules to .agents/rules/billing.md behind a read gate | 純文檔重排；本倉 AGENTS.md 的計費規則段已按 fork 改寫(星屑/抽獎上界等)，不拆 |
| `0cde9d94f` | feat(channels): add floating model redirect workbench to channel editor | 純 UI 新功能(模型重定向浮動工作台)，建在上游重寫後的渠道編輯器(挂起的 505805a4c)上 |
| `73a471f3a` | feat(policy): add request policies settings and routing decision records | 大型設置頁重構(請求策略/路由決策記錄)，含本倉已整體移除的敏感詞檢查、改寫渠道親和/重試/MJ 計費入口；69 個檔,單獨評估 |
| `75f3d246e` | fix(responses-ws): carry stream_id through events and correlate errors | WS 後續,依賴挂起的 73a471f3a(請求策略);其中「請求邊界性能採樣」一處已手工移植 |
| `e2471403c` | fix(pricing): adjust billing expression condition display | 改的檔來自挂起的 d52bdc0b4(按時段計價編輯器)，本倉沒有 |
| `42cbe6206` | chore(web): add intl-locale lint plugin and update agent rules | 開發工具(新增 intl-locale lint 規則)+ 改 AGENTS.md；本倉的測試收集方式與 lint 基線不同,引入會讓既有代碼 lint 失敗 |
| `d80694366` | fix(web): tidy channel health policy layout | 只改 73a471f3a 新建的「請求策略」頁(未取) |
| `ae249f4ec` | feat(responses-ws): extend channel support and share routing with HTTP | WS 後續,依賴挂起的 73a471f3a(請求策略) |
| `fa3cc1c6b` | feat(web): offer the Responses WebSocket toggle for advanced custom, sub2api and new-api channels | WS 後續 UI(依賴 ae249f4ec) |
| `32ef6d216` | feat(web): add channel passthrough controls and usage guidance | 純 UI，建在上游重寫後渠道編輯器的快捷選項面板上(本倉無) |
| `9e905a83e` | refactor: show subscription deductions and improve cost display (#7272) | 本倉日誌費用列已有自己的訂閱/錢包補收拆分顯示(9e427b460),與上游這套重寫衝突,保留本倉 |
| `ea336ed66` | fix(log): hide funding source labels when subscriptions are not in play (#7450) | 9e905a83e 的後續(訂閱來源標籤) |
| `96b0da227` | refactor(log): 恢复费用气泡并用图标区分订阅来源 (#7451) | 9e905a83e 的後續(費用氣泡圖標) |
| `1ea24a050` | docs: refresh project README across languages | README 文案(本倉 README 是自己的) |
| `b0bf2580e` | docs: restore README badge and partner placement | README 文案(本倉 README 是自己的) |
| `a89037d0b` | fix(web): start linked-account verification from the provider button | 純 UX，改的是上游重構後的安全驗證對話框(挂起的安全/審計重構),本倉版本結構不同 |
| `ef70d1b79` | chore: drop trailing blank line from .gitignore | 只刪 .gitignore 末尾空行,本倉 .gitignore 有自己的條目 |
| `b932e6d87` | fix(web): robustly display conditional task billing prices | 定價頁顯示,建在挂起的計費表達式編輯器/任務矩陣那條鏈(d52bdc0b4/39294418a 等)上 |
| `9c3d3aeb3` | perf(web): lazy-load provider icons and reduce page rerenders | 純前端性能優化,動到本倉改過的多個表格列(密鑰/用戶/日誌);非功能性,風險大於收益 |
| `4c34f25a4` | feat(channel): bind multiple task plugins to New API channels | 新功能(New API 渠道綁定多個任務插件),配置界面建在挂起的 ab489ab88(渠道插件擴展選擇器)上,本倉無入口；同時改分發器與任務輪詢 |
| `2d7aef741` | fix(web): restore CC Switch model dropdown | 修的回歸來自挂起的密鑰頁改版；本倉 CC Switch 對話框仍是改版前寫法,沒有這個 bug |
| `6b638788c` | fix(channels): preview upstream model changes before applying | UX 改進(上游模型變更先預覽再應用),依賴挂起的 12be9975c(統一錯誤提示)與新版公共組件 |
| `db536ec8b` | fix(channels): simplify model redirects and clarify panel controls | 純 UI，修的是 0cde9d94f 的模型重定向浮窗(未取) |
| `d904b9700` | feat(settings): improve group configuration UX and drag sorting | 上游分組配置頁的 UX；本倉分組配置已整體換成自己的用戶分組/模型分組頁(system-settings/groups),上游這幾個編輯器本倉沒有 |
| `03563a4a7` | feat(plugins): serve OpenAI Images API through task plugins | 大型新功能(任務插件提供 OpenAI Images 接口),依賴挂起的圖片計費表達式鏈(f362c7c51/f064bffa2/f256e40bc) |
| `2fddb863f` | feat(models): add bulk field selection to metadata sync | 改的是上游重寫後的元數據同步嚮導(挂起的 0c76e4dae 模型/供應商管理重構),本倉是舊版 |
| `65d3a2171` | fix(plugins): price only the resolutions each Seedance and Wan model offers | 內置插件 alibaba/doubao 的分辨率定價,疊在挂起的 03563a4a7 對同兩個插件的改動之上 |
| `9978ee1e2` | feat(billing): configure trust threshold and input pre-consume multiplier | 【刻意不取】上游把預扣改成「只按輸入 × 倍數」,去掉了輸出側預估;本倉恰好修過這個缺口(省略 max_tokens 時用 8192 兜底,否則一次請求就能把餘額扣成負數,見 relay/helper/price.go 註釋)。另依賴挂起的圖片計費 |
| `9a0be8750` | fix(web): keep task pricing editable after a plugin narrows its enum values | 定價頁表達式顯示,建在挂起的計費表達式顯示重構上(本倉無 display.ts/task-display.ts) |
| `9c293e8c0` | test(billing): fix websocket rejection refund test after pre-consume dropped output estimation (#7491) | WS 測試修正,依賴 75f3d246e 的測試改寫 |
| `474ed66fb` | fix(plugins/alibaba): keep upstream metadata in openai_image render (#7507) | 內置插件 alibaba 的 openai_image 渲染,依賴挂起的 03563a4a7 |
| `00e8a00cb` | fix(web): include provider icons in Windows builds | 修的是 9c3d3aeb3 引入的圖標懶加載在 Windows 構建下丟失(未取 9c3d3aeb3 則無此問題) |
| `54eee488b` | fix(web): isolate theme preferences in local storage | 本倉主題系統已大改(移除主題設置、站點默認主題、Steins Gate 等),上游這次主題存儲重構與之衝突 |
| `8cb88ebd1` | fix: move force operation into confirm slot so plugin-in-use dialog has one cancel button (#7528) | 插件表對話框按鈕排版,本倉插件表對話框結構與上游不同(UI 小修) |
| `a46f045d6` | fix: match current price column width to source columns so expressions wrap alike (#7542) | 上游價格同步表格列寬,改的是挂起的定價同步重構版本 |
| `6c14c0762` | feat: highlight differing values in source price expressions (#7543) | 上游價格同步表格的表達式差異高亮,改的組件(upstream-price-cells)本倉沒有或為舊版 |
| `d04c118c8` | fix(channel): preserve Responses WebSocket setting on save (#7468) | WS 後續 UI,修的是上游新編輯器的保存邏輯 |
| `87bb71e7f` | feat(plugins): serve Seedream images through the OpenAI Images protocol in doubao | 內置插件 doubao 走 OpenAI Images,依賴挂起的 03563a4a7 |
| `3509ae7c1` | fix(pricing): keep prices saved before a usage profile narrows editable | 改的是挂起的 0c76e4dae 引入的 model_pricing_config.go(本倉定價保存校驗在 model/pricing.go,且你工作區正有未提交改動) |
| `6cbb1c7ed` | feat(auth): require step-up verification for admin user management | 管理員用戶管理的二次驗證,建在挂起的安全驗證重構(d8cb17744/0973dc2b8 等)上 |
| `caca52f8d` | feat(auth): replace the dashboard access token with scoped access tokens | 大型安全重構(用可授權範圍的訪問令牌替換控制台訪問令牌,59 個檔),建在挂起的 d8cb17744/0973dc2b8/3f8a50cf8 安全審計重構上 |
| `2ffc59590` | feat(jsplugin): run task plugins on moejs | 【刻意不取】把插件 JS 引擎從 Sobek 換成 moejs v0.1.0-alpha.3(alpha 版),禁用 eval,並連帶升級 clickhouse 等依賴;基礎設施級變更,單獨評估 |
| `c71eefbcd` | docs: update readme | README 文案(本倉 README 是自己的) |
| `4924361ae` | feat(auth): edit the name and permissions of an access token | 訪問令牌改名/改權限,依賴 caca52f8d |
| `ae73ef8e2` | feat(plugins): add Seedream 5.0 flash and price each Seedream model by its own tiers | 內置插件 doubao Seedream 定價,疊在 03563a4a7/87bb71e7f 之上 |
| `04c64734c` | fix(pricing): keep model status bar spacing uniform (#7284) | 依賴挂起的 5c7cca015/0c76e4dae(模型卡片 UI 重寫)，本倉狀態條是舊版 |

空提交 / 无需同步:`be36cbb8f`(只改本仓没有的安全测试)、`0aec08fee`(空提交)、
`dfd3cd893`(本仓早已同样修过「非 root 管理员隐藏系统设置」)。

## 三、行为变化需要知道的

- **计费入口重构**(`9fe0457ee`):`controller/relay.go` 的估算/预扣/失败退款抽成
  `relay.PrepareRequestBilling` / `RefundFailedRequestBilling`(HTTP 与 Responses
  WebSocket 共用)。本仓的违规闸 `QyPreRelayGuard`、欠费免费模型闸
  `RejectOverdrawnFreeModelCall`、`QyPostRelayGuard` 全部搬进新函数;上游的敏感词
  检查**没有**带回来(本仓已整体移除)。AST 守卫测试同步改为在新文件里判位置。
- **Responses WebSocket**(`/v1/responses` GET 升级):按渠道开关,默认关。每个
  `response.create` 额外过本仓的实时计数与分组在途并发闸;客户端 IP 走
  `common.ClientIP`,内层请求去掉转发头。
- **图片计费**:`07578faf2` 按真实图片载荷计张数(仍受 `dto.MaxImageN` 上界);
  `e2bb05f70` 把 Images 接口的 `output_tokens_details` 映射进补全明细,配置了图片
  输出倍率的模型,输出图片 token 会按图片输出价计。
- **旧版 GitHub 绑定**(安全):登录名形式的旧绑定只有在 GitHub 已验证邮箱与账号邮箱
  一致时才迁移并登录,否则拒绝并提示用其他方式登录后重新绑定。
- **创建用户**只接受 guest/common/admin/root 四档角色。
- **任务插件源码列**改为 MySQL `longtext`(PostgreSQL/SQLite 仍为 `text`),上传上限
  8 MiB。**这是表结构变更,本机只验证了 SQLite**;MySQL/PostgreSQL 的实库升级验证
  (上游在提交说明里写了已跑过 5.7/8.4/9.6/17)本轮未在本机复跑。
- **性能指标**:改为请求边界采样并区分流结果,任务插件终态也采样;客户端主动断开等
  不计入成功率。
- **新渠道类型**:vLLM(62)、SGLang(63);Ollama 渠道可切 OpenAI 兼容接口。

---

# 上游同步记录(第二批):模型计费改走计费表达式(2026-10-02)

**分支**:`sync/billing-expr`(叠在 `sync/upstream-rc.41` 之上)。
**范围**:rc.34–rc.41 之间与「模型计费改用表达式」相关、此前一直挂起的整条链,共 30 个提交。
取 27、挂起 2 组(见下)。

## 上游的新口径(以 `pkg/billingexpr/expr.md` 为准)

- 旧倍率 / 按次定价**仍然能跑**,管理界面标为「已弃用」,新建默认用表达式;
- 提供「旧定价 → 表达式」转换:`POST /api/option/model_pricing/convert` 只返回草稿与生效价快照,
  界面左右对照,确认后才写入;
- 定价保存改走 `PATCH /api/option/model_pricing`,带 `expected_version` 乐观并发检查;
- 新增按次表达式 `fixed()`、图片缓存 `img_cr`、图片张数 `image_count`、插件级分模型定价。

## 逐条

| SHA | 标题 | 处理 |
|---|---|---|
| `0c76e4dae` | 模型/供应商管理与定价重构 | 后端全取;渠道/用户/套餐抽屉保留本仓(上游只是 Select→Combobox 外观替换);定价设置卡保持本仓去掉分组页的结构;OptionCombobox 补接本仓 popupClassName/onKeyDown |
| `5c7cca015` `bee45b58a` `04c64734c` | 健康条按小时序列 / 卡片网格 / 状态条间距 | 干净应用 |
| `0e0ba152b` | 定价编辑器与日志展示 | 日志列保留本仓预扣差额标记;task-price-display 用上游完整版替换本仓临时桩 |
| `71c1fd7ca` | 模型列表与可见性筛选 | 干净应用 |
| `4fc9d1f1f` | 重建 options 表主键、定价写入不再重置行 | 干净应用;**升级验证**见下 |
| `d52bdc0b4` `7cf9b473f` | 按时段计价编辑器 / 滚动区 | 干净应用 |
| `064ed943e` | 按次表达式 `fixed()` | 「零 token 免单」判据合并:本仓按次价格与上游 fixed() 都照收 |
| `f362c7c51` | 图片缓存与数量变量 | RequestInput 并集(本仓时钟覆写 + 上游 ImageCount) |
| `f064bffa2` | 预扣前校验图片数量 | 见下「本仓口径」;另修上游一处缺陷:补扣时 `WalletFunding.PreConsume` 会覆盖 consumed,失败退款少退,改为原子预留后累加 |
| `f256e40bc` | 内置 GPT 图片模型表达式默认价 | 冒烟向量并进本仓更严的生成器,补新变量 img_cr |
| `25ec832fa` `b3e279464` `39294418a` | 旧定价转表达式 / 转换预览 / 编辑器扩展 | 干净应用(日志详情保留本仓扩展钩子) |
| `74629e29f` | 插件级定价那一半 | 补齐 rc.37 挂起的部分;运行时与安全相关处保留本仓更新版本 |
| `2509e25fa` `e2471403c` `b932e6d87` `9a0be8750` `a46f045d6` `6c14c0762` `3509ae7c1` `2fddb863f` | 显示与编辑细节修正 | 干净应用 |

挂起:

- `12be9975c` 全局错误提示重构(37 个文件,非计费;本仓有自己的修法)。后续代码用到的
  `createServerError` / `getServerErrorMessage` / `requireServerSuccess` / `handleServerError(error, fallback)`
  以同签名在本仓补齐。
- `03563a4a7` 及其后 4 个插件定价提交:把阿里百炼图片从内置 Ali 渠道迁到 alibaba 任务插件
  (删除 `relay/channel/ali/image*.go`),属于渠道行为迁移,待项目方决定。
- `9978ee1e2`(上一节已列):预扣改为只按输入估算 —— 与本仓「输出侧 8192 兜底」的资金安全修复冲突,继续不取。

## 本仓刻意保留的口径差异

- ~~图片张数 n 对旧定价的按量(倍率)路径也生效~~ —— **2026-10-03 项目方定:按量路径以上游返回的 token 计费,张数不进价格**,
  与上游同口径(预扣 `relay/helper/price.go`、事后改张数 `RelayInfo.UpdateImageCount`、结算 `service/image_billing.go` 三处一致)。
  按次价格仍乘张数;dall-e 的 size×quality 两条路都保留。已知代价:上游不回 usage 时这一笔只按 1 个 token 收。
- 不恢复本仓已删除的「信任额度」旁路(上游 `trusted` 字段)。

## 验证

- Go:`go build` / `go vet` / gofmt 干净;relaykit 独立构建通过;全量 `go test` 只剩两条与本轮无关的:
  `relay/channel` 的 HTTP2 用例(基线即红,本机环境)、`TestDeleteRedemptionBatch/mysql`(基线同样红)。
- 数据库:模型管理/供应商/删除三组矩阵测试在 **SQLite 3.50.4 与 MySQL 8.4.9** 通过;
  用旧二进制(`b5f28ada9`)在空库初始化并写入定价行,再用新二进制连起两次 —— MySQL 与 SQLite 均无 DDL、
  无报错、定价行原样保留。**PostgreSQL 本机没有,未验证。**
- 前端:typecheck 干净;qy 套件 7 条失败与基线相同(首页 hero/promises,工作区正在改)。

# 上游同步记录(第三批):独立审计日志(2026-10-02)

**分支**:`sync/audit-log`(叠在 `sync/billing-expr` 之上)。
**范围**:`d8cb17744`(访问令牌管理与审计日志)、`3f8a50cf8`(补全令牌与额度操作记录)里**审计日志那一半**。
项目方选择「只做审计日志」:访问令牌管理(作用域令牌、轮换/吊销、「安全与访问」独立页)、
管理员权限面板重构、额度调整事务化重写**都不取**。

## 取了的

| 部分 | 内容 | 本仓适配 |
|---|---|---|
| 存储 | 独立 `audit_logs` 表(`model/audit_log.go`、`audit_other.go`),`other` 为 JSON 列;放在日志库,共享日志库时只由主节点迁移 | 去掉 `UserAccessToken` 状态查询那一节;IP 走 `common.ClientIP`(单一来源守卫) |
| 写入 | 管理操作、登录、账户安全、访问令牌请求、API 令牌操作全部改写到审计表;`RecordLogWithAdminInfo`/`RecordLoginLog`/`RecordOperationAuditLog` 换上游签名,写入改为同步 | 根动作闸门(`middleware/root_action.go`)、违规封禁/解封(`qianye/modules/violation/ban.go`)一并改写到审计表;封禁细节放在仅管理员可见的 `audit_info` |
| 中间件 | `AccessTokenAudit`(全局,含被限流/鉴权前拒绝的请求)、`TokenOperationAudit`(API 令牌路由);`authHelper`/`TryUserAuth` 补开/收尾审计,覆盖直挂在 `/api` 组外的扩展路由 | 用 d8cb17744 版本的令牌校验(上游后来改成作用域令牌查找,不取) |
| 令牌目标 | `controller/token.go` 把 id/名称/状态前后/变更字段交给审计 | 本仓 `group_only` 行内快切也记 `changed_fields`;审计口径收进 `recordTokenUpdateAuditChanges`,保证 `UpdateToken` 只有一处 `if statusOnly` 分支(groupmatrix 写入侧守卫按它定位) |
| 接口与权限 | `GET /api/audit`(`audit.read` 权限)、`GET /api/audit/self`;root 记录对非 root 永不可见 | 受限账号对两条路由一律拒绝(与 `/api/log/self` 同口径) |
| 前端 | `/usage-logs/audit` 页、侧栏「审计日志」、侧栏模块开关、用户权限里的 `audit` 资源;使用日志的「管理」「登录」类型标为「已弃用」(只用来查历史) | 详情弹窗不查作用域字典(本仓没有作用域令牌),遇到 scopes 只计数;`loginMethodLabel` 指向本仓 `profile/` 下的位置;`DetailSection` 补进 `log-detail-layout.tsx` |
| 其他 | `RelayNotFound` 禁缓存 404(升级后旧页面请求新接口不会被缓存成 404);`GenerateAccessToken` 记 `access_token.generate` | — |

不取:`controller/access_token.go` 的令牌管理接口、`model/user.go` 的 `access_token_created_at` 列、
`features/security/` 整页、`controller/user_quota.go` / `model/user_quota_adjustment.go`(额度调整事务化)。
`quota-audit-operation.ts` 只作为展示格式化取了(老的 `user.quota_*` 记录照样能读)。

## 行为变化需要知道的

- 新的管理/登录/安全记录**不再进 `logs` 表**,在「审计日志」页看;`logs` 里的历史记录保留,类型筛选里标「已弃用」。
- 清理旧日志(`DeleteOldLogBatch`)不会删审计表。
- 本仓原有的 `/qy/admin/audit-logs`(扩展模块自己的审计轨迹,写在扩展库)与新审计页是两回事,互不替代。

## 验证

- Go:build / vet / gofmt 干净;全量 `go test` 只剩 `relay/channel` 的 HTTP2 用例(基线即红)。
  移植上游审计测试(去掉访问令牌管理部分)与 API 令牌审计矩阵:**SQLite 3.50.4 与 MySQL 8.4.9** 全过,
  含全新库/旧版本库升级、迁移跑两次、共享与独立日志库。**PostgreSQL / ClickHouse 本机没有,未验证。**
- 本机 MySQL 预览实例升级:`audit_logs` 建表正确(JSON 列与索引),登录与令牌操作实际写入并在页面正确展示。
- 前端:typecheck、涉及文件 lint/format 干净;审计页 + 类型筛选 vitest 86 条全过;bun 套件与基线一致。
- vitest 配置把 `zod` 加进内联依赖:在 Bun 运行时下,外部化的 zod 丢 `export * as z`,
  凡间接引到 `import { z } from 'zod'` 的用例整份加载失败。改后失败文件 32 → 3,剩下 3 个基线同样失败。

# 补回:首页群聊卡片与二开落地页(2026-10-03)

同步分支是从**已提交**的代码切出来的,而这两样当时只在主工作区里、尚未提交,所以同步后的构建显示的是上游原样。
从主工作区原样搬进本分支(主工作区未动):

- 系统设置 → 站点与品牌信息 → 系统信息:「首页群聊入群链接」「首页群号」两项(`QyHomeGroupJoinUrl` / `QyHomeGroupNumber`,
  落库前与 `/api/status` 下发前各净化一次);首屏右侧在配置后换成群聊二维码卡,核心承诺里的社群一条带上群号。
- 首页路由换回二开落地页 `QyLanding`(撤销 `b5f28ada9` 的临时切回),`qy_home_*` 中英文案一并带上。
- 与上游新增的「文档链接」(`general_setting.docs_link`)字段合并,两边都保留。
- `qy/*.json` 里已提交代码在用却缺的 12 个键(AI 审核「命中计次记为」、模型/用户分组搜索)也从主工作区一并带上,
  qy 文案覆盖测试转绿。
- `group-contact.ts` 的 `no-control-regex` 豁免注释移到正则所在行(格式化把正则折到了下一行,原注释失效)。

主工作区里同期的其他未提交改动(风控预警建表开关、插件分模型 usageSchema、兑换码、定价、任务转发、版本基线)**没有**带过来。

# 上游同步记录(第四批):新版渠道编辑器与渠道插件扩展(2026-10-03)

**分支**:`sync/channel-editor`(叠在 `sync/audit-log` 之上)。
**起因**:项目方要上游渠道编辑器里的「插件扩展」(OpenAI 等渠道勾选任务插件模型,如 Sora)
与 New API 渠道绑定多个任务插件。两者都建在上游重写后的渠道编辑器(`505805a4c`)上,此前随它一起挂起。
本仓旧编辑器没有自己的改动(只有后来移植进去的上游功能与多密钥策略修正,后者上游 `d4c26bfb8` 有同款),
所以整体换成新编辑器。

| SHA | 标题 | 处理 |
|---|---|---|
| `505805a4c` | 统一渠道配置界面 | 编辑器取上游;后端 options 保留本仓更全的字段;**Passkey 域名确认那部分不取**(本仓 Passkey 设置页不同) |
| `d4c26bfb8` | 编辑时可改多密钥策略 | 干净应用(本仓 `fed9d1d3d` 的同款修正) |
| `ab489ab88` | 供应商与模型选择里的插件扩展 | 后端 options 已有 channelTypes,保留本仓;供应商排序取上游最终版 |
| (补) | c9a110190 / 9fe0457ee / 8529f209c / 5caafd3d8 / 69a500298 的新编辑器那一半 | 这几条当初接进旧编辑器,这里补上新编辑器侧 |
| `ed0ad6e3f` `0cde9d94f` `32ef6d216` `db536ec8b` | 快捷选项 / 模型重定向浮窗 / 透传说明 / 重定向简化 | 取;`combobox-input.tsx` 用上游最终版 |
| `4c34f25a4` | New API 渠道绑定多个任务插件 | 全取(分发器、任务轮询、插件 upstream 上下文、内置插件);无表结构变化,查询为可移植 GORM |
| `d04c118c8` | 保存时保留 Responses WebSocket 设置 | 只取 channel-form.ts |
| `6b638788c` | 上游模型变更先预览再应用 | 取;旧式错误提示换成已有的兼容 `handleServerError` |

本仓适配:

- `useChannelKeyDisclosure`:上游建在未合的 `45c3fbe8a`(验证凭据绑定会话)上,用本仓 `withVerification` 流程实现同形 hook。
- `lib/responses-websocket.ts`:上游 `fa3cc1c6b` 把开关放开到高级自定义 / Sub2API / New API,依赖挂起的 `ae249f4ec`;
  本仓后端只支持 OpenAI(1)与 Codex(57),只列这两类。
- 测试里的 `createAppQueryClient`(属于挂起的 `12be9975c`)换成与 `main.tsx` 同口径的本地 QueryClient。

仍挂起:`73a471f3a`(请求策略)、`ae249f4ec` / `fa3cc1c6b`(WS 扩到更多渠道)、`03563a4a7` 链(阿里图片迁插件)、`2ffc59590`(moejs)。

验证:Go 全量 build / vet / test 全过,relaykit 独立构建与测试通过;前端 typecheck、涉及文件 lint/format 干净,
渠道与公共组件 vitest 37 个文件 332 条全过;bun 套件只剩基线那条 `QyResponsiveDialog` 宽屏用例。
本机预览实测:新建渠道选 OpenAI 出现「插件扩展 · Sora 已选 0/2」,可勾 sora-2 / sora-2-pro 并单独重定向;
选 New API 出现「上游任务插件」多选。
