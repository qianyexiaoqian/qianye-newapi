package groupns

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
)

// funding_test.go —— 钱包出资闸门的完整判据表。
//
// ═══════════ 判据在这一轮被改成「成员资格优先」 ═══════════
//
// 旧判据的第一项也是 contains(U,M),但拒绝分支之后还要被上游那句
// 「任一活跃订阅 allow_wallet_overflow=0 就不许回落钱包」在**闸门外面**复核一次,
// 而 subscription_first 那条路径干脆在 tryWallet 之前就先 403 了。净效果是:
// 一个用户分组本来就含该模型分组的人,只要手上有一张 O=0 的套餐,套餐一用完就被拒 ——
// 而他本来根本不需要那张套餐就能用这个分组。
//
// 新判据:
//
//	contains(U,M)                       → 放行,**不看 allow_wallet_overflow**
//	不含 + 没有套餐解锁 M               → 放行(口径差异不该由本闸门变成故障)
//	不含 + 套餐解锁 M + 仍有余额        → 放行
//	不含 + 套餐解锁 M + 耗尽 + O=1      → 放行
//	不含 + 套餐解锁 M + 耗尽 + O=0      → 由 funding_gate_mode 决定(off/shadow 放行,enforce 拒绝)
//
// O 只统计**解锁 M 的**订阅,取值口径是任一为真即放行(见 planentitlement.UnlockFundingState)。

// fundingGateCase 是判据表的一行。gate 只有 enforce 一档会拒绝,
// 所以 mode 单独用 TestFundingGateModeIsOnlyARolloutSwitch 覆盖。
type fundingGateCase struct {
	name string
	// modelGroup 决定 contains:白名单里有 "共享池",没有 "反重力的哈基米"。
	modelGroup    string
	unlocked      bool
	funded        bool
	allowOverflow bool
	wantAllowed   bool
	why           string
}

func fundingGateTable() []fundingGateCase {
	return []fundingGateCase{
		{
			name: "用户分组含它 + 无任何套餐", modelGroup: "共享池",
			wantAllowed: true,
			why:         "这个人本来就不需要套餐就能用这个分组",
		},
		{
			name: "用户分组含它 + 套餐耗尽 + O=0", modelGroup: "共享池",
			unlocked: true, funded: false, allowOverflow: false,
			wantAllowed: true,
			why: "**项目方点名要改的核心格**。用户分组本身含该模型分组时," +
				"allow_wallet_overflow 一个字都不该说 —— 拿「套餐额度用完」去拦一个" +
				"本来就有这个分组的人没有道理",
		},
		{
			name: "不含 + 没有任何套餐解锁它", modelGroup: "反重力的哈基米",
			unlocked: false,
			// 鉴权处(middleware/auth.go)已经挡过一次,能走到计费说明它在某种口径下被允许。
			wantAllowed: true,
			why:         "越权拒绝会把一次鉴权/计费的口径差异变成一次线上故障",
		},
		{
			name: "不含 + 套餐解锁它 + 仍有余额 + O=0", modelGroup: "反重力的哈基米",
			unlocked: true, funded: true, allowOverflow: false,
			wantAllowed: true,
			why:         "套餐还供得起,O=0 说的是「用尽之后」,现在还没到那一步",
		},
		{
			name: "不含 + 套餐解锁它 + 已耗尽 + O=1", modelGroup: "反重力的哈基米",
			unlocked: true, funded: false, allowOverflow: true,
			wantAllowed: true,
			why:         "运营在套餐上显式勾了「额度用尽后允许用钱包余额」",
		},
		{
			name: "不含 + 套餐解锁它 + 已耗尽 + O=0", modelGroup: "反重力的哈基米",
			unlocked: true, funded: false, allowOverflow: false,
			wantAllowed: false,
			why:         "纯解锁分组耗尽 + 运营显式禁止钱包续付 —— 全站唯一一档拒绝",
		},
		{
			name: "auto 伪分组永不参与出资判定", modelGroup: autoGroup,
			unlocked: true, funded: false, allowOverflow: false,
			wantAllowed: true,
			why:         "auto 不是任何池子",
		},
	}
}

