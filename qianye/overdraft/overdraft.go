// Package overdraft 把「余额被扣成负数」这件事变成一页能打开看的东西。
//
// ═══════════════════ 为什么需要这个包 ═══════════════════
//
// 本站的预扣费**刻意**没有下限:结算时的补收走
// service.WalletFunding.Settle → model.DecreaseUserQuota,而后者是一条裸的
// `UPDATE users SET quota = quota - ?`,不校验余额、不夹 0。并发请求也不排队 ——
// users.quota 的变动走上游的批量更新队列(BATCH_UPDATE_ENABLED),
// 同一秒里 8 路请求各自读到的都是同一个旧余额。
//
// 这**不是缺陷,是项目方 2026-08-10 与 2026-08-19 两次拍板的取舍**,
// 完整理由与代价见 qianye/docs/decisions.md 的 D-01。想改它之前先读那一条。
//
// 但「接受透支」不等于「不管透支」。取舍成立的前提是它可运维:
//
//   - 运营要能回答「现在有多少账号是负的、合计欠多少、最深的是谁」,
//     才谈得上决定要不要追欠费、要不要封号。
//   - 这个数字**只有后端答得出**。上游的用户列表能按余额排序,但那是一页一页翻的
//     人工活,而且排序默认按 id;没有任何一处给出合计欠额。
//
// 本包就是那个答案,而且**只读**:它一个字都不改余额,不追欠、不封号、不清零。
// 处置动作仍然在上游的用户管理页,这里只负责让人知道要去处置谁。
//
// ═══════════════════ 为什么查询失败必须报错而不是返回空报告 ═══════════════════
//
// 这份报告的零值(0 个账号、合计欠 0)与「查询挂了」在界面上长得一模一样,
// 而两者的运营结论正好相反:前者是「今天很干净」,后者是「你什么都不知道」。
// 所以 Scan 把错误**原样返回**,由 controller 转成 500,绝不降级成一份
// 看起来很健康的空报告 —— 那是本仓在 group_ratio fail-open 上吃过的同一种亏。
package overdraft

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// DefaultTopLimit 是「最深的欠款账号」清单的默认长度。
//
// 20 而不是全量:这张清单的用途是「先追谁」,而追欠是人工动作,一屏之外的行
// 不会有人看。真要全量的人手里有 users 表。上界见 MaxTopLimit。
const DefaultTopLimit = 20

// MaxTopLimit 是清单长度的硬上界。参数来自 URL,不设界等于让任何管理员
// 一次把全站负余额账号拉进内存并序列化成 JSON。
const MaxTopLimit = 200

// ErrNoDatabase 表示主库还没初始化。与「查了但没有负余额账号」是两件事,
// 因此是一个错误而不是一份空报告(见包注释)。
var ErrNoDatabase = errors.New("主库未初始化")

