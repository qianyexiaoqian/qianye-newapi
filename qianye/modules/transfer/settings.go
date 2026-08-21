package transfer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settings.go —— 划转门槛的运营覆盖层。
//
// # 为什么划转也要这一层
//
// 裁决 5:门槛/单笔上限/日额度/日次数/冷却这类**运营每天要动的参数**必须能在线改,
// 让它们必须改文件 + 重启不现实;而功能总闸、收款人解析口径、跨库与保留期这类
// **一次改动影响面很大**的配置留在 YAML,需要一次显式的发布动作。
//
// transfer 曾是唯一一个没接 qy_settings 的资金模块 —— 10 个运营参数全在 YAML。
// 本文件照抄 commission/settings.go 已经过四轮审计的形状,不另起炉灶:
// 同一概念的第二份实现必然各自漂移,那是本仓库反复栽跟头的形状。
//
// # 生效值为什么直接复用 config.Transfer 而不是另起一个结构体
//
// 另起结构体意味着 validateCreate / evaluateRisk / loadParties / applyQuotaTransfer
// 的签名要全部改一遍,而且从此有两份"划转门槛"的定义 —— 加一个字段就要在两处
// 各加一次,漏一处就是一道空闸门。这里让覆盖层输出的仍然是 config.Transfer:
// YAML 是底,qy_settings 是覆盖,消费方一个字都不用改,也不可能读到"半份"配置。
//
// 反过来的代价是 config.Transfer 里同时装着可改与不可改的字段。这一点由
// editableKeys 白名单 + assignSetting 的 switch 双重限定:两处都没登记的字段
// 永远只能来自 YAML。
const settingScope = "transfer"

// 运营可改的键。取值一律是**十进制整数字符串** —— 划转没有百分比字段
// (fee_bps 本身就是万分比整数),因此不需要 commission 那套百分比解析。
const (
	keyMinQuota                = "min_quota"
	keyMaxPerTxQuota           = "max_per_tx_quota"
	keyDailyMaxQuota           = "daily_max_quota"
	keyDailyMaxCount           = "daily_max_count"
	keyCooldownSecs            = "cooldown_seconds"
	keyReceiverDailyMaxInCount = "receiver_daily_max_in_count"
	keyNewAccountFreezeHours   = "new_account_freeze_hours"
	keyFeeBps                  = "fee_bps"
	keyFeeMinQuota             = "fee_min_quota"

	// 支付密码的两项策略(裁决 1 + 裁决 5)。它们**不在 YAML 里**:
	// 支付密码是本轮新增的功能,没有历史配置文件需要兼容,直接以
	// qy_settings 为唯一来源,默认值见 defaultPayPwd*。
	//
	// **写入侧在这里**(管理端配置页、白名单、区间校验),**读取侧在
	// qianye/modules/paypass**(验密失败计数与锁定判定)。之所以不由本包
	// 导出一个读取函数给它调:划转的执行入口要调 paypass.Require,
	// 也就是 transfer → paypass,paypass 再 import transfer 就成环了。
	//
	// 两份定义(默认值、取值区间)的一致性由 paypass 那边的
	// settings_contract_test.go 顶着 —— 它按源码文本比对本文件的常量。
	// 改这里的键名、默认值或区间时,那条测试会立刻变红,核对数值后一并更新。
	//
	// 键名一旦发布就不能改:它是 qy_settings.k 的行键。
	keyPayPwdMaxAttempts = "pay_pwd_max_attempts"
	keyPayPwdLockMinutes = "pay_pwd_lock_minutes"
)

// editableKeys 限定管理端可写的键。白名单而非黑名单:
// 让管理端能往共享 KV 表里写任意键,等于把别的模块的配置面也交出去了。
//
// 顺序即管理端的字段渲染顺序(前端按 editable_keys 渲染),
// 因此按"金额门槛 → 频次门槛 → 手续费 → 支付密码"分组排列。
var editableKeys = []string{
	keyMinQuota, keyMaxPerTxQuota, keyDailyMaxQuota,
	keyDailyMaxCount, keyCooldownSecs, keyReceiverDailyMaxInCount, keyNewAccountFreezeHours,
	keyFeeBps, keyFeeMinQuota,
	keyPayPwdMaxAttempts, keyPayPwdLockMinutes,
}

