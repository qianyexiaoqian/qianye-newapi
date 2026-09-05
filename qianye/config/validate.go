package config

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/dsn"

	"github.com/shopspring/decimal"
)

// 枚举取值。集中定义避免各模块用字符串字面量各写一份。
const (
	RecipientLookupID      = "id"
	RecipientLookupIDEmail = "id_or_email"

	InsufficientClamp    = "clamp"
	InsufficientNegative = "negative"
	InsufficientBan      = "ban"

	LogLevelSilent = "silent"
	LogLevelError  = "error"
	LogLevelWarn   = "warn"
	LogLevelInfo   = "info"
)

// maxBps 是万分比的上限(100%)。被 transfer / commission / violation 使用。
const maxBps = 10000

// 计佣比例的对外单位是**百分比**,内部单位是万分比整数(百分比 × 100)。
//
// 为什么是两套单位:百分比给人看(运营说的是"返 10.25%",不是"返 1025 个万分之一"),
// 整数给机器算(资金参数不允许出现浮点误差)。两位小数的精度需求恰好落在
// ×100 上,换算全程只做整数与 decimal 运算。YAML 里直接写万分比整数
// (commission.*_rate_bps),管理端与 qy_settings 走百分比字符串,两者同尺度。
const (
	// RatePercentScale 是百分比 → 内部整数的倍率。两位小数 ⇒ 100。
	RatePercentScale = 100
	// MaxRatePercent 是费率上限:计佣不可能超过收入本身。
	MaxRatePercent = 100
	// MaxRateUnits 是内部整数的上限(100% × 100),与 maxBps 同值。
	MaxRateUnits = MaxRatePercent * RatePercentScale
)

// RatePercentUnits 把对外的百分比字符串换算成内部整数(百分比 × 100 = 万分比)。
//
// 全程走 decimal,一次都不经过 float64:10.25 在二进制浮点里不可精确表示,
// 而这个数字决定平台要为每一笔消费付出多少。
//
// 超过两位小数一律拒绝,不做四舍五入。静默把 10.005 变成 10.01 是一次
// 没有人签字的加薪 —— 资金参数宁可让人重新填一遍,也不能替他猜。
//
// 返回的 error 不带字段名,由调用方补上,这样同一段换算能服务管理端接口和分组费率。
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

// maxPreviewLogDays 是影响面预览回看日志的硬上界,理由与 reconcile 的
// maxReconcileDays 相同:一次超长回看会在日志库上跑出一条没有上界的聚合查询。
const maxPreviewLogDays = 31

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
	if err := validateTwoPhase(&c.TwoPhase); err != nil {
		return err
	}
	if err := ValidateTransfer(&c.Transfer); err != nil {
		return err
	}
	if err := validateInvite(&c.Invite); err != nil {
		return err
	}
	if err := validateCommission(&c.Commission, c.Stardust.Enabled); err != nil {
		return err
	}
	if err := validateTicket(&c.Ticket); err != nil {
		return err
	}
	if err := validateAvailability(&c.Availability); err != nil {
		return err
	}
	if err := validateViolation(&c.Violation); err != nil {
		return err
	}
	if err := validateGroupNamespace(&c.GroupNamespace); err != nil {
		return err
	}
	if err := validateGroupMatrix(&c.GroupMatrix); err != nil {
		return err
	}
	if err := validatePlanEntitlement(&c.PlanEntitlement); err != nil {
		return err
	}
	if err := validateLottery(&c.Lottery); err != nil {
		return err
	}
	if err := validateStardust(&c.Stardust); err != nil {
		return err
	}
	return validateMall(&c.Mall)
}

// maxStardustBps 是消费返 / 邀请返比例的上界:1000:1(每 1 美元等值返 1000 星屑)。
// 再往上没有任何合理用途,而一个多写的零在这里是"全站消费返十倍"。
const maxStardustBps = 10_000_000

// validateStardust 校验星屑的运行参数。
//
// 星屑不是钱,但它能经商城换成套餐 / 卡密 / 实物,所以比例与手调上限的越界
// 一律拒绝启动,而不是夹住。
func validateStardust(s *Stardust) error {
	if !s.Enabled {
		return nil
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("qianye: stardust.name 不能为空白")
	}
	if utf8.RuneCountInString(strings.TrimSpace(s.Name)) > 16 {
		return fmt.Errorf("qianye: stardust.name 不能超过 16 个字符")
	}
	// 刻度是额度数,与其余额度类字段同源:越界等于一道永不触发的换算。
	if err := checkQuotaCap("stardust.quota_per_unit", s.QuotaPerUnit); err != nil {
		return err
	}
	for _, item := range []struct {
		name  string
		value int
	}{
		{"stardust.consume_bps", s.ConsumeBps},
		{"stardust.invite_topup_bps", s.InviteTopupBps},
		{"stardust.invite_redeem_bps", s.InviteRedeemBps},
		{"stardust.invite_consume_bps", s.InviteConsumeBps},
	} {
		if item.value < 0 || item.value > maxStardustBps {
			return fmt.Errorf("qianye: %s 必须落在 [0, %d](万分比;10000 = 每 1 美元等值 1 星屑),收到 %d",
				item.name, maxStardustBps, item.value)
		}
	}
	// 两个星屑数量的上界与额度类字段同一条算术上界(星屑面值比额度小五个数量级,
	// 沿用 common.MaxQuota 不需要重做推导);名字不含 quota 不代表不受它约束。
	if err := checkQuotaCap("stardust.invite_register_stardust", s.InviteRegisterStardust); err != nil {
		return err
	}
	if err := checkQuotaCap("stardust.max_manual_adjust", s.MaxManualAdjust); err != nil {
		return err
	}
	if s.SettleDelayMinutes < 0 || s.SettleDelayMinutes > 12*60 {
		return fmt.Errorf("qianye: stardust.settle_delay_minutes 必须落在 [0, 720],收到 %d", s.SettleDelayMinutes)
	}
	if s.SettleIntervalSeconds <= 0 {
		return fmt.Errorf("qianye: stardust.settle_interval_seconds 必须大于 0")
	}
	if s.HeldAlertDays < 0 {
		return fmt.Errorf("qianye: stardust.held_alert_days 不能为负(0 = 不告警)")
	}
	if s.TopupScanIntervalSeconds <= 0 {
		return fmt.Errorf("qianye: stardust.topup_scan_interval_seconds 必须大于 0")
	}
	return nil
}

