package transfer

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// grouppolicy_enforce_test.go —— 分组限制的**行为级**回归。
//
// # 为什么这一层不能只有 AST 断言
//
// TestGroupPolicyIsEnforcedAtBothStagesOfCreate 解析 service.go,断言两处都出现了
// enforceGroupPolicy 这个标识符。它抓得住"函数写对了但没人调用",抓不住
// "调用了却没生效" —— 把两处的 `if err := enforceGroupPolicy(...); err != nil { return err }`
// 改成 `_ = enforceGroupPolicy(...)`,调用点仍在源码里,AST 断言照样全绿,
// 而分组限制已经被彻底废除。第三轮审计实测复现了这一点:两道闸门同时废掉,
// transfer 整包依然 ok。
//
// 因此本文件直接驱动两个判定点所在的生产函数,并且断言的不是"返回了错误",
// 而是**主库里两行的 quota 一分未动**。"拒绝了"和"拒绝了且没动钱"是两回事:
// 判定在扣款之后才做,同样会返回错误,钱却已经转走了。
//
// 关键手法:applyQuotaTransfer 的 tx 参数直接传库句柄而不是一个会被回滚的事务。
// 包在事务里再断言余额,验的是"回滚有效",不是"判定挡在写库之前" —— 那样即使
// 判定被挪到两条 UPDATE 之后,测试也照样绿。

// mainDBConfig 是一份不会误伤本文件用例的划转配置:
// 冻结期关掉(种子用户的 created_at 由 GORM 接管,不该成为干扰项),
// 收款方必须启用保持默认 true。
func mainDBConfig() config.Transfer {
	cfg := baseConfig()
	cfg.NewAccountFreezeHours = 0
	return cfg
}

// newMainDB 建一个只承载 users 表的主库替身,并接到 model.DB 上。
//
// 必须是真库:被测的两条不变量(分组判定挡在 UPDATE 之前、锁内复检读的是行上的
// 最新分组)都只有在真的读写一次 users 行时才能被证伪。
func newMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_main.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&model.User{}))

	// lockForUpdate 只在非 SQLite 上追加 FOR UPDATE。显式固定成 SQLite,
	// 否则同一进程内别处设过库类型时这里会拼出 SQLite 不认识的语句。
	prevType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	prevDB := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prevDB
		common.SetMainDatabaseType(prevType)
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return gdb
}

func seedMainUser(t *testing.T, gdb *gorm.DB, id int, group string, quota int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.User{
		Id:       id,
		Username: "user" + strconv.Itoa(id),
		AffCode:  "aff" + strconv.Itoa(id),
		Group:    group,
		Quota:    quota,
		Status:   common.UserStatusEnabled,
	}).Error)
}

func quotaOf(t *testing.T, gdb *gorm.DB, id int) int {
	t.Helper()
	var u model.User
	require.NoError(t, gdb.First(&u, "id = ?", id).Error)
	return u.Quota
}

func groupOf(t *testing.T, gdb *gorm.DB, id int) string {
	t.Helper()
	var u model.User
	require.NoError(t, gdb.First(&u, "id = ?", id).Error)
	return u.Group
}

// 本文件统一的资金要素:两侧初始余额、金额、手续费都取互不相等的值,
// 任何一侧被错误改动都会算出一个与"未动"不同的数字。
const (
	seedSenderQuota   = 1_000_000
	seedReceiverQuota = 777
	seedAmount        = 100_000
	seedFee           = 1_000
)

func seedAccepted() acceptedRequest {
	return acceptedRequest{
		FromUserId: 1,
		ToUserId:   2,
		Amount:     seedAmount,
		Fee:        seedFee,
		Total:      seedAmount + seedFee,
		IdemKey:    "1:group-policy-behavior",
	}
}

