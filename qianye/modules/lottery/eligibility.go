package lottery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"
	"github.com/QuantumNous/new-api/qianye/modules/violation"

	"gorm.io/gorm"
)

// eligibility.go —— 参与资格引擎。
//
// # 两个口径先定死,否则同一份规则会给出两种结论
//
//	规则:用 **publish 时冻结的快照**(rules_text,进 rules_hash)。
//	      运营中途改配置不影响进行中的活动。
//	用户属性:用**报名那一刻的值**,连同判定输入写进 entry.eligibility_snapshot。
//	      **开奖时不再复检资格。**
//
// 理由:① 名单冻结是承诺的一部分,开奖时重筛意味着名单不是被承诺的那份,
// 第三方复算必然对不上;② "报名时合格 → 开奖前被降组 → 开奖时剔除"会给管理员
// 一个开奖前调整名单的合法外衣;③ 用户已付费,事后没收资格是最容易引发纠纷
// 且最没法自证的做法。
//
// 代价:一个报名后被封禁的用户仍可能中奖 —— 他的账号状态问题在 payout 阶段
// 单独处理(转 held 交人工),而不是在名单里做手脚。
//
// 唯一例外是扣费瞬间主库行锁内复检**三项且只有三项**:users.status、
// users.group、余额是否够扣。它们是**扣钱的前提**而不是**资格的前提**。
//
// # fail-closed
//
// 任何一项条件的数据源读取失败,一律拒绝报名。风控条件一旦在故障时形同虚设,
// 那正是被主动构造去打的时刻(照抄 transfer.loadGroupRules 的取舍)。

// Rules 是一个活动的全部参与条件。它的规范化 JSON 就是 rules_text,
// 进 rules_hash、进 commit_hash,发布后永久冻结。
type Rules struct {
	// AllowGroups 非空时是白名单;DenyGroups 是黑名单,优先级更高。
	AllowGroups []string `json:"allow_groups"`
	DenyGroups  []string `json:"deny_groups"`

	MinAccountAgeDays int `json:"min_account_age_days"`
	// MinQuota 是"当前余额不低于 N"。它与参与费是两件事:
	// 余额刚好够扣的人可以参加一个不设余额门槛的活动。
	MinQuota int64 `json:"min_quota"`
	// MinUsedQuota 读 users.used_quota(全时段累计,不可切窗)。
	MinUsedQuota int64 `json:"min_used_quota"`
	// RecentSpendDays / RecentSpendQuota 是"近 N 日内消费满 M"。
	// 数据源是 qy_lot_spend_daily,报名路径禁止任何对 logs 的查询。
	RecentSpendDays  int   `json:"recent_spend_days"`
	RecentSpendQuota int64 `json:"recent_spend_quota"`

	// ExcludeViolation 为 true 时,有任何一条**真实**违规记录即排除。
	// 影子命中(shadow=true)与已撤销(status='revoked')一律不算:
	// 前者是观察样本、后者是平台自己承认的误判,当成真违规会误伤守规用户。
	ExcludeViolation bool `json:"exclude_violation"`
	// MaxViolationHits 是滚动窗口内允许的命中数上限,0 = 不限。
	// 与 ExcludeViolation 并列而不是二选一:运营可能只想挡"最近很闹腾"的人。
	MaxViolationHits int `json:"max_violation_hits"`
	// ExcludeEverAutoBanned 查 qy_violation_ban(自动封禁史)。
	//
	// 必须与"当前被封禁"分成两项:**管理员手工封禁不写 qy_violation_ban,
	// 只改 users.status**。混为一谈会让运营以为自己拦住了实际没拦住的人。
	ExcludeEverAutoBanned bool `json:"exclude_ever_auto_banned"`
	// ExcludeCurrentlyDisabled 恒为 true(被封禁的账号根本扣不了费)。
	// 仍然落进规则里,是为了让管理端表单能把这两项的口径分开写清楚。
	ExcludeCurrentlyDisabled bool `json:"exclude_currently_disabled"`

	// 本系统**不存在任何实名字段**(model.User 没有 real_name/id_card/verified,
	// 全仓只有 passkey 的 WebAuthn 标志位)。以下三项是可用的替代信号,
	// 管理端表单必须原样写明这一点,不含糊其辞。
	RequireEmail       bool `json:"require_email"`
	RequireOAuth       bool `json:"require_oauth"`
	RequirePayPassword bool `json:"require_pay_password"`

	// 频次与规模闸门。它们同样进承诺 —— 事后改上限等于改规则。
	MaxEntriesPerUser int `json:"max_entries_per_user"`
	// MaxAttemptsPerUser 把**失败尝试**也计入名额,防止用"必然余额不足"的
	// 报名膨胀哈希链与 proof 体积(失败条目永久留在链上占一个 seq)。
	MaxAttemptsPerUser int `json:"max_attempts_per_user"`
	MaxTotalEntries    int `json:"max_total_entries"`
	// MaxTotalUsers 与 MaxTotalEntries 分开:允许多次参与时,
	// 人数上限拦不住一个人刷 1000 笔。
	MaxTotalUsers int `json:"max_total_users"`
	MaxPerInviter int `json:"max_per_inviter"`
	// CooldownSeconds 是同一用户两次参与的最小间隔。次数上限防不住
	// "在 close 前 1 秒用脚本连发把全场名额吃光"。
	CooldownSeconds int `json:"cooldown_seconds"`
	// DedupIp 默认关闭,代价见 Activity.DedupIp。
	DedupIp bool `json:"dedup_ip"`
}