// validateMall 校验星屑商城的运行参数。
//
// secret_key 与 lottery.prize_secret_key 同一档纪律:开着商城就必须有这把钥匙,
// 预存的兑换码库存与收货地址不允许明文落库。
func validateMall(m *Mall) error {
	if !m.Enabled {
		return nil
	}
	if strings.TrimSpace(m.SecretKey) == "" {
		return fmt.Errorf("qianye: mall.secret_key 不能为空 —— " +
			"商城库存里存的是发给用户的**实际兑换码**与实物订单的收货地址,不允许明文落库。\n" +
			"    用 `openssl rand -base64 32` 生成一串填进去,再重启;\n" +
			"    暂时不想用商城就把 mall.enabled 置 false。")
	}
	if err := checkAESKey("mall.secret_key", m.SecretKey); err != nil {
		return err
	}
	if m.SecretKeyVersion < 0 {
		return fmt.Errorf("qianye: mall.secret_key_version 不得为负")
	}
	for version, raw := range m.SecretKeysRetired {
		if version <= 0 {
			return fmt.Errorf("qianye: mall.secret_keys_retired 的版本号必须大于 0,收到 %d", version)
		}
		if version == m.ActiveSecretKeyVersion() {
			return fmt.Errorf("qianye: mall.secret_keys_retired 不得包含当前启用的版本 %d"+
				"(轮换时旧钥搬进退役表的同时必须把 secret_key_version 抬到更大的数)", version)
		}
		if err := checkAESKey(fmt.Sprintf("mall.secret_keys_retired[%d]", version), raw); err != nil {
			return err
		}
	}
	if m.AddressRetentionDays < 30 {
		return fmt.Errorf("qianye: mall.address_retention_days 不得小于 30(收货地址在发货争议期内必须可查),收到 %d",
			m.AddressRetentionDays)
	}
	if m.MaxProducts <= 0 || m.MaxProducts > 10_000 {
		return fmt.Errorf("qianye: mall.max_products 必须落在 [1, 10000],收到 %d", m.MaxProducts)
	}
	if m.CodeUploadMax <= 0 || m.CodeUploadMax > 5_000 {
		return fmt.Errorf("qianye: mall.code_upload_max 必须落在 [1, 5000],收到 %d", m.CodeUploadMax)
	}
	if m.PendingGraceSeconds <= 0 {
		return fmt.Errorf("qianye: mall.pending_grace_seconds 必须大于 0")
	}
	return nil
}

