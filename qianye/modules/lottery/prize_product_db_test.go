package lottery

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/mall"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// prize_product_db_test.go —— 商品奖(prize_type=product):转盘与批次抽奖把商城商品
// (兑换码 / 实物 / 套餐)当奖品发,中奖即在同一个扩展库事务里生成一张 0 元商城订单。
//
// 走的全是真实 handler:建活动(product 档进 spec 原像)→ 发布(商品存在 / 上架 /
// 库存够 Σcount 的闸门)→ 三种商品各中一次(订单形状、幂等重放不重复建单、套餐
// 立即生效且不返星屑)→ 用户侧三个接口的字段 → 揭示 → 证据链复算 PASS 并导出
// fixture 给 verify.py / verify.ts。批次抽奖那一支走 prob 开奖 + 出款 worker。

const testMallSecretKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// newProductPrizeEnv 在转盘环境上再装商城的表、主库的套餐表与商城密钥配置。
func newProductPrizeEnv(t *testing.T) *wheelEnv {
	t.Helper()
	env := newWheelEnv(t, map[int]int64{
		wheelAdminId: 1000, wheelUserA: 1000, wheelUserB: 1000, wheelUserC: 1000,
	})
	require.NoError(t, env.ext.AutoMigrate(mall.Tables()...))
	// 主库换成文件库:发订阅那条链在一个事务里还会用 model.DB 再开查询,内存库的
	// 单连接会把自己饿死(mall/testdb_test.go 同一条理由)。
	dsn := filepath.Join(t.TempDir(), "main.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	main, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := main.DB()
	require.NoError(t, err)
	require.NoError(t, main.AutoMigrate(&model.User{}, &model.SubscriptionPlan{}, &model.UserSubscription{},
		&model.SubscriptionOrder{}, &model.Log{}))
	for _, uid := range []int{wheelAdminId, wheelUserA, wheelUserB, wheelUserC} {
		require.NoError(t, main.Create(&model.User{
			Id: uid, Username: "wheel-" + strconv.Itoa(uid), Password: "x", AffCode: "aff" + strconv.Itoa(uid),
			Group: "default", Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		}).Error)
	}
	prevDB, prevLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = main, main
	model.InitCol()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = prevDB, prevLogDB
		model.InitCol()
		_ = sqlDB.Close()
	})
	prev := qyConfig.Load()
	next := *prev
	next.Runtime = config.Runtime{ColdPathTimeoutMs: 3000}
	next.Mall = config.Mall{Enabled: true, SecretKey: testMallSecretKey, SecretKeyVersion: 1,
		AddressRetentionDays: 90, MaxProducts: 200, CodeUploadMax: 500, PendingGraceSeconds: 60}
	next.Stardust = config.Stardust{Enabled: true, Name: "星屑"}
	qyConfig.Store(&next)
	invalidateSettings()
	as := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", env.user); h(c) }
	}
	env.router.GET("/lottery/my-entries", as(handleListMyEntries))
	env.router.GET("/lottery/my/prizes/:payout_no", as(handleGetMyPrize))
	return env
}