// TestFundingGateDecisionTable 是判据表本体(enforce 档)。
//
// 除判据本身,每一行还断言**主库被问了几次**:
//
//	unlocked=false(含成员资格短路、auto、无解锁) → 0 次,必须零 I/O。
//	                                                这条闸门挂在每一笔 relay 请求上。
//	unlocked=true                                 → 恰好 1 次。余额与运营开关是钱的
//	                                                判据,不能拿一份 60 秒前的缓存作数,
//	                                                **放行方向同样不能**。
//
// 少了这半句,「从不回库」与「每次都回库」两种错误都能让这张表全绿。
func TestFundingGateDecisionTable(t *testing.T) {
	for _, tc := range fundingGateTable() {
		t.Run(tc.name, func(t *testing.T) {
			newTestDB(t)
			nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
			// 白名单即 contains 的来源:含 "共享池",不含 "反重力的哈基米"。
			useUpstreamGroups(t,
				map[string]string{"default": "默认", "共享池": "共享"},
				map[string]float64{"default": 1, "共享池": 1})
			authoritativeCalls := 0
			usePlanUnlock(t, func(_ int, mg string, authoritative bool) (bool, bool, bool) {
				if authoritative {
					authoritativeCalls++
				}
				if mg != tc.modelGroup {
					return false, false, false
				}
				return tc.unlocked, tc.funded, tc.allowOverflow
			})

			allowed, reason := ModelGroupFundingAllowed(7, "default", tc.modelGroup)
			assert.Equalf(t, tc.wantAllowed, allowed, "出资层判据错了。理由:%s", tc.why)
			if tc.wantAllowed {
				assert.Empty(t, reason)
			} else {
				assert.Contains(t, reason, "额度已用尽",
					"文案必须能自解释为什么昨天还能用 —— 「无权访问」会把排查方向从第一分钟就带偏")
				assert.Contains(t, reason, "不允许使用钱包余额",
					"拒绝的直接原因是运营在套餐上勾的那个开关,文案必须点名它,"+
						"否则用户会去充值钱包 —— 而充了也没用")
			}
			// tc.unlocked 只在 modelGroup 与本行一致时才生效;成员资格与 auto 两档
			// 在问到套餐之前就短路了,所以这里要按闸门真正会走到哪一步来算。
			_, memberShortCircuit := map[string]struct{}{"default": {}, "共享池": {}}[tc.modelGroup]
			wantAuthoritative := 0
			if tc.unlocked && !memberShortCircuit && tc.modelGroup != autoGroup {
				wantAuthoritative = 1
			}
			assert.Equalf(t, wantAuthoritative, authoritativeCalls,
				"unlocked=%v 时主库该被问 %d 次:余额与运营开关是钱的判据,"+
					"放行方向也不能拿 60 秒前的缓存作数;而 unlocked=false 那一档必须零 I/O",
				tc.unlocked, wantAuthoritative)
		})
	}
}

// TestFundingGateConfirmsWithMainDBBeforeDenying 钉住「放行读缓存,拒绝回主库」
// 在闸门这一侧的那一半。
//
// ═══════════ 它守的是复核里那条「该拒的放行」 ═══════════
//
// 判据来自 planentitlement 的 per-user 缓存(默认 60 秒新鲜期),而 funded 会在
// relay 的扣费事务里变假、allowOverflow 会被运营在管理端改掉 —— 两件事都不让
// 那份缓存失效。于是拿缓存去拒人两个方向都会错:套餐刚耗尽的那一分钟里
// allow_wallet_overflow=0 拦不住钱包(运营勾这个开关想禁止的**正是**这件事),
// 而运营刚把它改成 1 的那一分钟里用户照样吃 403。
//
// 每一行都让**缓存与主库反号**,断言闸门用的是主库那份。前两行是放行方向,
// 后两行是拒绝方向 —— 缺了放行方向,这条闸门就还漏着钱。
func TestFundingGateConfirmsWithMainDBBeforeDenying(t *testing.T) {
	const mg = "反重力的哈基米"
	cases := []struct {
		name string
		// 缓存(authoritative=false)与主库(true)各自的答案。unlocked 恒为 true,
		// 否则闸门在第一问就短路了,后面的判据根本不参与。
		cachedFunded, cachedOverflow bool
		dbFunded, dbOverflow         bool
		wantAllowed                  bool
		why                          string
	}{
		{
			name:         "缓存说还有余额、主库说已耗尽:必须拒绝",
			cachedFunded: true,
			wantAllowed:  false,
			why: "**这一行就是线上真的漏过钱的那一格**。订阅是在 relay 的扣费事务里被扣空的," +
				"没有任何一处让缓存失效,于是套餐刚耗尽的那一分钟里闸门照旧放行," +
				"钱包替一个运营明令禁止钱包续付的分组付了钱。" +
				"只在「即将拒绝」时才回主库确认是修不好它的 —— 那条路径根本走不到",
		},
		{
			name:           "缓存说允许回退、主库说运营已经关掉了:必须拒绝",
			cachedOverflow: true,
			wantAllowed:    false,
			why:            "运营刚把 allow_wallet_overflow 关掉,不该还有一分钟的宽限期继续花钱包",
		},
		{
			name:        "缓存说已耗尽、主库说还有余额:必须放行",
			dbFunded:    true,
			wantAllowed: true,
			why:         "管理员刚补了额度、或者周期重置刚落库,拿旧值拒人就是一次误拒",
		},
		{
			name:        "缓存说不许回退、主库说运营已经允许:必须放行",
			dbOverflow:  true,
			wantAllowed: true,
			why:         "运营改完还要再等一分钟才生效的话,工单已经进来了",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newTestDB(t)
			nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
			useUpstreamGroups(t,
				map[string]string{"default": "默认"},
				map[string]float64{"default": 1})
			authoritativeCalls := 0
			usePlanUnlock(t, func(_ int, _ string, authoritative bool) (bool, bool, bool) {
				if !authoritative {
					return true, tc.cachedFunded, tc.cachedOverflow
				}
				authoritativeCalls++
				return true, tc.dbFunded, tc.dbOverflow
			})

			allowed, _ := ModelGroupFundingAllowed(7, "default", mg)
			assert.Equalf(t, tc.wantAllowed, allowed, "理由:%s", tc.why)
			assert.Equal(t, 1, authoritativeCalls,
				"M 是套餐解锁的,余额与运营开关就必须回主库拿一次 —— 一次也不多")
		})
	}
}