// validateLottery 校验娱乐功能的运行参数。
//
// 只管"参数本身合不合法"。单个活动的奖档上限、条件窗口、费率上界由
// qianye/modules/lottery 在创建与发布两处各校验一次 —— 手改数据库绕过接口
// 是这套系统最现实的攻击面。
func validateLottery(l *Lottery) error {
	if !l.Enabled {
		return nil
	}
	// reveal_delay_seconds 是**唯一**不接受 0 的项:它是承诺-揭示协议的
	// 核心间隔。为 0 意味着名单哈希与种子在同一瞬间公开,验证者来不及抓到
	// 一份"揭示之前的名单快照",整个协议退化成"平台自己说它没改"。
	if l.RevealDelaySeconds <= 0 {
		return fmt.Errorf("qianye: lottery.reveal_delay_seconds 必须大于 0 —— " +
			"名单哈希必须先于种子公开,否则公正性无法被第三方举证")
	}
	// 三个额度上限的 0 一律是"不限制",所以这里只拒负数。
	// "派奖是净增发"这件事现在由 large_prize_alert_stardust 的二次确认盯着,
	// 而不是由一道谁都能调大的硬拒绝盯着(见 qianye/modules/lottery/caps.go)。
	if l.MaxTotalPrizeStardust < 0 || l.MaxStakeStardust < 0 || l.LargePrizeAlertStardust < 0 {
		return fmt.Errorf("qianye: lottery.max_total_prize_stardust / max_stake_stardust / " +
			"large_prize_alert_stardust 不能为负(0 = 不限制)")
	}
	// 上界与其余额度类字段同源。这四项此前只判了负数(pay_password_threshold_stardust
	// 连负数都不判),于是一份把 max_stake_stardust 配成 MaxInt64 的 YAML 能干净启动,
	// 再由 entry.go 的 `amount > MaxQuota` 在每一次参与上报错;
	// pay_password_threshold_stardust 越界则表现为支付密码**永不触发**,那是安全弱化
	// 而不是报错,更不该等到出事才发现。0 仍然是"不限制/不启用",所以只卡上界。
	for _, item := range []struct {
		name  string
		value int64
	}{
		{name: "lottery.max_stake_stardust", value: l.MaxStakeStardust},
		{name: "lottery.max_total_prize_stardust", value: l.MaxTotalPrizeStardust},
		{name: "lottery.large_prize_alert_stardust", value: l.LargePrizeAlertStardust},
		{name: "lottery.pay_password_threshold_stardust", value: l.PayPasswordThresholdStardust},
	} {
		if err := checkQuotaCap(item.name, item.value); err != nil {
			return err
		}
	}
	// 阈值高过硬顶 = 一道**永远不会触发**的二次确认:超过阈值的活动在够到阈值
	// 之前就已经被硬顶 400 掉了。这不是洁癖,是一道装上去却不通电的闸门,
	// 而它是本模块唯一还在盯着"多写一个零"的东西。
	if l.MaxTotalPrizeStardust > 0 && l.LargePrizeAlertStardust > l.MaxTotalPrizeStardust {
		return fmt.Errorf("qianye: lottery.large_prize_alert_stardust(%d)不得超过 "+
			"max_total_prize_stardust(%d)—— 否则这道二次确认永远触发不了",
			l.LargePrizeAlertStardust, l.MaxTotalPrizeStardust)
	}
	// 兑换码密钥是**必填**,与 violation.ai_review_key 同一档纪律(见 config.go 的
	// PrizeSecretKey 注释)。qy_lot_payout.secret_cipher 存的是发给中奖者的实际
	// 兑换码,和收款账号、渠道 api_key 同级:允许它明文落库,等于让任何拿到库
	// 备份、只读报表账号或离线 dump 的人直接读走 —— 而在线侧那一整套控制
	// (json:"-" 不下发、列表只回掩码、reveal 强制事由 + 双写审计)对他们
	// 一条都不起作用。
	//
	// 这一条会让"开着抽奖却没配密钥"的部署 FATAL 退出,是本 fork 记为 MAJOR 的
	// 那处破坏性变更(qianye/version/baseline.txt v1.0.0)。因此错误文案必须
	// 直接给出两条出路,而不是只说"不能为空"。
	//
	// 形状同样在启动时就查:配错的表现是**已履行的文本奖全部读不出来**,
	// 而那要等到管理员点开某一条 reveal 才会发现。
	if strings.TrimSpace(l.PrizeSecretKey) == "" {
		return fmt.Errorf("qianye: lottery.prize_secret_key 不能为空 —— " +
			"文本奖存的是发给中奖者的**实际兑换码**,与提现的收款账号同级,不允许明文落库。\n" +
			"    用 `openssl rand -base64 32` 生成一串填进去,再重启;\n" +
			"    暂时不想用娱乐功能就把 lottery.enabled 置 false。")
	}
	if err := checkAESKey("lottery.prize_secret_key", l.PrizeSecretKey); err != nil {
		return err
	}
	if l.PrizeSecretKeyVersion < 0 {
		return fmt.Errorf("qianye: lottery.prize_secret_key_version 不得为负")
	}
	for version, raw := range l.PrizeSecretKeysRetired {
		if version <= 0 {
			return fmt.Errorf("qianye: lottery.prize_secret_keys_retired 的版本号必须大于 0,收到 %d", version)
		}
		// 退役表里出现当前版本号 = 一次做了一半的轮换:运维把旧钥搬了进来,
		// 却忘了给新钥抬版本号。此时新密文按 v_n 写入用的是新钥,而库里已有的
		// v_n 行是旧钥封的 —— 后者从此永远解不开,并且没有任何迹象
		// (prizeSecretKeyForVersion 先命中当前版本,退役表里的旧钥根本轮不到)。
		// violation.ai_review_keys_retired 同样有这道检查。
		if version == l.ActivePrizeSecretKeyVersion() {
			return fmt.Errorf("qianye: lottery.prize_secret_keys_retired 不得包含当前启用的版本 %d"+
				"(当前密钥只在 prize_secret_key 里配一份;两处并存意味着新密文用一把钥匙、"+
				"而同版本的历史密文用另一把 —— 后者会永久不可读。轮换时旧钥搬进退役表的同时"+
				"必须把 prize_secret_key_version 抬到更大的数)", version)
		}
		if err := checkAESKey(fmt.Sprintf("lottery.prize_secret_keys_retired[%d]", version), raw); err != nil {
			return err
		}
	}
	if l.MaxGuessFeeBps < 0 || l.MaxGuessFeeBps > maxBps {
		return fmt.Errorf("qianye: lottery.max_guess_fee_bps 必须落在 [0, %d]", maxBps)
	}
	if l.DefaultGuessFeeBps < 0 || l.DefaultGuessFeeBps > l.MaxGuessFeeBps {
		return fmt.Errorf("qianye: lottery.default_guess_fee_bps(%d)必须落在 [0, max_guess_fee_bps(%d)]",
			l.DefaultGuessFeeBps, l.MaxGuessFeeBps)
	}
	if l.MaxTotalEntriesHard <= 0 {
		return fmt.Errorf("qianye: lottery.max_total_entries_hard 必须大于 0 —— " +
			"名单冻结要在单个事务里流式算完,没有上界就没有可预期的封盘耗时")
	}
	// 上界不是运维口味,是算术:名单规模是全站唯一一个乘在额度上却没有就地
	// 溢出检查的整数(estimate 里的 `hit * AmountQuota`),common.MaxQuota 的
	// 推导直接引用它。配得比它大,那条乘法就能在 int64 上绕回负数 —— 表现是
	// 预估支出变成负数,于是二次确认与告警阈值一起静默通过。
	if l.MaxTotalEntriesHard > MaxLotteryEntriesHard {
		return fmt.Errorf("qianye: lottery.max_total_entries_hard(%d)不得超过 %d —— "+
			"它与 common.MaxQuota 共同保证「单份额度 × 名单规模」不溢出 int64,"+
			"抬高它等于把抽奖预估支出的溢出防线拆掉", l.MaxTotalEntriesHard, MaxLotteryEntriesHard)
	}
	// 多注提交的整批预算。下界 1 秒:比它还小的值等于把这条路径关掉(连单注
	// 都跑不完);上界 300 秒纯粹是"任何反代都不会等这么久"的常识线,配到那里
	// 意味着用户会先拿到 504 而服务端还在扣钱。
	if l.EntryBatchMaxMs < 1000 || l.EntryBatchMaxMs > 300_000 {
		return fmt.Errorf("qianye: lottery.entry_batch_max_ms 必须在 1000..300000 之间,收到 %d —— "+
			"它是一次多注提交整批的时间预算上界,要按部署方自己的反向代理读超时来配:"+
			"配得比反代还大时,请求会在反代那一刻被切成 504,而服务端仍在逐注扣钱",
			l.EntryBatchMaxMs)
	}
	if l.MaxPrizeTiers <= 0 || l.MaxOptions < 2 {
		return fmt.Errorf("qianye: lottery.max_prize_tiers 必须大于 0,max_options 必须不小于 2")
	}
	if l.PayoutMaxAttempts <= 0 {
		return fmt.Errorf("qianye: lottery.payout_max_attempts 必须大于 0")
	}
	// 封面要整张读进内存才能校验魔数,上限必须有硬顶。校验放在 cover_enabled
	// 之外:填 0 或天文数字都是配置错误,不该等到某天打开上传功能才第一次暴露。
	if l.CoverMaxBytes <= 0 || l.CoverMaxBytes > MaxLotteryCoverBytes {
		return fmt.Errorf("qianye: lottery.cover_max_bytes 必须在 1..%d 字节之间,收到 %d"+
			"(封面需整张读进内存做魔数校验,不设硬顶等于把堆交给上传者;"+
			"不想收图请用 cover_enabled: false)",
			MaxLotteryCoverBytes, l.CoverMaxBytes)
	}
	if l.SpendMaxLookbackDays <= 0 {
		return fmt.Errorf("qianye: lottery.spend_max_lookback_days 必须大于 0")
	}
	if l.SpendRetentionDays > 0 && l.SpendRetentionDays < l.SpendMaxLookbackDays {
		return fmt.Errorf(
			"qianye: lottery.spend_retention_days(%d)不得小于 spend_max_lookback_days(%d),"+
				"否则「近 N 日消费」这道门槛会因为日桶已被清理而静默误拒守规用户",
			l.SpendRetentionDays, l.SpendMaxLookbackDays)
	}
	return nil
}

