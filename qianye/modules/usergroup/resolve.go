package usergroup

import (
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// resolveNewUserGroup 是 model.QyResolveNewUserGroup 的实现。
//
// 它跑在 model.User.prepareForInsert 里,也就是用户创建的主库事务内部。
// 三条硬性纪律:
//
//  1. 入参非空即原样返回 —— 调用方已经显式决定了分组,扩展不越权覆盖。
//  2. 未配置即原样返回空串 —— users.group 保持零值,由数据库默认值兜底成
//     default,与接入本 hook 之前逐字一致。
//  3. 任何不确定都原样返回 —— 读不到扩展库、目标分组已不存在,都退回上游
//     行为。宁可让新用户落进 default,也不能让注册失败或落进一个用不了的分组。
func resolveNewUserGroup(current string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}

	configured := currentDefaultGroup()
	if configured == "" {
		return current
	}

	// 保存时已经校验过一次,这里必须再校验一次:分组不是实体表,运营完全可能
	// 在配置之后把它从倍率表里删掉,而删除那一侧没有任何东西会回头来清理这条
	// 配置。少了这一道,一次分组重命名就会让此后所有新用户拿到一个不存在的
	// 分组 —— 注册成功、充值成功、一个模型都调不通。
	if !groupExists(configured) {
		warnStaleGroup(configured)
		return current
	}
	return configured
}

// warnStaleGroup 对「配置的默认分组已不存在」限频告警。
//
// 限频而不是每次都打:一旦发生,此后每一次注册都会命中,不限频会把日志刷爆,
// 反而把真正需要看见的那一条冲掉。
var (
	staleWarnMu sync.Mutex
	staleWarnAt int64
)

const staleWarnEverySeconds = 300

func warnStaleGroup(group string) {
	now := common.GetTimestamp()
	staleWarnMu.Lock()
	if staleWarnAt > 0 && now-staleWarnAt < staleWarnEverySeconds {
		staleWarnMu.Unlock()
		return
	}
	staleWarnAt = now
	staleWarnMu.Unlock()

	common.SysError("qianye/usergroup: 配置的新用户默认分组 " + group +
		" 在分组倍率表中已不存在,新用户已回落到上游默认分组 " + upstreamDefaultGroup +
		" —— 请到管理端重新选择,或把该分组加回「模型分组定价」的分组表")
}
