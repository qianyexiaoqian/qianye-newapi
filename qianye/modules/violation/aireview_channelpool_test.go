package violation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_channelpool_test.go —— 「一档指定一组渠道 + 轮询/随机」这件事。
//
// 这一格原来只能填**一个**渠道 id,于是"只发给我机房里的这两台"表达不了:
// 要么退回全部启用渠道(把云端厂商一起放进来),要么把全部流量压在一台上。
// 现在它是一组 id 加一个分发方式,而下面钉住的正是这两件事各自的边界:
//
//	清单  它是**约束**:清单外的端点一次都不该收到用户内容(除非显式开了转移)。
//	方式  轮询是**均分**:同规格的几台护栏机上,随机的方差会让某一台连吃几倍。
//
// 两者失效时都没有任何症状 —— 审核照跑、结论照回、花销照付,只有统计看得出来。

// TestPickAIChannelsSubsetIsAConstraint 钉住"清单是约束,不是偏好"。
func TestPickAIChannelsSubsetIsAConstraint(t *testing.T) {
	rt := rtWith(
		chRT(1, "自建甲", "https://a.invalid/v1", 1),
		chRT(2, "自建乙", "https://b.invalid/v1", 1),
		chRT(3, "云端厂商", "https://c.invalid/v1", 50),
	)

	t.Run("转移关着:云端厂商一次都不出现,哪怕它的权重是另外两个的 50 倍", func(t *testing.T) {
		// 摇 64 次:这一条挡的是"把清单当权重加成"那一类实现,单次通过说明不了
		// 任何事 —— 而它漏掉的后果是用户内容以某个概率出境到没人授权的地方。
		for i := 0; i < 64; i++ {
			got := pickAIChannels(rt, &aiScopeRT{Id: 7, ChannelIds: []int64{1, 2}})
			require.Len(t, got, 2, "第 %d 次:链长应当就是清单里可用的那几个", i)
			for _, ch := range got {
				assert.NotEqual(t, int64(3), ch.Id,
					"第 %d 次:清单外的端点收到了用户内容", i)
			}
		}
	})

	t.Run("清单里坏了一个:剩下的照常发,这一档不算失效", func(t *testing.T) {
		got := pickAIChannels(rt, &aiScopeRT{Id: 7, ChannelIds: []int64{1, 404}})
		require.Len(t, got, 1, "指定一组而不是一个,收益正是这里")
		assert.Equal(t, int64(1), got[0].Id)
	})

	t.Run("清单里全坏了 + 转移关着:一个都不返回", func(t *testing.T) {
		got := pickAIChannels(rt, &aiScopeRT{Id: 7, ChannelIds: []int64{404, 405}})
		assert.Empty(t, got,
			"回落会把内容发去运营明确没有选的端点 —— 而那往往正是指定它们的全部理由")
	})

	t.Run("转移开着:清单排在前面,后面才轮到清单外的补位", func(t *testing.T) {
		got := pickAIChannels(rt, &aiScopeRT{
			Id: 7, ChannelIds: []int64{1, 2}, ChannelMode: AIChannelModeRoundRobin,
			ChannelFailover: true,
		})
		require.Len(t, got, maxAIAttempts)
		assert.ElementsMatch(t, []int64{1, 2}, []int64{got[0].Id, got[1].Id},
			"「指定」的含义仍然是优先它们,而不是与其余渠道平起平坐")
		assert.Equal(t, int64(3), got[2].Id, "补位来自清单之外")
	})
}

// TestPickAIChannelsRoundRobinSpreadsEvenly 钉住轮询真的在轮。
//
// # 为什么不能只断言"结果都在清单里"
//
// 那条断言在加权随机下也成立。轮询这一档买的**只有**均分:一台同规格的护栏机
// 在随机下会在某一分钟里连吃几倍的量,而小模型机的并发很浅。所以这里断言的是
// 分布本身 —— 每台恰好拿到 n/len 次,一次不多一次不少。
func TestPickAIChannelsRoundRobinSpreadsEvenly(t *testing.T) {
	rt := rtWith(
		chRT(1, "护栏甲", "https://a.invalid/v1", 9), // 权重故意悬殊:轮询不看它
		chRT(2, "护栏乙", "https://b.invalid/v1", 1),
		chRT(3, "护栏丙", "https://c.invalid/v1", 1),
	)
	sc := &aiScopeRT{Id: 42, ChannelIds: []int64{1, 2, 3}, ChannelMode: AIChannelModeRoundRobin}

	firsts := map[int64]int{}
	const rounds = 30
	for i := 0; i < rounds; i++ {
		got := pickAIChannels(rt, sc)
		require.Len(t, got, maxAIAttempts)
		firsts[got[0].Id]++
		// 一条链里不能有重复:轮询是"从起点开始依次取",取回自己等于把一次
		// 重试浪费在刚刚失败的那台上。
		assert.NotEqual(t, got[0].Id, got[1].Id)
		assert.NotEqual(t, got[1].Id, got[2].Id)
	}
	require.Len(t, firsts, 3, "三台都要轮到")
	for id, n := range firsts {
		assert.Equal(t, rounds/3, n,
			"渠道 %d 拿到 %d 次起点,轮询的字面承诺是每台一样多(权重不参与)", id, n)
	}
}