// validatePlanEntitlement 校验套餐解锁的运行参数。
//
// 单条绑定的合法性(模型分组必须存在于分组倍率表、不得是 auto、restricted 不得
// 零绑定)由 qianye/modules/planentitlement 在写入与快照编译两处各校验一次 ——
// 手改数据库绕过接口是这套系统最现实的攻击面,而这份配置决定"钱从哪个池子扣"。
func validatePlanEntitlement(p *PlanEntitlement) error {
	if !p.On() {
		return nil
	}
	if p.CacheSeconds <= 0 {
		return fmt.Errorf("qianye: plan_entitlement.cache_seconds 必须大于 0")
	}
	if p.UserCacheSeconds <= 0 {
		return fmt.Errorf("qianye: plan_entitlement.user_cache_seconds 必须大于 0 —— " +
			"为 0 会让每一个带令牌分组的请求都回一次主库查订阅")
	}
	if p.UserMaxStaleSeconds < p.UserCacheSeconds {
		return fmt.Errorf(
			"qianye: plan_entitlement.user_max_stale_seconds(%d)不得小于 user_cache_seconds(%d),"+
				"否则解锁快照一过新鲜期就直接作废,刷新失败时已付款用户会立刻失去他买到的分组",
			p.UserMaxStaleSeconds, p.UserCacheSeconds)
	}
	return nil
}

// 分组登记的两个策略枚举。放在 config 包是因为 YAML 校验与模块判定必须读同一组
// 字面量 —— 两处各写一份字符串,拼错的那一份会静默退化成"默认档",
// 而默认档恰好是"什么都不做",于是配置写了、没生效、没有任何报错。
const (
	MissingRatioPolicyLegacyOne = "legacy_one"
	MissingRatioPolicyDeny      = "deny"

	FundingGateOff     = "off"
	FundingGateShadow  = "shadow"
	FundingGateEnforce = "enforce"
)

// validateGroupNamespace 校验分组登记的运行参数。
//
// 两个枚举**必须**在启动时校验:它们决定"要不要在鉴权处 403"与"要不要在出资处
// 拒绝",而拼错一个字母的表现是静默退回默认档 —— 运营以为自己打开了严格模式,
// 实际什么都没发生,而且没有任何提示。
func validateGroupNamespace(g *GroupNamespace) error {
	if !g.Enabled {
		return nil
	}
	if g.CacheSeconds <= 0 {
		return fmt.Errorf("qianye: group_namespace.cache_seconds 必须大于 0")
	}
	if g.MaxStaleSeconds < g.CacheSeconds {
		return fmt.Errorf(
			"qianye: group_namespace.max_stale_seconds(%d)不得小于 cache_seconds(%d),"+
				"否则每个刷新周期都会先触发一次陈旧告警",
			g.MaxStaleSeconds, g.CacheSeconds)
	}
	switch g.MissingRatioPolicy {
	case MissingRatioPolicyLegacyOne, MissingRatioPolicyDeny:
	default:
		return fmt.Errorf(
			"qianye: group_namespace.missing_ratio_policy=%q 非法(可选 %s|%s)——"+
				"拼错时会静默退回 %s,运营会以为严格模式已经打开",
			g.MissingRatioPolicy, MissingRatioPolicyLegacyOne, MissingRatioPolicyDeny,
			MissingRatioPolicyLegacyOne)
	}
	switch g.FundingGateMode {
	case FundingGateOff, FundingGateShadow, FundingGateEnforce:
	default:
		return fmt.Errorf(
			"qianye: group_namespace.funding_gate_mode=%q 非法(可选 %s|%s|%s)",
			g.FundingGateMode, FundingGateOff, FundingGateShadow, FundingGateEnforce)
	}
	return nil
}

