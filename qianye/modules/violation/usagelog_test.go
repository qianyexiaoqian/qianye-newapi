package violation

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usagelog_test.go —— 「被拦截的请求要在使用记录页看得见」这条契约。
//
// 项目方原话:「使用记录处,被阻断的 AI 审核(前置审核)应当显示在这里,让用户查阅」。
//
// 转发前拦截此前在数据库里不留任何痕迹(violationBlockError 带
// ErrOptionWithNoRecordErrorLog,而请求根本没走到消费日志那一步),用户看到的是
// 一次凭空消失的调用。这里钉住的是补上来的那一行:写不写、写成什么类型、
// 哪些字段普通用户能看见。

// blockedRecord 造一条"已经被拦下"的违规记录,字段取值刻意互不相同 ——
// 断言里任何一个字段串错位置都会红。
func blockedRecord() *Record {
	return &Record{
		RecNo:               "vr_req-block-1_71",
		UserId:              4210,
		Username:            "u4210",
		TokenId:             55,
		TokenName:           "tk-live",
		RuleId:              71,
		RuleName:            "内部规则名·破限词表 v3",
		PublicReason:        "内容涉及越狱指令",
		CategoryId:          11,
		CategoryName:        "内部代号 jailbreak_v3",
		CategoryPublicTitle: "破限",
		Phase:               PhasePrompt,
		Action:              ActionBlock,
		Blocked:             true,
		ModelName:           "gpt-4o",
		UsingGroup:          "selfserve",
		ChannelId:           0,
		RequestId:           "req-block-1",
		Ip:                  "203.0.113.9",
		MatchedTerms:        "ai:jailbreak@0.93",
		Status:              RecordActive,
		FeeStatus:           FeeStatusNone,
		CreatedAt:           common.GetTimestamp(),
	}
}

// TestBlockedUsageLogRowIsWrittenOnlyOncePerRequest 守的是"一次拦截只写一行"。
//
// 扣到费的那条路径已经由 fee.go 的 writeConsumeLog 写了一行消费日志(带真实扣费额)。
// 两边都写会让同一次请求在使用记录页出现两行,而用户会把它读成"扣了两次"——
// 这是本功能最容易犯、也最难解释的一个错。
func TestBlockedUsageLogRowIsWrittenOnlyOncePerRequest(t *testing.T) {
	tests := []struct {
		name  string
		mutFn func(*Record)
		want  bool
		why   string
	}{
		{
			name: "拦下了、没扣到费 → 写",
			want: true,
			why:  "这正是此前一行痕迹都没有的那一档,补它就是本功能的全部目的",
		},
		{
			name:  "拦下了、也扣到了费 → 不写(消费日志已经有了)",
			mutFn: func(r *Record) { r.FeeQuota = 1500 },
			want:  false,
			why:   "writeConsumeLog 只在 Charged > 0 时才走到,同一个数被抄进了 FeeQuota",
		},
		{
			name:  "用户 id 缺失 → 不写",
			mutFn: func(r *Record) { r.UserId = 0 },
			want:  false,
			why:   "挂不到人的日志在使用记录页是查不出来的一行垃圾",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := blockedRecord()
			if tc.mutFn != nil {
				tc.mutFn(rec)
			}
			row := blockedUsageLogRow(rec)
			assert.Equal(t, tc.want, row != nil, tc.why)
		})
	}

	assert.Nil(t, blockedUsageLogRow(nil), "nil 记录不能 panic")
}