// TestRoundRobinCursorSurvivesSnapshotReload 钉住游标不跟着快照归零。
//
// 游标放在 aiScopeRT 上是最自然的写法,而它错得很隐蔽:快照每次重载都重建那个
// 结构体,于是"改一次配置 = 轮询从头再来"。在一个每天改几次配置的站点上,
// 清单里的第一台会永远拿到最多的量,而界面上写着"轮询"。
func TestRoundRobinCursorSurvivesSnapshotReload(t *testing.T) {
	rt := rtWith(
		chRT(1, "甲", "https://a.invalid/v1", 1),
		chRT(2, "乙", "https://b.invalid/v1", 1),
	)
	newScope := func() *aiScopeRT {
		// 每次都是一个**新的**结构体,模拟快照重载。
		return &aiScopeRT{Id: 4242, ChannelIds: []int64{1, 2}, ChannelMode: AIChannelModeRoundRobin}
	}
	first := pickAIChannels(rt, newScope())[0].Id
	second := pickAIChannels(rt, newScope())[0].Id
	assert.NotEqual(t, first, second,
		"重建结构体之后仍然要接着上一次的位置轮 —— 归零等于第一台永远多吃一份")
}

// TestAIChannelIdsRoundTrip 钉住这一列在库里与内存之间的往返。
//
// 脏值必须被**跳过而不是报错**:一个解析不了的 id 让整行读失败,表现是这一页
// 整体 500,而这一列坏掉的正确后果只是"这一档指定的渠道少了一个"。
func TestAIChannelIdsRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want AIChannelIds
	}{
		{"空串 = 不指定", "", nil},
		{"NULL = 不指定(存量行可能是它)", nil, nil},
		{"单个 id", "7", AIChannelIds{7}},
		{"多个 id,顺序原样(轮询按它转)", "3,1,2", AIChannelIds{3, 1, 2}},
		{"两侧空白与空段都容忍", " 3 , ,1, ", AIChannelIds{3, 1}},
		{"驱动给 []byte 也认(MySQL 常见)", []byte("5,6"), AIChannelIds{5, 6}},
		{"脏值跳过而不是整行读失败", "3,abc,-1,0,4", AIChannelIds{3, 4}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ids AIChannelIds
			require.NoError(t, ids.Scan(tc.raw))
			assert.Equal(t, tc.want, ids)
		})
	}

	t.Run("Value 写回逗号分隔,空清单写空串而不是 NULL", func(t *testing.T) {
		v, err := AIChannelIds(nil).Value()
		require.NoError(t, err)
		assert.Equal(t, "", v, "列上有 not null,写 NULL 会直接失败")

		v, err = AIChannelIds{3, 1, 2}.Value()
		require.NoError(t, err)
		assert.Equal(t, "3,1,2", v)
	})
}

