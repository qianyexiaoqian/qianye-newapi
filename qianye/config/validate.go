package config

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
)

// 枚举取值。集中定义避免各模块用字符串字面量各写一份。
const (
	RecipientLookupID      = "id"
	RecipientLookupIDEmail = "id_or_email"

	WithdrawMethodQuota = "quota"
	WithdrawMethodFiat  = "fiat"

	RateFreezeOperationSetting = "operation_setting"
	RateFreezeFixed            = "fixed"

	InsufficientClamp    = "clamp"
	InsufficientNegative = "negative"
	InsufficientBan      = "ban"

	LogLevelSilent = "silent"
	LogLevelError  = "error"
	LogLevelWarn   = "warn"
	LogLevelInfo   = "info"
)

// maxBps 是万分比的上限(100%)。仍被 transfer / withdraw / violation 使用。
const maxBps = 10000

// MinAuditRetentionDays 是 audit.retention_days 允许的最小**非零**取值。
//
// # 下限的依据
//
// qy_audit_logs 是这套资金系统事后仲裁的唯一凭据(见 qianye/model/audit_log.go):
// 划转、佣金、提现、违规扣费的每一次判定都只在这里留痕。删掉一行,就再也无法回答
// "这笔钱当时为什么这么算、谁批的"。下限由两条外部时限中更长的那条决定:
//
//   - 资金纠纷与拒付:各卡组织与支付渠道的拒付(chargeback)受理窗口普遍延伸到
//     180 天,争议一旦进入仲裁还要再往后拖数周;
//   - 税务与审计留存:按**年**计,一个完整会计年度是可用的最小粒度。
//
// 取二者上界并进到一个完整年度 = 365 天。低于它的取值不是"省点磁盘",
// 是把仲裁凭据删在争议窗口还没关上的时候。
//
// # 为什么是拒绝启动而不是静默夹到下限
//
// 静默夹取会让运维以为自己配的是 7 天、实际跑的是 365 天 —— 那正是本扩展反复
// 栽跟头的"以为改了其实没改"。配置写错就该在启动那一刻炸,而不是留一个
// 与运维认知不符的实际行为。
const MinAuditRetentionDays = 365

// 返佣比例的对外单位是**百分比**,内部单位是"百分比 × 100"的整数。
//
// 为什么是两套单位:百分比给人看(运营说的是"返 10.25%",不是"返 1025 个万分之一"),
// 整数给机器算(资金参数不允许出现浮点误差)。两位小数的精度需求恰好落在
// ×100 上,换算全程只做整数与 decimal 运算。
const (
	// RatePercentScale 是百分比 → 内部整数的倍率。两位小数 ⇒ 100。
	RatePercentScale = 100
	// MaxRatePercent 是费率上限:返佣不可能超过收入本身。
	MaxRatePercent = 100
	// MaxRateUnits 是内部整数的上限(100% × 100)。
	MaxRateUnits = MaxRatePercent * RatePercentScale
)

// RatePercentUnits 把对外的百分比字符串换算成内部整数(百分比 × 100)。
//
// 全程走 decimal,一次都不经过 float64:10.25 在二进制浮点里不可精确表示,
// 而这个数字决定平台要为每一笔消费付出多少钱。
//
// 超过两位小数一律拒绝,不做四舍五入。静默把 10.005 变成 10.01 是一次
// 没有人签字的加薪 —— 资金参数宁可让人重新填一遍,也不能替他猜。
//
// 返回的 error 不带字段名,由调用方补上("commission.topup_rate_percent " + err),
// 这样同一段换算能服务 YAML、管理端接口和分组费率三处。
func RatePercentUnits(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("不能为空(填百分比,如 10 或 10.25 表示 10%% / 10.25%%)")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return 0, fmt.Errorf("不是合法数值: %q", raw)
	}
	if d.IsNegative() {
		return 0, fmt.Errorf("不得为负数,收到 %s", s)
	}
	if d.GreaterThan(decimal.NewFromInt(MaxRatePercent)) {
		return 0, fmt.Errorf("不得超过 %d(百分比),收到 %s", MaxRatePercent, s)
	}
	scaled := d.Mul(decimal.NewFromInt(RatePercentScale))
	if !scaled.Equal(scaled.Truncate(0)) {
		return 0, fmt.Errorf("最多两位小数,收到 %s", s)
	}
	// 上面已把取值钳在 0..MaxRateUnits,这里的窄化转换不可能溢出。
	return int(scaled.IntPart()), nil
}