// TestBlockedUsageLogRowShapeIsUserFacing 钉住那一行的形状。
//
// 三件事各自都有独立的失败模式:
//
//   - type 必须是错误(5)。写成消费(2)会造出一行 quota=0 的假消费,
//     而日消费明细、佣金与星屑的重算全都按 type 过滤,它会被逐个数进去。
//   - quota 必须是 0。这一次调用没有发生,任何非零数都会进 SumUsedQuota 的总和。
//   - 规则内部名与命中词必须落在 admin_info 下。它们是"怎么绕过这条规则"的直接
//     线索,而 model/log.go 的 formatUserLogs 只会替普通用户删掉 admin_info ——
//     写在顶层就等于发给了每一个被拦的人。
func TestBlockedUsageLogRowShapeIsUserFacing(t *testing.T) {
	rec := blockedRecord()
	row := blockedUsageLogRow(rec)
	require.NotNil(t, row)

	assert.Equal(t, model.LogTypeError, row.Type, "被拦截的请求是一次失败,不是一次消费")
	assert.Equal(t, 0, row.Quota, "没有发生的调用不能带任何消费额")
	assert.Equal(t, rec.UserId, row.UserId)
	assert.Equal(t, rec.RequestId, row.RequestId, "request_id 是与违规记录对账的唯一钥匙")
	assert.Equal(t, rec.ModelName, row.ModelName, "少了模型名,使用记录页按模型筛就漏掉这一行")
	assert.Equal(t, rec.TokenName, row.TokenName)
	assert.Equal(t, rec.UsingGroup, row.Group)
	assert.Empty(t, row.Ip, "IP 由用户设置决定,组装阶段一律留空")

	assert.Contains(t, row.Content, "破限", "对外文案要说清是哪一类")
	assert.Contains(t, row.Content, "内容涉及越狱指令", "对外文案要说清原因")
	assert.NotContains(t, row.Content, "内部规则名", "内部规则名绝不能进用户可见的 content")
	assert.NotContains(t, row.Content, "内部代号", "类型内部名同理")

	var payload map[string]any
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &payload))

	assert.Equal(t, true, payload["violation_blocked"], "前端靠这个键把拦截与普通错误分开")
	assert.Equal(t, blockKindContent, payload["violation_block_kind"],
		"前端靠它在「内容审核拦截」与「会话屏蔽」两句抬头之间分档;"+
			"它不能改读 admin_info 里的 phase —— 那一块对普通用户会被剥掉")
	assert.Equal(t, violationErrorCode(), payload["violation_code"],
		"用户在响应里看到的错误码要能在日志里对上")
	assert.NotContains(t, payload, "violation_fee_code",
		"这是使用记录页判定「违规扣费日志」的键(isViolationFeeLog)。"+
			"这一行恰恰是没扣到费的那一半,借用它会弹出一个「违规扣费 · 费用 0」的板块")
	assert.Equal(t, rec.RecNo, payload["qy_violation_rec_no"])
	assert.Equal(t, rec.PublicReason, payload["qy_reason"])
	assert.Equal(t, rec.CategoryPublicTitle, payload["qy_violation_category"])

	adminInfo, ok := payload["admin_info"].(map[string]any)
	require.True(t, ok, "规则内部名与命中词必须整体落在 admin_info 下,否则不会被 formatUserLogs 删掉")
	assert.Equal(t, rec.RuleName, adminInfo["qy_rule_name"])
	assert.Equal(t, rec.MatchedTerms, adminInfo["qy_matched_terms"])
	assert.Equal(t, rec.Phase, adminInfo["qy_phase"])

	// 反向:内部字符串一个都不该出现在顶层。逐键断言会漏掉将来新加的键,
	// 所以这里直接搜整份序列化结果去掉 admin_info 之后的部分。
	delete(payload, "admin_info")
	rest := common.MapToJsonStr(payload)
	assert.NotContains(t, rest, "内部规则名")
	assert.NotContains(t, rest, "ai:jailbreak", "命中词是规则库的直接线索")
}

// TestBlockedUsageLogFallsBackToTheDefaultMessage 覆盖"规则没填对外文案"这一档。
//
// 没填时必须给出与拦截响应体同一句话。留空会写出一行结尾是冒号的日志,
// 而那一行恰恰是用户唯一能看到的解释。
func TestBlockedUsageLogFallsBackToTheDefaultMessage(t *testing.T) {
	rec := blockedRecord()
	rec.PublicReason = ""
	rec.CategoryPublicTitle = ""

	row := blockedUsageLogRow(rec)
	require.NotNil(t, row)
	assert.Contains(t, row.Content, defaultBlockMessage)
}

