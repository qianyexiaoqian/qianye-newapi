package transfer

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
)

// grouplimit.go —— 划转门槛的「按用户分组分档」覆盖层。
//
// ══════════════════════ 这一层压在哪两层之间 ══════════════════════
//
//	YAML(config.Transfer)              底
//	qy_settings scope=transfer          全站覆盖(settings.go)
//	qy_transfer_group_limits            **本文件**:按用户分组再覆盖一层
//
// 三层合并之后输出的仍然是一个 config.Transfer。这一点与 settings.go 的理由
// 逐字同源:另起一个结构体意味着 validateCreate / evaluateRisk / loadParties /
// applyQuotaTransfer 的签名要全部改一遍,而且从此有两份「划转门槛」的定义 ——
// 加一个字段就要在两处各加一次,漏一处就是一道空闸门。
//
// ══════════════════ 「没配」与「配成 0」必须可区分 ══════════════════
//
// 划转门槛里的 0 **是一个有含义的取值**:`cfg.DailyMaxQuota > 0` 这类守卫遍布
// risk.go 与 validate.go,0 表示「这道闸门不设」。于是分档表里如果用零值表示
// 「这一档没覆盖这一项」,运营就永远配不出「vip 不限日额度」这句话 ——
// 他填的 0 会被当成「没配」而回落到全局的 2 亿。
//
// 本仓已经三次栽在这个形状上,因此这里用**指针列**承载三态:
//
//	nil    这一档没有覆盖这一项 → 回落全局(qy_settings 覆盖后的值)
//	&0     这一档显式配成 0     → 这道闸门对这一档不设
//	&n     这一档配成 n
//
// 指针在 GORM 上直接落成可空列,三种数据库一致;读回来 nil 就是 SQL NULL,
// 不经过任何「零值等于未设置」的推断。
//
// ══════════════════════ 为什么不塞进 qy_settings ══════════════════════
//
// 与 grouprule.go 的理由同源:qy_settings 是 (scope, k) → 字符串的 KV,适合
// 「一个数字一个开关」。分档是一张有结构的清单 —— 增删改要能定位到行、
// 两个管理员同时编辑不同分组不能互相覆盖、每一次改动都要能在审计里看到
// 「改的是哪一档、从什么变成什么」。把整张清单编码成一个 JSON 值塞进一个 KV,
// 两个管理员同时点保存就会丢掉其中一个人的修改。
//
// ══════════════════════ 分档的键是**用户分组** ══════════════════════
//
// 与 qy_transfer_group_rules.from_group 同一个命名空间:users.group。
// 归一化走同一个 normalizeGroupName(groupname.Effective),理由见那里 ——
// 「代码敏感、存储不敏感」的中间态会让一档收紧的门槛静默失效。

// tierableKeys 是允许按用户分组分档的门槛键,**顺序即管理端的渲染顺序**。
//
// 刻意只有金额与频次这七项,不含手续费(fee_bps / fee_min_quota)与支付密码
// 策略(pay_pwd_*):
//
//   - 项目方要的是「门槛按用户组单独配置」。手续费不是门槛,它是定价;
//     按分组定价要连带回答「重试期间用户换了分组按哪一档收费」,而手续费是
//     受理时算出、写进资金单、不参与幂等指纹的派生量(见 fundingFacts),
//     把它分档会让那段推理多出一个维度,而没有人要求过这件事。
//   - 支付密码的两项策略压根不在 config.Transfer 里,它们的读取侧在 paypass,
//     按分组分档要先把 paypass 的读取口径也改成按分组,射程之外。
//
// 新增可分档的键只需要往这里加一项 + 在 GroupLimit 上加一个指针列 +
// 在 fieldPtr 里加一个 case,三处缺一个就编译不过或者静默不生效,
// 由 TestTierableKeysAreAllAddressable 顶住。
var tierableKeys = []string{
	keyMinQuota, keyMaxPerTxQuota, keyDailyMaxQuota,
	keyDailyMaxCount, keyCooldownSecs, keyReceiverDailyMaxInCount,
	keyNewAccountFreezeHours,
}

// groupLimitCountGate 是"分档条数上限"这道闸门的锚点行键(见 qymodel.LockGate)。
const groupLimitCountGate = "transfer:group_limit_count"

