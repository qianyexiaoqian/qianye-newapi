package violation

// cyber_setting.go —— cyber 会话屏蔽的**管理端设置**(单行表 Id=1)。
//
// 与 AISetting 同构、同一条理由:开关、TTL、作用分组必须是运营能自助改的东西,
// 不能埋在 YAML 里等运维改配置重启。它进**同一份规则快照**(snapshot.cyber),
// 热路径读快照、不查库 —— cyberSessionPrecheck 每个请求都要判一次,查库不可接受。
//
// YAML 里只留一个底层判据 violation.cyber_session_block_trigger_codes(哪些上游
// 拒绝算命中),它取决于上游返回什么、几乎不改,所以不铺进管理端。

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CyberSetting 是 cyber 会话屏蔽的全局设置,单行(Id 恒为 1)。
type CyberSetting struct {
	Id int `json:"id" gorm:"primaryKey;autoIncrement:false"` // 恒为 1

	// Enabled 是本功能的总开关(管理员可自助开关)。关掉之后 snapshot.cyber 为 nil,
	// 热路径连会话身份都不去取。基础设施级的更大一层闸是 YAML violation.enabled。
	Enabled bool `json:"enabled" gorm:"not null"`

	// GroupScope / GroupScopeMode 决定**哪些模型分组**受这套屏蔽,复用与 AIScope、
	// 规则表完全相同的 compileScope 语义:
	//
	//	空名单             = 全部模型分组都受屏蔽。
	//	include + 名单     = 只有名单内的分组受屏蔽,其余不管。
	//	exclude + 名单     = 名单内的分组豁免,其余全部受屏蔽。
	//
	// ⚠ 比对的是**模型分组(info.UsingGroup,即请求实际使用的路由分组)**,
	// 与同页 AI 审核作用域同口径 —— cyber 屏蔽跟着"走哪个渠道池的流量"走。
	GroupScope     string `json:"group_scope" gorm:"type:varchar(1024);not null;default:''"`
	GroupScopeMode string `json:"group_scope_mode" gorm:"type:varchar(8);not null;default:'include'"`

	// TTLSeconds 是拉黑的存活时长,到期自动解封(换新会话本就能立刻继续,
	// 所以不需要管理端解封接口)。0 由 build 阶段回落到 cyberDefaultTTLSeconds。
	TTLSeconds int `json:"ttl_seconds" gorm:"not null;default:0"`

	// TriggerCodes 是**触发过滤规则**:哪些上游拒绝算 cyber 命中。一行一条
	// (也认逗号),子串匹配、大小写不敏感,同时比对上游错误码与错误正文。
	//
	// ── 为什么做成可编辑 + 可还原默认 ──
	//
	// new-api 不像 sub2api 那样自持号池 —— 我们唯一的信号是**上游透传回来的那段
	// 错误**。标准 OpenAI 形状 `{"error":{"code":"cyber_policy"}}` 下,error.code 会
	// 被 RelayErrorHandler 原样搬进 apiErr 的错误码,匹配是准的;但不同渠道/网关
	// 可能把它包成别的形状。所以把这份判据交给运营:出厂是 cyberDefaultTriggerText,
	// 管理端可改、可一键还原默认。空 = 回落默认(见 buildCyberRuntime),
	// 避免"清空后静默变成谁都不拦"。
	//
	// gorm 标签只写 type:text(与 AISetting.Prompt 同):MySQL 不允许 TEXT 列带
	// DEFAULT,加 default:'' 会让 AutoMigrate 直接报 Error 1101 起不来。
	// 空/NULL 都被 parseCyberTriggers 当成"回落默认",所以可空无害。
	TriggerCodes string `json:"trigger_codes" gorm:"type:text"`

	// CountTowardBan 决定 cyber 命中要不要**计入自动封号计数**。开了之后每次拉黑
	// 落一条违规记录、推进该用户的账号总量线与类型线,达到「处置策略」阈值即
	// 自动受限/封号 —— 复用违规模块现成的计数+封禁闸,不另造一套。
	CountTowardBan bool `json:"count_toward_ban" gorm:"not null;default:false"`
	// CategoryId 是**计数类型绑定**:cyber 命中计到哪个违规类型上(类型线用它、
	// 该类型自己的阈值就对这批命中生效)。0 = 不指定,落「未分类」兜底。
	// 只在 CountTowardBan=true 时有意义。
	CategoryId int64 `json:"category_id" gorm:"not null;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"not null"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null"`
	UpdatedBy int   `json:"updated_by" gorm:"not null;default:0"`
}

func (CyberSetting) TableName() string { return "qy_violation_cyber_setting" }

// cyberDefaultTTLSeconds 是 TTL 缺省 / 非法时的回落值,与 sub2api 的默认一致。
const cyberDefaultTTLSeconds = 3600

// cyberMaxTTLSeconds 是 TTL 上限:拉黑本就该到期自动解封,一个超长 TTL 等于
// 把一次误判变成"这条会话这辈子都用不了"。7 天足够覆盖任何真实的处置窗口。
const cyberMaxTTLSeconds = 7 * 24 * 3600

// cyberDefaultTriggerText 是出厂 / 「还原默认」按钮填回的触发过滤规则。
// 对齐 sub2api 确认过的那个上游拒绝码。一行一条。
const cyberDefaultTriggerText = "cyber_policy"

