package stardust

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/singleflight"
)

// forecast.go —— 「明日预计到账」:按今天到现在的消费,估下一次结算会记入多少星屑。
//
// 结算的时序是"今天花、明天结"(§4.1):一次日结只结**昨天**那一桶,所以今天这一桶
// 要到 nextDayStart(now) + settle_delay 才发。这一条接口回答的就是那一刻会到多少 ——
// 用户端此前只看得见"昨天发了多少",于是每一个白天都在盯一个不会动的数字。
//
// 两条线都算(D-16「两条线都发星屑」):消费返按我自己的消费,下线消费返按我名下
// 绑定中、未拉黑的下线的消费。两条在同一次运行里结、各自 floor、各自结转,所以
// 这里也分开算再相加 —— 把两笔 gross 先加起来再取整会比账本多发一颗星屑。
//
// **它是估算,不是账**:口径与日结逐字相同(aggregateDayConsume 的排除项、grossOf 的
// 截断、floor(carry + Σgross)),但四个输入在结算之前都还会变 —— 今天还没过完、
// 分组可能换、比例可能被运营改、账号可能在结算那一刻处于暂缓。所以这一条不写任何
// 库、不建任何桶,只读。
//
// # 为什么要缓存
//
// 消费聚合打的是 LOG_DB,而下线那一段还要按最多 500 个 user_id 扫一遍。余额页是
// 星屑宿主的第一张标签、每次进星屑都会打开,不缓存等于把一次日结级别的聚合挂在
// 一个人人都会点的页面上。所以:进程内缓存一小时(forecastTTL),到期后下一次请求
// 自己重算;用户可以手动刷新(?refresh=1),但两次真正的重算之间至少隔
// forecastManualMinSecs —— 否则那颗按钮就是一个人人可按的 LOG_DB 压测器。
//
// 缓存是 per-node 的(与 invite/inviter.go 同一个判断):多节点部署下手动刷新只
// 刷新当前这一节点。代价是下一次请求打到别的节点时可能看到稍旧的数,而响应里带着
// computed_at,界面上写的就是"数据截至"那一刻,不会假装它是实时的。

const (
	// forecastTTL 是一份估算的寿命。项目方定的口径:这个数一小时刷新一次。
	forecastTTL = time.Hour
	// forecastManualMinSecs 是两次**真正重算**之间的最小间隔。手动刷新在这个窗口
	// 之内直接拿回缓存那一份(响应里的 refresh_after 告诉界面按钮什么时候才有用)。
	forecastManualMinSecs = 60
	// forecastCacheCapacity 是缓存的条目上界。它是 LRU 的容量而不是"预期用户数":
	// 无界 map 在一个人人可访问的接口后面就是一条内存泄漏。
	forecastCacheCapacity = 50_000
)

// forecastLine 是一条获得线的估算:基数 → 比例 → 计提 → 加上结转余数 → 取整。
//
// 四个中间量全部下发,理由与昨日摘要那张卡相同:不摆出来的话"为什么消费了一整天
// 还是 0"没有答案 —— 答案通常是零头还没攒够 1 颗。
//
// 没有 json 标签:下发的形状由 handleGetForecast 的 gin.H 写死(照 handleGetMe),
// 因为下线那一条线还要多带 applies / counted 两个旗标,两条线不是同一个 JSON 形状。
type forecastLine struct {
	BaseQuota int64
	RateBps   int
	Gross     decimal.Decimal
	Carry     decimal.Decimal
	Estimated int64
}