// Normalize 做归一化与默认值。
//
// 默认值在这里给而不是在 config/defaults.go 里判 0:那些是全站配置,
// 这些是单个活动的业务默认。
func (r Rules) Normalize() Rules {
	r.AllowGroups = normalizeGroups(r.AllowGroups)
	r.DenyGroups = normalizeGroups(r.DenyGroups)

	clampNeg := func(v *int) {
		if *v < 0 {
			*v = 0
		}
	}
	clampNeg64 := func(v *int64) {
		if *v < 0 {
			*v = 0
		}
	}
	clampNeg(&r.MinAccountAgeDays)
	clampNeg64(&r.MinQuota)
	clampNeg64(&r.MinUsedQuota)
	clampNeg(&r.RecentSpendDays)
	clampNeg64(&r.RecentSpendQuota)
	clampNeg(&r.MaxViolationHits)
	clampNeg(&r.MaxEntriesPerUser)
	clampNeg(&r.MaxAttemptsPerUser)
	clampNeg(&r.MaxTotalEntries)
	clampNeg(&r.MaxTotalUsers)
	clampNeg(&r.MaxPerInviter)
	clampNeg(&r.CooldownSeconds)

	// 近 N 日消费的两个字段必须成对出现,只填一个是配置错误而不是"不限制"。
	if r.RecentSpendQuota > 0 && r.RecentSpendDays == 0 {
		r.RecentSpendDays = 30
	}
	if r.RecentSpendDays > 0 && r.RecentSpendQuota == 0 {
		r.RecentSpendDays = 0
	}

	// 尝试上限默认比成功上限宽 3 次:留出"余额刚好不够、改完再来"的余地,
	// 又不至于让链被无限膨胀。
	if r.MaxEntriesPerUser > 0 && r.MaxAttemptsPerUser == 0 {
		r.MaxAttemptsPerUser = r.MaxEntriesPerUser + 3
		// 派生值不许把一个**合法的**填写变成非法。
		//
		// buildActivity 在 Normalize 之后才拿 MaxAttemptsPerUser 去比 perUserCapHard,
		// 于是运营填 498/499/500(都 ≤ 500、都合法)会被派生出的 501/502/503
		// 顶回来,而报错文案念的正是「不得超过 500」—— 照着它填 500 会被反复拒绝,
		// 真实可用上界其实是 497,而没有任何一处说得出这个数。
		// 夹住而不是拒绝:+3 只是"留点余地"的便利,便利不该改变可填区间。
		if r.MaxAttemptsPerUser > perUserCapHard {
			r.MaxAttemptsPerUser = perUserCapHard
		}
	}
	if r.MaxAttemptsPerUser > 0 && r.MaxEntriesPerUser > 0 && r.MaxAttemptsPerUser < r.MaxEntriesPerUser {
		r.MaxAttemptsPerUser = r.MaxEntriesPerUser
	}

	// 恒真:被封禁的账号在主库行锁内根本扣不了费。
	r.ExcludeCurrentlyDisabled = true
	return r
}