// validateGroupMatrix 校验权威可选清单的运行参数。
//
// 这里只管"参数本身合不合法";单条清单项的合法性(模型分组必须存在于分组倍率表、
// 不得出现 auto)由 qianye/modules/groupmatrix 在写入与快照编译两处各校验一次 ——
// 手改数据库绕过接口是这套系统最现实的攻击面,而这份清单决定谁能发出请求。
func validateGroupMatrix(g *GroupMatrix) error {
	if !g.Enabled {
		return nil
	}
	if g.CacheSeconds <= 0 {
		return fmt.Errorf("qianye: group_matrix.cache_seconds 必须大于 0")
	}
	if g.MaxStaleSeconds < g.CacheSeconds {
		return fmt.Errorf(
			"qianye: group_matrix.max_stale_seconds(%d)不得小于 cache_seconds(%d),"+
				"否则每个刷新周期都会先触发一次陈旧告警,告警很快会变成背景噪声",
			g.MaxStaleSeconds, g.CacheSeconds)
	}
	if g.PreviewLogDays <= 0 || g.PreviewLogDays > maxPreviewLogDays {
		return fmt.Errorf("qianye: group_matrix.preview_log_days 必须落在 [1, %d]", maxPreviewLogDays)
	}
	if g.MaxPreviewPairs <= 0 {
		return fmt.Errorf("qianye: group_matrix.max_preview_pairs 必须大于 0 —— " +
			"为 0 时任何一次预览都会立刻标成 incomplete,而 incomplete 的预览禁止切 enforce")
	}
	if g.PreviewSampleLimit <= 0 {
		return fmt.Errorf("qianye: group_matrix.preview_sample_limit 必须大于 0 —— " +
			"没有样本的影响面报告只剩一个数字,运营无法据此通知到具体的人")
	}
	if g.MaxGrants <= 0 {
		return fmt.Errorf("qianye: group_matrix.max_grants 必须大于 0")
	}
	if !g.WriteGuardOn() {
		// 不阻止,但必须喊出来:关掉写侧之后,收紧期间每一次矩阵调整都会继续
		// 制造"保存得下、一发请求就 403"的孤儿令牌,而它们只在用户真的发请求时才暴露。
		common.SysError("qianye: group_matrix.write_guard_enabled 已关闭,新建/编辑令牌时不再校验分组可选性 —— " +
			"读侧仍会在请求时 403,孤儿令牌会继续增加")
	}
	return nil
}

