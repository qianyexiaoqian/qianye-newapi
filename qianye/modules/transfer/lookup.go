package transfer

import (
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 收款人查找方式。刻意没有"按用户名模糊搜索"这一档 ——
// 模糊搜索等于把整个用户表开放给任何登录用户枚举,与本项目对
// "已邀请用户列表必须脱敏"的要求自相矛盾。
const (
	lookupByID    = "id"
	lookupByEmail = "email"
)

// 阻断原因。前端据此做 i18n,不要依赖 message 文案。
const (
	blockedNotFound = "not_found"
	blockedSelf     = "self"
	blockedDisabled = "disabled"
	// blockedGroupDenied 对方所在分组不在本用户的可转范围内。
	blockedGroupDenied = "group_denied"
	// blockedGroupBlocked 本用户所在分组整体禁止发起划转。
	// 与上面那条分开,是因为前者该去换一个收款人,后者换谁都没用。
	blockedGroupBlocked = "group_blocked"
)

// resolveRecipient 按配置允许的方式精确查找收款人并返回脱敏信息。
//
// 结果里绝不出现:真实用户名全文、真实邮箱全文、余额、分组名、角色、注册时间。
// 确认弹窗要展示的只有"这是不是我要转的那个人",脱敏名 + 用户 ID 足够。
func resolveRecipient(c *gin.Context, meId int, identifier string, rules groupRuleSet) previewResponse {
	cfg := config.Get().Transfer
	raw := strings.TrimSpace(identifier)

	byType, value, ok := classifyIdentifier(raw, cfg.RecipientLookup)
	if !ok {
		recordLookup(c, meId, raw, byType, false)
		return previewResponse{BlockedReason: blockedNotFound}
	}

	user, err := findRecipient(byType, value)
	if err != nil {
		recordLookup(c, meId, raw, byType, false)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return previewResponse{BlockedReason: blockedNotFound}
		}
		// 主库异常也按"查不到"回复:把数据库错误透给前端只会泄漏实现细节。
		common.SysError("qianye/transfer: 解析收款人失败: " + err.Error())
		return previewResponse{BlockedReason: blockedNotFound}
	}
	recordLookup(c, meId, raw, byType, true)

	resp := previewResponse{
		Exists:         true,
		UserId:         user.Id,
		MaskedUsername: maskUsername(user.Username),
		MaskedEmail:    maskEmail(user.Email),
		Receivable:     true,
	}
	switch {
	case user.Id == meId:
		resp.Receivable = false
		resp.BlockedReason = blockedSelf
	case cfg.ReceiverMustBeEnabled() && user.Status != common.UserStatusEnabled:
		resp.Receivable = false
		resp.BlockedReason = blockedDisabled
	default:
		if reason := previewGroupBlock(meId, user, rules); reason != "" {
			resp.Receivable = false
			resp.BlockedReason = reason
		}
	}
	return resp
}

// previewGroupBlock 返回预校验阶段的分组阻断原因,空串表示放行。
//
// 为什么在预校验就判:不判的话,用户要把金额填完、确认"不可撤销"、点下提交,
// 才会吃到一个 403。分组限制是"换个收款人也许就行"的那类拒绝,越早说越好。
//
// 代价是它泄漏一位信息:"这个账号在你转不到的分组里"。这一位无法真正藏住 ——
// 提交同样会拒,只是慢一步。缓解手段沿用本模块已有的两道:解析接口按用户限流
// (SearchRateLimit,抗代理轮换),且每次解析都落一条 qy_transfer_lookup_logs
// 供事后追溯慢速枚举。绝不额外下发对方的分组名。
//
// 读不到发起方主库行时一律放行:这里只是提前提示,真正的闸门在 loadParties 与
// applyQuotaTransfer;为一次读失败就谎报"不能转"是更差的结果。
func previewGroupBlock(meId int, receiver *model.User, rules groupRuleSet) string {
	me, err := model.GetUserById(meId, false)
	if err != nil {
		common.SysError("qianye/transfer: 预校验读取发起方失败,跳过分组判定: " + err.Error())
		return ""
	}
	verdict := enforceGroupPolicy(me, receiver, rules)
	switch {
	case verdict == nil:
		return ""
	case errors.Is(verdict, errGroupSendBlocked):
		return blockedGroupBlocked
	default:
		return blockedGroupDenied
	}
}

// classifyIdentifier 判断用户输入该按哪种方式解析。
//
// recipient_lookup = "id" 时只认纯数字;"id_or_email" 额外允许完整邮箱精确匹配。
// 任何情况下都不接受用户名 —— 用户名是可枚举的短标识,邮箱至少需要事先知道。
func classifyIdentifier(raw, mode string) (byType, value string, ok bool) {
	if raw == "" || len(raw) > maxIdentifierLen {
		return lookupByID, "", false
	}
	if isAllDigits(raw) {
		// 前导零、超长数字都直接判不存在,不做容错:容错会让枚举者更省事。
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			return lookupByID, "", false
		}
		return lookupByID, raw, true
	}
	if mode != config.RecipientLookupIDEmail {
		return lookupByID, "", false
	}
	at := strings.LastIndex(raw, "@")
	if at <= 0 || at == len(raw)-1 || strings.ContainsAny(raw, "%_*") {
		// 含通配符的输入一律拒绝:即使我们用的是参数化查询,
		// 放行它也会让人误以为这里支持模糊匹配。
		return lookupByEmail, "", false
	}
	return lookupByEmail, raw, true
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// findRecipient 只做全等查询。列收窄到判定与展示真正用得到的五个,
// 避免把整行用户数据读进内存。
//
// group 是分组规则的判定输入,必须查出来 —— 但它只参与判定,绝不下发。
// 列名交给 GORM 按 schema 映射并加引号:group 在 MySQL 与 PostgreSQL 都是
// 保留字,裸拼字符串会在其中一种方言上直接语法错误。
func findRecipient(byType, value string) (*model.User, error) {
	q := model.DB.Model(&model.User{}).Select("id", "username", "email", "status", "group")
	if byType == lookupByID {
		q = q.Where("id = ?", value)
	} else {
		q = q.Where("email = ?", value)
	}
	var u model.User
	// 不加 Unscoped:软删除的账号必须表现为不存在。
	if err := q.First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// recordLookup 留下解析痕迹。
//
// 限流只能限速率,发现不了"每天都不超限但持续一个月扫库"的慢速枚举。
// 写失败只记日志:这是审计而非业务,不能因为它挡住用户的正常操作。
func recordLookup(c *gin.Context, meId int, identifier, byType string, hit bool) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	row := &LookupLog{
		UserId:     meId,
		Identifier: truncate(identifier, maxIdentifierLen),
		ByType:     byType,
		Hit:        hit,
		ClientIp:   truncate(c.ClientIP(), 64),
		CreatedAt:  common.GetTimestamp(),
	}
	if err := gdb.Create(row).Error; err != nil {
		db.MarkFailure(err)
	}
}