// TestBlockedUsageLogShowsWhatTheClientWasActuallyTold 是 2026-09-07 那份回归。
//
// 现象:审核渠道上配了「你搁这做啥呢?」,API 照着回了,而使用记录详情里显示的
// 仍是写死的兜底句「您的请求因违反内容策略被拒绝」。两条链路此前互不相干 ——
// 响应体走 Rule.BlockMessage(可被渠道覆盖),日志走 Rule.PublicReason。
//
// 使用记录恰恰是用户查"我为什么失败"的第一站,而他手上唯一的线索是响应体里
// 那句话。对不上就等于这一页没用。
func TestBlockedUsageLogShowsWhatTheClientWasActuallyTold(t *testing.T) {
	const channelMsg = "你搁这做啥呢?"
	// 渠道文案压过规则的两格文案 —— 与 violationBlockError 同一个优先级。
	cr := &compiledRule{R: Rule{
		Id: 71, Action: ActionBlock,
		PublicReason: "内容涉及越狱指令",
		BlockMessage: "规则自己那一句",
	}}
	v := &verdict{Rule: cr, BlockOverride: channelMsg}

	// 契约的两端:客户端收到的,与记录里冻结的,必须是同一句。
	assert.Equal(t, channelMsg, clientBlockMessage(cr, v.BlockOverride))
	assert.Equal(t, channelMsg, blockedReasonOf(v, true))

	rec := blockedRecord()
	rec.BlockedReason = blockedReasonOf(v, true)
	row := blockedUsageLogRow(rec)
	require.NotNil(t, row)
	assert.Contains(t, row.Content, channelMsg)
	assert.NotContains(t, row.Content, defaultBlockMessage,
		"配了渠道文案之后,兜底那句不该再出现")
	assert.NotContains(t, row.Content, rec.PublicReason,
		"渠道文案在场时,规则的对外原因不参与")

	payload := map[string]any{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &payload))
	assert.Equal(t, channelMsg, payload["qy_reason"],
		"详情弹窗的「原因」读的就是这一格")
}

// TestBlockedReasonFallsBackWhenNothingIsConfigured 钉住优先级的另外三档。
//
// 尤其是最后一档:没拦的那些(纯扣费规则、影子模式)客户端**什么都没被告知**,
// 给它们编一句"回给客户端的话"会让扣费日志把一句从未发出过的拦截文案写成扣费原因。
func TestBlockedReasonFallsBackWhenNothingIsConfigured(t *testing.T) {
	tests := []struct {
		name     string
		rule     Rule
		override string
		blocked  bool
		want     string
	}{
		{"渠道优先", Rule{PublicReason: "p", BlockMessage: "r"}, "ch", true, "ch"},
		{"渠道留空则用规则拦截文案", Rule{PublicReason: "p", BlockMessage: "r"}, "", true, "r"},
		{"渠道只有空白等于没配", Rule{BlockMessage: "r"}, "   ", true, "r"},
		{"两格都空则用内置兜底", Rule{PublicReason: "p"}, "", true, defaultBlockMessage},
		{"没拦就不算", Rule{BlockMessage: "r"}, "ch", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := &verdict{Rule: &compiledRule{R: tc.rule}, BlockOverride: tc.override}
			assert.Equal(t, tc.want, blockedReasonOf(v, tc.blocked))
		})
	}
}

// TestCyberSessionBlockKeepsItsOwnReason 守的是回落那一档。
//
// cyber 会话屏蔽自己拼 Record、不经 newRecord,所以 BlockedReason 恒空。
// 它必须继续走 PublicReason —— 否则那条路的日志会退化成内置兜底句,
// 而"此会话已被安全策略屏蔽"与"内容违规"是两件事:用户照着后者去改内容,
// 改了也没用,他要做的是开一条新会话。
func TestCyberSessionBlockKeepsItsOwnReason(t *testing.T) {
	rec := blockedRecord()
	rec.BlockedReason = ""
	rec.Phase = PhaseCyberBlock
	rec.PublicReason = cyberBlockPublicReason

	row := blockedUsageLogRow(rec)
	require.NotNil(t, row)
	assert.Contains(t, row.Content, cyberBlockPublicReason)
	assert.NotContains(t, row.Content, defaultBlockMessage)
}