// FormatRatePercent 是 RatePercentUnits 的逆:内部整数 → 对外百分比字符串。
// 1025 → "10.25",1000 → "10",0 → "0"。
func FormatRatePercent(units int) string {
	return decimal.NewFromInt(int64(units)).
		Div(decimal.NewFromInt(RatePercentScale)).String()
}

// validate 在加载后校验配置自洽性。
//
// 校验失败一律返回 error 让主程序 FatalLog:配置写错就该在启动时炸,
// 而不是带着一半失效的风控开关跑起来。
func validate(c *Config) error {
	if !c.Enabled {
		// 扩展未启用时不校验其余字段:用户可能只是把整段配置留着备用。
		return nil
	}

	if err := validateDatabase(&c.Database); err != nil {
		return err
	}
	if err := validateRuntime(&c.Runtime); err != nil {
		return err
	}
	if err := validateAudit(&c.Audit); err != nil {
		return err
	}
	if err := ValidateTransfer(&c.Transfer); err != nil {
		return err
	}
	if err := validateCommission(&c.Commission); err != nil {
		return err
	}
	if err := validateWithdraw(&c.Withdraw); err != nil {
		return err
	}
	if err := validateAvailability(&c.Availability); err != nil {
		return err
	}
	if err := validateViolation(&c.Violation); err != nil {
		return err
	}
	return validateGroupPricing(&c.GroupPricing)
}

// validateGroupPricing 校验分组定价的运行参数。
//
// 这里只管"参数本身合不合法";单条规则的价格上下界由
// qianye/modules/grouppricing 在写入与快照编译两处各校验一次 ——
// 手改数据库绕过接口是这套系统最现实的攻击面。
func validateGroupPricing(g *GroupPricing) error {
	if !g.Enabled {
		return nil
	}
	if g.RuleCacheSeconds <= 0 {
		return fmt.Errorf("qianye: group_pricing.rule_cache_seconds 必须大于 0")
	}
	if g.MaxStaleSeconds < g.RuleCacheSeconds {
		return fmt.Errorf(
			"qianye: group_pricing.max_stale_seconds(%d)不得小于 rule_cache_seconds(%d),"+
				"否则规则快照每次刷新前都会先过期一次,分组价会在生效与不生效之间来回抖动",
			g.MaxStaleSeconds, g.RuleCacheSeconds)
	}
	if g.ShadowFlushIntervalSeconds <= 0 {
		return fmt.Errorf("qianye: group_pricing.shadow_flush_interval_seconds 必须大于 0")
	}
	if g.ShadowRetentionDays <= 0 {
		return fmt.Errorf("qianye: group_pricing.shadow_retention_days 必须大于 0")
	}
	if g.MaxRules <= 0 {
		return fmt.Errorf("qianye: group_pricing.max_rules 必须大于 0")
	}
	if !g.IsShadow() {
		// 与 violation 同理:不阻止,但必须喊出来。这条开关一旦关掉,
		// 每一笔请求都按分组价真实扣费,而扣走的钱退不回来。
		common.SysError("qianye: group_pricing.shadow_mode 已关闭,分组级价格将真实参与扣费 —— " +
			"务必确认已在影子模式下用 /api/qy/admin/group-pricing/shadow/summary 对过账")
	}
	return nil
}

func validateDatabase(d *Database) error {
	if strings.TrimSpace(d.DSN) == "" {
		return fmt.Errorf("qianye: database.dsn 不能为空(扩展需要独立的 MySQL)")
	}
	lower := strings.ToLower(strings.TrimSpace(d.DSN))
	for _, bad := range []string{"postgres://", "postgresql://", "clickhouse://", "sqlite:", "file:"} {
		if strings.HasPrefix(lower, bad) {
			return fmt.Errorf("qianye: 本扩展仅支持 MySQL,database.dsn 不能以 %q 开头", bad)
		}
	}
	if lower == "local" || strings.HasPrefix(lower, "local ") {
		return fmt.Errorf("qianye: 本扩展仅支持 MySQL,不支持 SQLite(database.dsn = local)")
	}
	if !strings.Contains(d.DSN, "/") {
		return fmt.Errorf("qianye: database.dsn 格式不像 MySQL DSN(缺少库名)," +
			`期望形如 user:pass@tcp(host:3306)/dbname?charset=utf8mb4&parseTime=true`)
	}
	if d.MaxIdleConns <= 0 {
		return fmt.Errorf("qianye: database.max_idle_conns 必须大于 0")
	}
	if d.MaxOpenConns < d.MaxIdleConns {
		return fmt.Errorf("qianye: database.max_open_conns(%d) 不得小于 max_idle_conns(%d)",
			d.MaxOpenConns, d.MaxIdleConns)
	}
	// 负值会渲染出 "readTimeout=-1s" 这种 DSN,驱动直接拒绝解析,
	// 报出来的错与"超时配错了"毫无关联 —— 在这里就拦掉。
	if d.ReadTimeoutSeconds < 0 || d.WriteTimeoutSeconds < 0 {
		return fmt.Errorf("qianye: database.read_timeout_seconds / write_timeout_seconds 不能为负数")
	}
	switch d.LogLevel {
	case LogLevelSilent, LogLevelError, LogLevelWarn, LogLevelInfo:
	default:
		return fmt.Errorf("qianye: database.log_level 取值非法: %q(可选 silent|error|warn|info)", d.LogLevel)
	}
	return nil
}

