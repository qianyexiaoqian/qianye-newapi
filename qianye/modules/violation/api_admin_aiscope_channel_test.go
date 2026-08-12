package violation

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_aiscope_channel_test.go —— 「策略指定审核渠道」的两道写入闸。
//
// 两道闸挡的是同一种失效的两个入口:
//
//	上游  保存一条指向不存在 / 已停用渠道的策略
//	下游  删掉一个还被策略指着的渠道
//
// 两者的后果一模一样:那几档从此每一次都走 no_channel —— 不审核、直接放行,
// 而界面上它们看起来配得好好的。运行期**绝不**回落到随机池(那会把内容发去
// 运营明确没有选的端点),所以这件事只能在写入侧挡住。
//
// 必须走 handler:两道闸都只存在于 handler 里。测下层函数会得到一组全绿的
// 断言,而删除接口照样能把被指定的渠道删掉。

// newAIScopeChannelEnv 接上一个承载作用域、渠道与审计表的内存库。
func newAIScopeChannelEnv(t *testing.T) *gorm.DB {
	t.Helper()
	useTestConfig(t, "  enabled: true\n")
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	// 审计表一并建出来:两道闸都要求**失败也留痕**("我配了三次都没保存上"
	// 只能靠失败审计回答),表不存在时 audit.Write 只记一行日志就返回,
	// 那条断言会退化成假绿。
	require.NoError(t, gdb.AutoMigrate(
		&AIScope{}, &AIChannel{}, &AISetting{}, &Category{}, &qymodel.AuditLog{}))
	prevHandle := qyDBHandleForCtxTest.Swap(gdb)
	prevHealthy := qyDBHealthyForJSONTest.Swap(true)
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prevHandle)
		qyDBHealthyForJSONTest.Store(prevHealthy)
		_ = sqlDB.Close()
	})
	return gdb
}

func aiScopeCtx(t *testing.T, method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Set("id", 1)
	c.Set("username", "qy-admin")
	return c, rec
}

// TestUpsertAIScopeRejectsUnusableChannel 是上游那道闸。
func TestUpsertAIScopeRejectsUnusableChannel(t *testing.T) {
	tests := []struct {
		name       string
		channelId  int64
		wantStatus int
		wantMsg    string
		why        string
	}{
		{
			name: "不指定(0):放行,含义是按权重随机", channelId: 0,
			wantStatus: http.StatusOK,
			why:        "留空是默认取值,它不该需要任何渠道存在",
		},
		{
			name: "指定一个启用中的渠道:放行", channelId: 1,
			wantStatus: http.StatusOK,
		},
		{
			name: "指定一个停用的渠道:400", channelId: 2,
			wantStatus: http.StatusBadRequest, wantMsg: "停用",
			why: "停用的渠道不进快照,这一档会每次都走「无可用渠道」并直接放行 —— " +
				"而界面上它与正常配置长得一模一样",
		},
		{
			name: "指定一个不存在的渠道:400", channelId: 999,
			wantStatus: http.StatusBadRequest, wantMsg: "不存在",
			why: "同上,而且这一种连名字都 join 不出来",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newAIScopeChannelEnv(t)
			now := common.GetTimestamp()
			require.NoError(t, gdb.Create(&AIChannel{
				Id: 1, Name: "启用中", BaseUrl: "https://a.invalid/v1", Model: "m",
				Weight: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
			}).Error)
			require.NoError(t, gdb.Create(&AIChannel{
				Id: 2, Name: "停用的", BaseUrl: "https://b.invalid/v1", Model: "m",
				Weight: 1, Enabled: false, CreatedAt: now, UpdatedAt: now,
			}).Error)

			body := `{"name":"自助注册","enabled":true,"priority":100,` +
				`"group_scope":"selfserve","group_scope_mode":"include",` +
				`"pre_sample_rate_bps":0,"async_sample_rate_bps":1000,` +
				`"channel_id":` + strconv.FormatInt(tc.channelId, 10) + `}`
			c, rec := aiScopeCtx(t, http.MethodPut, "/violation/ai-review/scopes", body)
			adminUpsertAIScope(c)

			assert.Equal(t, tc.wantStatus, rec.Code, tc.why)
			if tc.wantMsg != "" {
				assert.Contains(t, rec.Body.String(), tc.wantMsg, tc.why)
			}

			var n int64
			require.NoError(t, gdb.Model(&AIScope{}).Count(&n).Error)
			if tc.wantStatus == http.StatusOK {
				assert.EqualValues(t, 1, n)
				var row AIScope
				require.NoError(t, gdb.Take(&row).Error)
				assert.Equal(t, tc.channelId, row.ChannelId,
					"指定的渠道必须真的落库 —— 落不下去等于静默回到加权随机")
				return
			}
			assert.EqualValues(t, 0, n, "被闸挡下的策略一行都不该落库")

			var audits []qymodel.AuditLog
			require.NoError(t, gdb.Find(&audits).Error)
			require.Len(t, audits, 1,
				"失败也要留痕:「我配了三次都没保存上」只能靠失败审计回答")
			assert.Equal(t, qymodel.ResultFail, audits[0].Result)
		})
	}
}