// seedMallProduct 直接落一件商城商品;code 类再按 codes 塞几枚 unused 码(密文内容
// 不重要:发码只改状态,不解密)。
func seedMallProduct(t *testing.T, ext *gorm.DB, kind string, stock int, mutate func(*mall.Product), codes int) *mall.Product {
	t.Helper()
	now := common.GetTimestamp()
	p := &mall.Product{
		ProductNo: "PD-" + strings.ToUpper(kind) + "-" + common.GetUUID()[:8], Kind: kind, Title: kind + " 奖品",
		Price: 30, Stock: stock, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if kind == mall.KindCode {
		p.Stock = mall.StockUnlimited
	}
	if mutate != nil {
		mutate(p)
	}
	require.NoError(t, ext.Create(p).Error)
	for i := 0; i < codes; i++ {
		require.NoError(t, ext.Create(&mall.CodeStock{
			ProductId: p.Id, Status: mall.CodeUnused, CodeCipher: []byte("c"), CodeNonce: []byte("n"), CreatedAt: now,
		}).Error)
	}
	return p
}

func seedMainPlan(t *testing.T) *model.SubscriptionPlan {
	t.Helper()
	plan := &model.SubscriptionPlan{
		Id: 7001, Title: "月卡", PriceAmount: 9.9, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
		Enabled: true, TotalAmount: 1000, QuotaResetPeriod: "never",
	}
	require.NoError(t, model.DB.Create(plan).Error)
	return plan
}

func productTier(tier int, name string, p *mall.Product, count, ppm int) map[string]any {
	return map[string]any{
		"tier": tier, "name": name, "count": count, "win_ppm": ppm,
		"prize_type": PrizeTypeProduct, "product_no": p.ProductNo,
	}
}

func mallOrderOf(t *testing.T, ext *gorm.DB, orderNo string) mall.Order {
	t.Helper()
	var o mall.Order
	require.NoError(t, ext.Where("order_no = ?", orderNo).Take(&o).Error)
	return o
}

func TestWheelProductPrizesEndToEnd(t *testing.T) {
	env := newProductPrizeEnv(t)
	ext := env.ext
	plan := seedMainPlan(t)
	codeP := seedMallProduct(t, ext, mall.KindCode, 0, nil, 2)
	physP := seedMallProduct(t, ext, mall.KindPhysical, 3, nil, 0)
	planP := seedMallProduct(t, ext, mall.KindPlan, mall.StockUnlimited, func(p *mall.Product) { p.PlanId = plan.Id }, 0)

	// 区间:code [0,1e5) / physical [1e5,2e5) / plan [2e5,3e5) / 星屑 [3e5,4e5) / 谢谢参与 [4e5,1e6)。
	body := wheelCreateBody(t, func(m map[string]any) {
		m["prizes"] = []map[string]any{
			productTier(1, "兑换码奖", codeP, 1, 100000),
			productTier(2, "实物奖", physP, 1, 100000),
			productTier(3, "月卡奖", planP, 1, 100000),
			{"tier": 4, "name": "星屑奖", "amount_quota": 500, "count": 1, "prize_type": PrizeTypeQuota, "win_ppm": 100000},
		}
	})
	env.user = wheelAdminId
	code, raw := env.call(t, http.MethodPost, "/admin/lottery/activities", body)
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	actNo := jsonString(t, raw, "data", "act_no")
	var created struct {
		Data activityWriteResult `json:"data"`
	}
	require.NoError(t, common.Unmarshal(raw, &created))
	assert.EqualValues(t, 500, created.Data.PrizeTotalQuota, "商品奖不占星屑总额")
	assert.Zero(t, created.Data.WorstCaseTextGrants, "商品奖不数进人工填码的口径")
	assert.EqualValues(t, 4, created.Data.ExpectWinners)

	var act Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
	var prizes []Prize
	require.NoError(t, ext.Where("act_id = ?", act.Id).Order("tier asc").Find(&prizes).Error)
	require.Len(t, prizes, 5)
	assert.Equal(t, PrizeTypeProduct, prizes[0].PrizeType)
	assert.Equal(t, codeP.ProductNo, prizes[0].ProductNo)
	assert.Equal(t, 1, prizes[0].StockLeft)
	// 商品号进了 spec 原像:按 11 位的 PrizeSpecLineV2 重算必须等于落库的 spec_hash。
	lines := make([]string, 0, len(prizes))
	for _, p := range prizes {
		lines = append(lines, prizeSpecLineOf(AlgoV2, p))
	}
	assert.Equal(t, SpecHashV2(lines), act.SpecHash)
	assert.Contains(t, act.SpecText, codeP.ProductNo)

	code, raw = env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/publish", "{}")
	require.Equalf(t, http.StatusOK, code, "发布失败: %s", raw)
	seedHex := seedOf(t, ext, act.Id)
	spinPath := "/lottery/activities/" + actNo + "/spins"

	// 用户侧详情:商品奖档带商品号 / 名称 / 种类。
	detail := activityDetailOf(t, env.router, actNo)
	require.Len(t, detail.Spec, 5)
	assert.Equal(t, PrizeTypeProduct, detail.Spec[0].PrizeType)
	assert.Equal(t, codeP.ProductNo, detail.Spec[0].ProductNo)
	assert.Equal(t, codeP.Title, detail.Spec[0].ProductTitle)
	assert.Equal(t, mall.KindCode, detail.Spec[0].ProductKind)
	assert.Empty(t, detail.Spec[3].ProductNo)

	// ── 第 1 转(A):兑换码 → 订单当场 done、码已发 ──
	env.user = wheelUserA
	cs1 := steerClientSeed(t, seedHex, actNo, 1, 0, 100000)
	code, raw = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-1", cs1))
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	s1 := decodeSpin(t, raw)
	assert.Equal(t, 1, s1.ResultTier)
	assert.Equal(t, PrizeTypeProduct, s1.PrizeType)
	assert.Equal(t, codeP.ProductNo, s1.ProductNo)
	require.NotEmpty(t, s1.MallOrderNo)
	assert.Zero(t, s1.Amount)
	o1 := mallOrderOf(t, ext, s1.MallOrderNo)
	assert.Equal(t, mall.SourceLottery, o1.Source)
	assert.Equal(t, mall.KindCode, o1.Kind)
	assert.Equal(t, mall.StatusDone, o1.Status)
	assert.Equal(t, wheelUserA, o1.UserId)
	assert.Zero(t, o1.Price)
	assert.NotZero(t, o1.CodeStockId)
	assert.EqualValues(t, 1000-wheelStake, stardustOf(t, ext, wheelUserA), "只扣参与费,商品奖不动账本")
	var payout Payout
	require.NoError(t, ext.Where("act_id = ? AND kind = ?", act.Id, PayoutProduct).Take(&payout).Error)
	assert.Equal(t, PayoutGranted, payout.Status)
	assert.Equal(t, s1.MallOrderNo, payout.MallOrderNo)
	assert.Equal(t, "lotprize:"+payout.PayoutNo, o1.IdemKey)
	assert.Equal(t, payout.PayoutNo, o1.RefNo)

	// 幂等重放:同一张单、不再建单。
	code, raw = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-1", cs1))
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	replayed := decodeSpin(t, raw)
	assert.True(t, replayed.Replayed)
	assert.Equal(t, s1.MallOrderNo, replayed.MallOrderNo)
	assert.Equal(t, codeP.ProductNo, replayed.ProductNo)
	var orders int64
	require.NoError(t, ext.Model(&mall.Order{}).Where("source = ?", mall.SourceLottery).Count(&orders).Error)
	assert.EqualValues(t, 1, orders)

	// ── 第 2 转(B):实物 → 订单 paid、等中奖者补地址 ──
	env.user = wheelUserB
	cs2 := steerClientSeed(t, seedHex, actNo, 2, 100000, 200000)
	code, raw = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-2", cs2))
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	s2 := decodeSpin(t, raw)
	assert.Equal(t, physP.ProductNo, s2.ProductNo)
	o2 := mallOrderOf(t, ext, s2.MallOrderNo)
	assert.Equal(t, mall.StatusPaid, o2.Status)
	assert.Zero(t, o2.AddressSetAt, "中奖那一刻没有地址")
	var physRow mall.Product
	require.NoError(t, ext.Where("id = ?", physP.Id).Take(&physRow).Error)
	assert.Equal(t, 1, physRow.Sold, "商城那一侧的库存跟着少一件")

	// ── 第 3 转(C):套餐 → 立即生效,source=lottery,**不返星屑** ──
	env.user = wheelUserC
	cs3 := steerClientSeed(t, seedHex, actNo, 3, 200000, 300000)
	code, raw = env.call(t, http.MethodPost, spinPath, spinBody(t, "spin-3", cs3))
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	s3 := decodeSpin(t, raw)
	assert.Equal(t, planP.ProductNo, s3.ProductNo)
	o3 := mallOrderOf(t, ext, s3.MallOrderNo)
	assert.Equal(t, mall.StatusDone, o3.Status, "套餐奖在转动回执返回之前就已发放")
	assert.NotZero(t, o3.UserSubscriptionId)
	assert.Empty(t, o3.FundOrderNo)
	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ? AND plan_id = ?", wheelUserC, plan.Id).Take(&sub).Error)
	assert.Equal(t, "lottery", sub.Source)
	assert.Equal(t, sub.Id, o3.UserSubscriptionId)
	kinds := make([]string, 0, 2)
	for _, row := range ledgerRowsOf(t, ext, wheelUserC, actNo) {
		kinds = append(kinds, row.Kind)
	}
	assert.Equal(t, []string{string(stardust.KindLotStake)}, kinds, "只有参与费那一笔")
	var rebates int64
	require.NoError(t, ext.Model(&stardust.Ledger{}).
		Where("user_id = ? AND kind IN ?", wheelUserC, []string{string(stardust.KindPlanBuyer), string(stardust.KindPlanInviter)}).
		Count(&rebates).Error)
	assert.Zero(t, rebates, "抽中的套餐不触发套餐返")
	assert.False(t, stardust.DefaultPlanReward(plan.Id).SourceAllowed("lottery"), "来源闭集不认 lottery")

	// ── 用户侧三个接口的字段 ──
	env.user = wheelUserA
	code, raw = env.call(t, http.MethodGet, "/lottery/activities/"+actNo+"/spins/me", "")
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	var mine struct {
		Data struct {
			Items []mySpinView `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(raw, &mine))
	require.Len(t, mine.Data.Items, 1)
	assert.Equal(t, PrizeTypeProduct, mine.Data.Items[0].PrizeType)
	assert.Equal(t, codeP.ProductNo, mine.Data.Items[0].ProductNo)
	assert.Equal(t, s1.MallOrderNo, mine.Data.Items[0].MallOrderNo)

	code, raw = env.call(t, http.MethodGet, "/lottery/my-entries", "")
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	var entries struct {
		Data struct {
			Items []myEntryView `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(raw, &entries))
	require.Len(t, entries.Data.Items, 1)
	require.NotNil(t, entries.Data.Items[0].Won)
	won := entries.Data.Items[0].Won
	assert.Equal(t, PayoutProduct, won.Kind)
	assert.Equal(t, PrizeTypeProduct, won.PrizeType)
	assert.Equal(t, payout.PayoutNo, won.PayoutNo)
	assert.Equal(t, codeP.ProductNo, won.ProductNo)
	assert.Equal(t, s1.MallOrderNo, won.MallOrderNo)

	code, raw = env.call(t, http.MethodGet, "/lottery/my/prizes/"+payout.PayoutNo, "")
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	var prize struct {
		Data myPrizeView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(raw, &prize))
	assert.Equal(t, PrizeTypeProduct, prize.Data.PrizeType)
	assert.Equal(t, codeP.ProductNo, prize.Data.ProductNo)
	assert.Equal(t, s1.MallOrderNo, prize.Data.MallOrderNo)
	assert.Equal(t, PayoutGranted, prize.Data.Status)
	assert.Empty(t, prize.Data.Secret)
	env.user = wheelUserB
	code, _ = env.call(t, http.MethodGet, "/lottery/my/prizes/"+payout.PayoutNo, "")
	assert.Equal(t, http.StatusNotFound, code, "别人的奖品 404")

	// ── 账面:三笔 product/granted、text_grant_count 与 payout_quota 都不动 ──
	var payouts []Payout
	require.NoError(t, ext.Where("act_id = ?", act.Id).Find(&payouts).Error)
	require.Len(t, payouts, 3)
	for _, p := range payouts {
		assert.Equal(t, PayoutProduct, p.Kind)
		assert.Equal(t, PayoutGranted, p.Status)
		assert.NotEmpty(t, p.MallOrderNo)
	}
	cur := loadAct(t, ext, act.Id)
	assert.Zero(t, cur.TextGrantCount)
	assert.Zero(t, cur.PayoutQuota)

	// ── 揭示 → 收尾 → 证据链 ──
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Update("close_at", common.GetTimestamp()-1).Error)
	require.NoError(t, lockActivity(context.Background(), ext, loadAct(t, ext, act.Id)))
	require.NoError(t, revealActivity(context.Background(), ext, loadAct(t, ext, act.Id)))
	runSettle(context.Background())
	assert.Equal(t, StatusFinished, loadAct(t, ext, act.Id).Status, "商品奖是终态 granted,不挡收尾")

	code, raw = env.call(t, http.MethodGet, "/lottery/public/"+actNo+"/proof?page_size=1000", "")
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	var envelope struct {
		Data proofDocument `json:"data"`
	}
	require.NoError(t, common.Unmarshal(raw, &envelope))
	doc := &envelope.Data
	require.Len(t, doc.Tiers, 5)
	assert.Equal(t, codeP.ProductNo, doc.Tiers[0].ProductNo)
	assert.Equal(t, PrizeTypeProduct, doc.Tiers[0].PrizeType)
	assert.Zero(t, doc.Tiers[0].StockLeft)
	assert.Equal(t, physP.ProductNo, doc.Spec[1].ProductNo)
	require.Len(t, doc.Winners, 3)
	for _, w := range doc.Winners {
		assert.Equal(t, PrizeTypeProduct, w.PrizeType)
		assert.Zero(t, w.Amount)
	}
	assert.NotContains(t, string(raw), s1.MallOrderNo, "商城单号不进匿名证据链")
	// 验证者按 11 位原像重算 spec_hash;票面与库存重放照旧。
	specLines := make([]string, 0, len(doc.Spec))
	for _, s := range doc.Spec {
		specLines = append(specLines, PrizeSpecLineV2(PrizeSpec{
			Tier: s.Tier, Name: s.Name, PrizeType: s.PrizeType, AmountQuota: s.AmountQuota, Count: s.Count,
			WinPpm: s.WinPpm, TextDesc: s.TextDesc, RedMatch: s.RedMatch, BlueMatch: s.BlueMatch,
			PoolShareBps: s.PoolShareBps, ProductNo: s.ProductNo,
		}))
	}
	assert.Equal(t, doc.SpecHash, SpecHashV2(specLines))
	replay := independentWheelReplay(t, doc)
	assert.Equal(t, replay.chainHead, doc.ChainHead)
	assert.Empty(t, replay.mismatches)
	assert.Equal(t, map[int]int{1: 0, 2: 0, 3: 0, 4: 1}, replay.stock)

	if out := os.Getenv("QY_WHEEL_PRODUCT_PROOF_OUT"); out != "" {
		line, err := common.Marshal(doc)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out, line, 0o644))
	}
}

