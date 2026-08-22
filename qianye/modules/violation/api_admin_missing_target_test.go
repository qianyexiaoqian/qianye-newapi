package violation

import (
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

// api_admin_missing_target_test.go —— 两条「目标不在那儿」的答复口径。
//
// 共同的形状:管理动作打在一个不存在(或已经处于终态)的目标上时,接口给出的
// 答复既误导人、又污染台账。这套系统把 qy_audit_logs 定义成事后仲裁的唯一凭据,
// 所以「一次没有发生的处置」绝不能在里面留下一条与真实处置长得一样的记录。

func missingTargetEnv(t *testing.T) *gorm.DB {
	t.Helper()
	useTestConfig(t, "  enabled: true\n")
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Rule{}, &Ban{}, &Counter{}, &qymodel.AuditLog{}))
	prevHandle := qyDBHandleForCtxTest.Swap(gdb)
	prevHealthy := qyDBHealthyForJSONTest.Swap(true)
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prevHandle)
		qyDBHealthyForJSONTest.Store(prevHealthy)
		_ = sqlDB.Close()
	})
	return gdb
}

func missingTargetCtx(t *testing.T, method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
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

// TestDeleteRuleRefusesAMissingTarget 守「删了一条不存在的规则不能算成功」。
//
// 此前 adminDeleteRule 直接 `Delete(&Rule{})` 且不看 RowsAffected:
//
//	DELETE /violation/rules/999999999 → 200 {"data":{}}
//	审计:action=rules.delete result=ok before_snap 空
//	     after_snap={"id":999999999, 其余全零}
//
// 而**同一资源的兄弟动词口径是反的**:PUT 同一个 id 回 404 qy_vio_not_found。
//
// 更糟的是审计行的信息量:afterRuleChange 收到的一直是 `&Rule{Id: id}`,于是
// 真删、重复删、目标不存在三者的审计行逐字段相同 —— 连被删规则的**名字**都
// 没有。那张表对"删掉的是什么"携带的信息量因此是零,而它是事后仲裁的唯一凭据。
//
// 触发不需要是攻击:并发双击、重试、两个管理员先后点同一行,都会走到这里。
func TestDeleteRuleRefusesAMissingTarget(t *testing.T) {
	gdb := missingTargetEnv(t)

	t.Run("目标不存在:404,且不留一条 result=ok 的审计", func(t *testing.T) {
		c, rec := missingTargetCtx(t, "DELETE", "/violation/rules/999999999", "")
		c.Params = gin.Params{{Key: "id", Value: "999999999"}}
		adminDeleteRule(c)

		assert.Equal(t, 404, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "qy_vio_not_found",
			"与兄弟动词 PUT 同一个 id 的口径必须一致")

		var rows int64
		require.NoError(t, gdb.Model(&qymodel.AuditLog{}).
			Where("action = ?", "rules.delete").Count(&rows).Error)
		assert.EqualValues(t, 0, rows,
			"一次没有发生的删除不能在仲裁表里留下记录 —— 那条记录与真实删除无法分辨")
	})

	t.Run("真删:200,而且审计带得出删掉的是哪一条", func(t *testing.T) {
		row := seedRule(t, gdb, goodRule(true))
		row.Name = "qy-被删掉的那一条"
		row.Mode = ModeEnforce
		require.NoError(t, gdb.Save(row).Error)

		c, rec := missingTargetCtx(t, "DELETE", "/violation/rules/1", "")
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(row.Id, 10)}}
		adminDeleteRule(c)
		require.Equal(t, 200, rec.Code, rec.Body.String())

		var entry qymodel.AuditLog
		require.NoError(t, gdb.Where("action = ?", "rules.delete").Take(&entry).Error)
		assert.Equal(t, qymodel.ResultOK, entry.Result)
		assert.Contains(t, entry.BeforeSnap, "qy-被删掉的那一条",
			"删完之后库里已经没有它了,before 快照是唯一还能说清删的是什么的东西")
		assert.Contains(t, entry.BeforeSnap, ModeEnforce,
			"mode 是本模块唯一决定要不要真扣钱/封号的开关,必须在快照里看得见")
	})

	t.Run("重复删同一条:第二次是 404,不再多一条 ok", func(t *testing.T) {
		var before int64
		require.NoError(t, gdb.Model(&qymodel.AuditLog{}).
			Where("action = ? AND result = ?", "rules.delete", qymodel.ResultOK).
			Count(&before).Error)

		c, rec := missingTargetCtx(t, "DELETE", "/violation/rules/1", "")
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		adminDeleteRule(c)
		assert.Equal(t, 404, rec.Code)

		var after int64
		require.NoError(t, gdb.Model(&qymodel.AuditLog{}).
			Where("action = ? AND result = ?", "rules.delete", qymodel.ResultOK).
			Count(&after).Error)
		assert.Equal(t, before, after, "重复删不该再产生一条「删除成功」")
	})
}

