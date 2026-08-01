package paypass

import (
	"context"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
)

// settingScope 是支付密码参数所在的 qy_settings 命名空间。
//
// 刻意用 "transfer" 而不是 "paypass":裁决 5 把「支付密码的错误次数阈值与锁定
// 时长」划进了划转的运营参数集合,管理端是在划转配置页里改它们的。本模块只是
// 这两个键的**消费方**,键的定义与管理端写入归 settings 那一侧 ——
// 另起一个 scope 会让同一个概念出现第二份配置面,而"同一概念的第 N 份拷贝
// 各自漂移"是本仓最贵的那类缺陷。
const settingScope = "transfer"

const (
	keyMaxAttempts = "pay_pwd_max_attempts"
	keyLockMinutes = "pay_pwd_lock_minutes"
)

// 默认值与硬边界。
//
// # 为什么越界值是丢弃而不是钳取
//
// qy_settings 是可以被人手工 UPDATE 的表。一个被写坏的 max_attempts=999999
// 若被钳成 100,锁定阈值仍然高得等于没有;而丢弃只是回落到默认的 5,
// 损失有界且可解释。这与 commission 的 rateOverride 是同一条口径。
const (
	defaultMaxAttempts = 5
	minMaxAttempts     = 1
	maxMaxAttempts     = 100

	defaultLockMinutes = 30
	minLockMinutes     = 1
	// 7 天。再长就该走管理员解锁或邮箱找回,而不是让用户干等。
	maxLockMinutes = 7 * 24 * 60
)

// policy 是当前生效的锁定策略。
type policy struct {
	MaxAttempts int
	LockMinutes int
}

// lockSeconds 是锁定时长的秒数形式。
func (p policy) lockSeconds() int64 { return int64(p.LockMinutes) * 60 }

// loadPolicy 读出运营配置的锁定策略。
//
// # 为什么不加进程内缓存
//
// 本函数只在支付密码相关的 HTTP 请求里被调用,而那条路径上必然有一次 bcrypt
// (默认 cost 10,约几十毫秒)。相比之下一次两行的主键范围 SELECT 可以忽略。
// 加缓存换来的是"管理员改完阈值 N 秒内不生效"这个必然要被报成 bug 的窗口,
// 以及一份需要在管理端写入时失效的耦合 —— 而写入方在另一个模块里。
//
// 读不到时回落默认值而不是拒绝服务:默认值(5 次 / 30 分钟)比任何运营覆盖
// 都更严格的可能性很小,但它是**确定的**,不会因为扩展库抖动就变成"不限次数"。
func loadPolicy(ctx context.Context) policy {
	p := policy{MaxAttempts: defaultMaxAttempts, LockMinutes: defaultLockMinutes}
	gdb := db.Get()
	if gdb == nil {
		return p
	}
	var rows []qymodel.Setting
	err := gdb.WithContext(ctx).
		Where("scope = ? AND k IN ?", settingScope, []string{keyMaxAttempts, keyLockMinutes}).
		Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/paypass: 读取锁定策略失败,本次按默认值(" +
			strconv.Itoa(defaultMaxAttempts) + " 次 / " + strconv.Itoa(defaultLockMinutes) +
			" 分钟)执行: " + err.Error())
		return p
	}
	for _, r := range rows {
		switch r.K {
		case keyMaxAttempts:
			p.MaxAttempts = boundedInt(r.K, r.V, p.MaxAttempts, minMaxAttempts, maxMaxAttempts)
		case keyLockMinutes:
			p.LockMinutes = boundedInt(r.K, r.V, p.LockMinutes, minLockMinutes, maxLockMinutes)
		}
	}
	return p
}

// boundedInt 解析一个整数运营覆盖,越界或无法解析时回落 def 并告警。
func boundedInt(key, raw string, def, lo, hi int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		common.SysError("qianye/paypass: qy_settings 里的 " + key + " = " + strconv.Quote(raw) +
			" 非法或越界(允许 " + strconv.Itoa(lo) + "~" + strconv.Itoa(hi) +
			"),本次回落默认值 " + strconv.Itoa(def))
		return def
	}
	return v
}