// validateAudit 校验审计保留期。
//
// 刻意不因 audit.enabled=false 而跳过:开关今天关着不代表明天不打开,而一个
// 非法的保留期只有在被打开之后才会显形 —— 显形的地方是那个删数据的任务。
//
// 全程只判定、不改写 *a:任何一次静默修正都会让运维读到的 YAML 与实际行为分叉。
func validateAudit(a *Audit) error {
	switch {
	case a.RetentionDays == 0:
		// 0 = 永久保留。这是默认值,也与扩展上线以来的实际行为逐位一致 ——
		// 升级到带清理任务的版本不改变任何现存部署的行为。
		return nil
	case a.RetentionDays < 0:
		return fmt.Errorf("qianye: audit.retention_days 不得为负数,收到 %d"+
			"(0 表示永久保留;大于 0 表示按天清理,最小 %d)",
			a.RetentionDays, MinAuditRetentionDays)
	case a.RetentionDays < MinAuditRetentionDays:
		return fmt.Errorf(
			"qianye: audit.retention_days(%d)低于硬下限 %d 天 —— qy_audit_logs 是资金"+
				"事后仲裁的唯一凭据,拒付争议窗口普遍到 180 天、税务与审计留存按年计,"+
				"删早了就再也无法自证「这笔钱当时为什么这么算」。"+
				"要永久保留请填 0;确需清理请填 %d 或更大。"+
				"这里不做静默夹取:那会让你以为配的是 %d 天,而实际跑的是别的值",
			a.RetentionDays, MinAuditRetentionDays, MinAuditRetentionDays, a.RetentionDays)
	}
	return nil
}

func validateRuntime(r *Runtime) error {
	// 续租必须显著快于过期,否则一次网络抖动就丢租约、任务反复易主。
	if r.LeaseRenewSeconds*2 >= r.LeaseTTLSeconds {
		return fmt.Errorf("qianye: runtime.lease_renew_seconds(%d) 必须小于 lease_ttl_seconds(%d) 的一半",
			r.LeaseRenewSeconds, r.LeaseTTLSeconds)
	}
	if r.HotHookQueueSize <= 0 {
		return fmt.Errorf("qianye: runtime.hot_hook_queue_size 必须大于 0")
	}
	if r.HotHookWorkers <= 0 {
		return fmt.Errorf("qianye: runtime.hot_hook_workers 必须大于 0")
	}
	if !r.FailOpen() {
		// 不阻止,但必须让部署者意识到自己把扩展变成了主业务的单点故障。
		common.SysError("qianye: runtime.hot_path_fail_open 被设为 false —— " +
			"新库故障时 relay 热路径将受影响,除非你确知后果,否则请改回 true")
	}
	return nil
}