// TestFundingGateShadowAlsoConfirmsWithMainDB 锁住影子档也走权威确认。
//
// 影子计数存在的唯一意义是回答「翻 enforce 安全吗」。用一份最长 60 秒前的缓存
// 数出来的数字回答不了那个问题:它会把"套餐刚扣空、但主库里运营早就允许回退"
// 这类根本不会拒的请求也记成一次,于是运营看着一个虚高的数字迟迟不敢翻档 ——
// 而真翻上去之后拒绝量又比预演的少,两次都白算。
//
// 断言落在**接缝被问的次数**上而不是计数器读数上:计数器由 guard.HotAsync 异步
// 自增,同步读它的话,闸门就算把这一笔记进去了,断言也照样绿 —— 那是一条永远
// 不会红的测试。
func TestFundingGateShadowAlsoConfirmsWithMainDB(t *testing.T) {
	const mg = "反重力的哈基米"
	newTestDB(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateShadow)
	useUpstreamGroups(t,
		map[string]string{"default": "默认"},
		map[string]float64{"default": 1})
	authoritativeCalls := 0
	usePlanUnlock(t, func(_ int, _ string, authoritative bool) (bool, bool, bool) {
		if !authoritative {
			return true, false, false // 缓存档:够拒绝
		}
		authoritativeCalls++
		return true, true, false // 主库:其实还有余额,这一笔根本不该记
	})

	allowed, _ := ModelGroupFundingAllowed(7, "default", mg)
	assert.True(t, allowed, "shadow 档本来就放行")
	assert.Equal(t, 1, authoritativeCalls,
		"影子档必须和 enforce 走同一条确认:两档的判据一旦分叉,"+
			"影子期数出来的量就预测不了翻档之后的量")
}

// TestFundingGateModeIsOnlyARolloutSwitch 锁住 funding_gate_mode 的语义:
// 它是**灰度开关**,不是第二条独立规则。
//
// 拒绝条件(不含 + 纯套餐解锁 + 耗尽 + O=0)固定不变,只翻 mode:
// off / shadow 一律放行,enforce 拒绝。让它携带独立语义会造出
// 「O=0 但 gate=off,到底扣不扣钱包」这种谁都没配出来的格子。
func TestFundingGateModeIsOnlyARolloutSwitch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		enabled     bool
		mode        string
		wantAllowed bool
	}{
		{"模块关闭", false, config.FundingGateEnforce, true},
		{"off(回滚档)", true, config.FundingGateOff, true},
		{"shadow(只记录)", true, config.FundingGateShadow, true},
		{"enforce(生效)", true, config.FundingGateEnforce, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newTestDB(t)
			nsConfig(t, tc.enabled, config.MissingRatioPolicyLegacyOne, tc.mode)
			useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
			usePlanUnlock(t, func(int, string, bool) (bool, bool, bool) {
				return true, false, false // 解锁了、已耗尽、且不许钱包续付
			})
			allowed, _ := ModelGroupFundingAllowed(7, "default", "反重力的哈基米")
			assert.Equal(t, tc.wantAllowed, allowed)
		})
	}
}

// TestFundingGateFailsOpenOnDegenerateInput 锁住 fail 方向:
// 匿名口径与空模型分组一律放行,不允许变成"少数人偶发 403"。
func TestFundingGateFailsOpenOnDegenerateInput(t *testing.T) {
	newTestDB(t)
	nsConfig(t, true, config.MissingRatioPolicyLegacyOne, config.FundingGateEnforce)
	useUpstreamGroups(t, map[string]string{"default": "默认"}, map[string]float64{"default": 1})
	usePlanUnlock(t, func(int, string, bool) (bool, bool, bool) { return true, false, false })

	for _, tc := range []struct {
		name       string
		userId     int
		modelGroup string
	}{
		{"匿名口径(userId<=0)", 0, "反重力的哈基米"},
		{"空模型分组", 7, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, reason := ModelGroupFundingAllowed(tc.userId, "default", tc.modelGroup)
			assert.True(t, allowed)
			assert.Empty(t, reason)
		})
	}
}