// Account 是一个负余额账号。
type Account struct {
	UserId      int    `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Group       string `json:"group"`
	// Status 是上游的账号状态(1 = 启用)。带上它是因为运营看这张表时的
	// 第一个问题就是「这个人是不是已经被停了」——已停用的欠款账号不用再追封号,
	// 只用决定要不要追钱。
	Status int `json:"status"`
	// Quota 是余额原值,**恒为负**。
	//
	// 刻意下发负数原值而不是「欠款额」的正数:前端渲染余额的组件全站只有一套,
	// 喂给它一个被取过反的数会显示成「余额 $1.23」而这个人其实欠 $1.23。
	// 取反只在 Report.TotalOwed 这一个聚合值上做,并且名字里写清楚了。
	Quota int `json:"quota"`
}

// Report 是一次负余额扫描的完整结果。
type Report struct {
	// At 是扫描时刻(秒)。这份报告没有缓存,每次请求都是实时的 ——
	// 它不在任何热路径上,而一个过期的欠款数字会让人做出过期的处置决定。
	At int64 `json:"at"`
	// Accounts 是负余额账号数。
	Accounts int64 `json:"accounts"`
	// TotalOwed 是合计欠额,**恒 >= 0**(= -SUM(quota))。
	//
	// 用 int64 而不是 int:quota 列是 int32,单个账号欠额有上界,但全站合计没有。
	TotalOwed int64 `json:"total_owed"`
	// Deepest 是欠得最深的那个账号;没有负余额账号时为 nil。
	//
	// 它就是 Top[0],单独下发是因为「最深的是谁」是运营的第一个问题,
	// 而前端不该靠「取数组第一个元素」来回答一个语义问题 —— Top 的长度
	// 受 limit 影响,而 Deepest 不受。
	Deepest *Account `json:"deepest"`
	// Top 是按欠额从深到浅排列的前 N 个账号。
	Top []Account `json:"top"`
	// Truncated 为 true 表示 Accounts > len(Top),清单被截断了。
	// 没有它的话「20 行」既可能是「正好 20 个人欠钱」也可能是「还有 3000 个」。
	Truncated bool `json:"truncated"`
}

// Scan 统计当前所有负余额账号。
//
// limit <= 0 时取 DefaultTopLimit,超过 MaxTopLimit 时夹到上界。
//
// 软删除:两条查询都必须带 deleted_at IS NULL。漏掉的话早已注销的账号会算进
// 合计欠额 —— 那笔钱既追不回也没必要追,而它会把「本月欠款涨了多少」这个
// 运营最常看的差值彻底污染掉。GORM 的 Model(&model.User{}) 自动带这个条件。
func Scan(ctx context.Context, limit int) (Report, error) {
	if limit <= 0 {
		limit = DefaultTopLimit
	}
	if limit > MaxTopLimit {
		limit = MaxTopLimit
	}

	report := Report{At: common.GetTimestamp(), Top: []Account{}}

	gdb := model.DB
	if gdb == nil {
		return report, ErrNoDatabase
	}
	gdb = gdb.WithContext(ctx)

	// COALESCE:一行都没命中时 SUM 返回 NULL,三种方言一致,
	// 而 NULL 扫进 int64 会报错而不是给 0。
	var agg struct {
		Accounts int64 `gorm:"column:accounts"`
		Total    int64 `gorm:"column:total"`
	}
	if err := gdb.Model(&model.User{}).
		Where("quota < ?", 0).
		Select("COUNT(*) AS accounts, COALESCE(SUM(quota), 0) AS total").
		Scan(&agg).Error; err != nil {
		return report, err
	}
	report.Accounts = agg.Accounts
	// agg.Total 是负数之和,取反成欠款额。SUM 在 MySQL 上返回 DECIMAL、
	// 在 PostgreSQL/SQLite 上返回整数,三者都能扫进 int64。
	report.TotalOwed = -agg.Total

	// `group` 是三种方言里的保留字。列名**逐个**传给 Select(而不是拼一条
	// "id, username, ..." 的字符串),让 GORM 按当前方言自己加引号:
	// PostgreSQL 的 "group" 与 MySQL/SQLite 的 `group` 由它区分。
	//
	// 刻意不走 model.QyCommonGroupCol():那个常量在 InitDB 之前是空串,
	// 拼进 SELECT 会生成 `..., AS grp, ...` 这种语法错误 —— 而它只在真的
	// 有人访问这个端点时才炸,启动、编译、单测一路全绿。
	var rows []struct {
		Id          int    `gorm:"column:id"`
		Username    string `gorm:"column:username"`
		DisplayName string `gorm:"column:display_name"`
		Group       string `gorm:"column:group"`
		Status      int    `gorm:"column:status"`
		Quota       int    `gorm:"column:quota"`
	}
	// ORDER BY quota ASC:quota 恒为负,升序即欠得最深的在前。
	// 再按 id 排一次是为了让并列的行有稳定顺序 —— 否则同额度的两个账号
	// 会在两次刷新之间互相换位,看起来像数据在跳。
	if err := gdb.Model(&model.User{}).
		Where("quota < ?", 0).
		Select("id", "username", "display_name", "group", "status", "quota").
		Order("quota asc, id asc").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return report, err
	}
	for _, r := range rows {
		report.Top = append(report.Top, Account{
			UserId:      r.Id,
			Username:    r.Username,
			DisplayName: r.DisplayName,
			Group:       r.Group,
			Status:      r.Status,
			Quota:       r.Quota,
		})
	}
	if len(report.Top) > 0 {
		deepest := report.Top[0]
		report.Deepest = &deepest
	}
	report.Truncated = report.Accounts > int64(len(report.Top))

	return report, nil
}