// TestUnbanTellsTheTruthWhenThereIsNothingToUnban 守解封的答复口径。
//
// 此前:没有待解除的封禁时回 500 qy_internal_error「处理失败,请稍后重试」。
// 真正的原因 claimUnban 已经写好了(「该用户没有待解除的违规封禁」),被
// internalError 整段吞掉,换成一句让人重试的话 —— 而重试**永远不会成功**。
//
// 最常见的触发不是构造出来的:两个管理员同时点、封禁列表缓存陈旧(useQuery 不会
// 自动失效,别人解封之后本地那一行还是 banned、按钮仍可点)。表现是运营对着一件
// 永远不会成功的事反复重试,同时把一次正常点击伪装成服务端故障、污染 5xx 告警。
func TestUnbanTellsTheTruthWhenThereIsNothingToUnban(t *testing.T) {
	gdb := missingTargetEnv(t)
	const userId = 7788

	t.Run("没有待解除的封禁:409 + 说清原因,不是 500「请稍后重试」", func(t *testing.T) {
		err := unbanUser(nil, userId, "note", false, 1)
		require.Error(t, err)
		require.ErrorIs(t, err, errNoPendingBan)

		c, rec := missingTargetCtx(t, "POST", "/violation/bans/7788/unban", `{"note":"x"}`)
		respondUnbanError(c, err)
		assert.Equal(t, 409, rec.Code,
			"这是客户端状态冲突,不是服务端故障 —— 500 会污染告警")
		assert.Contains(t, rec.Body.String(), "qy_vio_no_pending_ban")
		assert.NotContains(t, rec.Body.String(), "请稍后重试",
			"重试永远不会成功,这句话在指错方向")
	})

	t.Run("状态正在变化:另一个 code,因为这一次确实该重试", func(t *testing.T) {
		c, rec := missingTargetCtx(t, "POST", "/violation/bans/7788/unban", `{}`)
		respondUnbanError(c, errBanStatusChurning)
		assert.Equal(t, 409, rec.Code)
		assert.Contains(t, rec.Body.String(), "qy_vio_ban_churning")
		assert.NotContains(t, rec.Body.String(), "qy_vio_no_pending_ban",
			"两档要求的下一步动作完全不同,不能共用一个 code")
	})

	t.Run("确实有待解除的封禁时照常解封", func(t *testing.T) {
		now := common.GetTimestamp()
		require.NoError(t, gdb.Create(&Ban{
			UserId: userId, BanCycle: 1, Status: BanBanned, CreatedAt: now,
		}).Error)
		ban, err := claimUnban(gdb, userId, "手工解封", 1)
		require.NoError(t, err, "有得解的时候不能被这道判据误伤")
		require.NotNil(t, ban)

		var got Ban
		require.NoError(t, gdb.Where("user_id = ?", userId).Take(&got).Error)
		assert.Equal(t, BanUnbanned, got.Status)
	})
}
