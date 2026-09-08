package violation

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"
)

// 被拦截的请求在「使用记录」里的那一行。
//
// # 为什么必须有
//
// 项目方原话:「使用记录处,被阻断的 AI 审核(前置审核)应当显示在这里,让用户查阅」。
//
// 转发前拦截是唯一一种**在数据库里不留任何痕迹**的失败:violationBlockError 带着
// ErrOptionWithNoRecordErrorLog(那一条是必须的,否则违规拒绝会算进渠道错误统计
// 并触发渠道自动禁用),而请求根本没走到消费日志那一步。于是用户在使用记录页看到
// 的是一片空白 —— 他刚刚被拒的那一次调用像从未发生过,唯一的线索是 HTTP 响应体里
// 一句转瞬即逝的话。
//
// 违规记录页(userListRecords)确实有这一条,但那是**违规**视角:只有配了扣费或
// 计数的规则才让人想到去翻它,而"我的请求为什么失败了"这个问题,任何人第一个打开
// 的都是使用记录。
//
// # 两个来源共用这一行
//
//	规则命中被拦(handleHit)     每一次都写。
//	cyber 会话屏蔽(precheck)    **按会话节流**,一个屏蔽周期一行 ——
//	                            客户端不知道自己被本地拦了,重试往往是几十上百次。
//
// 两者写出来的行形状逐格相同(只有抬头与错误码分档),前端因此只认一种形状。
//
// # 一次拦截只写一行
//
// 扣到费的那条路径已经由 fee.go 的 writeConsumeLog 写了一行消费日志(type=2,
// 带真实扣费额),那一行同样标了 qy_violation_blocked。这里只补**没扣到费**的那半 ——
// 两边都写会让同一次请求在使用记录页出现两行,而用户会把它读成"扣了两次"。
// 判据是 rec.FeeQuota:writeConsumeLog 的调用点在 chargeFee 里,只有 Charged > 0
// 才走到,而 applyFeeToRecord 把同一个数抄进了 rec.FeeQuota。
//
// # 为什么是 type=5(错误)而不是 type=2(消费)
//
// 这一次请求**失败了**,而且没有产生任何消费。写成消费日志会给出一行 quota=0 的
// 假消费,它会被消费统计、日消费明细、佣金与星屑的重算逐个数进去(那几条链路都
// 按 type 过滤),而它根本不是一次消费。type=5 是上游给"这次调用失败了,原因是 X"
// 准备的位置,拦截正是其中一种。
//
// 刻意**不**看 constant.ErrorLogEnabled:那个开关管的是上游渠道错误(高频、噪音大、
// 默认关)。拦截日志是低频的、面向用户的,而且它是本功能唯一的用户可见出口 ——
// 把它挂在一个默认关闭的环境变量上,等于这个需求默认不生效。

// 拦截的两种来源,写进日志 other 的 violation_block_kind 供前端分档。
//
// 取值刻意是"这次拦的是什么"而不是内部阶段名(prompt / cyber_block):前端要回答的
// 是"给用户看哪一句话",而阶段名回答的是"代码走的哪条路径" —— 后者以后还会增加,
// 而每加一个前端都得跟着改一次。
const (
	blockKindContent = "content" // 内容审核:规则命中(词表 / 正则 / 频率 / AI 审核)
	blockKindSession = "session" // 会话屏蔽:cyber 拉黑,与这一次的内容无关
)

// recordBlockedUsageLog 给一次被拦截的请求写一行使用记录。
//
// rec 的每一次读取都发生在**本函数返回之前**,交给 worker 的闭包不再碰它:
// 调用点随后会把同一个指针交给 persist,那一侧的 GORM Create 会读遍全部字段
// 并回写 rec.Id。
func recordBlockedUsageLog(rec *Record) {
	row := blockedUsageLogRow(rec)
	if row == nil {
		return
	}
	ip := rec.Ip
	guard.HotAsync("violation.blocked_usage_log", func(ctx context.Context) error {
		// IP 是否入库由用户自己的设置决定,口径必须与 RecordConsumeLog /
		// RecordErrorLog 一致 —— 同一张表里对同一个用户给出两种口径是隐私事故,
		// 不只是不一致。读设置要碰缓存/库,所以放在 worker 里,不占 relay 线程。
		if model.QyUserRecordsIpInLog(row.UserId) {
			row.Ip = ip
		}
		// ── 这一行**永远**返回 nil ──
		//
		// guard.hotRunWithBudget 对返回的错误统一走 db.MarkFailure,而那记的是
		// **扩展库**的熔断计数。这次写入落在上游的 LOG_DB(logs 表)上,两者
		// 可以是完全不同的实例:把一次日志库故障算成扩展库故障,会把佣金、订单、
		// 违规记录那些真正的资金路径一起熔断掉,而它们此刻好好的。
		// (persistAIReviewCtx 用 MarkLogFailure 解决同一个问题,但那是千夜自己的
		// 台账库;上游 LOG_DB 在千夜这边没有对应的熔断器,所以只能记日志。)
		if err := model.QyCreateLog(ctx, row); err != nil {
			common.SysError("qianye/violation: 拦截使用记录写入失败: " + err.Error())
		}
		return nil
	})
}