// ValidateTransfer 校验一份划转配置的自洽性。
//
// 导出是因为它有第二个调用方:划转门槛已经可以被管理端在线覆盖
// (qy_settings, scope=transfer),而覆盖后的组合必须过同一道校验 ——
// 逐字段的区间检查看不出 min_quota > max_per_tx_quota 这种跨字段矛盾,
// 而那个组合会让**任何金额**都不合法,等于把划转静默关停。
//
// 管理端写入前用它挡住非法组合,读取合并后再用它兜一次底(qy_settings
// 是可以被人手工 UPDATE 的)。绝不在模块里另写一份:这里加一条规则、
// 那边忘了跟上,正是本仓库反复出现的"第 N 份拷贝各自漂移"。
func ValidateTransfer(t *Transfer) error {
	if err := checkBps("transfer.fee_bps", t.FeeBps); err != nil {
		return err
	}
	if err := checkQuotaCap("transfer.min_quota", t.MinQuota); err != nil {
		return err
	}
	if err := checkQuotaCap("transfer.max_per_tx_quota", t.MaxPerTxQuota); err != nil {
		return err
	}
	if t.MinQuota > t.MaxPerTxQuota {
		return fmt.Errorf("qianye: transfer.min_quota(%d) 不得大于 max_per_tx_quota(%d)",
			t.MinQuota, t.MaxPerTxQuota)
	}
	switch t.RecipientLookup {
	case RecipientLookupID, RecipientLookupIDEmail:
	default:
		return fmt.Errorf("qianye: transfer.recipient_lookup 取值非法: %q(可选 id|id_or_email;"+
			"刻意不提供用户名模糊搜索,那等于开放用户枚举)", t.RecipientLookup)
	}
	return nil
}

func validateCommission(cm *Commission) error {
	if err := checkRatePair(
		"topup", cm.TopupRatePercent, cm.TopupRateBpsDeprecated); err != nil {
		return err
	}
	if err := checkRatePair(
		"consume", cm.ConsumeRatePercent, cm.ConsumeRateBpsDeprecated); err != nil {
		return err
	}
	if cm.Levels != 1 {
		return fmt.Errorf("qianye: commission.levels 当前仅支持 1 级,收到 %d", cm.Levels)
	}
	if err := checkQuotaCap("commission.max_per_order_quota", cm.MaxPerOrderQuota); err != nil {
		return err
	}
	if cm.MinSettleQuota <= 0 {
		return fmt.Errorf("qianye: commission.min_settle_quota 必须大于 0" +
			"(佣金按 decimal 累计,达到该值才结算为整数 quota,否则小额佣金会被截断归零)")
	}
	return nil
}