// TestApplyQuotaTransferDeniedByGroupLeavesQuotaUntouched 是主库锁内那道闸门的行为级回归。
//
// 被拒的三种形态各跑一遍,每一遍都回读 users 两行,断言余额与种子值逐一相等。
// 最后一个用例是对照组:规则允许时钱必须**真的**划走 —— 少了它,"钱没动"可能
// 只是因为整个函数在更早的地方就退出了,前面三条断言会退化成永真。
func TestApplyQuotaTransferDeniedByGroupLeavesQuotaUntouched(t *testing.T) {
	cases := []struct {
		name          string
		rows          []GroupRule
		senderGroup   string
		receiverGroup string
		wantErr       *bizError
		wantApplied   bool
	}{
		{
			name:        "白名单外的目标分组:拒绝且不动钱",
			rows:        []GroupRule{rule("vip", GroupPolicyAllowList, "vip")},
			senderGroup: "vip", receiverGroup: "default",
			wantErr: errGroupTargetDenied,
		},
		{
			name:        "deny_all 分组:拒绝且不动钱",
			rows:        []GroupRule{rule("vip", GroupPolicyDenyAll, "")},
			senderGroup: "vip", receiverGroup: "vip",
			wantErr: errGroupSendBlocked,
		},
		{
			// 兜底规则也必须在锁内生效,否则"给全站加一条 * deny_all"这个最常见的
			// 应急操作只挡得住受理阶段。
			name:        "兜底规则同样在锁内生效",
			rows:        []GroupRule{rule(groupWildcard, GroupPolicyDenyAll, "")},
			senderGroup: "default", receiverGroup: "default",
			wantErr: errGroupSendBlocked,
		},
		{
			name:        "对照组:规则允许时额度必须真的划走",
			rows:        []GroupRule{rule("vip", GroupPolicyAllowList, "default")},
			senderGroup: "vip", receiverGroup: "default",
			wantApplied: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newMainDB(t)
			seedMainUser(t, gdb, 1, tc.senderGroup, seedSenderQuota)
			seedMainUser(t, gdb, 2, tc.receiverGroup, seedReceiverQuota)

			acc := seedAccepted()
			var snap quotaSnapshot
			// 传库句柄而不是事务:被拒时"钱没动"必须由判定本身保证,
			// 不能靠外层回滚兜底(见文件头注释)。
			err := applyQuotaTransfer(gdb, acc, mainDBConfig(), buildGroupRuleSet(tc.rows), &snap)

			if tc.wantApplied {
				require.NoError(t, err)
				assert.Equal(t, seedSenderQuota-(seedAmount+seedFee), quotaOf(t, gdb, 1))
				assert.Equal(t, seedReceiverQuota+seedAmount, quotaOf(t, gdb, 2))
				assert.Equal(t, quotaSnapshot{
					FromBefore: seedSenderQuota,
					FromAfter:  seedSenderQuota - (seedAmount + seedFee),
					ToBefore:   seedReceiverQuota,
					ToAfter:    seedReceiverQuota + seedAmount,
				}, snap)
				return
			}

			require.Error(t, err)
			assert.Same(t, tc.wantErr, err)
			assert.Equal(t, seedSenderQuota, quotaOf(t, gdb, 1),
				"分组规则拒绝之后,发起方额度必须一分未动")
			assert.Equal(t, seedReceiverQuota, quotaOf(t, gdb, 2),
				"分组规则拒绝之后,收款方额度必须一分未动")
			assert.Equal(t, quotaSnapshot{}, snap,
				"被拒的划转不得留下余额快照:它会被写进明细与账本日志")
		})
	}
}

// TestApplyQuotaTransferRechecksGroupAfterAcceptance 复现锁内复检存在的理由。
//
// 场景:用户提交时双方都在 vip(受理阶段放行),管理员在受理与落账之间把收款方
// 调出了 vip。只有以行锁那一刻读到的 users.group 为准,这一笔才拦得住;
// 沿用受理阶段的结论(或干脆吞掉锁内的判定结果)就会让钱转到不该去的分组。
//
// 这条用例专门覆盖"锁内那一处被废掉、受理那一处还在"的形态 ——
// 上一条用例里两处判定的输入相同,单独废掉锁内那处仍可能被受理阶段掩盖。
func TestApplyQuotaTransferRechecksGroupAfterAcceptance(t *testing.T) {
	gdb := newMainDB(t)
	seedMainUser(t, gdb, 1, "vip", seedSenderQuota)
	seedMainUser(t, gdb, 2, "vip", seedReceiverQuota)

	rules := buildGroupRuleSet([]GroupRule{rule("vip", GroupPolicyAllowList, groupSelfToken)})
	cfg := mainDBConfig()
	acc := seedAccepted()

	// 受理阶段:此刻双方同组,必须放行。
	sender, receiver, err := loadParties(acc, cfg, rules)
	require.NoError(t, err)
	require.Equal(t, "vip", sender.Group)
	require.Equal(t, "vip", receiver.Group)

	// 管理员在这中间把收款方调出了 vip。
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", 2).
		Update("group", "default").Error)
	require.Equal(t, "default", groupOf(t, gdb, 2))

	var snap quotaSnapshot
	err = applyQuotaTransfer(gdb, acc, cfg, rules, &snap)
	require.Error(t, err)
	assert.Same(t, errGroupTargetDenied, err,
		"锁内必须以行上的最新分组重判,而不是沿用受理阶段的结论")
	assert.Equal(t, seedSenderQuota, quotaOf(t, gdb, 1), "复检拒绝后发起方额度必须一分未动")
	assert.Equal(t, seedReceiverQuota, quotaOf(t, gdb, 2), "复检拒绝后收款方额度必须一分未动")
	assert.Equal(t, quotaSnapshot{}, snap)
}