// TestDeleteAIChannelBlockedWhilePinned 是下游那道闸。
//
// 不做成"删除时自动把那几档改回不指定":那等于替运营决定"发给谁都行",
// 而指定渠道的理由往往正是"只能发给这一个"。
func TestDeleteAIChannelBlockedWhilePinned(t *testing.T) {
	gdb := newAIScopeChannelEnv(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&AIChannel{
		Id: 1, Name: "自建审核端点", BaseUrl: "https://a.invalid/v1", Model: "m",
		Weight: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&AIScope{
		Id: 5, Name: "内部对接", Enabled: true, Priority: 100,
		GroupScope: "internal", GroupScopeMode: GroupScopeInclude,
		AsyncSampleRateBps: 1000, ChannelId: 1, CreatedAt: now, UpdatedAt: now,
	}).Error)

	del := func(t *testing.T) *httptest.ResponseRecorder {
		t.Helper()
		c, rec := aiScopeCtx(t, http.MethodDelete, "/violation/ai-review/channels/1", "")
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		adminDeleteAIChannel(c)
		return rec
	}

	rec := del(t)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"删掉被指定的渠道会让那一档静默停止审核 —— 这是本模块最典型的失效形状")
	assert.Contains(t, rec.Body.String(), "内部对接",
		"报错必须点名是哪几条策略在指着它,否则运营无从下手")

	var n int64
	require.NoError(t, gdb.Model(&AIChannel{}).Count(&n).Error)
	assert.EqualValues(t, 1, n, "被挡下的删除一行都不该落")

	t.Run("那几档改回不指定之后就删得掉了", func(t *testing.T) {
		require.NoError(t, gdb.Model(&AIScope{}).Where("id = ?", 5).
			Update("channel_id", 0).Error)
		rec := del(t)
		assert.Equal(t, http.StatusOK, rec.Code)
		var n int64
		require.NoError(t, gdb.Model(&AIChannel{}).Count(&n).Error)
		assert.EqualValues(t, 0, n)
	})
}