// 支付密码策略的默认值。刻意不给"0 = 不限":
// 阈值为 0 等于"错多少次都不锁",那不是一个运营选项,是一个漏洞。
const (
	defaultPayPwdMaxAttempts = 5
	defaultPayPwdLockMinutes = 30
)

// settingBound 是一个可编辑键的取值闭区间。
//
// 这张表是**唯一**的取值范围定义:读取路径(applyOverrides,防的是有人手工
// UPDATE 了 qy_settings)与写入路径(管理端 PUT)共用它,管理端还把它整张
// 下发给前端做输入框校验。三份各写一遍就会漂移,而漂移的方向通常是
// "界面允许、后端拒绝"或者更糟的"界面拒绝、后端放行"。
type settingBound struct{ Lo, Hi int64 }

var settingBounds = map[string]settingBound{
	// 金额类一律夹在主库额度上限内:users.quota 是 int32,
	// 一个超过 MaxQuota 的门槛不是"更宽松",是"永远无法满足"。
	keyMinQuota:      {0, int64(common.MaxQuota)},
	keyMaxPerTxQuota: {0, int64(common.MaxQuota)},
	keyDailyMaxQuota: {0, int64(common.MaxQuota)},
	keyFeeMinQuota:   {0, int64(common.MaxQuota)},

	keyDailyMaxCount:           {0, 100000},
	keyCooldownSecs:            {0, 86400},
	keyReceiverDailyMaxInCount: {0, 100000},
	keyNewAccountFreezeHours:   {0, 8760},

	// fee_bps 是万分比:10000 = 100%,与 config.checkBps 的上界同源。
	keyFeeBps: {0, 10000},

	// 下界是 1 而不是 0 —— 见 defaultPayPwd* 的说明。
	keyPayPwdMaxAttempts: {1, 100},
	keyPayPwdLockMinutes: {1, 10080},
}

// opSettings 是 YAML 与运营覆盖合并后的生效配置。
type opSettings struct {
	// Transfer 与 YAML 同构:可编辑字段已被覆盖,其余(enabled /
	// recipient_lookup / require_receiver_enabled / lookup_log_retain_days)
	// 原样来自 YAML。
	//
	// **它是全站兜底,不是某个用户的生效门槛。** 按用户分组分档之后,资金路径
	// 必须走 transferFor(用户分组) 拿最终那一份 —— 直接读这个字段等于绕过分档,
	// 而绕过的表现是「管理端配了一档、用户还按全局门槛放行」,零告警。
	Transfer config.Transfer

	PayPwdMaxAttempts int
	PayPwdLockMinutes int

	// Tiers 是按用户分组的门槛覆盖(qy_transfer_group_limits),见 grouplimit.go。
	// **map 与快照同生命周期,任何持有者都禁止修改它**:resolveSettings 把
	// opSettings 按值复制交出去,而 map 是引用,改一处即改所有在途请求。
	Tiers tierSet
}

// settingsCacheSeconds 是运营配置的刷新周期。
//
// 刻意不起后台协程去刷:配置只在非热路径(HTTP 处理器)被读到,惰性刷新足够。
const settingsCacheSeconds = 60

// settingsStaleGraceSeconds 是扩展库读不到时**最多沿用多久的旧快照**。
//
// 沿用旧快照本身是对的:上一份快照是运营批准过的值,比回落 YAML(更宽松)安全。
// 但它必须有上界 —— 没有上界时,这条降级路径的时长等于故障时长,运营刚收紧的
// 门槛会在整个故障期间失效,而且日志被节流后看不出它已经持续了多久。
//
// 取 10 分钟而不是等于 TTL:短暂抖动(慢查询、一次连接打满)不该让划转停摆,
// 而一次持续十分钟的扩展库故障已经是必须有人介入的事故,此时停掉划转
// (503,可重试)远比按一份没人再确认过的门槛继续放行资金操作安全。
const settingsStaleGraceSeconds = 600