// validateDatabase 校验扩展库配置。
//
// # 受支持的方言
//
// MySQL(默认与主推)与 PostgreSQL >= 9.6。SQLite 与 ClickHouse 明确不支持,
// 理由不是"没写适配"而是语义:扩展库承载资金 —— 佣金账本、两阶段资金单、
// 提现、抽奖出款 —— 这些路径靠 SELECT ... FOR UPDATE 的行锁串行化读改写。
// SQLite 没有行锁,LockForUpdate 只能退化成空操作,而扩展的多节点租约
// (qy_task_leases)与迁移互斥本来就假定多个进程共享同一个库,那正是 SQLite
// 最不擅长的形态。ClickHouse 连唯一约束都没有,幂等键无从谈起。
//
// 拒绝的措辞必须说出这个理由:只写"仅支持 MySQL"会让人以为是适配工作量问题,
// 于是下一个人把 sqlite 驱动接上去,而资金串行化在那一刻静默失效。
func validateDatabase(d *Database) error {
	if strings.TrimSpace(d.DSN) == "" {
		return fmt.Errorf("qianye: database.dsn 不能为空(扩展需要独立的 MySQL 或 PostgreSQL)")
	}
	lower := strings.ToLower(strings.TrimSpace(d.DSN))
	if strings.HasPrefix(lower, "clickhouse://") {
		return fmt.Errorf("qianye: 扩展库只支持 MySQL 与 PostgreSQL,database.dsn 不能以 %q 开头 —— "+
			"资金路径依赖 SELECT ... FOR UPDATE 的行锁,ClickHouse 无法提供;"+
			"它连唯一约束都没有,幂等键更无从谈起", "clickhouse://")
	}
	// SQLite 的判据整包在 qianye/dsn,与 db.DetectDialect 是**同一个函数**。
	//
	// 以前这里只匹配前缀(sqlite: / file: / local),于是最自然的一种写法
	// —— 照抄上游 SQLITE_PATH 的 `./data/qy_ext.db` —— 三条都不匹配,又因为
	// 含 '/' 通过了下面"不像 MySQL DSN(缺少库名)"那一关,最后落到 mysql
	// 驱动手里。运维拿到的是 `default addr for network './data' unknown`:
	// 里面没有 SQLite、没有行锁、没有"不支持",下面这段刻意写下的理由一个字
	// 都没送到。判据提成叶子包之后,两个读者不可能再漂移。
	if dsn.IsSQLite(lower) {
		return fmt.Errorf("qianye: 扩展库不支持 SQLite(database.dsn = %q)—— "+
			"这不是「没写适配」而是语义:资金路径(佣金账本、两阶段资金单、提现、抽奖出款)"+
			"靠 SELECT ... FOR UPDATE 的行锁串行化读改写,SQLite 没有行锁,"+
			"LockForUpdate 只能退化成空操作;多节点租约与迁移互斥又假定多个进程共享同一个库。"+
			"请填 MySQL(user:pass@tcp(host:3306)/dbname)或 PostgreSQL(postgres://...)", d.DSN)
	}
	if isPostgresDSN(lower) {
		return validatePostgresDatabase(d, lower)
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

// isPostgresDSN 判断 DSN 是否指向 PostgreSQL。
//
// 两种写法都要认:URL 形式(postgres:// / postgresql://)与 libpq 关键字形式
// (host=... user=... dbname=...)。判据必须与 db.DetectDialect 完全一致,
// 否则会出现"配置校验按 MySQL 判、驱动按 PostgreSQL 开"这种最难查的分叉。
// 不直接调用 db 包是为了避免 config → db 的反向依赖(db 依赖 config)。
func isPostgresDSN(lower string) bool {
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return true
	}
	for _, tok := range strings.Fields(lower) {
		switch {
		case strings.HasPrefix(tok, "host="),
			strings.HasPrefix(tok, "dbname="),
			strings.HasPrefix(tok, "user="),
			strings.HasPrefix(tok, "port="):
			return true
		}
	}
	return false
}

// validatePostgresDatabase 是 validateDatabase 的 PostgreSQL 分支。
//
// 连接池与 log_level 的校验与 MySQL 完全共用;只有两处必须分开:
//
//  1. 库名的形状。URL 形式的库名在 path 上(postgres://h/dbname),关键字形式
//     在 dbname= 上。拿 MySQL 那条"必须含 /"的判据去卡关键字形式会误报。
//
// write_timeout_seconds 在 PostgreSQL 上没有对应物(见 db.normalizePostgresDSN):
// 读写的时间上界统一落在 read_timeout_seconds 映射出来的 statement_timeout 上。
// 刻意**不**为此告警:它的默认值是 30,不是运维填的,每次启动喊一句
// "本项被忽略" 只会训练所有人无视启动日志。
func validatePostgresDatabase(d *Database, lower string) error {
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		rest := lower[strings.Index(lower, "://")+3:]
		path := rest
		if i := strings.IndexAny(rest, "?"); i >= 0 {
			path = rest[:i]
		}
		if i := strings.Index(path, "/"); i < 0 || strings.TrimSpace(path[i+1:]) == "" {
			return fmt.Errorf("qianye: database.dsn 缺少库名," +
				`期望形如 postgres://user:pass@host:5432/dbname?sslmode=disable`)
		}
	} else if !strings.Contains(lower, "dbname=") {
		return fmt.Errorf("qianye: database.dsn 缺少 dbname=," +
			`期望形如 host=127.0.0.1 port=5432 user=postgres dbname=qy_ext sslmode=disable`)
	}
	if d.MaxIdleConns <= 0 {
		return fmt.Errorf("qianye: database.max_idle_conns 必须大于 0")
	}
	if d.MaxOpenConns < d.MaxIdleConns {
		return fmt.Errorf("qianye: database.max_open_conns(%d) 不得小于 max_idle_conns(%d)",
			d.MaxOpenConns, d.MaxIdleConns)
	}
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

// validateTwoPhase 校验补偿任务的三个判定刻度。
//
// 这三项与本文件其余闸门的方向相反:多数闸门上 0 表示"不设这道限制",而
// 补偿任务的 0 表示"不等了,立刻下判决"。最重的一条是 manual_review_after_seconds:
// 补偿任务探到"主库没动"之后要等足够久才敢判失败,因为很可能只是主库事务
// 还没提交(见 service/twophase/compensate.go);阈值为 0 时任何存活超过一秒的
// pending 单都会被判 failed,而主库随后提交 —— 钱动了,新库却记着没动。
//
// 因此这里拒绝启动,不做静默兜底:兜底会让运维读到的 YAML 与实际跑的值分叉,
// 那正是本扩展反复栽跟头的地方。compensate_interval_seconds 与 batch_size
// 不在此列,它们的消费方本来就带 <=0 回落,且判错只影响节奏不影响判定。
func validateTwoPhase(t *TwoPhase) error {
	for _, f := range []struct {
		name string
		val  int
		why  string
	}{
		{"pending_grace_seconds", t.PendingGraceSeconds,
			"补偿任务会在主库事务尚未提交时就介入探针"},
		{"max_probe_attempts", t.MaxProbeAttempts,
			"第一次退避就把资金单转成 uncertain 交人工裁决"},
		{"manual_review_after_seconds", t.ManualReviewAfterSeconds,
			"存活超过一秒的 pending 单会被直接判 failed,而主库随后提交,两库账目分叉"},
	} {
		if f.val <= 0 {
			return fmt.Errorf("qianye: two_phase.%s 必须大于 0,收到 %d"+
				"(这里的 0 不是「不设限制」而是「立刻下判决」:%s)。"+
				"想恢复默认值请【删掉这一行】,而不是填 0", f.name, f.val, f.why)
		}
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
	// 额度类 YAML 字段一律走同一道上界。此前只有 6 个键走它,而 config.go 里
	// `yaml:"*quota*"` 的额度键有 14 个 —— 漏掉的那 8 个各自被下游的就地守卫
	// 接住(所以没有活的溢出),但那意味着一份把 fee_min_quota 配成 MaxInt64 的
	// YAML 能干净启动,然后**每一次划转**都在 computeFee 里报错。
	// 上界应该在启动那一刻就说话,而不是等第一笔资金操作。
	if err := checkQuotaCap("transfer.daily_max_quota", t.DailyMaxQuota); err != nil {
		return err
	}
	if err := checkQuotaCap("transfer.fee_min_quota", t.FeeMinQuota); err != nil {
		return err
	}
	if err := checkQuotaCap("transfer.max_per_tx_quota", t.MaxPerTxQuota); err != nil {
		return err
	}
	// MaxPerTxQuota == 0 表示不设单笔上限(validate.go 的 `cfg.MaxPerTxQuota > 0` 守卫),
	// 此时 min > max 是空谈。少了这个前置,
	// "只关掉单笔上限"这个合法意图会撞上一条本不适用的跨字段规则,直接起不来。
	if t.MaxPerTxQuota > 0 && t.MinQuota > t.MaxPerTxQuota {
		return fmt.Errorf("qianye: transfer.min_quota(%d) 不得大于 max_per_tx_quota(%d)",
			t.MinQuota, t.MaxPerTxQuota)
	}
	// daily_max_quota 低于 min_quota 是同一类「任何金额都不合法」的组合,只是它
	// 拦在风控那一层而不是受理校验:用户每一笔都能过受理、每一笔都被
	// errDailyLimitExceeded 拒掉,白吃一次冷却与风控预占,而管理端一点提示都没有。
	// 0 同样表示不设这道闸门,因此与上面一条同口径地跳过。
	if t.DailyMaxQuota > 0 && t.DailyMaxQuota < t.MinQuota {
		return fmt.Errorf("qianye: transfer.daily_max_quota(%d) 不得小于 min_quota(%d) —— "+
			"这个组合会让这一档的每一笔划转都在风控处被拒",
			t.DailyMaxQuota, t.MinQuota)
	}
	switch t.RecipientLookup {
	case RecipientLookupID, RecipientLookupIDEmail:
	default:
		return fmt.Errorf("qianye: transfer.recipient_lookup 取值非法: %q(可选 id|id_or_email;"+
			"刻意不提供用户名模糊搜索,那等于开放用户枚举)", t.RecipientLookup)
	}
	return nil
}

func validateInvite(iv *Invite) error {
	// 日界偏移必须落在真实时区的范围里(UTC-12 .. UTC+14)。
	// 越界的值不会报错、不会崩,只会让星屑的日桶与日结日界一起漂到一个
	// 不存在的时区上 —— 那是一次全站日聚合重新分桶,而没有任何东西会喊。
	// 不看 enabled:日界还被下线日消费报表与星屑日结用着,邀请关着它照样生效。
	if iv.DayOffsetMinutes < -720 || iv.DayOffsetMinutes > 840 {
		return fmt.Errorf("qianye: invite.day_offset_minutes 必须落在 -720..840"+
			"(UTC-12 .. UTC+14),收到 %d", iv.DayOffsetMinutes)
	}
	if iv.InviterCacheSecs <= 0 {
		return fmt.Errorf("qianye: invite.inviter_cache_seconds 必须大于 0,收到 %d", iv.InviterCacheSecs)
	}
	return nil
}

// validateCommission 校验佣金段。
//
// 比例与门槛一律越界即拒绝启动,不夹住:这些数字决定平台要付出去多少额度,
// 而一个被夹到边界的值与一个有人批准的值在账本上长得一模一样。
func validateCommission(cm *Commission, stardustEnabled bool) error {
	if err := checkBps("commission.topup_rate_bps", cm.TopupRateBps); err != nil {
		return err
	}
	if err := checkBps("commission.consume_rate_bps", cm.ConsumeRateBps); err != nil {
		return err
	}
	// 兑换码档可空(nil = 跟随充值档);填了就必须合法。
	if cm.RedemptionRateBps != nil {
		if err := checkBps("commission.redemption_rate_bps", *cm.RedemptionRateBps); err != nil {
			return err
		}
	}
	if cm.Levels != 1 {
		return fmt.Errorf("qianye: commission.levels 当前仅支持 1 级,收到 %d"+
			"(多级分销正是要规避的「拉人头」形状)", cm.Levels)
	}
	if err := checkQuotaCap("commission.min_settle_stardust", cm.MinSettleStardust); err != nil {
		return err
	}
	if err := checkQuotaCap("commission.max_per_order_stardust", cm.MaxPerOrderStardust); err != nil {
		return err
	}
	if err := checkQuotaCap("commission.min_credit_stardust", cm.MinCreditStardust); err != nil {
		return err
	}
	if cm.MinSettleStardust <= 0 {
		return fmt.Errorf("qianye: commission.min_settle_stardust 必须大于 0" +
			"(佣金按 decimal 全精度累计,达到该值才结算为整数星屑,否则小额佣金会被截断归零)")
	}
	if cm.MinCreditStardust <= 0 {
		return fmt.Errorf("qianye: commission.min_credit_stardust 必须大于 0" +
			"(它是自动入账的起点,也是单次入账的下限)")
	}
	// 佣金以星屑记账,而星屑账本(qy_sd_ledger)在 stardust 关掉时根本不存在 ——
	// 计佣照跑、结算照跑,到自动入账那一步每一轮都失败,账本停在"可用"再也出不去。
	// 这是启动期就能判死的配置错误,不该等到第一个人攒够门槛才暴露。
	if cm.Enabled && !stardustEnabled {
		return fmt.Errorf("qianye: commission.enabled=true 需要 stardust.enabled=true" +
			"(D-16 之后佣金以星屑结算、自动入账进 qy_sd_balance;星屑关着时佣金无处可发)")
	}
	// 持有期为负会让整天的佣金在**这一天刚开始**时就成熟,防套利延迟反向生效。
	if cm.HoldingDays < 0 {
		return fmt.Errorf("qianye: commission.holding_days 不能为负(0 = 当天结束即可结算),收到 %d", cm.HoldingDays)
	}
	// settle_interval_seconds / credit_interval_seconds 是节奏参数:0 由 module.go 回落成 300,
	// 与其余"周期"类字段同一口径,不在这里拒绝。
	return nil
}

func validateTicket(t *Ticket) error {
	if !t.Enabled {
		return nil
	}
	// 标题/正文/消息条数上限为 0 不是"不限制",而是"一个字都不许填"/"一条都不许发",
	// 那会让整个功能在运行期表现为"提交总是失败",而配置看起来完全正常。
	if t.TitleMaxRunes <= 0 || t.TitleMaxRunes > 500 {
		return fmt.Errorf("qianye: ticket.title_max_runes 必须在 1..500 之间,收到 %d", t.TitleMaxRunes)
	}
	if t.BodyMaxRunes <= 0 || t.BodyMaxRunes > 50000 {
		return fmt.Errorf("qianye: ticket.body_max_runes 必须在 1..50000 之间,收到 %d"+
			"(正文是 Markdown 源码,整条消息按 rune 计,不是渲染后的长度)", t.BodyMaxRunes)
	}
	if t.MaxMessagesPerTicket <= 0 {
		return fmt.Errorf("qianye: ticket.max_messages_per_ticket 必须大于 0,收到 %d"+
			"(0 会让工单建出来之后一条回复都发不了;不想限制请填一个足够大的数)",
			t.MaxMessagesPerTicket)
	}
	if t.ImageMaxPerMessage <= 0 {
		return fmt.Errorf("qianye: ticket.image_max_per_message 必须大于 0,收到 %d"+
			"(不想收图请用 image_enabled: false)", t.ImageMaxPerMessage)
	}
	// 图片要整张读进内存才能校验魔数,上限必须有硬顶。校验放在 image_enabled 之外:
	// 填 0 或天文数字都是配置错误,不该等到某天打开图片功能才第一次暴露出来。
	if t.ImageMaxBytes <= 0 || t.ImageMaxBytes > MaxTicketImageBytes {
		return fmt.Errorf("qianye: ticket.image_max_bytes 必须在 1..%d 字节之间,收到 %d"+
			"(图片需整张读进内存做魔数校验,不设硬顶等于把堆交给上传者)",
			MaxTicketImageBytes, t.ImageMaxBytes)
	}
	if t.ImageRetentionDays < 0 {
		return fmt.Errorf("qianye: ticket.image_retention_days 不得为负数(0 表示永久保留)")
	}
	// 负数会让"用了多少"永远大于配额,表现为任何人都传不了图,而配置看起来正常。
	if t.ImageUserQuotaBytes < 0 {
		return fmt.Errorf("qianye: ticket.image_user_quota_bytes 不得为负数"+
			"(0 表示不限制,但那样就没有任何一道总量闸了),收到 %d", t.ImageUserQuotaBytes)
	}
	if t.ImageUserQuotaBytes > 0 && t.ImageUserQuotaBytes < t.ImageMaxBytes {
		return fmt.Errorf("qianye: ticket.image_user_quota_bytes(%d)小于 image_max_bytes(%d),"+
			"那样第一张合法图片就会被配额拒掉", t.ImageUserQuotaBytes, t.ImageMaxBytes)
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
	// AI 审核密钥是**可选**的:不配就是"存不下 api_key",不是"启动失败"。
	// 但填了就必须是一把真钥匙 —— 填半截(比如粘贴时少了尾巴)的后果是
	// 全部审核渠道的密钥都写不进去,而界面上只会显示一句通用失败。
	if strings.TrimSpace(v.AIReviewKey) != "" {
		if err := checkAESKey("violation.ai_review_key", v.AIReviewKey); err != nil {
			return err
		}
	}
	if v.AIReviewKeyVersion < 0 {
		return fmt.Errorf("qianye: violation.ai_review_key_version 不能为负数,收到 %d", v.AIReviewKeyVersion)
	}
	for version, key := range v.AIReviewKeysRetired {
		if version <= 0 {
			return fmt.Errorf("qianye: violation.ai_review_keys_retired 的版本号必须大于 0,收到 %d", version)
		}
		if version == v.AIReviewKeyVersion {
			return fmt.Errorf("qianye: violation.ai_review_keys_retired 不得包含当前启用的版本 %d"+
				"(当前密钥只在 ai_review_key 里配一份,两处不一致会让新密文用一把钥匙、解密用另一把)", version)
		}
		if err := checkAESKey(fmt.Sprintf("violation.ai_review_keys_retired[%d]", version), key); err != nil {
			return err
		}
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

// checkQuotaCap 校验额度上限类字段不超过 common.MaxQuota。
//
// 那不是列宽(users.quota 在三个方言上都是 64 位列),而是全站额度换算的**算术**
// 上界:超过它的门槛过不了 common/quota_math.go 的换算,于是不是"更严格",
// 而是**永远无法被满足** —— 必须在配置阶段就拒绝。
func checkQuotaCap(name string, v int64) error {
	if v < 0 {
		return fmt.Errorf("qianye: %s 不得为负数,收到 %d", name, v)
	}
	if v > int64(common.MaxQuota) {
		return fmt.Errorf("qianye: %s(%d)超过全站额度上界 %d(common.MaxQuota,由代码写死)",
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
