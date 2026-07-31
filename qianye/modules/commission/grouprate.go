package commission

// grouprate.go —— 按分组差异化的返佣费率。
//
// # 口径:按【被邀请人(下线)】的分组,不是邀请人的分组
//
// 这两种口径的业务含义完全不同,必须选一个并且讲清楚:
//
//	按下线分组   = "这个用户带来的收益,按他自己的档位返"
//	按邀请人分组 = "这个推广者的等级决定他能拿几个点"
//
// 本项目选前者,三条理由:
//
//  1. **费率要跟着毛利走。** 佣金基数是下线消费/充值掉的额度,而平台从这笔
//     额度上赚多少,取决于下线所在分组的倍率 —— 低价分组同样的上游成本只
//     换回更少的额度,毛利本来就薄。用邀请人的分组算,等于让"推广者自己
//     买了什么档位"去决定平台为**别人**的消费付多少钱,费率与毛利彻底脱钩,
//     一个买了低价分组的大客户被高档位推广者邀请就会直接倒贴。
//
//  2. **按邀请人分组可以自助加薪。** 邀请人只要给自己买一个高档位分组,
//     名下所有下线的返佣比例立刻整体上浮 —— 一次消费换永久费率提升,
//     是标准的套利路径。按下线分组则要求他去说服每一个下线换档,而下线
//     换到高档位本身就意味着平台单位用量收入更高,激励方向是自洽的。
//
//  3. **可解释。** 一条计佣行冻结的是"这个下线当时在哪个分组、按几个点返",
//     用户和客服看同一行就能对上,不需要再去查邀请人当时的档位。
//
// # 冻结
//
// 命中的费率在计佣当刻写进 accrual 行(RateUnits/RateGroup)。理由与提现
// 冻结汇率完全一致:事后改费率不能让历史佣金变得无法解释。日聚合行更进一步 ——
// 费率与分组一起进幂等键,分组或费率变了就落新的一行,避免一行里混着两套费率
// (base × rate ≠ gross,那种行永远对不平)。
//
// # 未配置的分组
//
// 回落到全局默认费率(YAML 的 *_rate_percent,可被运营覆盖)。禁用某条规则
// 等价于回落,不等于零费率 —— 想给某个分组零费率请显式填 0。

import (
	"context"
	"errors"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"

	"gorm.io/gorm/clause"
)

