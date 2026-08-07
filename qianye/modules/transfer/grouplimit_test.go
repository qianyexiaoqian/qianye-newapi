package transfer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// grouplimit_test.go —— 「门槛按用户分组分档」的回归。
//
// # 这一组防的是什么
//
// 三种在本仓反复出现的形状,每一种都静默:
//
//  1. **零值与未配置不可区分**。分档表里的 0 是一个有含义的策略(「这一档不设
//     这道闸门」),而不是「没配」。压扁成零值之后,运营永远配不出「vip 不限
//     日额度」—— 他填的 0 会被当成没配而回落到全局的 2 亿。
//  2. **断链**。合并函数写对了,消费点没接上:handleGetLimits 与 create 只要
//     还在读 settings.Transfer,分档就只是写进了一张没人读的表,而界面显示
//     「已保存」。因此下面的断言一律从真正的消费点进。
//  3. **USD 往返被分档破坏**。上一轮把门槛改成 USD 录入,存储仍是额度整数;
//     分档必须走同一条通道 —— 界面显示 USD → 不改直接保存 → 存回的 quota
//     逐位相同,否则运营每打开一次页面就会多出一条无中生有的门槛变更审计。

// tierGlobal 是本文件统一的全站兜底门槛。数字刻意都不是零值,
// 这样「回落到全局」与「被压成 0」在断言里长得完全不一样。
func tierGlobal() config.Transfer {
	return config.Transfer{
		Enabled:                 true,
		MinQuota:                500_000,
		MaxPerTxQuota:           50_000_000,
		DailyMaxQuota:           100_000_000,
		DailyMaxCount:           10,
		CooldownSecs:            60,
		ReceiverDailyMaxInCount: 20,
		NewAccountFreezeHours:   24,
		FeeBps:                  100,
		FeeMinQuota:             1_000,
		RecipientLookup:         config.RecipientLookupID,
	}
}

func i64(v int64) *int64 { return &v }

// tierSettings 组一份带分档的生效配置,不碰数据库 —— 合并是纯函数。
func tierSettings(rows ...GroupLimit) opSettings {
	return opSettings{Transfer: tierGlobal(), Tiers: buildTierSet(rows)}
}

// TestTierZeroIsNotTheSameAsUnset 是本文件的核心不变量。
//
// 「没配」与「配成 0」必须是两件事:前者回落全局,后者把这道闸门关掉。
// 把 GroupLimit 的指针列换成普通 int64(或者在 override 里加一句
// `if v == 0 { return 0, false }`),这条用例的后两组断言会同时翻面。
func TestTierZeroIsNotTheSameAsUnset(t *testing.T) {
	global := tierGlobal()

	t.Run("整档缺席时七项全部回落全局", func(t *testing.T) {
		cfg, err := tierSettings().transferFor("vip")
		require.NoError(t, err)
		assert.Equal(t, global, cfg)
	})

	t.Run("只覆盖一项时,其余六项仍然是全局值", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: true, DailyMaxCount: i64(99),
		}).transferFor("vip")
		require.NoError(t, err)
		assert.Equal(t, 99, cfg.DailyMaxCount, "覆盖项必须生效")
		assert.Equal(t, global.DailyMaxQuota, cfg.DailyMaxQuota,
			"没覆盖的项必须回落全局,而不是被清成 0")
		assert.Equal(t, global.CooldownSecs, cfg.CooldownSecs)
		assert.Equal(t, global.MinQuota, cfg.MinQuota)
	})

	t.Run("显式配成 0 表示这一档不设这道闸门,绝不能被当成没配", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: true,
			DailyMaxQuota: i64(0), CooldownSecs: i64(0),
		}).transferFor("vip")
		require.NoError(t, err)
		assert.Zero(t, cfg.DailyMaxQuota,
			"配成 0 必须落成 0 —— 否则运营永远配不出「这一档不限日额度」")
		assert.Zero(t, cfg.CooldownSecs)
		// 对照:同一次调用里没覆盖的那些项仍然是全局值,
		// 证明 0 不是「整档被清空」的副作用。
		assert.Equal(t, global.DailyMaxCount, cfg.DailyMaxCount)
		assert.Equal(t, global.MinQuota, cfg.MinQuota)
	})

	t.Run("停用的分档整档失效,回落全局", func(t *testing.T) {
		cfg, err := tierSettings(GroupLimit{
			UserGroup: "vip", Enabled: false, DailyMaxQuota: i64(0),
		}).transferFor("vip")
		require.NoError(t, err)
		assert.Equal(t, global.DailyMaxQuota, cfg.DailyMaxQuota)
	})
}

