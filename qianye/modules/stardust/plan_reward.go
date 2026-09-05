package stardust

import (
	"context"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm/clause"
)

// plan_reward.go —— 套餐上的星屑返还定义(qy_sd_plan_reward)的读写与缓存。
//
// 读路径在购买事件的 hook 里(hooks.go),写路径在管理端接口里(api_admin_rates.go),
// 两边由不同的人并行写,所以数据访问先在这里定死:读只经 planRewardFor,
// 写只经 savePlanReward / deletePlanReward,写完必须 invalidatePlanRewards。

// DefaultPlanReward 是没有配过的套餐的口径:按售价 1:1 返给买家、不返上线、
// 只有真花了钱的两条来源返(项目方原话"默认是根据售价一比一返还,或者直接不返还")。
func DefaultPlanReward(planId int) PlanReward {
	return PlanReward{PlanId: planId, BuyerBps: 10_000, InviterBps: 0, Sources: DefaultPlanRewardSources}
}

// SourceAllowed 回答"这个来源要不要返"。`stardust`(商城自购)不在闭集里,
// NormalizeSources 根本不会让它进 Sources 列,这里自然返回 false —— 星屑买套餐再返
// 星屑是同一种积分的自供回路,不允许被配置打开(design-15 D-F)。
func (r PlanReward) SourceAllowed(source string) bool {
	for _, s := range strings.Split(r.Sources, ",") {
		if s == source {
			return true
		}
	}
	return false
}

var (
	planRewardMu     sync.Mutex
	planRewardCache  map[int]PlanReward
	planRewardLoaded int64
	planRewardEpoch  uint64
)

// planRewards 返回全表快照(套餐数量是几十的量级,整表缓存比逐个点查省事),
// 结构与 groupRates 一致:临界区外 SELECT,写回时按代次判断快照是否已被作废。
func planRewards(ctx context.Context) map[int]PlanReward {
	planRewardMu.Lock()
	if planRewardCache != nil && common.GetTimestamp()-planRewardLoaded < settingsCacheSeconds {
		cached := planRewardCache
		planRewardMu.Unlock()
		return cached
	}
	epoch := planRewardEpoch
	prior := planRewardCache
	planRewardMu.Unlock()

	gdb := db.Get()
	if gdb == nil {
		if prior != nil {
			return prior
		}
		common.SysError("qianye/stardust: 读取套餐返还定义失败,本轮按默认口径发放: " + db.ErrNotReady.Error())
		return map[int]PlanReward{}
	}
	var rows []PlanReward
	if err := gdb.WithContext(ctx).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		if prior != nil {
			return prior
		}
		common.SysError("qianye/stardust: 读取套餐返还定义失败,本轮按默认口径发放: " + err.Error())
		return map[int]PlanReward{}
	}
	fresh := make(map[int]PlanReward, len(rows))
	for _, r := range rows {
		fresh[r.PlanId] = r
	}
	planRewardMu.Lock()
	if epoch == planRewardEpoch {
		planRewardCache = fresh
		planRewardLoaded = common.GetTimestamp()
	}
	planRewardMu.Unlock()
	return fresh
}

// planRewardFor 返回套餐的返还定义;没配过的返回 DefaultPlanReward,第二个返回值说明是否配过。
func planRewardFor(ctx context.Context, planId int) (PlanReward, bool) {
	if r, ok := planRewards(ctx)[planId]; ok {
		return r, true
	}
	return DefaultPlanReward(planId), false
}

// savePlanReward 写入(upsert)一份返还定义。Sources 必须已经过 NormalizeSources。
func savePlanReward(ctx context.Context, r PlanReward) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	r.UpdatedAt = common.GetTimestamp()
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "plan_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"buyer_bps", "inviter_bps", "sources", "operator_id", "updated_at"}),
	}).Create(&r).Error
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	invalidatePlanRewards()
	return nil
}

// deletePlanReward 删掉一份返还定义,此后该套餐回到 DefaultPlanReward。
func deletePlanReward(ctx context.Context, planId int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	if err := gdb.WithContext(ctx).Where("plan_id = ?", planId).Delete(&PlanReward{}).Error; err != nil {
		db.MarkFailure(err)
		return err
	}
	invalidatePlanRewards()
	return nil
}

// invalidatePlanRewards 失效本进程的套餐返还快照。
func invalidatePlanRewards() {
	planRewardMu.Lock()
	planRewardCache = nil
	planRewardLoaded = 0
	planRewardEpoch++
	planRewardMu.Unlock()
}