// TestLoadPartiesEnforcesGroupPolicyAtAcceptance 是受理阶段那道闸门的行为级回归。
//
// 受理阶段被废掉不会直接造成资损(锁内还有一道),但用户会一路走到扣款事务里
// 才被拒,白吃一次冷却与风控预占 —— 而他提交时看到的规则确实是不允许的。
// 因此这里除了断言错误,同样回读 users 两行:受理阶段绝不该写任何东西。
func TestLoadPartiesEnforcesGroupPolicyAtAcceptance(t *testing.T) {
	cases := []struct {
		name          string
		rows          []GroupRule
		senderGroup   string
		receiverGroup string
		wantErr       *bizError
	}{
		{
			name:        "白名单外的目标分组被拒",
			rows:        []GroupRule{rule("vip", GroupPolicyAllowList, "vip")},
			senderGroup: "vip", receiverGroup: "default",
			wantErr: errGroupTargetDenied,
		},
		{
			name:        "deny_all 分组被拒",
			rows:        []GroupRule{rule("vip", GroupPolicyDenyAll, "")},
			senderGroup: "vip", receiverGroup: "vip",
			wantErr: errGroupSendBlocked,
		},
		{
			name:        "黑名单命中被拒",
			rows:        []GroupRule{rule("vip", GroupPolicyDenyList, "default")},
			senderGroup: "vip", receiverGroup: "default",
			wantErr: errGroupTargetDenied,
		},
		{
			name:        "对照组:规则允许时正常返回双方",
			rows:        []GroupRule{rule("vip", GroupPolicyAllowList, "default")},
			senderGroup: "vip", receiverGroup: "default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newMainDB(t)
			seedMainUser(t, gdb, 1, tc.senderGroup, seedSenderQuota)
			seedMainUser(t, gdb, 2, tc.receiverGroup, seedReceiverQuota)

			sender, receiver, err := loadParties(seedAccepted(), mainDBConfig(), buildGroupRuleSet(tc.rows))

			if tc.wantErr == nil {
				require.NoError(t, err)
				require.NotNil(t, sender)
				require.NotNil(t, receiver)
				assert.Equal(t, 1, sender.Id)
				assert.Equal(t, 2, receiver.Id)
				return
			}

			require.Error(t, err)
			assert.Same(t, tc.wantErr, err)
			assert.Nil(t, sender, "被拒时不得把用户行交给调用方")
			assert.Nil(t, receiver)
			assert.Equal(t, seedSenderQuota, quotaOf(t, gdb, 1), "受理阶段不得改动任何余额")
			assert.Equal(t, seedReceiverQuota, quotaOf(t, gdb, 2), "受理阶段不得改动任何余额")
		})
	}
}

// TestLoadPartiesRepliesAboutRecipientBeforeGroupPolicy 锁定受理阶段的判定次序。
//
// 分组判定排在"收款人存在性/状态"之后是刻意的:抢在它们前面报出去,等于用一句
// "分组不允许"回答了"这个账号存不存在" —— 拿一条 * deny_all 规则当探针,
// 就能凭返回码区分"账号不存在"与"账号存在但分组不让转",凭空多一条枚举旁路。
func TestLoadPartiesRepliesAboutRecipientBeforeGroupPolicy(t *testing.T) {
	// 一条挡住所有人的兜底规则:分组判定若排在前面,下面两个用例都会返回它的错误。
	rules := buildGroupRuleSet([]GroupRule{rule(groupWildcard, GroupPolicyDenyAll, "")})
	cfg := mainDBConfig()

	t.Run("收款人不存在", func(t *testing.T) {
		gdb := newMainDB(t)
		seedMainUser(t, gdb, 1, "default", seedSenderQuota)

		acc := seedAccepted()
		acc.ToUserId = 999
		_, _, err := loadParties(acc, cfg, rules)
		require.Error(t, err)
		assert.Same(t, errReceiverNotFound, err,
			"分组不允许时也必须先回答「收款人不存在」,否则可以用分组规则当枚举探针")
	})

	t.Run("收款人已禁用", func(t *testing.T) {
		gdb := newMainDB(t)
		seedMainUser(t, gdb, 1, "default", seedSenderQuota)
		seedMainUser(t, gdb, 2, "default", seedReceiverQuota)
		require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", 2).
			Update("status", common.UserStatusDisabled).Error)

		_, _, err := loadParties(seedAccepted(), cfg, rules)
		require.Error(t, err)
		assert.Same(t, errReceiverDisabled, err)
	})
}
