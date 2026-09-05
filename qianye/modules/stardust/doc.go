// Package stardust 是「星屑」—— 只存在于扩展库里的积分货币 —— 的账本与运营参数。
//
// 设计稿见 qianye/docs/design-15-stardust.md(§2 货币定义、§3 账本)。娱乐活动
// (lottery)与星屑商城(mall)只认它;它不可划转、不可兑回主库余额、不能消费模型。
// 本包只负责三件事:表、写账的唯一入口(Credit / Debit)、qy_settings 的运营覆盖层。
// 结算(消费返日结)、充值 / 兑换码 / 注册 / 套餐返的扫描与 hook、用户端与管理端
// 接口都在本包的其它文件里,它们**只经 Credit / Debit 动账**。
//
// # 不变量
//
//   - available 恒 ≥ 0。星屑不是钱,没有透支的理由;每一次扣减都是条件 UPDATE
//     并断言 RowsAffected == 1,锁内快速失败只是让错误更早、更便宜。
//
//   - 单笔与余额都 ≤ common.MaxQuota。星屑数值比额度小五个数量级,沿用同一条
//     算术上界,不另立一套。
//
//   - 一个 kind 只落一列(kindColumn):获得类落 total_earned、花掉类落 total_spent、
//     退回类落 total_refunded、手调落 total_adjusted(带符号净额)。于是余额行上的
//     每个数字都由流水解释:
//
//     I0  available == total_earned − total_spent + total_refunded + total_adjusted
//     I1  available == Σ qy_sd_ledger.amount(per user),且最后一行的 balance_after
//     等于 available —— 账本只追加,任何一行都不允许被改写或删除
//     I2  Σ qy_sd_accrual.gross WHERE status='settled' == Σ ledger.amount WHERE
//     kind='consume_rebate' + carry(消费返的余数结转落在 [0,1))
//
//     三条都由 /admin/stardust/ledger-check 逐用户核对;这里能做到的只是让每一次
//     写入都不可能单独打破它们。
//
// # 锁序契约(合库后第一次出现,不是既有顺序)
//
// 余额行(qy_sd_balance)与活动行(qy_lot_activity)从此在**同一个扩展库事务**里
// 被加锁。为了从结构上消灭死锁:
//
//   - 同一事务里**活动行锁必须先于余额行锁**。报名 / 投注 / 转一次的顺序是
//     reserveEntry(锁活动行)→ Debit(锁余额行),绝不能反过来。
//   - 持有余额行锁的事务**不得再去锁 published 状态的活动行**。派奖 worker 的事务里
//     对活动累计字段的 UPDATE 必须挪到 Credit 之前(活动 → 余额),而不是之后。
//   - 转盘的星屑奖在转动事务里直接以 paid 落行,永远不进派奖 worker 的扫描集。
//
// LockBalance 是本包唯一的加锁点,任何模块都不许自己对 qy_sd_balance 发 FOR UPDATE。
//
// # Credit / Debit 的调用契约
//
// 两个函数都在**调用方的事务**里工作,自己不开事务、不提交、不回滚。它们返回任何
// 非 nil error 时,调用方必须让整个扩展库事务回滚 —— 包括 ErrInsufficient。
//
// 理由写透:协议的第 3 步先以 (idem_scope, idem_key) DoNothing 插入流水行,第 4 步才做
// 余额的条件 UPDATE。若调用方吞掉第 4 步之后的错误(或吞掉 ErrInsufficient 之后
// 继续做别的写入)然后提交,那一行幂等流水就会残留在库里,而余额一分没动;
// 用户下一次带同一个幂等键重试时会被判成"重放",拿回一张 Inserted=false 的收据,
// 余额仍然不动 —— 一笔既没扣成也永远扣不成的账。所以本包的错误只有一种处理方式:
// return,让事务整体回滚。lottery 包里由 AST 守卫钉住"Credit/Debit 的错误分支
// 只能 return"。
//
// 幂等语义:同一 (idem_scope, idem_key) 第二次到来时返回 Result{Inserted:false},
// 其余字段来自已存在的那一行,余额不动。调用方若要判断"重放的内容是否一致"
// (例如管理员手调比对 user_id 与 delta),必须自己拿 LedgerId 读回那一行比对。
package stardust