// 兑换码库存在发布后被商城卖空:中奖不丢,订单停在 paid、活动挂旗 prize_code_short。
func TestWheelCodePrizeShortStockAwaitsAndFlags(t *testing.T) {
	env := newProductPrizeEnv(t)
	ext := env.ext
	codeP := seedMallProduct(t, ext, mall.KindCode, 0, nil, 1)
	body := wheelCreateBody(t, func(m map[string]any) {
		m["prizes"] = []map[string]any{productTier(1, "兑换码奖", codeP, 1, 500000)}
	})
	env.user = wheelAdminId
	code, raw := env.call(t, http.MethodPost, "/admin/lottery/activities", body)
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	actNo := jsonString(t, raw, "data", "act_no")
	code, raw = env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/publish", "{}")
	require.Equalf(t, http.StatusOK, code, "发布时库存够: %s", raw)
	var act Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
	// 发布之后码被商城卖掉。
	require.NoError(t, ext.Where("product_id = ?", codeP.Id).Delete(&mall.CodeStock{}).Error)

	env.user = wheelUserA
	cs := steerClientSeed(t, seedOf(t, ext, act.Id), actNo, 1, 0, 500000)
	code, raw = env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "spin-1", cs))
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	s := decodeSpin(t, raw)
	require.NotEmpty(t, s.MallOrderNo)
	o := mallOrderOf(t, ext, s.MallOrderNo)
	assert.Equal(t, mall.StatusPaid, o.Status, "码不够:订单停在 paid 等补码,中奖不丢")
	assert.Zero(t, o.CodeStockId)
	var flags []Flag
	require.NoError(t, ext.Where("act_id = ? AND code = ?", act.Id, FlagPrizeCodeShort).Find(&flags).Error)
	require.Len(t, flags, 1)
	assert.Contains(t, flags[0].Detail, s.MallOrderNo)

	// 重放:同一张单,不再建、不再告警一遍。
	code, raw = env.call(t, http.MethodPost, "/lottery/activities/"+actNo+"/spins", spinBody(t, "spin-1", cs))
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	assert.Equal(t, s.MallOrderNo, decodeSpin(t, raw).MallOrderNo)
	require.NoError(t, ext.Where("act_id = ? AND code = ?", act.Id, FlagPrizeCodeShort).Find(&flags).Error)
	assert.Len(t, flags, 1)
}