// forecastData 是一份估算的全部内容,也是缓存里存的东西。
type forecastData struct {
	// Day 是今天这一桶的桶日;SettleAt 是它被结算的时刻。
	//
	// SettleAt 刻意不复用 /stardust/me 的 next_settle_at:那一个回答的是"下一次
	// 结算在什么时候",今天的门槛还没过时它指的是今天这一跑;而这里要回答的永远是
	// **今天这一桶**什么时候结,那恒等于下一个日界 + 延迟。
	Day        string
	SettleAt   int64
	ComputedAt int64
	HoldReason string
	Consume    forecastLine
	Invite     forecastLine
	// InviteApplies 表示下线消费返这条线对我生效(邀请功能开着且我这一档比例 > 0)。
	// 不生效时不扫 LOG_DB,界面上整条线不出现。
	InviteApplies bool
	// InviteCounted 为假表示下线太多(> maxPendingInvitees),这一次没扫 ——
	// 与"扫了、是 0"必须分得开,否则界面会把"没算"说成"没有"。
	InviteCounted bool
	Total         int64
}

var (
	forecastCache = hot.NewHotCache[int, forecastData](hot.LRU, forecastCacheCapacity).
			WithTTL(forecastTTL).
			WithJanitor().
			Build()
	forecastSF singleflight.Group
)

func init() {
	userRouteInstallers = append(userRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/stardust/forecast", handleGetForecast)
	})
}

// handleGetForecast 下发我的「明日预计到账」。?refresh=1 请求重算。
func handleGetForecast(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	force := c.Query("refresh") == "1" || c.Query("refresh") == "true"
	// 冷路径预算(runtime.cold_path_timeout_ms):估算要扫 LOG_DB,而
	// aggregateDayConsume 自带的是**日结**那一档的 180 秒 —— 让一个人人都会打开的
	// 页面挂三分钟不可接受。超时就报错,卡片上留一颗重试按钮,不缓存、下次重来。
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	data, err := forecastFor(ctx, c.GetInt("id"), force)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{
		"day":            data.Day,
		"settle_at":      data.SettleAt,
		"computed_at":    data.ComputedAt,
		"refresh_after":  data.ComputedAt + forecastManualMinSecs,
		"expires_at":     data.ComputedAt + int64(forecastTTL/time.Second),
		"quota_per_unit": QuotaPerUnit(),
		"hold_reason":    data.HoldReason,
		"consume": gin.H{
			"base_quota": data.Consume.BaseQuota,
			"rate_bps":   data.Consume.RateBps,
			"gross":      data.Consume.Gross.String(),
			"carry":      data.Consume.Carry.String(),
			"estimated":  data.Consume.Estimated,
		},
		"invite": gin.H{
			"applies":    data.InviteApplies,
			"counted":    data.InviteCounted,
			"base_quota": data.Invite.BaseQuota,
			"rate_bps":   data.Invite.RateBps,
			"gross":      data.Invite.Gross.String(),
			"carry":      data.Invite.Carry.String(),
			"estimated":  data.Invite.Estimated,
		},
		"estimated_total": data.Total,
	})
}

// forecastFor 取一份估算:命中缓存就用缓存的,否则重算并存回。
//
// force 只在缓存那一份已经够老时才真的重算 —— 手动刷新是给用户用的,不是给
// 一个循环用的。singleflight 让同一个用户的并发请求只聚合一次:余额页刷新两次、
// 或者两个标签页同时打开,都不该变成两遍 LOG_DB 扫描。
func forecastFor(ctx context.Context, userId int, force bool) (forecastData, error) {
	now := common.GetTimestamp()
	if cached, found, err := forecastCache.Get(userId); err == nil && found {
		if !force || now-cached.ComputedAt < forecastManualMinSecs {
			return cached, nil
		}
	}
	v, err, _ := forecastSF.Do(strconv.Itoa(userId), func() (any, error) {
		data, err := computeForecast(ctx, userId, common.GetTimestamp())
		if err != nil {
			return nil, err
		}
		forecastCache.Set(userId, data)
		return data, nil
	})
	if err != nil {
		return forecastData{}, err
	}
	return v.(forecastData), nil
}