func normalizeGroups(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	// 排序让同一组条件无论运营以什么顺序勾选都算出同一个 rules_hash。
	sort.Strings(out)
	return out
}

// CanonicalText 返回将被落库并参与哈希的规范化 JSON。
//
// 走 map 而不是直接 Marshal 结构体:encoding/json 对 map 的键排序有明确的
// 字节序保证,而结构体的顺序是字段声明顺序 —— 有人重排字段就会静默改变
// 所有历史活动的 rules_hash 复算结果。
func (r Rules) CanonicalText() (string, error) {
	return Canonical(map[string]any{
		"allow_groups":               r.AllowGroups,
		"deny_groups":                r.DenyGroups,
		"min_account_age_days":       r.MinAccountAgeDays,
		"min_quota":                  r.MinQuota,
		"min_used_quota":             r.MinUsedQuota,
		"recent_spend_days":          r.RecentSpendDays,
		"recent_spend_quota":         r.RecentSpendQuota,
		"exclude_violation":          r.ExcludeViolation,
		"max_violation_hits":         r.MaxViolationHits,
		"exclude_ever_auto_banned":   r.ExcludeEverAutoBanned,
		"exclude_currently_disabled": r.ExcludeCurrentlyDisabled,
		"require_email":              r.RequireEmail,
		"require_oauth":              r.RequireOAuth,
		"require_pay_password":       r.RequirePayPassword,
		"max_entries_per_user":       r.MaxEntriesPerUser,
		"max_attempts_per_user":      r.MaxAttemptsPerUser,
		"max_total_entries":          r.MaxTotalEntries,
		"max_total_users":            r.MaxTotalUsers,
		"max_per_inviter":            r.MaxPerInviter,
		"cooldown_seconds":           r.CooldownSeconds,
		"dedup_ip":                   r.DedupIp,
	})
}

// ParseRules 从落库的 rules_text 还原条件。
//
// 只用于判定与展示;**任何哈希一律用落库的原文**,绝不拿这里还原出的结构体
// 重新序列化 —— 那正是"验证者和平台算出两个不同哈希"的经典来源。
func ParseRules(text string) (Rules, error) {
	var r Rules
	if strings.TrimSpace(text) == "" {
		return Rules{}.Normalize(), nil
	}
	if err := common.UnmarshalJsonStr(text, &r); err != nil {
		return Rules{}, fmt.Errorf("qianye/lottery: 解析参与条件失败: %w", err)
	}
	return r.Normalize(), nil
}

// ─────────────────────────── 判定 ───────────────────────────

// Subject 是判定所需的全部用户属性,由 LoadSubject 一次性装载。
type Subject struct {
	Status    int    `json:"status"`
	Role      int    `json:"role"`
	Group     string `json:"group"`
	Quota     int64  `json:"quota"`
	UsedQuota int64  `json:"used_quota"`
	CreatedAt int64  `json:"created_at"`
	InviterId int    `json:"inviter_id"`

	HasEmail    bool  `json:"has_email"`
	HasOAuth    bool  `json:"has_oauth"`
	HasPayPass  bool  `json:"has_pay_pass"`
	RecentSpend int64 `json:"recent_spend"`

	ViolationHits  int  `json:"violation_hits"`
	HasViolation   bool `json:"has_violation"`
	EverAutoBanned bool `json:"ever_auto_banned"`

	// IsCreator 是活动创建者标记。
	//
	// 契约里的 Subject 没有这一项,这里补上是刻意的偏离:创建者禁参与是**硬规则**,
	// 放在 Evaluate 之外意味着每个调用点都要记得再判一次,而漏判的后果是
	// "创建者中了自己办的奖"—— 那是这套系统最不能出的事。
	IsCreator bool `json:"is_creator"`
	// Username 只用于写进参与明细的快照,不参与任何判定。
	Username string `json:"username"`
}