// TestAIScopeChannelIdsSerializeAsEmptyArray 钉住"没指定渠道"下发的是 `[]`。
//
// nil 切片序列化出来是 `null`,而界面拿到它之后第一件事是 `channel_ids.filter`
// —— 一次 TypeError。AI 审核那一页四张卡共用路由层的错误边界,于是「这一档
// 没指定渠道」这种最普通的配置会把**整页**打成白屏,而白屏时连"作用域读不出来"
// 这句话都显示不了。与 nil_array_json_test.go 盯的是同一个缺陷形状。
//
// 断言必须落在**序列化之后的 JSON** 上:nil 切片的 len 也是 0,
// assert.Len(ids, 0) 对 nil 与空切片一视同仁地通过,把修复回滚照样全绿。
func TestAIScopeChannelIdsSerializeAsEmptyArray(t *testing.T) {
	t.Run("策略行回显", func(t *testing.T) {
		raw, err := common.Marshal(AIScope{Id: 1, Name: "没指定渠道"})
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"channel_ids":[]`)
		assert.NotContains(t, string(raw), `"channel_ids":null`)
	})

	t.Run("汇总表", func(t *testing.T) {
		raw, err := common.Marshal(summarizeAIScopes([]AIScope{{Id: 1, Name: "没指定渠道"}}))
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"channel_ids":[]`)
		assert.NotContains(t, string(raw), `"channel_ids":null`)
	})

	t.Run("有值时照常是数组", func(t *testing.T) {
		raw, err := common.Marshal(AIScope{Id: 1, ChannelIds: AIChannelIds{3, 1}})
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"channel_ids":[3,1]`,
			"顺序也要一字不差:轮询按它转")
	})
}

// TestMigrateAIScopeChannelIdsMovesTheConstraint 钉住存量的单渠道约束搬过去了。
//
// 两列的零值指向**相反**的行为:legacy 列为 0 与新列为空都写作"不指定",
// 而"不指定"的含义是「在全部启用渠道之间分发」。没搬走的那几档因此会开始把
// 用户内容发给池子里的每一个渠道 —— 一次没有任何症状的数据出境扩大。
func TestMigrateAIScopeChannelIdsMovesTheConstraint(t *testing.T) {
	gdb := newAIWiringDB(t)
	table := AIScope{}.TableName()
	// AutoMigrate 建出来的表已经没有 legacy 列了(结构体上没有它),
	// 手工加回来,才是"从旧版本升上来"的那种库。
	require.NoError(t, gdb.Exec(
		"ALTER TABLE "+table+" ADD COLUMN "+legacyAIScopeChannelColumn+
			" integer NOT NULL DEFAULT 0").Error)
	require.NoError(t, gdb.Create(&AIScope{
		Id: 1, Name: "内部对接", Enabled: true, GroupScope: "internal",
		GroupScopeMode: GroupScopeInclude, AsyncSampleRateBps: 1000,
	}).Error)
	require.NoError(t, gdb.Create(&AIScope{
		Id: 2, Name: "自助注册", Enabled: true, GroupScope: "selfserve",
		GroupScopeMode: GroupScopeInclude, AsyncSampleRateBps: 1000,
	}).Error)
	require.NoError(t, gdb.Exec(
		"UPDATE "+table+" SET "+legacyAIScopeChannelColumn+" = 9 WHERE id = 1").Error)

	moved, err := migrateAIScopeChannelIds(context.Background(), gdb)
	require.NoError(t, err)
	assert.EqualValues(t, 1, moved, "只有真的指定过渠道的那一档要搬")

	var rows []AIScope
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, AIChannelIds{9}, rows[0].ChannelIds,
		"存量的「只发给这一个」必须原样活下来")
	assert.Empty(t, rows[1].ChannelIds, "本来就没指定的那一档不该被改写")

	t.Run("幂等:再跑一次不动任何东西", func(t *testing.T) {
		require.NoError(t, gdb.Model(&AIScope{}).Where("id = ?", 1).
			Update("channel_ids", "9,10").Error)
		moved, err := migrateAIScopeChannelIds(context.Background(), gdb)
		require.NoError(t, err)
		assert.EqualValues(t, 0, moved)
		var row AIScope
		require.NoError(t, gdb.Where("id = ?", 1).Take(&row).Error)
		assert.Equal(t, AIChannelIds{9, 10}, row.ChannelIds,
			"搬过之后再跑一次不能把后来加的第二个渠道盖掉")
	})

	t.Run("列删掉之后是 no-op", func(t *testing.T) {
		dropped, err := dropLegacyAIScopeChannelColumn(context.Background(), gdb)
		require.NoError(t, err)
		assert.True(t, dropped)
		moved, err := migrateAIScopeChannelIds(context.Background(), gdb)
		require.NoError(t, err)
		assert.EqualValues(t, 0, moved)
	})
}

// TestUpsertAIScopeRejectsMissingChannelList 钉住"旧页面不能悄悄清空清单"。
//
// 一次不带 channel_ids 的提交,配上一条已经指定了渠道的策略,只可能来自一个
// 这一格存在之前的页面。当成"清空"会把「只发给自建端点」改成「发给全部启用
// 渠道」,而提交者根本不知道自己改了这件事 —— 用户内容的出境目的地在一次改
// 抽样率的保存里悄悄变宽。真想清空的新页面发的是空数组,与缺席分得开。
func TestUpsertAIScopeRejectsMissingChannelList(t *testing.T) {
	base := `{"id":5,"name":"内部对接","enabled":true,"priority":100,` +
		`"group_scope":"internal","group_scope_mode":"include","channel_group":"自建护栏",` +
		`"pre_sample_rate_bps":0,"async_sample_rate_bps":1000`

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantIds    []int64
		why        string
	}{
		{
			name: "旧页面(字段缺席):400,库里那一格一个字节都不动",
			body: base + `}`, wantStatus: http.StatusBadRequest,
			wantIds: []int64{1},
			why:     "把缺席当成清空 = 一次没人按下过的数据出境扩大",
		},
		{
			name: "新页面显式清空(空数组):放行",
			body: base + `,"channel_ids":[]}`, wantStatus: http.StatusOK,
			wantIds: nil,
			why:     "空数组是一次显式选择,与「这个页面不认识这一格」不是一回事",
		},
		{
			name:       "旧页面带着单个 channel_id:按一个渠道的清单收下",
			body:       base + `,"channel_id":1}`,
			wantStatus: http.StatusOK, wantIds: []int64{1},
			why: "旧页面仍然能保存,只是表达不了多选",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newAIScopeChannelEnv(t)
			now := common.GetTimestamp()
			require.NoError(t, gdb.Create(&AIChannel{
				Id: 1, Name: "自建端点", BaseUrl: "https://a.invalid/v1", Model: "m",
				Weight: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
			}).Error)
			require.NoError(t, gdb.Create(&AIScope{
				Id: 5, Name: "内部对接", Enabled: true, Priority: 100,
				GroupScope: "internal", GroupScopeMode: GroupScopeInclude,
				AsyncSampleRateBps: 1000, ChannelIds: AIChannelIds{1},
				CreatedAt: now, UpdatedAt: now,
			}).Error)

			c, rec := aiScopeCtx(t, http.MethodPut, "/violation/ai-review/scopes", tc.body)
			adminUpsertAIScope(c)
			assert.Equal(t, tc.wantStatus, rec.Code, tc.why)

			var row AIScope
			require.NoError(t, gdb.Where("id = ?", 5).Take(&row).Error)
			assert.ElementsMatch(t, tc.wantIds, []int64(row.ChannelIds), tc.why)
		})
	}
}

// TestDeleteAIChannelSeesEveryMemberOfTheList 钉住引用检查看的是整张清单。
//
// 清单是 CSV,而 CSV 的包含匹配在三家数据库上写法各异:一句子串 LIKE 会把 13
// 当成 3(挡下一次完全正常的删除),而按整列相等只认单元素的那一档(把多渠道
// 清单里的引用整片漏掉,删掉之后那几档少一个渠道)。两个方向都在这里钉住。
func TestDeleteAIChannelSeesEveryMemberOfTheList(t *testing.T) {
	gdb := newAIScopeChannelEnv(t)
	now := common.GetTimestamp()
	for _, id := range []int64{3, 13} {
		require.NoError(t, gdb.Create(&AIChannel{
			Id: id, Name: "端点", BaseUrl: "https://a.invalid/v1",
			Model: "m", Weight: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
		}).Error)
	}
	require.NoError(t, gdb.Create(&AIScope{
		Id: 5, Name: "内部对接", Enabled: true, Priority: 100,
		GroupScope: "internal", GroupScopeMode: GroupScopeInclude,
		AsyncSampleRateBps: 1000, ChannelIds: AIChannelIds{1, 13},
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	del := func(id string) *httptest.ResponseRecorder {
		c, rec := aiScopeCtx(t, http.MethodDelete, "/violation/ai-review/channels/"+id, "")
		c.Params = gin.Params{{Key: "id", Value: id}}
		adminDeleteAIChannel(c)
		return rec
	}

	assert.Equal(t, http.StatusBadRequest, del("13").Code,
		"清单中间那一个也是引用 —— 只认单元素清单会把它整片漏掉")
	assert.Equal(t, http.StatusOK, del("3").Code,
		"13 里的 3 不是一次引用 —— 子串匹配会在这里挡下一次完全正常的删除")
}
