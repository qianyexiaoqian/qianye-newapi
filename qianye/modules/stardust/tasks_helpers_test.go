package stardust

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// tasks_helpers_test.go —— 后台任务(日结 / 充值扫描)与三个事件 hook 用例的脚手架,
// 叠在 api_helpers_test.go 之上。
//
// 三条获得途径都要回主库:结算读 users 与 logs,充值扫描读 top_ups / subscription_orders,
// 邀请判定(invite.InviteeEligible)读 users 并在扩展库落 qy_invite_relation 快照。
// 刻度钉死成 500000(= 上游 QuotaPerUnit 的默认值):换算断言里的每一个数字都是按它手算的,
// 而 common.QuotaPerUnit 是管理员可热改的全局变量,不钉住就没法断言。
//
// commission 的邀请关系缓存是进程级的、按 user_id 索引、不随测试库重建而失效,
// 因此本包用例里的主库用户 id 互不重复(11–21 / 101–114 / 301–322 / 401)。

// newTaskEnv 建扩展库 + 主库,并补上任务链路会碰到的表。
func newTaskEnv(t *testing.T, mutate func(*config.Stardust)) apiEnv {
	t.Helper()
	env := newAPIEnv(t, func(s *config.Stardust) {
		s.QuotaPerUnit = 500_000
		if mutate != nil {
			mutate(s)
		}
	})
	require.NoError(t, env.main.AutoMigrate(&model.TopUp{}, &model.SubscriptionOrder{}))
	require.NoError(t, env.ext.AutoMigrate(&invite.InviteRelation{}))
	prevQPU := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = prevQPU })
	return env
}

// seedMainUser 往主库插一个账号;没给的字段取"普通用户、启用、default 分组"。
// username 与 aff_code 都是唯一索引,按 id 派生。
func seedMainUser(t *testing.T, main *gorm.DB, u model.User) {
	t.Helper()
	if u.Username == "" {
		u.Username = "u" + strconv.Itoa(u.Id)
	}
	u.Password, u.Email, u.AffCode = "x", u.Username+"@x.test", "aff"+strconv.Itoa(u.Id)
	if u.Role == 0 {
		u.Role = common.RoleCommonUser
	}
	if u.Status == 0 {
		u.Status = common.UserStatusEnabled
	}
	if u.Group == "" {
		u.Group = "default"
	}
	require.NoError(t, main.Create(&u).Error)
}

// consumeLog 是一行 type=2 的消费日志;other 是 JSON 原文(空串 = 没有 other)。
func consumeLog(userId int, at int64, quota int, other string) model.Log {
	return model.Log{UserId: userId, CreatedAt: at, Type: model.LogTypeConsume, Quota: quota, Other: other, ModelName: "gpt-4o"}
}

// seedTopUp 插一笔充值订单。缺省是一笔 creem 渠道、已在 1000 秒前成功的订单:
// creem 的 Amount 本身就是额度,换算不掺 QuotaPerUnit,断言里的数字就是订单上的数字;
// 完成时刻要早于前向扫描的余量(topupSettleGraceSec),否则会被当成"刚成功"钉住游标。
func seedTopUp(t *testing.T, main *gorm.DB, row model.TopUp) {
	t.Helper()
	now := common.GetTimestamp()
	if row.TradeNo == "" {
		row.TradeNo = "T" + strconv.Itoa(row.Id)
	}
	if row.PaymentProvider == "" && row.PaymentMethod == "" {
		row.PaymentProvider = model.PaymentProviderCreem
	}
	if row.Status == "" {
		row.Status = common.TopUpStatusSuccess
	}
	if row.CreateTime == 0 {
		row.CreateTime = now - 2000
	}
	if row.CompleteTime == 0 && row.Status == common.TopUpStatusSuccess {
		row.CompleteTime = now - 1000
	}
	require.NoError(t, main.Create(&row).Error)
}

// setCursor 写一条 qy_kv 水位线(Save 按主键 upsert)。
func setCursor(t *testing.T, gdb *gorm.DB, key string, v int64) {
	t.Helper()
	require.NoError(t, gdb.Save(&qymodel.KV{K: key, V: strconv.FormatInt(v, 10), UpdatedAt: common.GetTimestamp()}).Error)
}

// cursorOf 直接读回水位线,不经生产代码的 loadKV:断言不该依赖被测的读取器。
func cursorOf(t *testing.T, gdb *gorm.DB, key string) int64 {
	t.Helper()
	var row qymodel.KV
	require.NoError(t, gdb.Where("k = ?", key).Take(&row).Error)
	v, err := strconv.ParseInt(row.V, 10, 64)
	require.NoError(t, err)
	return v
}

// settleRunOf 回读某个 run_date 的运行记录;不存在时返回 nil。
func settleRunOf(t *testing.T, gdb *gorm.DB, runDate string) *SettleRun {
	t.Helper()
	var rows []SettleRun
	require.NoError(t, gdb.Where("run_date = ?", runDate).Find(&rows).Error)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// accrualsOfDay 按 user_id 索引某一天的全部日桶。
func accrualsOfDay(t *testing.T, gdb *gorm.DB, day string) map[int]Accrual {
	t.Helper()
	var rows []Accrual
	require.NoError(t, gdb.Where("bucket_date = ?", day).Find(&rows).Error)
	out := make(map[int]Accrual, len(rows))
	for _, r := range rows {
		out[r.UserId] = r
	}
	return out
}

// ledgerCount 是全表流水行数,用来断言"这一步谁都没被记账"。
func ledgerCount(t *testing.T, gdb *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, gdb.Model(&Ledger{}).Count(&n).Error)
	return n
}

func bpsPtr(v int) *int { return &v }