// TestTierKeyIsTheUserGroupNamespace 锁定分档的键是**用户分组**,并且与
// qy_transfer_group_rules 用同一套归一化。
//
// 大小写折叠对一道资金门槛是 fail-closed 的那一侧:一档收紧的门槛配在 VIP 上、
// 用户的 users.group 是 vip,不折叠就会静默回落到更宽松的全局值。
func TestTierKeyIsTheUserGroupNamespace(t *testing.T) {
	s := tierSettings(GroupLimit{UserGroup: "vip", Enabled: true, MaxPerTxQuota: i64(1_000_000)})

	for _, group := range []string{"vip", "VIP", " Vip "} {
		cfg, err := s.transferFor(group)
		require.NoError(t, err)
		assert.EqualValues(t, 1_000_000, cfg.MaxPerTxQuota, "输入 %q 必须命中同一档", group)
	}
	// 空的 users.group(历史行、手工改过库的账号)折叠成 default,
	// 而不是命中一个不存在的空分组。
	cfg, err := s.transferFor("")
	require.NoError(t, err)
	assert.Equal(t, tierGlobal().MaxPerTxQuota, cfg.MaxPerTxQuota)
}

// TestTierFailsClosedWhenMergedConfigIsSelfContradictory 锁定失败关闭的方向与范围。
//
// 场景是真实会发生的:vip 那一档配了 min_quota=2000 万,运营随后把**全站**
// 单笔上限调到 1000 万。写入时那一档是合法的,现在合并起来任何金额都不合法。
// 此时必须拒绝这一档的划转(503),而不是拿一组谁都没批准过的门槛去放行;
// 同时**只能拒这一档** —— 让一个分组的错配把全站划转掐掉,是运营最不该承担的
// 连带风险。
func TestTierFailsClosedWhenMergedConfigIsSelfContradictory(t *testing.T) {
	global := tierGlobal()
	global.MaxPerTxQuota = 10_000_000
	s := opSettings{
		Transfer: global,
		Tiers: buildTierSet([]GroupLimit{
			{UserGroup: "vip", Enabled: true, MinQuota: i64(20_000_000)},
		}),
	}

	_, err := s.transferFor("vip")
	require.Error(t, err)
	assert.Same(t, errGroupLimitInvalid, err)
	assert.Equal(t, http.StatusServiceUnavailable, errGroupLimitInvalid.Status,
		"配置状态问题必须是 503:重试没有用,只能由管理员去改")

	other, err := s.transferFor("default")
	require.NoError(t, err, "一档配坏了绝不能把别的分组一起拖下水")
	assert.Equal(t, global, other)
}

// TestValidateGroupLimitGuardsBoundsAndCrossFields 锁定写入侧的三道校验。
//
// 区间与全站门槛共用 settingBounds;跨字段复用 config.ValidateTransfer,
// 并且必须拿**当前全局值**去合并 —— 只看这一档自己的几个数,
// 单独把 min_quota 调到全局 max_per_tx_quota 之上会一路放行,
// 而那一档人从此任何金额都不合法。
func TestValidateGroupLimitGuardsBoundsAndCrossFields(t *testing.T) {
	global := tierGlobal()

	t.Run("超出区间的取值被拒", func(t *testing.T) {
		row := GroupLimit{UserGroup: "vip", DailyMaxCount: i64(settingBounds[keyDailyMaxCount].Hi + 1)}
		require.Error(t, validateGroupLimit(&row, global))
	})

	t.Run("跨字段矛盾必须看到没覆盖的那些项", func(t *testing.T) {
		// 只覆盖 min_quota,值大于**全局**的单笔上限。
		row := GroupLimit{UserGroup: "vip", MinQuota: i64(global.MaxPerTxQuota + 1)}
		err := validateGroupLimit(&row, global)
		require.Error(t, err, "跨字段校验必须把这一档没覆盖的项按全局值补齐")
	})

	t.Run("一项都不覆盖的空档被拒", func(t *testing.T) {
		row := GroupLimit{UserGroup: "vip"}
		require.Error(t, validateGroupLimit(&row, global),
			"不覆盖任何项与不配置完全等价,留着只会让运营以为这组人被单独配过")
	})

	t.Run("通配符不是一档", func(t *testing.T) {
		row := GroupLimit{UserGroup: groupWildcard, DailyMaxCount: i64(1)}
		require.Error(t, validateGroupLimit(&row, global))
	})

	t.Run("合法输入被就地归一化", func(t *testing.T) {
		row := GroupLimit{UserGroup: " VIP ", DailyMaxCount: i64(3), Remark: "  内部  "}
		require.NoError(t, validateGroupLimit(&row, global))
		assert.Equal(t, "vip", row.UserGroup, "落库的必须就是被校验过的那个值")
		assert.Equal(t, "内部", row.Remark)
	})
}