// 发布期闸门:商品不存在 / 未上架 / 未用码不够 / 实物余量不够,四条都是 400
// qy_lot_prize_product_short;草稿照建(创建期只认格式)。
func TestPublishRejectsProductPrizeShortfalls(t *testing.T) {
	env := newProductPrizeEnv(t)
	ext := env.ext
	env.user = wheelAdminId
	disabled := seedMallProduct(t, ext, mall.KindPhysical, 5, func(p *mall.Product) { p.Enabled = false }, 0)
	oneCode := seedMallProduct(t, ext, mall.KindCode, 0, nil, 1)
	soldOut := seedMallProduct(t, ext, mall.KindPhysical, 2, func(p *mall.Product) { p.Sold = 2 }, 0)
	missing := &mall.Product{ProductNo: "PD-MISSING"}

	cases := []struct {
		name  string
		tiers []map[string]any
		want  string
	}{
		{"商品不存在", []map[string]any{productTier(1, "x", missing, 1, 500000)}, "不存在"},
		{"商品未上架", []map[string]any{productTier(1, "x", disabled, 1, 500000)}, "未上架"},
		{"未用码少于 Σcount", []map[string]any{productTier(1, "x", oneCode, 2, 500000)}, "未用兑换码"},
		{"实物余量少于 Σcount", []map[string]any{productTier(1, "x", soldOut, 1, 500000)}, "库存"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := wheelCreateBody(t, func(m map[string]any) { m["prizes"] = tc.tiers })
			code, raw := env.call(t, http.MethodPost, "/admin/lottery/activities", body)
			require.Equalf(t, http.StatusOK, code, "草稿期不核商品: %s", raw)
			actNo := jsonString(t, raw, "data", "act_no")
			code, raw = env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/publish", "{}")
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Equal(t, "qy_lot_prize_product_short", errorCode(t, raw))
			assert.Contains(t, string(raw), tc.want)
			assert.Equal(t, StatusDraft, loadActByNo(t, ext, actNo).Status)
		})
	}

	// 够就放行:同一件只有一枚码的商品,count=1 可以发布。
	body := wheelCreateBody(t, func(m map[string]any) {
		m["prizes"] = []map[string]any{productTier(1, "x", oneCode, 1, 500000)}
	})
	code, raw := env.call(t, http.MethodPost, "/admin/lottery/activities", body)
	require.Equalf(t, http.StatusOK, code, "%s", raw)
	code, raw = env.call(t, http.MethodPost, "/admin/lottery/activities/"+jsonString(t, raw, "data", "act_no")+"/publish", "{}")
	assert.Equalf(t, http.StatusOK, code, "%s", raw)
}

