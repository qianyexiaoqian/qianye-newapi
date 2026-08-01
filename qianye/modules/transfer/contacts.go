package transfer

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

// contacts.go —— 划转联系人簿(需求 3-C)。
//
// ════════════════════════════════════════════════════════════════════════
// 不变量(裁决 1,不可协商):联系人**只**做一件事 —— 把收款人字段填好。
//
//	它不是信任凭据。
//	它不降低任何风控档位。
//	它不跳过支付密码。
//	它不跳过分组限制。
//	它不跳过日限额、单笔上下限、冷却、新账号冻结。
//
// 项目方原话:「联系人只是方便快速填写表,不是因为是联系人就可以绕过支付
// 密码的验证。」
//
// 落到实现上就是一句话:**本包的联系人代码与动钱路径之间没有任何数据流。**
// 联系人接口的全部输出是一个展示用的 contactView,前端拿它去填输入框,
// 之后仍要走 /transfer/preview → 确认弹窗 → /transfer(验密、限额、分组)
// 这条完整链路,一步不少。
//
// 这条不变量由 contacts_isolation_test.go 用 AST 双向钉死:
//
//	方向一:service.go / validate.go / risk.go / handler.go 等动钱路径文件里
//	        不允许出现任何 Contact 相关标识符。日后有人想写
//	        `if isContact { skipPayPassword }`,那一行会让测试变红。
//	方向二:本文件与 api_contacts.go 里不允许出现动钱/验密相关标识符
//	        (twophase / applyQuotaTransfer / reserveRisk / validateCreate /
//	        PayPwd*)。反过来的"联系人顺手把钱转了"同样被挡住。
//
// 单向注释挡不住"优化":注释不会失败。双向 AST 断言会。
// ════════════════════════════════════════════════════════════════════════
//
// # 隐私口径
//
// 联系人簿天生有两个泄漏面,两个都必须按本仓既有口径处理,不另开新面:
//
//  1. **添加过程就是一次用户枚举探针**。输入一个 ID/邮箱看返回"存在"还是
//     "不存在",与 /transfer/preview 是同一个动作。因此添加走的是同一个
//     resolveRecipient:同一个 recipient_lookup 开关(默认只认纯数字 ID,
//     永不接受用户名模糊搜索)、同一张 qy_transfer_lookup_logs 反枚举日志
//     (同一个保留期清理任务)、同一个按用户 ID 的 SearchRateLimit。
//     绝不为联系人另开一个不设防的查询接口。
//
//  2. **列表回显的是别人的身份**。这里只下发脱敏用户名 + 用户 ID,
//     与 commission 的下线列表、transfer 的对手方展示同一套 maskUsername。
//     真实用户名、邮箱(哪怕脱敏后的)、余额、分组、角色、注册时间一律不下发。
//     邮箱刻意连脱敏形态都不给:确认"是不是我要转的那个人"发生在 preview
//     那一步(有日志、有限流),列表只需要"我存的这几个人里是哪一个",
//     而那件事由用户自己写的备注名 + 脱敏用户名就够了。
//
// 已知残留:列表每次都会回读一次主库状态,因此对**已经在簿子里的**这至多
// maxContactsPerUser 个账号,它是一条不写日志的状态变化观察通道。接受它的
// 理由是:这些账号都是本人先经 preview 解析过(已留日志、已计限流)才进来的,
// 集合有界,且输出只有脱敏名与三档状态,没有任何新的标识信息。
// 换成"只用添加时的快照"能消掉这条通道,但代价是对方注销/封禁后列表永远
// 显示成正常 —— 那正是需求明确不要的结果。

// 联系人对外的三档状态。前端据此做 i18n,不要依赖任何文案。
//
// gone 与 unknown 必须分开:前者是"对方账号确实没了",后者是"这次没读到
// 主库"。合成一档会让扩展库/主库抖一下就把整簿子显示成"已注销",
// 而用户看到的是"我存的人全没了"。
const (
	contactStatusActive   = "active"
	contactStatusDisabled = "disabled"
	contactStatusGone     = "gone"
	contactStatusUnknown  = "unknown"
)