// TestDisablingAScopeIsNeverBlockedByItsBrokenReferences 钉的是止损动作本身。
//
// # 这道闸曾经把唯一的出路一起焊死
//
// 两道引用闸(类型必须活着、渠道必须存在且启用)原本对**任何**提交生效,
// 包括 `enabled:false` 那一次。于是这条路一定会被走到:
//
//	停掉一个坏渠道(渠道停用侧没有对称的闸,这是被支持的日常动作)
//	  → 列表把指着它的那几档标成「渠道不可用」并写出后果
//	  → 管理员按提示去关掉那一档
//	  → 400,关不掉
//
// 而列表上的一键启停走的正是这个 upsert。除了关不掉,那一档的改名、调抽样率、
// 改分组也一并动不了,唯一被接受的保存是把这一格改回「不指定」—— 那是一次
// 含义完全不同的永久变更(指定渠道的理由往往正是"只能发给这一个")。
//
// 停用的档一次都不会被 scopeFor 看到,拦它挡不住任何静默失效:这道闸在
// enabled=false 这一支上没有收益,只有代价。
//
// 反向那一半同样重要,所以两个方向在同一张表里:重新启用时闸必须照样拦得住,
// 否则这个修复就变成了"把闸拆了"。
func TestDisablingAScopeIsNeverBlockedByItsBrokenReferences(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		channelId  int64
		categoryId int64
		wantStatus int
		wantMsg    string
		why        string
	}{
		{
			name:    "停用一档指着「已停用渠道」的策略:放行",
			enabled: false, channelId: 2,
			wantStatus: http.StatusOK,
			why: "这正是界面提示管理员去做的止损动作 —— " +
				"挡住它等于让那一档既不审核、又关不掉",
		},
		{
			name:    "停用一档指着「已归档类型」的策略:放行",
			enabled: false, categoryId: 777,
			wantStatus: http.StatusOK,
			why: "归档类型是被支持的日常动作(adminArchiveCategory 只接管规则、" +
				"不管 AI 作用域),归档完之后指着它的那一档必须还能关掉",
		},
		{
			name:    "停用时顺手改名 / 调抽样率:一并放行",
			enabled: false, channelId: 2,
			wantStatus: http.StatusOK,
			why:        "被闸拦住的不只是那一格,是整条策略的任何一次保存",
		},
		{
			name:    "把它重新启用起来:照样 400",
			enabled: true, channelId: 2,
			wantStatus: http.StatusBadRequest, wantMsg: "停用",
			why: "闸没有被拆掉,只是挪到了它真正开始生效的那一刻 —— " +
				"启用一档指向坏渠道的策略仍然是静默失效",
		},
		{
			name:    "重新启用一档指着已归档类型的策略:照样 400",
			enabled: true, categoryId: 777,
			wantStatus: http.StatusBadRequest, wantMsg: "不存在或已归档",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newAIScopeChannelEnv(t)
			now := common.GetTimestamp()
			// 渠道 2 是"管理员刚刚停掉的那个坏端点"。
			require.NoError(t, gdb.Create(&AIChannel{
				Id: 2, Name: "坏掉的端点", BaseUrl: "https://b.invalid/v1", Model: "m",
				Weight: 1, Enabled: false, CreatedAt: now, UpdatedAt: now,
			}).Error)
			// 类型 777 从来没建过 —— 与"已归档"在软删作用域下等价。
			require.NoError(t, gdb.Create(&AIScope{
				Id: 5, Name: "内部对接", Enabled: true, Priority: 100,
				GroupScope: "internal", GroupScopeMode: GroupScopeInclude,
				AsyncSampleRateBps: 1000,
				ChannelId:          tc.channelId, CategoryId: tc.categoryId,
				CreatedAt: now, UpdatedAt: now,
			}).Error)

			body := `{"id":5,"name":"内部对接","enabled":` +
				strconv.FormatBool(tc.enabled) + `,"priority":100,` +
				`"group_scope":"internal","group_scope_mode":"include",` +
				`"pre_sample_rate_bps":0,"async_sample_rate_bps":1000,` +
				`"channel_id":` + strconv.FormatInt(tc.channelId, 10) + `,` +
				`"category_id":` + strconv.FormatInt(tc.categoryId, 10) + `}`
			c, rec := aiScopeCtx(t, http.MethodPut, "/violation/ai-review/scopes", body)
			adminUpsertAIScope(c)

			assert.Equal(t, tc.wantStatus, rec.Code, tc.why)
			if tc.wantMsg != "" {
				assert.Contains(t, rec.Body.String(), tc.wantMsg, tc.why)
			}

			// 断言的是**库里那一行到底关掉了没有**,不是 HTTP 码。
			// 200 而没落库是这个缺陷最坏的一种形状:管理员以为关掉了。
			var row AIScope
			require.NoError(t, gdb.Where("id = ?", 5).Take(&row).Error)
			if tc.wantStatus == http.StatusOK {
				assert.False(t, row.Enabled,
					"止损动作必须真的落库 —— 回 200 而那一档还开着是更坏的一种失败")
				assert.Equal(t, tc.channelId, row.ChannelId,
					"关掉它不该顺手改写那一格:指定渠道是运营的显式选择,"+
						"「发给谁都行」得由他自己按")
				return
			}
			assert.True(t, row.Enabled, "被闸挡下的提交一个字段都不该落库")
		})
	}
}