// 创建期的格式规则:商品奖必须带 product_no、金额与文本说明必须为 0 / 空;
// 非商品奖不许带 product_no;双色球不许商品奖。
func TestNormalizePrizeTypeProductRules(t *testing.T) {
	ok, desc, no, err := normalizePrizeType(prizeInput{PrizeType: PrizeTypeProduct, ProductNo: " PD-1 "}, DrawModeWheel)
	require.NoError(t, err)
	assert.Equal(t, PrizeTypeProduct, ok)
	assert.Empty(t, desc)
	assert.Equal(t, "PD-1", no)

	for name, in := range map[string]struct {
		p    prizeInput
		mode string
	}{
		"缺 product_no":    {prizeInput{PrizeType: PrizeTypeProduct}, DrawModeProb},
		"带星屑数":            {prizeInput{PrizeType: PrizeTypeProduct, ProductNo: "PD-1", AmountQuota: 1}, DrawModeRank},
		"带文本说明":           {prizeInput{PrizeType: PrizeTypeProduct, ProductNo: "PD-1", TextDesc: "x"}, DrawModeRank},
		"双色球":             {prizeInput{PrizeType: PrizeTypeProduct, ProductNo: "PD-1"}, DrawModeBall},
		"星屑奖带 product_no": {prizeInput{PrizeType: PrizeTypeQuota, ProductNo: "PD-1", AmountQuota: 1}, DrawModeRank},
		"文本奖带 product_no": {prizeInput{PrizeType: PrizeTypeText, ProductNo: "PD-1", TextDesc: "x"}, DrawModeRank},
		"商品号里有控制字符":       {prizeInput{PrizeType: PrizeTypeProduct, ProductNo: "PD\x1f1"}, DrawModeRank},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := normalizePrizeType(in.p, in.mode)
			assert.Error(t, err)
		})
	}
}