const (
	// maxContactsPerUser 是每人可存的联系人条数上限。
	//
	// 刻意是常量而不是配置项:按裁决 5 的划线,它既不是运营每天要动的门槛
	// (它不决定任何金额),也不是安全开关(它不决定能不能转),做成配置项
	// 要在 config.go / defaults.go / validate.go / selfcheck.go / example.yaml
	// 五处共享文件各登记一遍,而收益是零。
	//
	// 50 的依据:这是一个手动维护的常用收款人簿,不是通讯录导入。上限的作用
	// 是给"把联系人簿当成枚举结果暂存区"设一个天花板,而不是省存储。
	maxContactsPerUser = 50

	// maxAliasRunes 备注名长度上限,按 rune 计。列表里它与脱敏用户名并排显示,
	// 再长就会把脱敏名挤出可视区,反而看不清转给谁。
	maxAliasRunes = 32
)

// Contact 是 owner 自己维护的一条收款人记录。
//
// 与主库无外键(跨库不可能有)。ContactUserId 指向主库 users.id,对方注销后
// 这一行**不删** —— 让它凭空消失,用户只会以为自己的数据丢了。
type Contact struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// (owner_user_id, contact_user_id) 唯一:同一个对方只能存一条。
	// 去重必须落在数据库上而不是只做应用层预检:预检与插入之间有窗口,
	// 唯一索引才是最终裁判。
	OwnerUserId   int `json:"-" gorm:"not null;uniqueIndex:uk_qy_tr_ct_pair,priority:1;index:idx_qy_tr_ct_owner,priority:1"`
	ContactUserId int `json:"contact_user_id" gorm:"not null;uniqueIndex:uk_qy_tr_ct_pair,priority:2"`

	// Alias 是 owner 自己起的备注名,只有 owner 看得到,与对方的真实身份无关。
	Alias string `json:"alias" gorm:"type:varchar(64);not null;default:''"`

	// MaskedSnapshot 是添加当时对方的**脱敏**用户名。
	//
	// 存脱敏而不是原文:扩展库没有任何理由持有另一个用户的真实用户名副本,
	// 那只会多出一处需要跟着主库改名、跟着注销清理的影子身份数据。
	// 它只在对方账号已经读不到时兜底展示,让那一行仍然"是个人"而不是一串 ID。
	MaskedSnapshot string `json:"-" gorm:"type:varchar(64);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_tr_ct_owner,priority:2"`
}

func (Contact) TableName() string { return "qy_transfer_contacts" }

// contactView 是联系人对外的**唯一**形态。
//
// 刻意没有 receivable / blocked_reason 一类字段:那会让人误以为联系人列表
// 是一次预授权,而"能不能转给他"取决于提交那一刻的分组规则、限额与余额。
// 判定只有一处,就是 /transfer 的执行入口。
type contactView struct {
	Id     int64  `json:"id"`
	UserId int    `json:"user_id"`
	Alias  string `json:"alias"`
	// MaskedUsername 由后端脱敏。放前端打码等于没脱敏。
	MaskedUsername string `json:"masked_username"`
	Status         string `json:"status"`
	CreatedAt      int64  `json:"created_at"`
}

// 联系人相关的业务错误。code 一旦发布就不能改:前端按 code 映射 i18n。
var (
	errContactNotFound = newBizError("qy_contact_not_found",
		"该联系人不存在", http.StatusNotFound)
	// errContactUserNotFound 与 errReceiverNotFound 分开,是因为前端的下一步不同:
	// 这里该提示"检查一下 ID 是不是打错了",而不是"换个收款人"。
	errContactUserNotFound = newBizError("qy_contact_user_not_found",
		"未找到该账号", http.StatusBadRequest)
	errContactSelf = newBizError("qy_contact_self",
		"不能把自己加为联系人", http.StatusBadRequest)
	errContactDuplicate = newBizError("qy_contact_duplicate",
		"该账号已在联系人列表中", http.StatusConflict)
	errContactLimit = newBizError("qy_contact_limit",
		"联系人数量已达上限", http.StatusBadRequest)
)