// TestTierableKeysAreAllAddressable 锁定「加一个可分档键」这件事不会半途而废。
//
// 三处必须同时改:tierableKeys、GroupLimit 的指针列、fieldPtr 的 case。
// 漏掉 fieldPtr 那一处的表现最恶劣 —— 管理端保存成功、界面显示已覆盖,
// 而判定端读不到,那一档静默按全局门槛放行。
func TestTierableKeysAreAllAddressable(t *testing.T) {
	var row GroupLimit
	for _, key := range tierableKeys {
		require.NotNil(t, row.fieldPtr(key), "可分档键 %q 在 fieldPtr 里没有对应的列", key)
		_, ok := settingBounds[key]
		require.True(t, ok, "可分档键 %q 必须有取值区间", key)
		require.True(t, assignTransferSetting(&config.Transfer{}, key, 0),
			"可分档键 %q 必须能写进 config.Transfer", key)
		_, ok = transferSettingValue(config.Transfer{}, key)
		require.True(t, ok, "可分档键 %q 必须能从 config.Transfer 读回来", key)
		require.True(t, editable(key), "可分档键 %q 必须同时是全站可编辑键", key)
	}
	// 手续费与支付密码策略刻意不可分档,见 tierableKeys 的说明。
	for _, key := range []string{keyFeeBps, keyFeeMinQuota, keyPayPwdMaxAttempts, keyPayPwdLockMinutes} {
		assert.Nil(t, row.fieldPtr(key), "%q 不该是可分档键", key)
	}
}

// TestGroupLimitSnapshotKeepsUnsetAsNull 锁定审计快照能区分三态。
//
// 审计是事后仲裁「这一档当时到底配了什么」的唯一凭据。把未覆盖写成 0,
// 事后就再也分不清「当时不限日额度」与「当时跟着全局走」——
// 而这两者在同一天里可能相差两个数量级。
func TestGroupLimitSnapshotKeepsUnsetAsNull(t *testing.T) {
	row := GroupLimit{UserGroup: "vip", Enabled: true, DailyMaxQuota: i64(0)}
	snap := groupLimitSnapshot(&row)

	var decoded map[string]any
	require.NoError(t, common.UnmarshalJsonStr(snap, &decoded))

	require.Contains(t, decoded, keyDailyMaxQuota)
	assert.EqualValues(t, 0, decoded[keyDailyMaxQuota], "显式的 0 必须原样出现")
	require.Contains(t, decoded, keyDailyMaxCount,
		"未覆盖的项必须出现且为 null —— 省略会让人分不清「当时没配」与「当时还没这一项」")
	assert.Nil(t, decoded[keyDailyMaxCount])
}

// ─────────────────────────── 消费点(断链防线)───────────────────────────