// 批次抽奖(prob):商品奖落 planned,由出款 worker 在扩展库里生成商城订单后落 granted;
// worker 重入不重复建单;收尾不被商品奖挡住。
func TestProbDrawGrantsProductPrizeViaWorker(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8, EntryCloseGraceSeconds: 0, RevealDelaySeconds: 0,
		MaxStakeStardust: 5_000_000,
	})
	require.NoError(t, gdb.AutoMigrate(mall.Tables()...))
	physP := seedMallProduct(t, gdb, mall.KindPhysical, mall.StockUnlimited, nil, 0)

	now := common.GetTimestamp()
	prizes := []Prize{
		{Tier: 1, Name: "实物奖", Count: 50, PrizeType: PrizeTypeProduct, ProductNo: physP.ProductNo, WinPpm: 600000},
		{Tier: 2, Name: "星屑奖", AmountQuota: 2000, Count: 50, PrizeType: PrizeTypeQuota, WinPpm: 400000},
	}
	specLines := make([]string, 0, len(prizes))
	for _, p := range prizes {
		specLines = append(specLines, prizeSpecLineOf(AlgoV2, p))
	}
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusDraft
		a.Kind = KindDraw
		a.Algo = AlgoV2
		a.DrawMode = DrawModeProb
		a.AllowMultiWin = true
		a.OpenAt = now - 3600
		a.CloseAt = now - 2
		a.DrawAt = now - 1
		a.RulesText = `{"min_quota":0}`
		a.RulesHash = RulesHash(`{"min_quota":0}`)
		a.SpecHash = SpecHashV2(specLines)
		a.SpecText = strings.Join(specLines, SEP)
		a.CommitHash = ""
	})
	require.NoError(t, gdb.Create(&Seed{ActId: act.Id, Seed: newSecret(), RefSalt: newSecret(), IpSalt: newSecret(), CreatedAt: now}).Error)
	for i := range prizes {
		prizes[i].ActId = act.Id
	}
	require.NoError(t, gdb.Create(&prizes).Error)
	commit, err := computeCommit(context.Background(), gdb, act)
	require.NoError(t, err)
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).Updates(map[string]any{
		"status": StatusPublished, "commit_hash": commit, "chain_head": commit, "published_at": now, "close_at": now + 3600,
	}).Error)
	act = loadAct(t, gdb, act.Id)
	salts, err := loadSalts(context.Background(), gdb, act.Id)
	require.NoError(t, err)
	for uid := 301; uid < 311; uid++ {
		seedTicket(t, gdb, act, &Entry{UserId: uid, UserRef: UserRef(salts.RefSalt, uid), Amount: act.StakeQuota})
	}
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).Update("close_at", now-2).Error)
	runLock(context.Background())
	runReveal(context.Background())
	drawn := loadAct(t, gdb, act.Id)
	require.Equal(t, StatusSettling, drawn.Status)

	var productPlanned int64
	require.NoError(t, gdb.Model(&Payout{}).Where("act_id = ? AND kind = ? AND status = ?", act.Id, PayoutProduct, PayoutPlanned).Count(&productPlanned).Error)
	require.Positive(t, productPlanned, "两档合计 100%,商品奖那一档必然有人中;它落 planned 交给 worker")
	assert.Zero(t, drawn.TextGrantCount)

	DrivePayouts(context.Background())
	DrivePayouts(context.Background())
	var granted []Payout
	require.NoError(t, gdb.Where("act_id = ? AND kind = ?", act.Id, PayoutProduct).Find(&granted).Error)
	require.EqualValues(t, productPlanned, len(granted))
	var orders int64
	require.NoError(t, gdb.Model(&mall.Order{}).Where("source = ?", mall.SourceLottery).Count(&orders).Error)
	assert.EqualValues(t, productPlanned, orders, "每个中奖位恰好一张商城单,worker 跑两遍也不重复建单")
	for _, p := range granted {
		assert.Equal(t, PayoutGranted, p.Status)
		require.NotEmpty(t, p.MallOrderNo)
		o := mallOrderOf(t, gdb, p.MallOrderNo)
		assert.Equal(t, p.UserId, o.UserId)
		assert.Equal(t, mall.StatusPaid, o.Status)
		assert.Equal(t, "lotprize:"+p.PayoutNo, o.IdemKey)
	}
	var physRow mall.Product
	require.NoError(t, gdb.Where("id = ?", physP.Id).Take(&physRow).Error)
	assert.EqualValues(t, productPlanned, physRow.Sold)

	runSettle(context.Background())
	assert.Equal(t, StatusFinished, loadAct(t, gdb, act.Id).Status)
}

