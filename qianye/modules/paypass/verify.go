package paypass

import (
	"context"

	"github.com/QuantumNous/new-api/common"
)

// verify 是支付密码校验的唯一实现。
//
// # 执行顺序是安全性的一部分,改动前先读完这段
//
//	① 读行
//	② 锁定期已过 → 清零(否则解锁后第一次输错立刻重锁)
//	③ **无论账号处于哪种状态,都跑一次同 cost 的 bcrypt 比对**
//	④ 才开始按状态分支
//
// ③排在④前面不是风格问题。反过来写(先判断"没设过密码就早返回")会让
// "未设置"在几百微秒内返回,而"密码错误"要等一次完整的 bcrypt(默认 cost 10,
// 几十毫秒)—— 响应时间就成了一个不需要任何权限就能读的账号状态探针。
// 占位摘要(hash.go 的 dummyHash)存在的全部意义就是让③在没有真实哈希时
// 也付出同样的时间。
//
// # 已知的残余时间差(如实写明,不假装没有)
//
// "密码错误"这条路径比另外两条多两条短语句(自增 + 回读),约一次索引写的量级,
// 相对于 bcrypt 的几十毫秒是两个数量级以下的噪声。要把它也抹平就得给
// "未设置/已锁定"造两条假写入,那是把真实的数据库写入用作伪装,代价与收益
// 明显不成比例。何况本接口全程要求登录态,调用方就是账号本人,而本人本来
// 就能从 /pay-password/status 读到这三种状态。
//
// # 为什么 ctx 不是请求 ctx
//
// 调用方一律用 guard.ColdContext(context.Background())(见 gate.go)。
// 用 c.Request.Context() 会让**客户端主动断开连接**取消掉②③④里的失败计数写入 ——
// 攻击者只要每次发完请求立刻断开,就能无限次试密码而错误计数一次都不增长。
// 这不是理论:HTTP 客户端取消请求是一行代码。
func verify(ctx context.Context, userId int, password string) error {
	if userId <= 0 {
		return errInvalidParam
	}
	gdb, err := handle(ctx)
	if err != nil {
		return err
	}
	now := common.GetTimestamp()
	row, _, err := loadRow(ctx, gdb, userId)
	if err != nil {
		return err
	}
	// ② 锁定期已过:清零后按"未锁定"继续,给用户一个完整的新窗口。
	if row.LockedUntil != 0 && !row.lockedAt(now) {
		if err := clearExpiredLock(ctx, gdb, userId, now); err != nil {
			return err
		}
		row.FailCount = 0
		row.LockedUntil = 0
	}

	// ③ 恒定成本比对。row.Hash 为空时 compareHash 会用占位摘要顶上。
	matched := compareHash(row.Hash, password)

	// ④ 状态分支。
	switch {
	case !row.isSet():
		// 首次使用强制设置:给一个明确的 code,前端据此把用户引到设置页,
		// 而不是笼统地报"密码错误"让用户反复试一个根本不存在的密码。
		return errPayPwdNotSet
	case row.lockedAt(now):
		return errPayPwdLocked
	case password == "":
		// 空密码不计入错误次数:它只可能来自没收集到输入的客户端,
		// 计进去等于让一个前端 bug 把用户批量锁死。
		return errPayPwdRequired
	case !matched:
		// 锁定策略只在这一条分支上用得到,所以读它也放在这里:验密成功是绝大多数
		// 情况,让每一笔划转都白查一次 qy_settings 没有意义。
		until, err := noteFailure(ctx, gdb, userId, now, loadPolicy(ctx))
		if err != nil {
			// 计数写失败时仍然拒绝本次验密 —— 绝不能因为"记不下来"就放行。
			common.SysError("qianye/paypass: 记录支付密码错误次数失败(本次仍拒绝): " + err.Error())
			return errPayPwdWrong
		}
		if until > now {
			// 正是这一次触发了锁定,直接告诉用户,免得他继续试把锁越续越长。
			return errPayPwdLocked
		}
		return errPayPwdWrong
	}

	// 验密成功:清掉此前累积的错误次数。清不掉不影响本次放行(密码是对的),
	// 但要留痕 —— 计数不清零会让下一次输错更快触发锁定。
	if err := clearFailures(ctx, gdb, userId, now); err != nil {
		common.SysError("qianye/paypass: 验密成功后清理错误计数失败: " + err.Error())
	}
	return nil
}

// statusView 是 /pay-password/status 的响应,也是管理端查询的载体。
//
// 刻意不含任何哈希、盐或算法参数:这个结构体会被直接 c.JSON 出去。
type statusView struct {
	IsSet             bool  `json:"is_set"`
	Locked            bool  `json:"locked"`
	LockedUntil       int64 `json:"locked_until"`
	FailCount         int   `json:"fail_count"`
	RemainingAttempts int   `json:"remaining_attempts"`
	MaxAttempts       int   `json:"max_attempts"`
	LockMinutes       int   `json:"lock_minutes"`
	SetAt             int64 `json:"set_at"`
	ChangedAt         int64 `json:"changed_at"`
}

// loadStatus 汇总一个用户的支付密码状态。
func loadStatus(ctx context.Context, userId int) (*statusView, error) {
	gdb, err := handle(ctx)
	if err != nil {
		return nil, err
	}
	row, _, err := loadRow(ctx, gdb, userId)
	if err != nil {
		return nil, err
	}
	p := loadPolicy(ctx)
	now := common.GetTimestamp()

	// 锁定期已过的行按"未锁定、计数为 0"展示。这里刻意只改展示值不写库:
	// 状态查询是只读接口,真正的清零发生在下一次 verify 里(那条路径必然会走到)。
	locked := row.lockedAt(now)
	failCount := row.FailCount
	lockedUntil := row.LockedUntil
	if !locked {
		lockedUntil = 0
		if row.LockedUntil != 0 {
			failCount = 0
		}
	}
	remaining := p.MaxAttempts - failCount
	if remaining < 0 || locked {
		remaining = 0
	}
	return &statusView{
		IsSet:             row.isSet(),
		Locked:            locked,
		LockedUntil:       lockedUntil,
		FailCount:         failCount,
		RemainingAttempts: remaining,
		MaxAttempts:       p.MaxAttempts,
		LockMinutes:       p.LockMinutes,
		SetAt:             row.SetAt,
		ChangedAt:         row.ChangedAt,
	}, nil
}