// TestUserLimitsEndpointShowsTheTierNotTheGlobalFallback 从用户端 /transfer/limits
// 进,锁住「分档接到了回显链路上」。
//
// 断链的表现极其具体:handleGetLimits 只要还读 settings.Transfer,vip 用户就会
// 在界面上看到一个全站的「还能转 1 亿」,而提交时按自己那一档被拒 ——
// 而用户看不出问题出在哪。
func TestUserLimitsEndpointShowsTheTierNotTheGlobalFallback(t *testing.T) {
	gdb := newSettingsTestDB(t)
	mainDB := newMainDB(t)
	// callTransferHandler 固定以用户 1 的身份调用。
	seedMainUser(t, mainDB, 1, "vip", 90_000_000)
	useSettingsConfig(t, tierGlobal())

	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "vip", Enabled: true,
		DailyMaxQuota: i64(3_000_000), DailyMaxCount: i64(2), CooldownSecs: i64(0),
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()

	rec := callTransferHandler(t, http.MethodGet, "/api/qy/transfer/limits", "", handleGetLimits)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got struct {
		Data struct {
			MinQuota            int64 `json:"min_quota"`
			DailyMaxQuota       int64 `json:"daily_max_quota"`
			DailyMaxCount       int   `json:"daily_max_count"`
			CooldownSecs        int   `json:"cooldown_seconds"`
			RemainingDailyQuota int64 `json:"remaining_daily_quota"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &got))

	assert.EqualValues(t, 3_000_000, got.Data.DailyMaxQuota, "日额度必须是 vip 那一档的值")
	assert.Equal(t, 2, got.Data.DailyMaxCount)
	assert.Zero(t, got.Data.CooldownSecs, "显式配成 0 的冷却必须回显成 0,而不是全局的 60")
	assert.EqualValues(t, tierGlobal().MinQuota, got.Data.MinQuota,
		"这一档没覆盖的项必须回显全局值")
	assert.EqualValues(t, 3_000_000, got.Data.RemainingDailyQuota,
		"今日剩余必须按这一档的日额度算,否则用户看到的剩余量是别人那一档的")
}

// TestTransferRejectsAboveTierPerTxCapEvenWhenGlobalAllows 从真正会动钱的入口进。
//
// 这条是分档存在的理由本身:全站允许 5000 万一笔,vip 那一档只允许 100 万。
// create 只要还拿 settings.Transfer 去做受理校验,这一笔就会被放行。
func TestTransferRejectsAboveTierPerTxCapEvenWhenGlobalAllows(t *testing.T) {
	gdb := newSettingsTestDB(t)
	mainDB := newMainDB(t)
	seedMainUser(t, mainDB, 1, "vip", 90_000_000)
	seedMainUser(t, mainDB, 2, "default", 0)
	useSettingsConfig(t, tierGlobal())

	require.NoError(t, gdb.Create(&GroupLimit{
		UserGroup: "vip", Enabled: true, MaxPerTxQuota: i64(1_000_000),
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	invalidateSettings()

	settings, err := effectiveCtx(context.Background())
	require.NoError(t, err)

	cfg, err := settings.transferFor("vip")
	require.NoError(t, err)
	assert.EqualValues(t, 1_000_000, cfg.MaxPerTxQuota)

	// 受理校验按这一档判:全站上限之下、本档上限之上的那个金额必须被拒。
	_, err = validateCreate(1, createRequest{
		ToUserId: 2, Amount: 2_000_000, Confirm: true, ClientRequestId: "tier-cap",
	}, cfg)
	require.Error(t, err)
	assert.Same(t, errAmountOutOfRange, err)

	// 对照:同一笔金额在全站门槛下是合法的 —— 证明拒绝确实来自分档。
	_, err = validateCreate(1, createRequest{
		ToUserId: 2, Amount: 2_000_000, Confirm: true, ClientRequestId: "tier-cap",
	}, settings.Transfer)
	require.NoError(t, err)
}

// ─────────────────────────── 管理端与往返一致性 ───────────────────────────

// TestAdminGroupLimitRoundTripIsByteIdentical 是 USD 往返不变量在**分档**上的落点。
//
// 上一轮把门槛改成 USD 录入:界面显示 USD、存储仍是额度整数,换算走整数运算。
// 分档页复用同一对换算函数(前端 qyQuotaDraftText / qyQuotaDraftValue),
// 因此这里钉住后端这一侧的前提:**GET 回来的数原样 PUT 回去,存回的 quota
// 必须逐位相同**,并且不会把「未覆盖」压成 0。
//
// 差一个 quota 的后果不是显示问题:运营什么都没改点一下保存,审计里就多出一条
// 无中生有的门槛变更,而门槛变更正是「谁把日额度放大了十倍」要追的那条线索。
func TestAdminGroupLimitRoundTripIsByteIdentical(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, tierGlobal())

	// 刻意混了三种状态:普通值、显式 0、未覆盖。
	first := putGroupLimit(t, map[string]any{
		"user_group": "vip",
		"enabled":    true,
		"remark":     "内部账号",
		"overrides": map[string]any{
			keyMinQuota:      1,        // 最小的非零额度,USD 侧是 0.000002
			keyDailyMaxQuota: 12345678, // 一个不会被任何取整规则吞掉的数
			keyCooldownSecs:  0,        // 显式关闭这道闸门
			keyDailyMaxCount: nil,      // 未覆盖
		},
	})
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	stored := readTier(t, gdb, "vip")
	require.NotNil(t, stored.MinQuota)
	assert.EqualValues(t, 1, *stored.MinQuota)
	require.NotNil(t, stored.DailyMaxQuota)
	assert.EqualValues(t, 12345678, *stored.DailyMaxQuota)
	require.NotNil(t, stored.CooldownSecs)
	assert.EqualValues(t, 0, *stored.CooldownSecs, "显式 0 必须落成 0,不是 NULL")
	assert.Nil(t, stored.DailyMaxCount, "未覆盖必须落成 NULL,不是 0")

	// 原样回放一次(这正是「打开页面什么都不改直接保存」的动作)。
	replay := putGroupLimit(t, map[string]any{
		"user_group": "vip",
		"enabled":    true,
		"remark":     "内部账号",
		"overrides": map[string]any{
			keyMinQuota:      *stored.MinQuota,
			keyDailyMaxQuota: *stored.DailyMaxQuota,
			keyCooldownSecs:  *stored.CooldownSecs,
			keyDailyMaxCount: nil,
		},
	})
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())

	after := readTier(t, gdb, "vip")
	assert.Equal(t, *stored.MinQuota, *after.MinQuota, "往返之后必须逐位相同")
	assert.Equal(t, *stored.DailyMaxQuota, *after.DailyMaxQuota)
	assert.Equal(t, *stored.CooldownSecs, *after.CooldownSecs)
	assert.Nil(t, after.DailyMaxCount, "回放不得把未覆盖变成 0")
	assert.Equal(t, stored.CreatedAt, after.CreatedAt,
		"整行替换不得把「这一档是什么时候建的」归零")
}

// TestAdminGroupLimitClearsOverrideOnReplace 锁定 PUT 是整行替换。
//
// 补丁语义下「把某一项改回跟随全局」这个动作根本无法表达 —— 运营在界面上
// 清掉一个输入框、保存、刷新,那一项还在。
func TestAdminGroupLimitClearsOverrideOnReplace(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, tierGlobal())

	require.Equal(t, http.StatusOK, putGroupLimit(t, map[string]any{
		"user_group": "vip", "enabled": true,
		"overrides": map[string]any{keyDailyMaxCount: 3, keyCooldownSecs: 5},
	}).Code)

	require.Equal(t, http.StatusOK, putGroupLimit(t, map[string]any{
		"user_group": "vip", "enabled": true,
		"overrides": map[string]any{keyDailyMaxCount: 3},
	}).Code)

	after := readTier(t, gdb, "vip")
	require.NotNil(t, after.DailyMaxCount)
	assert.EqualValues(t, 3, *after.DailyMaxCount)
	assert.Nil(t, after.CooldownSecs, "第二次没带的项必须被清成「跟随全局」")
}

// TestAdminGroupLimitRejectsUneditableKey 锁定白名单之外的键让整个请求 400,
// 而不是被静默丢掉 —— 静默丢掉的表现是「界面说保存成功、那一项从没生效过」。
func TestAdminGroupLimitRejectsUneditableKey(t *testing.T) {
	newSettingsTestDB(t)
	useSettingsConfig(t, tierGlobal())

	w := putGroupLimit(t, map[string]any{
		"user_group": "vip", "enabled": true,
		"overrides": map[string]any{keyFeeBps: 200},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestAdminGroupLimitWritesAuditOnBothOutcomes 锁定删除那一条埋点带着 before 快照。
//
// 删掉一档等于把这一组人的门槛整体换回全站兜底(通常更宽松),而界面上只是
// 少了一行。被删掉的那一档配了什么,只存在于审计的 before 里。
func TestAdminGroupLimitWritesAuditOnBothOutcomes(t *testing.T) {
	gdb := newSettingsTestDB(t)
	useSettingsConfig(t, tierGlobal())

	require.Equal(t, http.StatusOK, putGroupLimit(t, map[string]any{
		"user_group": "vip", "enabled": true,
		"overrides": map[string]any{keyDailyMaxQuota: 7_000_000},
	}).Code)

	w := callTransferHandler(t, http.MethodDelete,
		"/admin/transfer/group-limits?user_group=vip", "", adminDeleteGroupLimit)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", "transfer.group_limit.delete").Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Contains(t, rows[0].BeforeSnap, "7000000",
		"删除审计必须带着被删掉那一档的完整内容 —— 库里已经没有它了")

	// 删完之后这一档必须真的回落全局。
	invalidateSettings()
	s, err := effectiveCtx(context.Background())
	require.NoError(t, err)
	cfg, err := s.transferFor("vip")
	require.NoError(t, err)
	assert.Equal(t, tierGlobal().DailyMaxQuota, cfg.DailyMaxQuota)
}

// ─────────────────────────── 脚手架 ───────────────────────────

func readTier(t *testing.T, gdb *gorm.DB, name string) GroupLimit {
	t.Helper()
	var row GroupLimit
	require.NoError(t, gdb.Where("user_group = ?", name).Take(&row).Error)
	return row
}

func putGroupLimit(t *testing.T, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := common.Marshal(payload)
	require.NoError(t, err)
	return callTransferHandler(t, http.MethodPut,
		"/admin/transfer/group-limits", string(body), adminPutGroupLimit)
}