// acceptAlias 规范化并校验备注名。
//
// 比 sanitizeRemark 更严,不是随手加码:备注名会与脱敏用户名并排渲染在
// 选择列表里,而备注是 owner 自己输入的自由文本。双向覆盖(U+202E)能让
// "转给张三"在视觉上显示成别的名字,零宽字符能造出两条肉眼完全相同的记录。
// 备注则是落进主库账本 content 的,那里要防的是日志注入(控制字符),
// 两者要防的东西不同,所以规则不同 —— 这不是同一概念的第二份拷贝。
//
// 空备注是合法的:列表会退回展示脱敏用户名。
func acceptAlias(raw string) string {
	var b strings.Builder
	n := 0
	for _, r := range raw {
		switch {
		case r == utf8Replacement:
			// 非法 UTF-8 被 range 解码成 U+FFFD,原样留着只会在列表里显示成方块。
			continue
		case unicode.IsControl(r):
			continue
		case unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp):
			// Cf 覆盖零宽字符(U+200B/200C/200D)与双向覆盖(U+202A-202E、U+2066-2069)。
			continue
		}
		if n >= maxAliasRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// utf8Replacement 是 range 对非法字节的解码结果(U+FFFD)。写成转义形式而不是
// 字面量:U+FFFD 在多数编辑器里渲染成一个方块,肉眼分不出它有没有被改过。
const utf8Replacement = '\uFFFD'

// loadContacts 读出 owner 的联系人簿。
//
// 只按 owner_user_id 过滤,永远不接受调用方传进来的"随便看谁的簿子" ——
// 越权的入口不在于查询写得对不对,而在于有没有一条不带 owner 的路径。
func loadContacts(ctx context.Context, ownerId int) ([]Contact, error) {
	if ownerId <= 0 {
		return nil, errInvalidParam
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	var rows []Contact
	// 上限本来就只有 maxContactsPerUser 条,不分页:分页会给一个至多 50 行的
	// 列表引入一整套 offset 语义,而那正是本仓分页缺陷最集中的地方。
	// Limit 仍然要写死,防的是历史数据或并发竞态留下的超额行。
	if err := gdb.Where("owner_user_id = ?", ownerId).
		Order("id desc").Limit(maxContactsPerUser).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}

// hydrateContacts 把库里的行渲染成对外视图,并回读一次主库补上当前状态。
//
// 为什么要回读:对方可能已经被封禁或注销。不回读就只能一直显示"正常",
// 用户会一路填到确认弹窗才被告知转不了。
//
// 为什么不因此把行删掉:那会让用户以为自己存的数据丢了。行留着,状态如实说。
//
// 主库读失败时全部标 unknown 而不是 gone:见 contactStatus* 的说明。
func hydrateContacts(ctx context.Context, rows []Contact) []contactView {
	views := make([]contactView, 0, len(rows))
	if len(rows) == 0 {
		return views
	}

	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ContactUserId)
	}

	live := make(map[int]model.User, len(ids))
	resolved := true
	var users []model.User
	// 列收窄到展示与判定真正用得到的两个。不加 Unscoped:软删除的账号必须
	// 表现为"已注销",与 findRecipient 同口径。
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username", "status").
		Where("id IN ?", ids).Find(&users).Error; err != nil {
		resolved = false
		common.SysError("qianye/transfer: 回读联系人对应主库用户失败,本次状态标记为未知: " + err.Error())
	}
	for _, u := range users {
		live[u.Id] = u
	}

	missing := contactStatusGone
	if !resolved {
		missing = contactStatusUnknown
	}
	for _, r := range rows {
		v := contactView{
			Id:     r.Id,
			UserId: r.ContactUserId,
			Alias:  r.Alias,
			// 读不到对方时退回添加当时的脱敏快照,让这一行仍然是"一个人"。
			MaskedUsername: r.MaskedSnapshot,
			Status:         missing,
			CreatedAt:      r.CreatedAt,
		}
		if u, ok := live[r.ContactUserId]; ok {
			// 以主库现值为准:对方改过名之后,快照会误导用户转错人。
			v.MaskedUsername = maskUsername(u.Username)
			v.Status = contactStatusActive
			if u.Status != common.UserStatusEnabled {
				v.Status = contactStatusDisabled
			}
		}
		views = append(views, v)
	}
	return views
}