// blockedUsageLogRow 组装那一行,返回 nil 表示这一次不该写。
//
// 独立成函数不是为了缩短上面那个:它上面那层是 guard.HotAsync,测试环境里扩展库
// 不可用、队列作业根本不会执行,断言会永远为真(与 persistRecord 从 persist 里
// 提出来是同一条理由)。而这里要钉住的恰恰是最容易漂移的两件事 ——
// "一次拦截只写一行"的判据,以及 admin_info 与顶层字段的归属。
func blockedUsageLogRow(rec *Record) *model.Log {
	if rec == nil || rec.UserId <= 0 {
		return nil
	}
	// 扣到费的那一次已经有消费日志了,不再补第二行。
	if rec.FeeQuota > 0 {
		return nil
	}
	// 原因取**用户在 API 上真正收到的那句话**,而不是规则的对外原因。
	//
	// 这一页回答的是"我这次为什么失败",而用户手上唯一的线索就是响应体里那句。
	// 两边给出不同的话,他没法把两者对上 —— 2026-09-07 报上来的正是这个:
	// 审核渠道上配了「你搁这做啥呢?」,API 照着回了,而这里显示的是写死的兜底句。
	//
	// 回落到 PublicReason 是给 cyber 会话屏蔽那条路留的:它自己拼 Record、
	// 不经 newRecord,所以 BlockedReason 恒空(见 cyberBlockLogRecord)。
	reason := rec.BlockedReason
	if reason == "" {
		reason = rec.PublicReason
	}
	if reason == "" {
		reason = defaultBlockMessage
	}
	// 抬头分两档。cyber 会话屏蔽认的是**上游的拒绝码**、拦的是整条会话,
	// 与"这段内容违规"是两回事;沿用同一句抬头会让用户把一次会话级屏蔽
	// 读成"我刚才那句话被判违规了",然后照着改内容,而改内容没有任何用
	// —— 他要做的是开一条新会话。
	head := "请求被内容审核拦截"
	code := violationErrorCode()
	// kind 是给**前端**分档用的那一位。前端不能改读 admin_info 里的 phase:
	// 那一块会被 formatUserLogs 替普通用户删掉,于是同一行在管理端与用户端会
	// 长出两个标题。也不能让前端去比对错误码字面量 —— 那等于把一个后端常量
	// 抄进前端,改一处忘一处时没有任何东西会红。
	kind := blockKindContent
	if rec.Phase == PhaseCyberBlock {
		head = "会话已被安全策略屏蔽"
		code = cyberBlockErrorCode
		kind = blockKindSession
	}
	content := head + ":" + reason
	if rec.CategoryPublicTitle != "" {
		content = head + "(" + rec.CategoryPublicTitle + "):" + reason
	}

	// 字段归属与 writeConsumeLog 逐条对齐(裁定 C34):用户应知的放 other 顶层,
	// 仅管理员可见的放 admin_info —— model/log.go 的 formatUserLogs 会为普通用户
	// 删掉 admin_info。两处若各写各的,同一次拦截在管理端与用户端会长成两个样子。
	other := model.NewLogOther()
	other.MergePublic(map[string]any{
		"violation_blocked": true,
		// 刻意**不是** violation_fee_code:那个键是使用记录页判定"这是一条违规
		// 扣费日志"的判据(isViolationFeeLog),而这一行恰恰是没扣到费的那一半。
		// 借用它会让详情弹窗弹出一个「违规扣费 · 费用 0」的板块 ——
		// 一句用户读起来只会更困惑的话。
		//
		// violation_code 与用户在 HTTP 响应里拿到的那个码逐字相同:他带着码来
		// 提工单时,运营要能在这一列上直接搜到。
		"violation_code":        code,
		"violation_block_kind":  kind,
		"qy_reason":             reason,
		"qy_violation_category": rec.CategoryPublicTitle,
	})
	// 记录号只在真有那条违规记录时才写。cyber 屏蔽期内的重复请求不产生
	// qy_violation_record(计数只在拉黑那一刻推进一次),写一个查不到的号
	// 比不写更糟 —— 用户拿它去申诉,运营查无此条。
	if rec.RecNo != "" {
		other.SetPublic("qy_violation_rec_no", rec.RecNo)
	}
	other.MergeAdmin(map[string]any{
		"qy_rule_id":       rec.RuleId,
		"qy_rule_name":     rec.RuleName,
		"qy_phase":         rec.Phase,
		"qy_matched_terms": rec.MatchedTerms,
	})

	row := &model.Log{
		UserId:    rec.UserId,
		Username:  rec.Username,
		CreatedAt: common.GetTimestamp(),
		Type:      model.LogTypeError,
		Content:   content,
		TokenName: rec.TokenName,
		TokenId:   rec.TokenId,
		ModelName: rec.ModelName,
		// Quota 恒为 0:这一行描述的是一次没有发生的调用。写进任何非零数都会被
		// 消费统计当成真实消费加进去(SumUsedQuota 在 type=0「全部类型」下不分类型求和)。
		Quota:     0,
		ChannelId: rec.ChannelId,
		Group:     rec.UsingGroup,
		RequestId: rec.RequestId,
		Other:     other.JSONString(),
	}
	return row
}