// parseCyberTriggers 把编辑框文本拆成规则列表:按换行/逗号切、去空白、
// 折小写去重。空文本回落默认,避免"清空 = 谁都不拦"这种静默失效。
func parseCyberTriggers(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	seen := map[string]struct{}{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		s := strings.ToLower(strings.TrimSpace(f))
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		out = append(out, cyberDefaultTriggerText)
	}
	return out
}

// cyberRuntime 是 cyber 屏蔽在快照里的那一份。nil 表示本功能不生效
// (总开关关着、设置行还没建)。
type cyberRuntime struct {
	ttlSeconds int
	// scope 只用分组这一维:模型作用域对"会话屏蔽"没有意义。
	scope scopeMatcher
	// triggers 是"哪些上游拒绝算命中"的判据(已折小写)。见 matchesCyberTrigger。
	triggers []string
	// countTowardBan / categoryId 决定要不要计入自动封号、计到哪个违规类型上。
	countTowardBan bool
	categoryId     int64
}

// groupManaged 报告某个模型分组(info.UsingGroup)是否受这套屏蔽约束。
func (rt *cyberRuntime) groupManaged(modelGroup string) bool {
	return rt.scope.groupInScope(modelGroup)
}

// buildCyberRuntime 从库里的设置行装配运行期形态,供 reloadCtx 调用。
// 返回 nil = 本功能这次快照不生效(与 buildAIRuntime 的判空口径一致)。
func buildCyberRuntime(gdb *gorm.DB) *cyberRuntime {
	if gdb == nil {
		return nil
	}
	var s CyberSetting
	if err := gdb.Where("id = ?", 1).Take(&s).Error; err != nil {
		// 设置行还没建(第一次部署)不是错误,按"未启用"处理。
		return nil
	}
	if !s.Enabled {
		return nil
	}
	ttl := s.TTLSeconds
	if ttl <= 0 || ttl > cyberMaxTTLSeconds {
		ttl = cyberDefaultTTLSeconds
	}
	return &cyberRuntime{
		ttlSeconds:     ttl,
		scope:          compileScope("", s.GroupScope, s.GroupScopeMode),
		triggers:       parseCyberTriggers(s.TriggerCodes),
		countTowardBan: s.CountTowardBan,
		categoryId:     s.CategoryId,
	}
}

// ensureCyberSetting 补建出厂设置行:关闭 + 空作用域 + 默认 TTL。
// 补建不改变任何行为(Enabled=false),但没有它管理端会看到一张空表单,
// 分不清"还没配"与"配成了空"。与 ensureAISetting 同一条理由。
func ensureCyberSetting(ctx context.Context, gdb *gorm.DB) error {
	if gdb == nil {
		return db.ErrNotReady
	}
	now := common.GetTimestamp()
	row := CyberSetting{
		Id: 1, Enabled: false,
		GroupScope: "", GroupScopeMode: GroupScopeInclude,
		TTLSeconds:   cyberDefaultTTLSeconds,
		TriggerCodes: cyberDefaultTriggerText,
		CreatedAt:    now, UpdatedAt: now,
	}
	return gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

// validateCyberSetting 是写入闸:归一 + 范围校验,与 validateAIScope 同规格。
func validateCyberSetting(s *CyberSetting) error {
	s.GroupScope = strings.TrimSpace(s.GroupScope)
	if s.GroupScopeMode == "" {
		s.GroupScopeMode = GroupScopeInclude
	}
	switch s.GroupScopeMode {
	case GroupScopeInclude, GroupScopeExclude:
	default:
		return fmt.Errorf("作用分组方向非法(%q);可选 %q / %q",
			s.GroupScopeMode, GroupScopeInclude, GroupScopeExclude)
	}
	// 分组名单里禁通配符:分组是精确 map 查表(见 compileScope / groupInScope),
	// 写个 vip* 会保存成功、界面正常、线上永不命中 —— 与 AIScope 同一道闸。
	if err := validateGroupScopeList(s.GroupScope); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(s.GroupScope); n > 1024 {
		return fmt.Errorf("作用分组名单过长(%d 字符,上限 1024)", n)
	}
	// TTL 允许 0(表示"用默认值"),但不接受负数与超长。
	if s.TTLSeconds < 0 {
		return fmt.Errorf("拉黑时长不能为负数,收到 %d", s.TTLSeconds)
	}
	if s.TTLSeconds > cyberMaxTTLSeconds {
		return fmt.Errorf("拉黑时长过长(%d 秒,上限 %d 秒 = 7 天)", s.TTLSeconds, cyberMaxTTLSeconds)
	}
	// 触发过滤规则:归一(去两侧空白),挡住"粘一整页进来"的误操作。空文本合法
	// (运行期回落默认,见 parseCyberTriggers),所以这里不强制非空。
	s.TriggerCodes = strings.TrimSpace(s.TriggerCodes)
	if utf8.RuneCountInString(s.TriggerCodes) > 2048 {
		return fmt.Errorf("触发过滤规则过长(%d 字符,上限 2048)", utf8.RuneCountInString(s.TriggerCodes))
	}
	// 计数类型 id 负数永远解析不到任何类型(与 validateAIScope 同一道闸)。
	// 0 合法 = 不指定,落「未分类」兜底;存不存在不在这里挡(手上没有类型快照),
	// 运行期 categoryForRule 对查不到的 id 一律回落兜底,不会产生孤儿。
	if s.CategoryId < 0 {
		return fmt.Errorf("计数类型 id 非法(%d);不指定请留 0", s.CategoryId)
	}
	return nil
}