// 商品还挂在进行中的活动上时,商城的删除闸门(mall.ProductReferenced)要答"是"。
func TestProductReferencedByLiveActivity(t *testing.T) {
	gdb := newPayoutEnv(t, config.Lottery{Enabled: true})
	require.NoError(t, gdb.AutoMigrate(mall.Tables()...))
	live := seedActivity(t, gdb, func(a *Activity) { a.Kind = KindDraw; a.DrawMode = DrawModeWheel })
	done := seedActivity(t, gdb, func(a *Activity) { a.Kind = KindDraw; a.Status = StatusFinished })
	require.NoError(t, gdb.Create(&[]Prize{
		{ActId: live.Id, Tier: 1, Name: "a", Count: 1, PrizeType: PrizeTypeProduct, ProductNo: "PD-LIVE"},
		{ActId: done.Id, Tier: 1, Name: "b", Count: 1, PrizeType: PrizeTypeProduct, ProductNo: "PD-DONE"},
	}).Error)
	ctx := context.Background()
	got, err := productReferenced(ctx, "PD-LIVE")
	require.NoError(t, err)
	assert.True(t, got)
	got, err = productReferenced(ctx, "PD-DONE")
	require.NoError(t, err)
	assert.False(t, got, "已结束的活动不再建单,不挡删除")
	got, err = productReferenced(ctx, "PD-NONE")
	require.NoError(t, err)
	assert.False(t, got)
}