// Missing 是一条未满足的条件。Code 是稳定的机器可读标识,前端按
// t('qy_lot_missing_' + code) 渲染;need/have 供界面显示"还差多少"。
type Missing struct {
	Code string `json:"code"`
	Need any    `json:"need"`
	Have any    `json:"have"`
}

// 未满足条件的 code。一旦发布就不能改 —— 前端按 code 映射 i18n。
const (
	MissAdmin        = "admin"
	MissCreator      = "creator"
	MissDisabled     = "disabled"
	MissGroup        = "group"
	MissAccountAge   = "account_age"
	MissBalance      = "balance"
	MissStake        = "stake"
	MissUsedQuota    = "used_quota"
	MissRecentSpend  = "recent_spend"
	MissViolation    = "violation"
	MissViolationCnt = "violation_hits"
	MissEverBanned   = "ever_banned"
	MissEmail        = "email"
	MissOAuth        = "oauth"
	MissPayPassword  = "pay_password"
)

// Evaluate 是纯函数:输入一份已装载的 Subject,输出未满足的条件清单。
// 空切片 = 通过。
//
// stakeQuota 单独传入而不是塞进 Rules:它是活动的资金要素,不是参与条件,
// 而"余额够不够扣这一笔"必须与"余额门槛"分成两条,否则用户看到的提示会是
// 一个他改不了的数字。
func Evaluate(r Rules, s *Subject, stakeQuota int64, now int64) []Missing {
	out := make([]Missing, 0, 8)

	// ── 硬规则:不可配置 ──
	//
	// 掌握数据库读权限的人能提前看到种子,这是 commit-reveal 的固有弱点,
	// 只能用"他们不能下场"来堵。而且一个管理员中奖的抽奖,无论多干净的协议
	// 都会被质疑。
	if s.Role >= common.RoleAdminUser {
		out = append(out, Missing{Code: MissAdmin, Need: false, Have: true})
	}
	if s.IsCreator {
		out = append(out, Missing{Code: MissCreator, Need: false, Have: true})
	}
	if s.Status != common.UserStatusEnabled {
		out = append(out, Missing{Code: MissDisabled, Need: common.UserStatusEnabled, Have: s.Status})
	}

	// ── 分组 ──
	if !GroupAllowed(r, s.Group) {
		out = append(out, Missing{Code: MissGroup, Need: groupNeed(r), Have: s.Group})
	}

	// ── 账号年龄 ──
	if r.MinAccountAgeDays > 0 {
		need := int64(r.MinAccountAgeDays) * 86400
		age := now - s.CreatedAt
		if s.CreatedAt <= 0 || age < need {
			out = append(out, Missing{Code: MissAccountAge, Need: r.MinAccountAgeDays, Have: age / 86400})
		}
	}

	// ── 余额与消费 ──
	if r.MinQuota > 0 && s.Quota < r.MinQuota {
		out = append(out, Missing{Code: MissBalance, Need: r.MinQuota, Have: s.Quota})
	}
	if stakeQuota > 0 && s.Quota < stakeQuota {
		out = append(out, Missing{Code: MissStake, Need: stakeQuota, Have: s.Quota})
	}
	if r.MinUsedQuota > 0 && s.UsedQuota < r.MinUsedQuota {
		out = append(out, Missing{Code: MissUsedQuota, Need: r.MinUsedQuota, Have: s.UsedQuota})
	}
	if r.RecentSpendQuota > 0 && s.RecentSpend < r.RecentSpendQuota {
		out = append(out, Missing{Code: MissRecentSpend, Need: r.RecentSpendQuota, Have: s.RecentSpend})
	}

	// ── 风控 ──
	if r.ExcludeViolation && s.HasViolation {
		out = append(out, Missing{Code: MissViolation, Need: 0, Have: 1})
	}
	if r.MaxViolationHits > 0 && s.ViolationHits > r.MaxViolationHits {
		out = append(out, Missing{Code: MissViolationCnt, Need: r.MaxViolationHits, Have: s.ViolationHits})
	}
	if r.ExcludeEverAutoBanned && s.EverAutoBanned {
		out = append(out, Missing{Code: MissEverBanned, Need: false, Have: true})
	}

	// ── 账号可信度(本系统没有实名信息,这些是替代信号)──
	if r.RequireEmail && !s.HasEmail {
		out = append(out, Missing{Code: MissEmail, Need: true, Have: false})
	}
	if r.RequireOAuth && !s.HasOAuth {
		out = append(out, Missing{Code: MissOAuth, Need: true, Have: false})
	}
	if r.RequirePayPassword && !s.HasPayPass {
		out = append(out, Missing{Code: MissPayPassword, Need: true, Have: false})
	}
	return out
}