// GroupRate 是一条分组差异化费率规则。
//
// 存在扩展库而不是 YAML:管理端要能增删改(YAML 只能改文件后重载),
// 而这是运营日常要调的参数,不是启动级配置。
type GroupRate struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// GroupName 是主库 users.group 的取值,**一律以归一化后的形式存储**
	// (groupname.Normalize:去空白 + 小写)。用唯一索引而不是主键:
	// varchar 主键在 MySQL 上会被每一个二级索引带上,得不偿失。
	//
	// 列上刻意没有写 COLLATE:扩展库是 MySQL,建表继承的库默认排序规则
	// (5.7 general_ci / 8.0 0900_ai_ci)大小写不敏感,而 Go 侧是精确匹配。
	// 消掉这个中间态的办法是让写入与判定都只产生小写键(见 groupname 包的
	// 包注释,以及那里记下的重音不敏感残留与建议 DDL),而不是加一条
	// auto_migrate=false 的部署根本不会执行的 ALTER。
	GroupName string `json:"group_name" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_cgr_group"`

	// TopupRateUnits / ConsumeRateUnits 与 Accrual.RateUnits 同单位:
	// 百分比 × 100。对外一律经 config.FormatRatePercent 换回百分比。
	TopupRateUnits   int `json:"topup_rate_units" gorm:"not null;default:0"`
	ConsumeRateUnits int `json:"consume_rate_units" gorm:"not null;default:0"`

	// Enabled 为 false 时该规则不生效,等价于回落全局默认 ——
	// 不是"零费率"。想要零费率请把两个比例显式填 0。
	Enabled bool `json:"enabled" gorm:"not null"`

	Remark     string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	OperatorId int    `json:"operator_id" gorm:"not null;default:0"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt  int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (GroupRate) TableName() string { return "qy_commission_group_rate" }

// TopupPercent / ConsumePercent 是下发给管理端的百分比形式。
func (g GroupRate) TopupPercent() string   { return config.FormatRatePercent(g.TopupRateUnits) }
func (g GroupRate) ConsumePercent() string { return config.FormatRatePercent(g.ConsumeRateUnits) }

// ───────────────────────── 缓存 ─────────────────────────

var (
	groupRateMu     sync.Mutex
	groupRateCache  map[string]GroupRate
	groupRateLoaded int64
	// groupRateEpoch 与 settingsEpoch 同一语义:见 settings.go 的说明。
	groupRateEpoch uint64
)

// groupRates 返回全部生效的分组费率规则,按分组名索引。
//
// 与 blockedInvitees 同一个套路:规则条数等于分组数(几条到几十条),
// 整表拉进内存每 settingsCacheSeconds 刷一次,远比每条计佣事件回一次库便宜。
// 读不到时沿用上一份快照,再不济返回空表回落全局费率 —— 绝不因为读不到
// 分组规则就停止计佣。
//
// 结构与 effectiveCtx 完全一致,不要再分叉出第三种写法:持锁只做读/写快照,
// SELECT 一律在临界区之外发出(持锁查库时一条慢 SELECT 会把所有计佣协程串在
// 这把互斥锁上),写回缓存时用代次判断这份快照是否已被 invalidateGroupRates 作废。
func groupRates(ctx context.Context) map[string]GroupRate {
	groupRateMu.Lock()
	now := common.GetTimestamp()
	if groupRateCache != nil && now-groupRateLoaded < settingsCacheSeconds {
		cached := groupRateCache
		groupRateMu.Unlock()
		return cached
	}
	epoch := groupRateEpoch
	snapshot := groupRateCache
	groupRateMu.Unlock()

	// 三条回落路径都必须上报降级(F5)。
	//
	// 沿用旧快照不算降级 —— 那是缓存本来的语义,费率仍是运营配的那份。
	// 只有**返回空表**才算:那一刻起所有分组一律按全局默认费率计佣,而结果会
	// 冻结进账本行,事后再也分不出"这行是降级"还是"当时配的就是这个"。
	// 没有这个计数器,一次几小时的扩展库故障会安静地按错误费率发完钱。
	gdb := db.Get()
	if gdb == nil {
		if snapshot != nil {
			return snapshot
		}
		groupRateDegrade.note("读取分组费率失败: " + db.ErrNotReady.Error())
		return map[string]GroupRate{}
	}
	var rows []GroupRate
	if err := gdb.WithContext(ctx).Where("enabled = ?", true).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		if snapshot != nil {
			return snapshot
		}
		groupRateDegrade.note("读取分组费率失败: " + err.Error())
		return map[string]GroupRate{}
	}
	m := make(map[string]GroupRate, len(rows))
	for _, r := range rows {
		// 用 billingGroup 而不是 normalizeGroup 建键:查表的那一侧
		// (resolveRate)走的就是 billingGroup,两侧必须是同一个函数,
		// 否则升级前写进来的历史行会变成一条永远查不到的规则。
		m[billingGroup(r.GroupName)] = r
	}
	groupRateMu.Lock()
	if groupRateEpoch == epoch {
		groupRateCache = m
		groupRateLoaded = common.GetTimestamp()
	}
	groupRateMu.Unlock()
	return m
}

func invalidateGroupRates() {
	groupRateMu.Lock()
	groupRateCache = nil
	groupRateLoaded = 0
	groupRateEpoch++
	groupRateMu.Unlock()
}

// normalizeGroup 把分组名归一成比较键(去空白 + 折叠大小写)。
//
// 早先这里只做 TrimSpace 并写着"刻意不折叠大小写,否则 VIP 的规则会套到 vip
// 头上"。那个理由站不住:扩展库是 MySQL,group_name 列继承的库默认排序规则
// 大小写不敏感,所以"VIP 与 vip 是两行"这件事在存储层根本不成立 —— 不折叠
// 得到的不是两条独立规则,而是**管理端写 VIP、库里改的却是 vip 那一行、
// 判定时又查不到 VIP** 的三方错位。口径统一到折叠,理由与残留边界见 groupname 包。
//
// 空串在这里原样返回空串:调用方(api_admin 的两个处理器)靠 `== ""` 拒绝
// "根本没填分组名"的请求,在这里折叠成 default 会让那道校验静默失效,
// 变成"不填分组名就悄悄改掉 default 分组的费率"。判定口径请用 billingGroup。
func normalizeGroup(g string) string { return groupname.Normalize(g) }

// billingGroup 是"这次计佣按哪个分组的费率算"的口径:normalizeGroup 之后
// 把空分组折叠成 default。
//
// 折叠空串是与 transfer 的分组规则对齐(F7):主库 users.group 的列默认值就是
// 'default',但历史行与被直接改过库的账号可能留着空串,而它们在业务上就是默认
// 分组的用户。不折叠的话,给 default 配的低费率对这批账号完全不生效,他们会
// 按全局默认费率(通常更高)拿钱 —— 出钱这一侧不该是全仓最宽松的那个口径。
func billingGroup(g string) string { return groupname.Effective(g) }

// ───────────────────────── 费率解析 ─────────────────────────

// rateDecision 是一次费率解析的结果,直接冻结进 accrual 行。
type rateDecision struct {
	// Units 是本次计佣生效的费率(百分比 × 100)。
	Units int
	// Group 是被邀请人当时所在的分组,原样记录(即便没有命中规则)。
	Group string
	// Matched 表示是否命中了分组规则。只用于日志与管理端解释,不参与算钱。
	Matched bool
}

// resolveRate 决定一次计佣按几个点返。
//
// group 一律是**被邀请人**的分组(口径见文件头)。source 取 SourceConsume
// 或其余(充值/兑换码),它们各有一档比例。
//
// 未配置或被禁用时回落全局默认费率。分组名为空按 default 分组判定(见 billingGroup),
// 因此 "" 也可能命中一条规则 —— 那正是它应该走的那一条。
func resolveRate(ctx context.Context, group, source string, s opSettings) rateDecision {
	g := billingGroup(group)
	fallback := s.TopupRateUnits
	if source == SourceConsume {
		fallback = s.ConsumeRateUnits
	}
	// Group 记的是**判定用的那个值**而不是原始输入:流水行冻结的分组必须能
	// 让人拿着它复算出同一个费率,记原始的 "VIP" 而按 "vip" 算是解释不通的。
	d := rateDecision{Units: fallback, Group: g}
	rule, ok := groupRates(ctx)[g]
	if !ok {
		return d
	}
	d.Matched = true
	if source == SourceConsume {
		d.Units = rule.ConsumeRateUnits
	} else {
		d.Units = rule.TopupRateUnits
	}
	return d
}

// ───────────────────────── 写入 ─────────────────────────

// errEmptyGroupName 是写入侧对空分组名的拒绝。
//
// 校验放在数据层而不是只放在 HTTP 处理器里:空分组名一旦落库,它既不等于
// default(判定侧会把空串折叠成 default,于是这一行永远查不到)、又占着唯一
// 索引位,是一条只能靠人工删库才能收拾的僵尸规则。
var errEmptyGroupName = errors.New("qianye: 分组费率规则必须指定分组名")

// upsertGroupRate 新增或覆盖一条分组费率规则。
//
// 调用方负责写审计:费率直接决定平台要付多少钱,"谁在什么时候把某个分组
// 从 5% 改成 12%"事后必须能查到人。
func upsertGroupRate(ctx context.Context, row *GroupRate) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	// 归一化必须在这里再做一次而不是信任调用方:ON DUPLICATE KEY 的命中与否
	// 完全取决于这个值,而 group_name 不在 DoUpdates 里 —— 写进去一个没归一
	// 的 "VIP",在 ci 排序规则下会命中 vip 那一行、改掉别人的费率,而管理端
	// 回显的却是 VIP。
	row.GroupName = normalizeGroup(row.GroupName)
	if row.GroupName == "" {
		return errEmptyGroupName
	}
	now := common.GetTimestamp()
	row.CreatedAt = now
	row.UpdatedAt = now
	err := gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "group_name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"topup_rate_units", "consume_rate_units", "enabled",
			"remark", "operator_id", "updated_at",
		}),
	}).Create(row).Error
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	invalidateGroupRates()
	return nil
}

// deleteGroupRate 删除一条规则,该分组随即回落全局默认费率。
// 返回是否真的删掉了一行,调用方据此区分"删除成功"与"本来就不存在"。
func deleteGroupRate(ctx context.Context, group string) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	g := normalizeGroup(group)
	if g == "" {
		return false, errEmptyGroupName
	}
	res := gdb.WithContext(ctx).Where("group_name = ?", g).Delete(&GroupRate{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return false, res.Error
	}
	invalidateGroupRates()
	return res.RowsAffected > 0, nil
}

// listGroupRates 返回全部规则(含被禁用的),供管理端展示。
func listGroupRates(ctx context.Context) ([]GroupRate, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []GroupRate
	if err := gdb.WithContext(ctx).Order("group_name asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}

// findGroupRate 读一条规则,用于改动前后的审计快照。
func findGroupRate(ctx context.Context, group string) (*GroupRate, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []GroupRate
	if err := gdb.WithContext(ctx).Where("group_name = ?", normalizeGroup(group)).
		Limit(1).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}