// maxGroupLimitCount 是分档条数的硬上限,理由与 maxGroupRuleCount 同源:
// 这张表每 settingsCacheSeconds 会被全量读一遍,不设上界的话一次脚本误操作
// 就能把它拖成慢查询,而用户分组本身就是个位数量级的东西。
const maxGroupLimitCount = 200

// GroupLimit 是一条「某个用户分组的划转门槛覆盖」。
//
// 每一个可分档的门槛都是 *int64 —— 三态的落点,见文件头。
// 统一用 int64 而不是跟着 config.Transfer 的 int/int64 混排:分档的读写、
// 校验、审计快照全部按 key 走同一段代码,类型一分叉就要写两套。
// 收敛回 int 发生在 assignTransferSetting,与 qy_settings 覆盖层同一处。
type GroupLimit struct {
	// UserGroup 是**用户分组**(users.group),一律以归一化后的形式存储。
	// 刻意不支持 groupWildcard:兜底就是全局门槛本身,再给它一行只会制造
	// 「兜底的兜底」这种没人推得清的层级。
	UserGroup string `json:"user_group" gorm:"column:user_group;type:varchar(64);primaryKey"`

	MinQuota                *int64 `json:"min_quota" gorm:"column:min_quota"`
	MaxPerTxQuota           *int64 `json:"max_per_tx_quota" gorm:"column:max_per_tx_quota"`
	DailyMaxQuota           *int64 `json:"daily_max_quota" gorm:"column:daily_max_quota"`
	DailyMaxCount           *int64 `json:"daily_max_count" gorm:"column:daily_max_count"`
	CooldownSecs            *int64 `json:"cooldown_seconds" gorm:"column:cooldown_seconds"`
	ReceiverDailyMaxInCount *int64 `json:"receiver_daily_max_in_count" gorm:"column:receiver_daily_max_in_count"`
	NewAccountFreezeHours   *int64 `json:"new_account_freeze_hours" gorm:"column:new_account_freeze_hours"`

	// Enabled 为 false 时整档视同不存在 ⇒ 这一组人回落全局门槛。
	// 它与「某一项没覆盖」是两件事:前者是一个开关,后者是七个独立的三态。
	//
	// 刻意不写 gorm:"default:true":MySQL 与 PostgreSQL 对布尔默认值的归一化
	// 不同,会让 AutoMigrate 每次重启都发一条 ALTER TABLE(见 AGENTS.md)。
	Enabled bool `json:"enabled" gorm:"not null"`

	Remark string `json:"remark" gorm:"type:varchar(255);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
	UpdatedBy int   `json:"updated_by" gorm:"not null;default:0"`
}

func (GroupLimit) TableName() string { return "qy_transfer_group_limits" }

// fieldPtr 返回某个可分档键对应的那个指针字段的地址,未登记的键返回 nil。
//
// 返回 **int64 而不是分成 get/set 两个 switch:两个 switch 必然漂移,
// 而漂移的形状是「读得到、写不进去」—— 管理员保存成功、界面显示已覆盖、
// 实际那一档还在按全局门槛放行,零告警。
func (g *GroupLimit) fieldPtr(key string) **int64 {
	switch key {
	case keyMinQuota:
		return &g.MinQuota
	case keyMaxPerTxQuota:
		return &g.MaxPerTxQuota
	case keyDailyMaxQuota:
		return &g.DailyMaxQuota
	case keyDailyMaxCount:
		return &g.DailyMaxCount
	case keyCooldownSecs:
		return &g.CooldownSecs
	case keyReceiverDailyMaxInCount:
		return &g.ReceiverDailyMaxInCount
	case keyNewAccountFreezeHours:
		return &g.NewAccountFreezeHours
	}
	return nil
}

// override 取出这一档对某个键的覆盖值。第二个返回值 false 表示**没有覆盖**,
// 与「覆盖成 0」严格区分 —— 那正是本文件存在的理由。
func (g GroupLimit) override(key string) (int64, bool) {
	p := (&g).fieldPtr(key)
	if p == nil || *p == nil {
		return 0, false
	}
	return **p, true
}

// tierSet 是一次配置快照里冻结的分档视图。**调用方禁止修改它。**
//
// 与 groupRuleSet 同形:键是归一化后的用户分组名,缺键 = 这一档没有覆盖,
// 回落全局。停用的行在构建时就被丢掉,因此判定端不需要再看 Enabled。
type tierSet map[string]GroupLimit

func buildTierSet(rows []GroupLimit) tierSet {
	out := make(tierSet, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		out[normalizeGroupName(row.UserGroup)] = row
	}
	return out
}

// loadTiers 读出全部分档行。
//
// 与 loadOverrides 同一条口径:读失败一律向上抛,绝不回落成「没有分档」——
// 空表是合法的「全站一套门槛」,查库失败不是。两者混同会让扩展库抖动的那一瞬间
// 变成**全部分档同时失效**,而分档既可能比全局松也可能比全局紧,
// 失效的方向无法预先断言,这正是不能容忍它静默发生的原因。
//
// 不加 Limit:截断会静默丢掉几档,那等于悄悄把那几组人的门槛换回全局。
// 数量上界由写入侧的 maxGroupLimitCount 保证。
func loadTiers(ctx context.Context) ([]GroupLimit, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	rows := make([]GroupLimit, 0, 16)
	if err := gdb.WithContext(ctx).Order("user_group asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}

// tightenOnlyKeys 列出「分档只许比全局更严」的门槛。
//
// 判据是这一项到底在卖什么:额度型门槛(单笔上限、日额度、日笔数、冷却、
// 收款方入账笔数)分档放宽就是这个功能的产品形态;而新账号冻结期是一道
// **反滥用身份闸门**,它一旦可以被分档放宽,就等于可以被一次购买抹掉。
var tightenOnlyKeys = map[string]bool{
	keyNewAccountFreezeHours: true,
}

// stricterSetting 返回同一个键上更严的那个取值。
//
// 两类语义必须分开:
//   - 下限型(min_quota / cooldown_seconds / new_account_freeze_hours):数越大越严;
//   - 上限型(max_per_tx / daily_max_quota / daily_max_count /
//     receiver_daily_max_in_count):数越小越严,而 **0 表示不限**,
//     所以 0 是最松而不是最严 —— 按 min 直接比会把「不限」当成「一分钱都不许转」。
func stricterSetting(key string, a, b int64) int64 {
	switch key {
	case keyMinQuota, keyCooldownSecs, keyNewAccountFreezeHours:
		if a > b {
			return a
		}
		return b
	default:
		if a == 0 {
			return b
		}
		if b == 0 {
			return a
		}
		if a < b {
			return a
		}
		return b
	}
}

// strictestTransfer 逐项取两份门槛里更严的那个(只作用于可分档的七项)。
func strictestTransfer(a, b config.Transfer) config.Transfer {
	out := a
	for _, key := range tierableKeys {
		av, aok := transferSettingValue(a, key)
		bv, bok := transferSettingValue(b, key)
		if !aok || !bok {
			continue
		}
		assignTransferSetting(&out, key, stricterSetting(key, av, bv))
	}
	return out
}

// transferForSenderDay 返回发起方这一笔该用的门槛。
//
// # 为什么不能只看「此刻的 users.group」
//
// 分档按当前分组解析,而 users.group 是**用户自己花钱就能改的**:任何一个
// upgrade_group 指向更松档、且允许余额支付的套餐(这正是用户组商品的设计用途)
// 都能让人在当天额度用满之后花几千 quota 换一档接着转 —— 而 qy_transfer_user_state
// 里当天已经用掉的计数一个都不重置,只是上限换了一档。实测过 0.01 美元换到
// 6000 倍日额度。
//
// # 口径
//
// 今天的计数是在哪一档下累起来的(dayGroup),那一档就在今天剩下的时间里
// 继续作数:实际门槛 = 严(当前档, 今天那一档)。于是
//   - 当天还没转过账的人换档 ⇒ dayGroup 为空 ⇒ 直接按新档,那是他买到的东西;
//   - 当天已经转过账的人换档 ⇒ 今天仍受旧档约束,明天自然日一到重新开始。
//
// 跨日由 rollDay 负责:bucket 一变,dayGroup 与三个计数一起清零。
func (s opSettings) transferForSenderDay(userGroups []string, st *UserState, bucket int32) (config.Transfer, error) {
	cur, err := s.transferForBase(userGroups)
	if err != nil {
		return config.Transfer{}, err
	}
	if st == nil || st.DayBucket != bucket || st.DayOutGroup == "" {
		return cur, nil
	}
	for _, g := range userGroups {
		if normalizeGroupName(st.DayOutGroup) == normalizeGroupName(g) {
			return cur, nil
		}
	}
	day, err := s.transferFor(st.DayOutGroup)
	if err != nil {
		// 今天那一档本身配坏了:按当前档走,不把一次历史错配变成这个人今天
		// 再也转不了账。放宽方向由 transferFor 自己的 fail-closed 兜住。
		return cur, nil
	}
	return strictestTransfer(cur, day), nil
}

// transferForBase 把 baseUserGroups 给出的多个基准分组逐项取严成一份门槛。
//
// 清单里通常只有一个(users.group 本身),取严退化成 transferFor,行为一字不变。
// 第二个只在「用户此刻坐的分组是某张已到期套餐的显式 downgrade_group 落点」时
// 出现 —— 那时他既在落点那一档、也仍然要受买套餐之前那一档约束,两档取严。
//
// 任何一档解析失败都整体失败关闭:与 transferFor 同一条口径,宁可让这一档人的
// 划转返回 503,也不能拿一份谁都没批准过的组合去放行资金操作。
func (s opSettings) transferForBase(userGroups []string) (config.Transfer, error) {
	if len(userGroups) == 0 {
		return s.transferFor("")
	}
	out, err := s.transferFor(userGroups[0])
	if err != nil {
		return config.Transfer{}, err
	}
	for _, g := range userGroups[1:] {
		next, err := s.transferFor(g)
		if err != nil {
			return config.Transfer{}, err
		}
		out = strictestTransfer(out, next)
	}
	return out, nil
}

// transferFor 返回某个用户分组**实际生效**的那份门槛,是资金路径唯一该用的入口。
//
// 纯函数,不碰数据库:分档合并是这一层最容易写错的一段(三态、回落、跨字段
// 校验),必须能脱离数据库直接测到。
//
// # 合并结果不自洽时失败关闭
//
// 逐键的区间校验挡不住跨字段矛盾(min_quota > max_per_tx_quota 时任何金额都
// 不合法,而两个值各自都在区间内)。写入侧已经用当时的全局值挡过一次,但**全局
// 门槛后来还会变** —— 运营把全站单笔上限调低到某一档的 min_quota 之下,那一档
// 就地变成「任何金额都不合法」。此时宁可让这一档人的划转返回 503,也不能拿一份
// 谁都没批准过的组合去放行资金操作。
//
// 失败面刻意只有这一档:全局门槛非法时整个划转停摆(errSettingsInvalid),
// 而一档配坏了只停这一档 —— 让一个分组的错配把全站划转掐掉,是运营最不该
// 承担的连带风险。
func (s opSettings) transferFor(userGroup string) (config.Transfer, error) {
	out := s.Transfer
	row, ok := s.Tiers[normalizeGroupName(userGroup)]
	if !ok {
		return out, nil
	}
	for _, key := range tierableKeys {
		v, has := row.override(key)
		if !has {
			continue
		}
		if tightenOnlyKeys[key] {
			// 反滥用闸门只许分档**收紧**,不许放宽。
			//
			// new_account_freeze_hours 的判据分成了两半:年龄取自 users.created_at
			// (攻击者改不了),档位取自 users.group(攻击者可以花 0.01 美元买一个
			// 升组套餐改掉)。于是一个注册 9 秒的号买一次套餐就把一道 30 天的
			// 反套现闸门抹掉了,而这道门存在的唯一理由正是「批量注册的小号是
			// 零成本套现的入口」(见 loadParties)。
			//
			// 其余六项是额度型门槛 —— 「买高档得更宽的额度」正是这个功能要卖的
			// 东西,不在此列。
			global, _ := transferSettingValue(s.Transfer, key)
			if v < global {
				v = global
			}
		}
		assignTransferSetting(&out, key, v)
	}
	if err := config.ValidateTransfer(&out); err != nil {
		warnThrottled("用户分组 " + row.UserGroup + " 的划转门槛分档与当前全局门槛组合非法,已暂停该分组的划转: " + err.Error())
		return config.Transfer{}, errGroupLimitInvalid
	}
	return out, nil
}

// 生效值的来源。字符串而不是布尔:将来若再多一层(例如按套餐),
// 布尔会变成一个必须同时改前后端的谎。
//
// 逐项的来源由 buildGroupLimitRow 在管理端算出来。资金路径一律走 transferFor,
// 拿到的是一份合并好的 config.Transfer —— 刻意不给判定端一张「值 + 来源」的
// map,那种形状迟早会有一处只读了值、忘了看它是不是有效。
const (
	tierSourceGlobal = "global"
	tierSourceGroup  = "group"
)

// validateGroupLimit 校验并就地归一化一条分档,写库前必须过这一关。
//
// global 传的是**当前生效的全局门槛**(YAML + qy_settings)。跨字段校验必须
// 看到「这一档没覆盖的那些项现在是多少」,否则单独把 min_quota 调到全局
// max_per_tx_quota 之上会一路放行,而那一档人从此任何金额都不合法。
//
// 返回的 error 是给管理员看的修正依据(与 validateGroupRule 同口径),
// 因此是自然语言而不是 code —— 这些消息只有管理员看得到。
func validateGroupLimit(row *GroupLimit, global config.Transfer) error {
	row.UserGroup = strings.TrimSpace(row.UserGroup)
	if row.UserGroup == "" {
		return fmt.Errorf("用户分组不能为空")
	}
	if row.UserGroup == groupWildcard {
		return fmt.Errorf("分档不支持通配符 %q:兜底就是全局门槛本身,请直接改上面那一组", groupWildcard)
	}
	// 先归一再校验,不能反过来:落库的必须就是被校验过的那个值,
	// 否则「校验通过的字符串」与「实际存进去的字符串」是两个东西。
	row.UserGroup = normalizeGroupName(row.UserGroup)
	if err := checkGroupToken(row.UserGroup); err != nil {
		return err
	}

	merged := global
	covered := 0
	for _, key := range tierableKeys {
		p := row.fieldPtr(key)
		if *p == nil {
			continue
		}
		v := **p
		b := settingBounds[key]
		if v < b.Lo || v > b.Hi {
			return fmt.Errorf("%s 取值 %d 超出允许区间 [%d, %d]", key, v, b.Lo, b.Hi)
		}
		assignTransferSetting(&merged, key, v)
		covered++
	}
	if covered == 0 {
		// 一档不覆盖任何项与「没有这一档」完全等价。留着它只会让运营在列表里
		// 看到一行、以为这组人被单独配过,而实际判定与别人一模一样。
		return fmt.Errorf("这一档没有覆盖任何门槛,与不配置完全等价;请至少覆盖一项,或直接删掉这一档")
	}
	if err := config.ValidateTransfer(&merged); err != nil {
		return err
	}
	row.Remark = truncate(strings.TrimSpace(row.Remark), maxGroupRuleRemark)
	return nil
}

// groupLimitSnapshot 把一档摊成审计快照。
//
// **未覆盖的项必须以 null 出现,不能省略、更不能写 0。** 审计是事后仲裁
// 「这一档当时到底配了什么」的唯一凭据,而 0 与未覆盖在这套门槛里是两个
// 完全不同的策略(不设限 vs 跟全局走)。省略字段则让人无法区分
// 「当时没配」与「当时的版本还没有这一项」。
func groupLimitSnapshot(row *GroupLimit) string {
	if row == nil {
		return ""
	}
	out := map[string]any{
		"user_group": row.UserGroup,
		"enabled":    row.Enabled,
		"remark":     row.Remark,
	}
	for _, key := range tierableKeys {
		if v, has := row.override(key); has {
			out[key] = v
			continue
		}
		out[key] = nil
	}
	return common.MapToJsonStr(out)
}

// errGroupLimitInvalid 表示某一档与当前全局门槛的组合自相矛盾。
//
// 用 503 而不是 400:这是服务端配置状态问题,不是用户请求写错了,重试也没用,
// 必须由管理员去改。文案刻意说明「你所在的用户组」,否则用户会以为是自己的
// 账号出了问题而反复重试。
var errGroupLimitInvalid = newBizError("qy_transfer_group_limit_invalid",
	"你所在用户组的划转门槛配置异常,已暂停划转,请联系管理员", http.StatusServiceUnavailable)

var errGroupLimitNotFound = newBizError("qy_transfer_group_limit_not_found",
	"该用户分组没有门槛覆盖", http.StatusNotFound)