func validateWithdraw(w *Withdraw) error {
	if !w.Enabled {
		return nil
	}
	if len(w.Methods) == 0 {
		return fmt.Errorf("qianye: withdraw.methods 不能为空")
	}
	for _, m := range w.Methods {
		if m != WithdrawMethodQuota && m != WithdrawMethodFiat {
			return fmt.Errorf("qianye: withdraw.methods 含非法取值 %q(可选 quota|fiat)", m)
		}
	}
	// 收款信息属 PII,没有密钥就绝不允许开启法币提现 —— 明文落库不可接受。
	//
	// 这两条错误会让 config.Load() 失败 → main.go FatalLog → 【站点起不来】。
	// 因此文案必须直接给出补救动作:运维往 methods 里加一个 "fiat" 就重启,
	// 只看到"密钥不能为空"是猜不出还要生成两把、且必须是两把不同的钥匙的。
	if w.HasWithdrawMethod(WithdrawMethodFiat) {
		if err := checkAESKey("withdraw.pii_key", w.PIIKey); err != nil {
			return fmt.Errorf("%w\n"+
				"    withdraw.methods 里有 \"fiat\" 时,pii_key 与 digest_key 两项都是必填前置条件。\n"+
				"    分别用 `openssl rand -base64 32` 生成两串【互不相同】的随机值填进去,再重启。", err)
		}
		if strings.TrimSpace(w.DigestKey) == "" {
			return fmt.Errorf("qianye: withdraw.digest_key 不能为空" +
				"(用于收款账号的风控指纹,必须独立于 pii_key 且不随其轮换)。\n" +
				"    用 `openssl rand -base64 32` 另生成一串,不要与 pii_key 填同一个值 ——" +
				"合成一把之后,pii_key 一轮换历史指纹就全部失效。")
		}
		if strings.TrimSpace(w.DigestKey) == strings.TrimSpace(w.PIIKey) {
			return fmt.Errorf("qianye: withdraw.digest_key 不得与 pii_key 相同" +
				"(pii_key 可轮换、digest_key 永不轮换,共用一个值意味着轮换那天" +
				"跨账户风控指纹会全部作废,而那正是提现场景最有价值的风控信号)")
		}
	}
	if w.PIIKeyVersion < 0 {
		return fmt.Errorf("qianye: withdraw.pii_key_version 不能为负数,收到 %d", w.PIIKeyVersion)
	}
	// 历史密钥在这里就必须校验格式:等到解密某一行时才发现密钥是坏的,那一刻
	// 队列里已经有一批打不了款的单据,而错误信息只会说"无法解密"。
	for version, key := range w.PIIKeysRetired {
		if version <= 0 {
			return fmt.Errorf("qianye: withdraw.pii_keys_retired 的版本号必须大于 0,收到 %d", version)
		}
		if version == w.PIIKeyVersion {
			return fmt.Errorf("qianye: withdraw.pii_keys_retired 不得包含当前启用的版本 %d"+
				"(当前密钥只在 pii_key 里配一份,两处不一致会让新密文用一把钥匙、解密用另一把)", version)
		}
		if err := checkAESKey(fmt.Sprintf("withdraw.pii_keys_retired[%d]", version), key); err != nil {
			return err
		}
	}
	switch w.RateFreezeMode {
	case RateFreezeOperationSetting, RateFreezeFixed:
	default:
		return fmt.Errorf("qianye: withdraw.rate_freeze_mode 取值非法: %q(可选 operation_setting|fixed)",
			w.RateFreezeMode)
	}
	if err := checkDecimal("withdraw.min_fiat_amount", w.MinFiatAmount); err != nil {
		return err
	}
	if err := checkDecimal("withdraw.rate_freeze_fixed", w.RateFreezeFixed); err != nil {
		return err
	}
	if err := checkBps("withdraw.fiat_fee_bps", w.FiatFeeBps); err != nil {
		return err
	}
	if err := checkQuotaCap("withdraw.min_quota", w.MinQuota); err != nil {
		return err
	}
	if err := checkQuotaCap("withdraw.max_quota_per_order", w.MaxQuotaPerOrder); err != nil {
		return err
	}
	if w.MaxQuotaPerOrder > 0 && w.MinQuota > w.MaxQuotaPerOrder {
		return fmt.Errorf("qianye: withdraw.min_quota(%d) 不得大于 max_quota_per_order(%d)",
			w.MinQuota, w.MaxQuotaPerOrder)
	}
	if w.RemarkMaxRunes <= 0 || w.RemarkMaxRunes > 2000 {
		return fmt.Errorf("qianye: withdraw.remark_max_runes 必须在 1..2000 之间,收到 %d", w.RemarkMaxRunes)
	}
	// 凭证图片要整张读进内存才能校验魔数,上限必须有硬顶。
	// 校验放在 fiat 之外:proof_max_bytes 配成 0 或天文数字都是配置错误,
	// 不该等到某个站点某天打开 fiat 才第一次暴露出来。
	if w.ProofMaxBytes <= 0 || w.ProofMaxBytes > MaxWithdrawProofBytes {
		return fmt.Errorf("qianye: withdraw.proof_max_bytes 必须在 1..%d 字节之间,收到 %d"+
			"(凭证需整张读进内存做魔数校验,不设硬顶等于把堆交给上传者)",
			MaxWithdrawProofBytes, w.ProofMaxBytes)
	}
	return nil
}

func validateAvailability(a *Availability) error {
	if !a.Enabled {
		return nil
	}
	if a.BucketSeconds <= 0 || 3600%a.BucketSeconds != 0 {
		return fmt.Errorf("qianye: availability.bucket_seconds(%d) 必须是 3600 的因数,"+
			"否则小时级汇总会跨桶错位", a.BucketSeconds)
	}
	if a.FlushIntervalSeconds <= 0 {
		return fmt.Errorf("qianye: availability.flush_interval_seconds 必须大于 0")
	}
	return nil
}