// settingsVersionCheckSeconds 是跨节点感知的上界。
//
// invalidateSettings() 只清**本进程**的缓存。多节点部署里,运营在 A 节点收紧
// 某一档的单笔上限 / 日额度 / 日笔数 / 冷却 / 新账号冻结期之后,B 节点最长
// settingsCacheSeconds(60 秒)仍按旧值放行,零告警 —— 实测过一笔在库里明写
// 单笔上限 1000 的情况下走掉 5000 的划转,而同一时刻 /transfer/limits 还在
// 向用户回显旧值,前后端口径一致地都是错的。
//
// 分组**规则**那一半刻意不缓存(见 loadGroupRules:「多节点口径不一致比多一次
// 查库严重得多」),门槛这一半却缓存了 60 秒 —— 同一道闸门的两半采用了相反的
// 新鲜度策略,而缓存的恰好是决定钱能走多少的那一半。
//
// 修法与 commission 的缓存收敛同形:加一张单行版本表,缓存命中时先花一次主键
// 查询比对版本号,版本没变才沿用。取 2 秒与 commission 的轮询周期一致 ——
// 一次主键查询比全量重载便宜三个数量级,而 60 秒的口径不一致窗口是真金白银。
const settingsVersionCheckSeconds = 2

// SettingsVersion 是单行表,每次门槛/分档写操作 +1。
//
// 与 violation.RuleVersion 同形:autoIncrement:false 是必须的 ——
// GORM 默认会把整型主键建成 AUTO_INCREMENT,而这里的主键恒为 1。
type SettingsVersion struct {
	Id        int   `json:"id" gorm:"primaryKey;autoIncrement:false"` // 恒为 1
	Version   int64 `json:"version" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (SettingsVersion) TableName() string { return "qy_transfer_settings_version" }

var (
	settingsMu     sync.Mutex
	settingsCache  *opSettings
	settingsLoaded int64
	// settingsVersion 是本进程这份快照对应的版本号;
	// settingsVersionChecked 是上一次比对版本号的时刻。
	settingsVersion        int64
	settingsVersionChecked int64
	// settingsEpoch 是缓存的代次,每次失效自增。
	//
	// 查库放到临界区之外之后,"SELECT 返回"与"写回缓存"之间就出现了一个窗口:
	// 管理员在这中间改了门槛并调 invalidateSettings(),在途的旧快照会把它
	// 静默盖掉,此后 60 秒全按旧门槛放行。代次让写回方能发现"我读的那一版
	// 已经作废了"并丢弃本次结果。
	settingsEpoch uint64
	// settingsWarnAt 限制降级告警的打印频率:扩展库抖动时每个请求打一行
	// 会把日志淹掉,反而看不见。
	settingsWarnAt int64
)

// errSettingsInvalid 表示合并后的门槛自相矛盾(例如 min_quota > max_per_tx_quota)。
//
// 管理端 PUT 会挡住这种组合,因此它只可能来自有人手工 UPDATE 了 qy_settings。
// 此时**失败关闭**:宁可整个划转不可用,也不能拿一份没人批准过的门槛去放行资金操作。
// 用 503 是因为这是服务端状态问题,不是用户请求写错了。
var errSettingsInvalid = newBizError("qy_transfer_settings_invalid",
	"划转门槛配置异常,已暂停划转,请联系管理员", http.StatusServiceUnavailable)

// effectiveCtx 返回当前生效的划转配置,是**资金路径唯一该用的入口**。
//
// 合并结果不自洽时直接报错(失败关闭):见 errSettingsInvalid。
func effectiveCtx(ctx context.Context) (opSettings, error) {
	s, valid, err := resolveSettings(ctx)
	if err != nil {
		return opSettings{}, err
	}
	if !valid {
		return opSettings{}, errSettingsInvalid
	}
	return s, nil
}

// resolveSettings 返回合并后的配置,并告知它是否通过跨字段校验。
//
// # valid=false 那一份只有管理端接口该消费
//
// 非法组合意味着有人绕过接口手工改了 qy_settings。资金路径必须失败关闭
// (effectiveCtx 会把它翻译成 errSettingsInvalid),但管理端页面**恰恰是修复
// 它的唯一界面** —— 让那个页面也一起打不开,等于把管理员锁在自己写坏的那一行
// 外面,只能直连数据库救场。
//
// # 查库为什么在锁外
//
// 持锁查库时,一条慢 SELECT 会把所有读配置的协程 —— 划转提交、预览、/limits
// 回显 —— 串在同一把互斥锁上,一次行锁等待就能让整个划转入口停摆。
// 代价是并发首次加载时可能有几个协程各查一次 qy_settings(几行的表),
// 比把它们排成一队便宜得多。
//
// # 读失败为什么返回 error 而不是回落 YAML
//
// 与本模块 loadGroupRules 的口径一致:回落到 YAML 默认值往往比运营配置**更宽松**
// (YAML 的默认日额度是 2 亿),那等于扩展库抖一下就把风控门槛整体放开。
// 有上一份快照时**在 settingsStaleGraceSeconds 之内**沿用它(那仍然是运营批准过
// 的值);一份都没有、或者陈旧超过这个上界时,宁可拒绝这次划转。
//
// # 陈旧沿用为什么必须有时间上界
//
// 没有上界时,这条降级路径的时长等于故障时长而不是缓存 TTL:失败分支既不清
// settingsCache 也不推进 settingsLoaded,于是每一次请求都重试→失败→沿用旧快照,
// 库读不回来就一直沿用下去。运营刚把某一档的单笔上限从 5000 万收紧到 100 万、
// 随后扩展库不可用,那一档就会按 5000 万一直放行,而且 warnThrottled 还被节流,
// 日志上看不出它持续了多久。
//
// 更糟的是方向不确定:故障期间若有人调过 invalidateSettings()(缓存已清),
// 同一个故障立刻变成 fail-closed。加上上界之后,超过宽限期一律 fail-closed,
// 方向不再取决于「保存动作和故障谁先发生」。
func resolveSettings(ctx context.Context) (opSettings, bool, error) {
	now := common.GetTimestamp()
	settingsMu.Lock()
	if settingsCache != nil && now-settingsLoaded < settingsCacheSeconds {
		snap := *settingsCache
		knownVersion := settingsVersion
		checkedAt := settingsVersionChecked
		settingsMu.Unlock()
		if now-checkedAt < settingsVersionCheckSeconds {
			return snap, true, nil
		}
		// 一次主键查询。读失败时沿用缓存:把一次探测失败升级成一次全量重载,
		// 只会在扩展库已经不舒服的时候再加一份负载。
		dbVersion, ok := readSettingsVersion(ctx)
		settingsMu.Lock()
		settingsVersionChecked = now
		settingsMu.Unlock()
		if !ok || dbVersion == knownVersion {
			return snap, true, nil
		}
		// 版本变了 —— 别的节点刚改过门槛,落到下面走一次全量重载。
	} else {
		settingsMu.Unlock()
	}
	settingsMu.Lock()
	epoch := settingsEpoch
	settingsMu.Unlock()

	// 版本号必须在拉数据**之前**读:反过来的话,两者之间的一次写入会让我们
	// 拿到旧数据配新版本号,此后 60 秒都不会再重载。
	loadedVersion, _ := readSettingsVersion(ctx)
	overrides, tiers, err := loadSettingRows(ctx)
	if err != nil {
		settingsMu.Lock()
		stale := settingsCache
		var snap opSettings
		staleAge := int64(0)
		if stale != nil {
			snap = *stale
			staleAge = common.GetTimestamp() - settingsLoaded
		}
		settingsMu.Unlock()
		if stale == nil {
			return opSettings{}, false, err
		}
		if staleAge > settingsStaleGraceSeconds {
			// 超过宽限期就失败关闭:再沿用下去,这条降级路径的时长等于故障时长,
			// 而运营刚刚收紧的门槛会在整个故障期间失效。
			warnThrottled(fmt.Sprintf(
				"读取划转门槛覆盖持续失败已 %d 秒(上限 %d 秒),不再沿用旧快照,划转暂停: %s",
				staleAge, settingsStaleGraceSeconds, err.Error()))
			return opSettings{}, false, err
		}
		warnThrottled("读取划转门槛覆盖失败,本次沿用上一份快照(可能已陈旧): " + err.Error())
		// 缓存里的快照当初是校验通过才被写进去的,沿用它仍然是自洽的。
		return snap, true, nil
	}

	merged := opSettings{
		Transfer:          config.Get().Transfer,
		PayPwdMaxAttempts: defaultPayPwdMaxAttempts,
		PayPwdLockMinutes: defaultPayPwdLockMinutes,
		Tiers:             buildTierSet(tiers),
	}
	applyOverrides(&merged, overrides)
	// 逐键的区间校验挡不住跨字段矛盾(min_quota > max_per_tx_quota 时
	// 任何金额都不合法,而两个值各自都在区间内)。复用 YAML 那一份校验器,
	// 不在这里再写一份。
	if err := config.ValidateTransfer(&merged.Transfer); err != nil {
		// 绝不写进缓存:一份不自洽的配置一旦成为"上一份快照",
		// 扩展库随后抖一下就会让它被当作降级兜底继续用下去。
		common.SysError("qianye/transfer: qy_settings 里的划转门槛非法,已暂停划转: " + err.Error())
		return merged, false, nil
	}

	settingsMu.Lock()
	// 代次变了说明这份快照在途期间已被 invalidateSettings 作废(管理端刚改过)。
	// 此时只把结果返给本次调用方,绝不写回缓存 —— 写回等于把一次已经生效的
	// 调整按回去,而且会盖上一个新鲜的时间戳,让后续 60 秒都读不到真值。
	if settingsEpoch == epoch {
		settingsCache = &merged
		settingsLoaded = common.GetTimestamp()
		settingsVersion = loadedVersion
		settingsVersionChecked = settingsLoaded
	}
	settingsMu.Unlock()
	return merged, true, nil
}

// readSettingsVersion 读单行版本表。行不存在(首次部署)按 0 处理,不是错误。
func readSettingsVersion(ctx context.Context) (int64, bool) {
	gdb := db.Get()
	if gdb == nil {
		return 0, false
	}
	var ver SettingsVersion
	if err := gdb.WithContext(ctx).Where("id = ?", 1).Take(&ver).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, true
		}
		return 0, false
	}
	return ver.Version, true
}

// bumpSettingsVersion 把版本号 +1,让其它节点在 settingsVersionCheckSeconds 内感知。
//
// 失败只告警不报错:本节点的 invalidateSettings 已经生效,漏掉的只是"别的节点
// 早两秒还是晚六十秒知道",把它升级成一次保存失败反而更糟。
func bumpSettingsVersion() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	now := common.GetTimestamp()
	const t = "qy_transfer_settings_version"
	if err := gdb.Exec(`INSERT INTO `+t+` (id, version, updated_at)
		VALUES (1, 1, ?)
		`+db.UpsertHead(gdb, t, "id")+` version = `+t+`.version + 1, updated_at = ?`, now, now).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/transfer: 门槛版本号自增失败,其他节点可能延迟感知: " + err.Error())
	}
}

// invalidateSettings 在管理端改配置后立即失效缓存,避免"改完 60 秒还没生效"的困惑。
func invalidateSettings() {
	settingsMu.Lock()
	settingsCache = nil
	settingsLoaded = 0
	settingsVersion = 0
	settingsVersionChecked = 0
	settingsEpoch++
	settingsMu.Unlock()
	// 本进程之外还有别的节点。它们不会收到这次调用,只会在下一次版本比对时
	// 看到号变了 —— 所以这一行是跨节点收敛的**唯一**触发点。
	bumpSettingsVersion()
}

// loadSettingRows 读出这份快照需要的两张表:全站覆盖与按用户分组的分档。
//
// 两者必须在**同一次刷新**里取,并一起写进同一份快照:分开缓存会出现
// 「全局门槛已经是新的、分档还是旧的」的中间态,而分档的校验(transferFor 里的
// ValidateTransfer)恰恰是拿这两者合并起来判的 —— 半新半旧的组合可能凭空
// 自相矛盾,把一档人的划转停掉,而库里那两行其实是自洽的。
//
// 任一张读失败都整体失败,由调用方按「沿用上一份快照 / 一份都没有就拒绝」处理。
func loadSettingRows(ctx context.Context) (map[string]string, []GroupLimit, error) {
	overrides, err := loadOverrides(ctx)
	if err != nil {
		return nil, nil, err
	}
	tiers, err := loadTiers(ctx)
	if err != nil {
		return nil, nil, err
	}
	return overrides, tiers, nil
}

// loadOverrides 读出本模块在 qy_settings 里的全部运营覆盖。
//
// 必须接 ctx:它是调用方预算的唯一着力点,也是熔断可用性探针唯一认得的形式 ——
// 没接 ctx 的语句既没有超时保护,也没资格给熔断投健康票。
func loadOverrides(ctx context.Context) (map[string]string, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var rows []qymodel.Setting
	if err := gdb.WithContext(ctx).Where("scope = ?", settingScope).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.K] = r.V
	}
	return m, nil
}

// applyOverrides 把运营覆盖叠到 YAML 之上。
//
// 只遍历 editableKeys:qy_settings 里出现的其他键(别的版本写的、手工塞的)
// 一律无视,而不是"有什么就读什么"。
//
// 非法值一律丢弃并告警,不钳到边界:qy_settings 是可以被人手工 UPDATE 的,
// 一个被写坏的 999999999999 若被钳成 MaxQuota 就会静默地放开单笔上限,
// 而丢弃只是回落到 YAML 的默认门槛,损失有界且可解释。
func applyOverrides(s *opSettings, m map[string]string) {
	for _, key := range editableKeys {
		raw, ok := m[key]
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		v, err := parseSetting(key, raw)
		if err != nil {
			warnThrottled("qy_settings 里的划转配置 " + key + " 非法,已忽略: " + err.Error())
			continue
		}
		assignSetting(s, key, v)
	}
}

// parseSetting 解析并按 settingBounds 校验一个配置值。
//
// 读取路径与管理端写入路径共用,因此"界面能填的"与"库里能生效的"永远是同一个区间。
func parseSetting(key, raw string) (int64, error) {
	b, ok := settingBounds[key]
	if !ok {
		return 0, fmt.Errorf("未登记的配置键")
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q 不是十进制整数", raw)
	}
	if v < b.Lo || v > b.Hi {
		return 0, fmt.Errorf("取值 %d 超出允许区间 [%d, %d]", v, b.Lo, b.Hi)
	}
	return v, nil
}

// assignSetting 把一个已校验的值写进生效配置。
//
// switch 里没有的键写不进任何字段,这是白名单之外的第二道限定:
// 即便有人往 editableKeys 里加了一个 config.Transfer 不该被在线改的字段名,
// 也必须再改这里才会真的生效。
func assignSetting(s *opSettings, key string, v int64) {
	if assignTransferSetting(&s.Transfer, key, v) {
		return
	}
	switch key {
	case keyPayPwdMaxAttempts:
		s.PayPwdMaxAttempts = int(v)
	case keyPayPwdLockMinutes:
		s.PayPwdLockMinutes = int(v)
	}
}

// assignTransferSetting 把一个已校验的值写进一份 config.Transfer,
// 返回 false 表示这个键不属于 config.Transfer(支付密码那两项)。
//
// 单独抽出来是因为它有**两个覆盖层**共用:qy_settings 的全站覆盖
// (assignSetting)与 qy_transfer_group_limits 的按分组分档(transferFor)。
// 两层各写一份 switch 就会漂移,而漂移的形状是「全站能改的项、分档改不动」
// 或者反过来 —— 后者更糟:分档写进去了、判定端那个 case 不存在,
// 于是那一档静默地按全局门槛放行。
//
// int64 → int 的收敛只在这一处发生;取值早已被 settingBounds 夹在
// int32 容量之内(见 settingBounds 的说明),因此这次转换不可能溢出。
func assignTransferSetting(t *config.Transfer, key string, v int64) bool {
	switch key {
	case keyMinQuota:
		t.MinQuota = v
	case keyMaxPerTxQuota:
		t.MaxPerTxQuota = v
	case keyDailyMaxQuota:
		t.DailyMaxQuota = v
	case keyFeeMinQuota:
		t.FeeMinQuota = v
	case keyDailyMaxCount:
		t.DailyMaxCount = int(v)
	case keyCooldownSecs:
		t.CooldownSecs = int(v)
	case keyReceiverDailyMaxInCount:
		t.ReceiverDailyMaxInCount = int(v)
	case keyNewAccountFreezeHours:
		t.NewAccountFreezeHours = int(v)
	case keyFeeBps:
		t.FeeBps = int(v)
	default:
		return false
	}
	return true
}

// settingValue 取出生效配置里某个键的当前值,供接口回显与审计快照使用。
// 与 assignSetting 严格成对:一个键在这里取不到值,就说明它压根没有被应用。
func settingValue(s opSettings, key string) int64 {
	if v, ok := transferSettingValue(s.Transfer, key); ok {
		return v
	}
	switch key {
	case keyPayPwdMaxAttempts:
		return int64(s.PayPwdMaxAttempts)
	case keyPayPwdLockMinutes:
		return int64(s.PayPwdLockMinutes)
	}
	return 0
}

// transferSettingValue 是 assignTransferSetting 的读取侧,严格成对。
// 分档的管理端要按 key 回显「这一档最终生效的是多少」,那份合并结果同样是一个
// config.Transfer,不能再走 opSettings。
func transferSettingValue(t config.Transfer, key string) (int64, bool) {
	switch key {
	case keyMinQuota:
		return t.MinQuota, true
	case keyMaxPerTxQuota:
		return t.MaxPerTxQuota, true
	case keyDailyMaxQuota:
		return t.DailyMaxQuota, true
	case keyFeeMinQuota:
		return t.FeeMinQuota, true
	case keyDailyMaxCount:
		return int64(t.DailyMaxCount), true
	case keyCooldownSecs:
		return int64(t.CooldownSecs), true
	case keyReceiverDailyMaxInCount:
		return int64(t.ReceiverDailyMaxInCount), true
	case keyNewAccountFreezeHours:
		return int64(t.NewAccountFreezeHours), true
	case keyFeeBps:
		return int64(t.FeeBps), true
	}
	return 0, false
}

// settingsSnapshot 把生效配置摊成对外形状。接口回显与审计快照共用它,
// 免得两处形状漂移。
func settingsSnapshot(s opSettings) map[string]any {
	out := make(map[string]any, len(editableKeys))
	for _, key := range editableKeys {
		out[key] = settingValue(s, key)
	}
	return out
}

// writeSetting 在给定事务内落一条运营覆盖。
//
// 接 tx 而不是自取 db.Get():一次批量保存里的多个键必须要么全生效要么全不生效。
// 逐条自取连接时,第一个键写成功、第二个撞上死锁,库里就留下了一组谁都没有
// 批准的门槛组合(比如上限已经放开、日额度还没跟上),而所有节点会在
// settingsCacheSeconds 内开始按它放行,接口那边只回了一个 500。
//
// 调用方负责写审计,成功与失败都要写 —— 门槛变更必须可追溯到人。
func writeSetting(tx *gorm.DB, key, value string, operatorId int) error {
	if tx == nil {
		return db.ErrNotReady
	}
	row := qymodel.Setting{
		Scope:      settingScope,
		K:          key,
		V:          value,
		OperatorId: operatorId,
		UpdatedAt:  common.GetTimestamp(),
	}
	err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
	}
	return err
}

// warnThrottled 按 settingsCacheSeconds 节流地打一条配置告警。
//
// 配置读失败与非法值都是"每个请求都会重现"的状态:不节流就会把日志刷满,
// 真正需要看到的那条反而被埋掉。
func warnThrottled(msg string) {
	now := common.GetTimestamp()
	settingsMu.Lock()
	should := now-settingsWarnAt >= settingsCacheSeconds
	if should {
		settingsWarnAt = now
	}
	settingsMu.Unlock()
	if should {
		common.SysError("qianye/transfer: " + msg)
	}
}