// saveContact 落一条联系人。
//
// 调用方必须先经 resolveRecipient 解析出 contactUserId 与脱敏名 ——
// 这个函数刻意不接受原始 identifier,免得日后多出一条绕过反枚举日志的路径。
func saveContact(ctx context.Context, ownerId, contactUserId int, maskedName, alias string) (contactView, error) {
	// 自己不能加自己。与 validateCreate 里的自转判定一样硬编码拒绝:
	// 它没有任何合法用途。
	if ownerId <= 0 || contactUserId <= 0 || ownerId == contactUserId {
		return contactView{}, errContactSelf
	}
	gdb := db.Get()
	if gdb == nil {
		return contactView{}, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	row := Contact{
		OwnerUserId:    ownerId,
		ContactUserId:  contactUserId,
		Alias:          alias,
		MaskedSnapshot: truncate(maskedName, 64),
		CreatedAt:      common.GetTimestamp(),
	}
	err := gdb.Transaction(func(tx *gorm.DB) error {
		// 计数与插入必须同事务,否则两个并发请求会双双读到旧计数一起通过。
		var cnt int64
		if err := tx.Model(&Contact{}).
			Where("owner_user_id = ?", ownerId).Count(&cnt).Error; err != nil {
			return err
		}
		if cnt >= maxContactsPerUser {
			return errContactLimit
		}
		var dup Contact
		e := tx.Where("owner_user_id = ? AND contact_user_id = ?", ownerId, contactUserId).
			First(&dup).Error
		switch {
		case e == nil:
			return errContactDuplicate
		case !errors.Is(e, gorm.ErrRecordNotFound):
			return e
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		if errors.Is(err, errContactLimit) || errors.Is(err, errContactDuplicate) {
			return contactView{}, err
		}
		// 预检与插入之间有窗口,唯一索引才是最终裁判(与 api_group_rules.go
		// 同口径):回读一次把驱动错误翻译成 409,而不是把表名与索引名吐给前端。
		if dup, e := contactExists(ctx, ownerId, contactUserId); e == nil && dup {
			return contactView{}, errContactDuplicate
		}
		db.MarkFailure(err)
		return contactView{}, err
	}

	// 刚写进去的这一行必然对应一个刚解析成功的活账号,直接按 active 回执,
	// 不为了一个字段再回读一次主库。
	return contactView{
		Id:             row.Id,
		UserId:         row.ContactUserId,
		Alias:          row.Alias,
		MaskedUsername: row.MaskedSnapshot,
		Status:         contactStatusActive,
		CreatedAt:      row.CreatedAt,
	}, nil
}

func contactExists(ctx context.Context, ownerId, contactUserId int) (bool, error) {
	gdb := db.Get()
	if gdb == nil {
		return false, db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	var cnt int64
	if err := gdb.Model(&Contact{}).
		Where("owner_user_id = ? AND contact_user_id = ?", ownerId, contactUserId).
		Count(&cnt).Error; err != nil {
		return false, err
	}
	return cnt > 0, nil
}

// renameContact 改备注名。
//
// # 为什么是"先读后写"而不是看 UPDATE 的 RowsAffected
//
// 扩展库固定是 MySQL,而 MySQL 在**新值与旧值相同**时对 UPDATE 返回
// affected_rows = 0(除非连接开了 CLIENT_FOUND_ROWS,go-sql-driver 默认不开)。
// 用 RowsAffected == 0 判"找不到",用户把备注改成和原来一样的字就会收到一个
// 404 —— 而记录明明在。归属判定必须由那次带 owner_user_id 的 SELECT 做,
// UPDATE 的行数只是副产物。
//
// 注意:SQLite 在这种情况下返回 1,所以这条差异在测试库上不会自然复现,
// contacts_test.go 用 GORM 回调把 RowsAffected 强制置 0 来复现它。
func renameContact(ctx context.Context, ownerId int, id int64, alias string) error {
	if ownerId <= 0 || id <= 0 {
		return errInvalidParam
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	// owner_user_id 必须进 WHERE:只按 id 查再在应用层比对 owner,
	// 是本仓最常见的越权形状 —— 少写一个 if 就变成任意改别人的数据。
	var row Contact
	err := gdb.Where("id = ? AND owner_user_id = ?", id, ownerId).First(&row).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errContactNotFound
	case err != nil:
		db.MarkFailure(err)
		return err
	}
	if row.Alias == alias {
		return nil
	}
	if err := gdb.Model(&Contact{}).
		Where("id = ? AND owner_user_id = ?", id, ownerId).
		Update("alias", alias).Error; err != nil {
		db.MarkFailure(err)
		return err
	}
	return nil
}

// deleteContact 物理删除一条联系人。
//
// 这里用硬删除而不是像 payee_account 那样软删:收款方式的软删是为了留住
// digest 风控线索与"曾经绑过谁"的证据,而联系人簿只是 owner 自己的快捷方式,
// 不参与任何风控判定。真正的审计线索在 qy_transfer_lookup_logs(添加时那次
// 解析)与 qy_transfer_orders(真转过钱的话)里,删这一行不会抹掉任何证据。
//
// DELETE 的 RowsAffected 是可信的(MySQL 不会因为"值没变"而返回 0),
// 因此这里可以据它判 404。
func deleteContact(ctx context.Context, ownerId int, id int64) error {
	if ownerId <= 0 || id <= 0 {
		return errInvalidParam
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	res := gdb.Where("id = ? AND owner_user_id = ?", id, ownerId).Delete(&Contact{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errContactNotFound
	}
	return nil
}