func validateViolation(v *Violation) error {
	if !v.Enabled {
		return nil
	}
	switch v.InsufficientBalancePolicy {
	case InsufficientClamp, InsufficientNegative, InsufficientBan:
	default:
		return fmt.Errorf("qianye: violation.insufficient_balance_policy 取值非法: %q"+
			"(可选 clamp|negative|ban)", v.InsufficientBalancePolicy)
	}
	mult, err := decimal.NewFromString(strings.TrimSpace(v.FeeMultiplier))
	if err != nil {
		return fmt.Errorf("qianye: violation.fee_multiplier 不是合法数值: %q", v.FeeMultiplier)
	}
	if mult.IsNegative() || mult.GreaterThan(decimal.NewFromInt(100)) {
		return fmt.Errorf("qianye: violation.fee_multiplier 必须在 0..100 之间,收到 %s", v.FeeMultiplier)
	}
	if err := checkDecimal("violation.fixed_fee_amount", v.FixedFeeAmount); err != nil {
		return err
	}
	if err := checkQuotaCap("violation.max_fee_quota", v.MaxFeeQuota); err != nil {
		return err
	}
	if v.AutoBanThreshold < 0 {
		return fmt.Errorf("qianye: violation.auto_ban_threshold 不得为负数(0 表示不自动封号)")
	}
	if err := checkBps("violation.global_block_rate_limit_bps", v.GlobalBlockRateLimitBps); err != nil {
		return err
	}
	// 这里曾经有一条"shadow_mode 已关闭"的启动告警。它随全局开关一起删除了:
	// 现在"会不会真实扣费"取决于库里有几条 mode=enforce 的规则,不是一个配置项,
	// 启动期读不到也不该猜。等价的可见性由 GET /admin/violation/stats 的
	// rules.enforce_rule 提供,管理端每次打开规则页都会看到。
	return nil
}

// ───────────────────────────── 通用校验辅助 ─────────────────────────────

func checkBps(name string, v int) error {
	if v < 0 || v > maxBps {
		return fmt.Errorf("qianye: %s 必须在 0..%d 之间(万分比,5%% = 500),收到 %d", name, maxBps, v)
	}
	return nil
}

// checkRatePair 校验一对"新百分比字段 + 已废弃万分比字段"。
//
// 三步的顺序本身是有意义的:
//
//  1. 先按**旧字段自己的口径**校验。运维写的是 topup_rate_bps: 20000,
//     错误信息就必须点名 topup_rate_bps,而不是它换算之后的百分比 ——
//     否则报出来的字段名在配置文件里根本搜不到。
//  2. 再校验新字段本身(格式、范围、小数位)。
//  3. 最后比对两者。新旧字段同时存在且互相矛盾时直接拒绝启动:
//     替运维挑一个生效值,正是本项目反复吃过亏的"以为改了其实没改"。
//
// 数值上 units 与 bps 恰好同尺度(百分比 × 100 = 万分之一),所以第 3 步
// 可以直接比较,不需要再换算一次。
func checkRatePair(kind, percent string, deprecatedBps *int) error {
	percentKey := "commission." + kind + "_rate_percent"
	bpsKey := "commission." + kind + "_rate_bps"

	if deprecatedBps != nil {
		if err := checkBps(bpsKey, *deprecatedBps); err != nil {
			return err
		}
	}
	units, err := RatePercentUnits(percent)
	if err != nil {
		return fmt.Errorf("qianye: %s %w", percentKey, err)
	}
	if deprecatedBps != nil && *deprecatedBps != units {
		return fmt.Errorf(
			"qianye: %s(%s)与已废弃的 %s(%d)同时存在且互相矛盾 —— "+
				"请删掉 %s,只保留百分比写法(%d bps 等于 %s)",
			percentKey, strings.TrimSpace(percent), bpsKey, *deprecatedBps,
			bpsKey, *deprecatedBps, FormatRatePercent(*deprecatedBps))
	}
	return nil
}

// checkQuotaCap 校验额度上限类字段不超过主库 users.quota 的 int32 容量。
// 超过则跨库写入时必然溢出,必须在配置阶段就拒绝。
func checkQuotaCap(name string, v int64) error {
	if v < 0 {
		return fmt.Errorf("qianye: %s 不得为负数,收到 %d", name, v)
	}
	if v > int64(common.MaxQuota) {
		return fmt.Errorf("qianye: %s(%d)超过主库额度上限 %d(users.quota 是 int32)",
			name, v, common.MaxQuota)
	}
	return nil
}

func checkDecimal(name, raw string) error {
	d, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("qianye: %s 不是合法数值: %q", name, raw)
	}
	if d.IsNegative() {
		return fmt.Errorf("qianye: %s 不得为负数,收到 %s", name, raw)
	}
	return nil
}

// checkAESKey 校验 base64 编码的 32 字节 AES-256 密钥。
func checkAESKey(name, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("qianye: %s 不能为空 —— 启用法币提现必须配置密钥,"+
			"收款信息属个人敏感信息,不允许明文落库", name)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("qianye: %s 不是合法的 base64: %v", name, err)
	}
	if len(key) != 32 {
		return fmt.Errorf("qianye: %s 解码后必须是 32 字节(AES-256),实际 %d 字节", name, len(key))
	}
	return nil
}