// GroupAllowed 判断分组是否被允许。供主库行锁内复检复用。
//
// **本函数不读任何配置**:规则由调用方以快照形式传入。锁内重新读配置会让
// 运营在受理与落账之间保存一次就出现"受理放行、锁内拒绝",而用户提交那一刻
// 看到的规则确实是允许的。
func GroupAllowed(r Rules, group string) bool {
	for _, g := range r.DenyGroups {
		if g == group {
			return false
		}
	}
	if len(r.AllowGroups) == 0 {
		return true
	}
	for _, g := range r.AllowGroups {
		if g == group {
			return true
		}
	}
	return false
}

func groupNeed(r Rules) any {
	if len(r.AllowGroups) > 0 {
		return r.AllowGroups
	}
	return map[string]any{"deny": r.DenyGroups}
}

// SnapshotJSON 把判定输入冻结成文本,写进 entry.eligibility_snapshot。
//
// 主库 users 没有历史版本:用户报名之后被降组、被扣光余额、被封禁,
// 事后就再也无法回答"他当时到底符不符合条件"。这份快照是争议仲裁的唯一凭据。
func SnapshotJSON(s *Subject) string {
	b, err := common.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// ─────────────────────────── 装载 ───────────────────────────

// ErrSubjectUnavailable 表示资格判定所需的数据源读取失败。
//
// 一律拒绝报名,绝不 fail-open:风控条件在故障时形同虚设的那一刻,
// 正是它最可能被主动构造去打的时刻。
var ErrSubjectUnavailable = errors.New("qianye/lottery: 资格校验暂时不可用")

// LoadSubject 装载判定所需的全部属性。
//
// 判定顺序的实现约束(先便宜后昂贵、先本地后跨库):
//
//	L1 主库一次主键读 —— status/role/group/quota/used_quota/created_at/
//	   inviter_id/email/各 OAuth 标识 **8 项合并成一次读**,绝不每个条件各查一次
//	L2 扩展库索引点查 —— 违规记录 EXISTS(LIMIT 1,不用 COUNT(*))、
//	   滚动窗口计数(主键 O(1))、自动封禁史
//	L3 扩展库主键范围扫描 —— 近 N 日消费,最多 90 行,索引覆盖
//
// **报名路径禁止任何对 logs 的查询**(由 no_logs_query_test.go 的 AST 断言守住)。
//
// 任何一步失败一律返回 error,绝不返回部分结果 —— 部分结果会让调用方在
// "这一项没查到"和"这一项不满足"之间做出错误的乐观推断。
func LoadSubject(ctx context.Context, userId int, r Rules, creatorId int) (*Subject, error) {
	var u model.User
	if err := model.DB.WithContext(ctx).
		Omit("password", "access_token").
		First(&u, "id = ?", userId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: 用户不存在", ErrSubjectUnavailable)
		}
		return nil, fmt.Errorf("%w: %v", ErrSubjectUnavailable, err)
	}

	s := &Subject{
		Status:    u.Status,
		Role:      u.Role,
		Group:     u.Group,
		Quota:     int64(u.Quota),
		UsedQuota: int64(u.UsedQuota),
		CreatedAt: u.CreatedAt,
		InviterId: u.InviterId,
		Username:  u.Username,
		HasEmail:  strings.TrimSpace(u.Email) != "",
		HasOAuth: u.GitHubId != "" || u.DiscordId != "" || u.OidcId != "" ||
			u.WeChatId != "" || u.TelegramId != "" || u.LinuxDOId != "",
		IsCreator: creatorId > 0 && creatorId == userId,
	}

	gdb := db.Get()
	if gdb == nil {
		return nil, fmt.Errorf("%w: 扩展库不可用", ErrSubjectUnavailable)
	}
	gdb = gdb.WithContext(ctx)

	if r.RequirePayPassword {
		var cnt int64
		if err := gdb.Model(&paypass.PayPassword{}).
			Where("user_id = ? AND hash <> ''", userId).Count(&cnt).Error; err != nil {
			db.MarkFailure(err)
			return nil, fmt.Errorf("%w: %v", ErrSubjectUnavailable, err)
		}
		s.HasPayPass = cnt > 0
	}

	if r.ExcludeViolation {
		// EXISTS 而不是 COUNT(*):这里只关心"有没有",而 COUNT 会把这个用户
		// 全部历史命中都扫一遍。走 idx_qy_vrec_user(user_id, created_at)。
		//
		// 必须排除影子命中与已撤销记录:前者是观察样本、后者是平台自己承认的
		// 误判,当成真违规会误伤守规用户。
		var hit violation.Record
		err := gdb.Select("id").
			Where("user_id = ? AND shadow = ? AND status <> ?", userId, false, "revoked").
			Limit(1).Take(&hit).Error
		switch {
		case err == nil:
			s.HasViolation = true
		case errors.Is(err, gorm.ErrRecordNotFound):
			s.HasViolation = false
		default:
			db.MarkFailure(err)
			return nil, fmt.Errorf("%w: %v", ErrSubjectUnavailable, err)
		}
	}

	if r.MaxViolationHits > 0 {
		var counter violation.Counter
		err := gdb.Where("user_id = ?", userId).Take(&counter).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			db.MarkFailure(err)
			return nil, fmt.Errorf("%w: %v", ErrSubjectUnavailable, err)
		}
		s.ViolationHits = counter.HitCount
	}

	if r.ExcludeEverAutoBanned {
		var ban violation.Ban
		err := gdb.Select("id").Where("user_id = ?", userId).Limit(1).Take(&ban).Error
		switch {
		case err == nil:
			s.EverAutoBanned = true
		case errors.Is(err, gorm.ErrRecordNotFound):
			s.EverAutoBanned = false
		default:
			db.MarkFailure(err)
			return nil, fmt.Errorf("%w: %v", ErrSubjectUnavailable, err)
		}
	}

	if r.RecentSpendQuota > 0 && r.RecentSpendDays > 0 {
		spend, err := RecentSpend(ctx, userId, r.RecentSpendDays)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSubjectUnavailable, err)
		}
		s.RecentSpend = spend
	}
	return s, nil
}

// describeSubjectForAudit 挑出可以进审计快照的字段。
//
// 刻意不整行序列化:Subject 里有余额与消费额,而审计详情的可见范围
// 比参与明细宽。整行序列化是本仓反复出现的泄漏形状。
func describeSubjectForAudit(s *Subject) map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"group":       s.Group,
		"status":      s.Status,
		"is_creator":  s.IsCreator,
		"has_email":   s.HasEmail,
		"has_oauth":   s.HasOAuth,
		"ever_banned": s.EverAutoBanned,
	}
}

// auditCategory 是本模块全部审计的分类。
const auditCategory = qymodel.AuditCategoryLottery