// computeForecast 算一份估算。全程只读:主库快照一次、扩展库余额行一次、
// LOG_DB 今日聚合一到两次。
func computeForecast(ctx context.Context, userId int, now int64) (forecastData, error) {
	delay := config.Get().Stardust.SettleDelayMinutes
	if delay < 0 {
		delay = 0
	}
	out := forecastData{
		Day:        invite.DayKey(now),
		SettleAt:   invite.NextDayStart(now) + int64(delay)*60,
		ComputedAt: now,
	}

	snap, err := loadUserSnapshot(ctx, userId)
	if err != nil {
		return forecastData{}, wrapInternal("读取账号快照", err)
	}
	out.HoldReason = snap.holdReason()

	gdb := db.Get()
	if gdb == nil {
		return forecastData{}, db.ErrNotReady
	}
	// 余额行缺失时用零值,**不插库**(理由同 handleGetMe:读接口不该有副作用)。
	bal := Balance{UserId: userId}
	var balRows []Balance
	if err := gdb.WithContext(ctx).Where("user_id = ?", userId).Limit(1).Find(&balRows).Error; err != nil {
		db.MarkFailure(err)
		return forecastData{}, wrapInternal("读取余额", err)
	}
	if len(balRows) == 1 {
		bal = balRows[0]
	}

	qpu := QuotaPerUnit()
	start := invite.DayStart(now)
	mine, err := aggregateDayConsume(ctx, start, start+secondsPerDay, []int{userId})
	if err != nil {
		return forecastData{}, wrapInternal("汇总我的今日消费", err)
	}
	out.Consume = forecastLine{RateBps: consumeRateFor(ctx, snap.Group), Carry: bal.Carry}
	for _, r := range mine {
		out.Consume.BaseQuota += r.BaseQuota
	}
	out.Consume.Gross = grossOf(out.Consume.BaseQuota, out.Consume.RateBps, qpu)

	// 下线消费返:功能关着、或我这一档比例是 0(含合规门未确认)时整条线不成立,
	// 一次 LOG_DB 都不扫。
	out.Invite = forecastLine{Carry: bal.InviteCarry, Gross: decimal.Zero}
	if guard.Feature(guard.FlagInvite) {
		out.Invite.RateBps = inviteConsumeRateFor(ctx, snap.Group)
	}
	if out.Invite.RateBps > 0 {
		out.InviteApplies = true
		rows, counted, err := pendingTodayInviteeConsume(ctx, userId, now)
		if err != nil {
			return forecastData{}, wrapInternal("汇总下线今日消费", err)
		}
		out.InviteCounted = counted
		// 逐个下线取整再求和,而不是把基数加起来再算一次:日结落的是一对
		// (邀请人, 下线) 一桶,每一桶各自截断到 10 位小数(recomputeInviteDay)。
		for _, r := range rows {
			out.Invite.BaseQuota += r.BaseQuota
			out.Invite.Gross = out.Invite.Gross.Add(grossOf(r.BaseQuota, out.Invite.RateBps, qpu))
		}
	}

	// 暂缓中的账号明天一颗都拿不到:三种原因都不 Credit(§4.1 步骤 3),
	// 计提照常发生但要等账号恢复。基数与计提照样下发,界面据此解释"算了、没发"。
	if out.HoldReason == "" {
		out.Consume.Estimated = floorToStardust(out.Consume.Carry.Add(out.Consume.Gross))
		out.Invite.Estimated = floorToStardust(out.Invite.Carry.Add(out.Invite.Gross))
	}
	out.Total = out.Consume.Estimated + out.Invite.Estimated
	return out, nil
}

// floorToStardust 把 carry + Σgross 折成这一次能发出去的整数星屑,与结算事务里那
// 一步同一条式子(floor + QuotaFromDecimal 的饱和)。负数不可能出现在这条路径上
// (gross ≥ 0、carry ∈ [0,1)),真出现了也按 0 下发而不是把负数印到界面上。
func floorToStardust(total decimal.Decimal) int64 {
	n := int64(common.QuotaFromDecimal(total.Floor()))
	if n < 0 {
		return 0
	}
	return n
}
