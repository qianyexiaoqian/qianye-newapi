package model

import (
	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// qy_aff_code.go —— 邀请码(users.aff_code)的唯一生成口。
//
// ─────────────────────────── 为什么要收敛 ───────────────────────────
//
// 上游把 `common.GetRandomString(4)` 原样抄在三个地方:
//
//	model.User.Insert          注册
//	model.User.InsertWithTx    OAuth 注册
//	controller.GetAffCode      老用户首次查看自己的邀请码时补发
//
// 位数是一个全站唯一的业务参数,散成三份拷贝就一定会漏改其中一份 ——
// 而漏改不会报任何错,只会让一部分用户拿到 4 位码、另一部分拿到 8 位码,
// 直到有人发现冲突为止。这次把三处收敛到本函数,下次改位数只改一行。
//
// ─────────────────────────── 为什么是 8 位 ───────────────────────────
//
// 字符集是 lo.AlphanumericCharset(0-9a-zA-Z,62 个字符)。
//
//	4 位  62^4 ≈ 1.5e7    —— 千级用户量下生日悖论已不可忽略
//	8 位  62^8 ≈ 2.2e14   —— 同等用户量下冲突概率降到 1e-9 量级
//
// 列宽不是约束:users.aff_code 是 varchar(32) + uniqueIndex,8 位绰绰有余。
//
// **存量邀请码不受影响**:本函数只在"生成新码"时被调用,不改写任何已有行。
// 老用户手里的 4 位码继续有效 —— GetUserIdByAffCode 是按值精确匹配的,
// 与长度无关。已经发出去的推广链接不会失效。
//
// ─────────────────────────── 为什么要探测重试 ───────────────────────────
//
// 改动前**没有任何重试**:随机撞上已存在的 aff_code 时,
// tx.Create(user) 直接被唯一索引拒绝,整个注册失败,用户看到一条数据库报错。
// 8 位之后这几乎不会发生,但"几乎不会发生"和"发生了就注册失败"是两回事,
// 而重试的成本只是一次索引命中的 COUNT。

const (
	// AffCodeLength 是新生成邀请码的长度。改这一个常量即可全站生效。
	AffCodeLength = 8

	// affCodeMaxAttempts 是撞码后的重生成次数上限。
	//
	// 单次冲突概率在 1e-9 量级,连续 5 次都撞上在物理上不会发生;
	// 这个上限存在的意义只是"绝不写出无界循环",不是真的指望它被用满。
	affCodeMaxAttempts = 5
)

// NewAffCode 生成一个当前未被占用的邀请码。
//
// tx 传本次写入所在的事务(注册路径),这样探测与插入看到的是同一份数据;
// 没有事务时传 nil,回落到全局 DB。
//
// 探测失败(数据库读不动)时直接返回当前候选码,不返回错误:
// 唯一索引才是最终权威,这里只是把冲突概率从"靠运气"降到"可忽略"。
// 为一次探测失败中断注册是本末倒置 —— 那才是真正会被用户看到的故障。
func NewAffCode(tx *gorm.DB) string {
	db := tx
	if db == nil {
		db = DB
	}

	var code string
	for range affCodeMaxAttempts {
		code = affCodeGenerator()

		var count int64
		if err := db.Model(&User{}).Where("aff_code = ?", code).Count(&count).Error; err != nil {
			return code
		}
		if count == 0 {
			return code
		}
	}
	return code
}

// affCodeGenerator 是随机码来源。
//
// 提成变量只为一件事:让"撞码后确实会重新生成"这条行为可测。
// 随机函数没法在测试里制造一次可控的冲突,而这条重试路径正是改动前缺失的那一段,
// 不可测就等于没有被验证过。生产路径分毫不改。
var affCodeGenerator = func() string { return common.GetRandomString(AffCodeLength) }
