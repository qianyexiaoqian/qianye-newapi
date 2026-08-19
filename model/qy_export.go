package model

// qy_export.go —— 千夜扩展与上游 model 包之间的唯一耦合面。
//
// 这是一个纯新增文件:它的存在不需要修改任何上游既有文件,因此合并上游时冲突为 0。
// 所有符号统一带 Qy 前缀,规避上游未来出现同名符号。
//
// 铁律:本文件禁止 import 任何 qianye/* 包。
// model 是底层包,扩展依赖它;反向依赖会形成循环。扩展侧的回调一律通过本文件底部
// 的 hook 变量在 qianye.Init() 中注入,这样上游既有文件里只需插入一行同包调用,
// 连 import 都不必改。

import (
	"errors"
	"sort"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ───────────────────────────── 1. 私有能力导出 ─────────────────────────────

// QyLockForUpdate 暴露 lockForUpdate:为 MySQL/PostgreSQL 生成 FOR UPDATE 行锁,
// SQLite 下自动降级为空操作(该方言不支持)。
func QyLockForUpdate(tx *gorm.DB) *gorm.DB { return lockForUpdate(tx) }

// QyApplyUserQuotaCacheDelta 暴露 cacheIncrUserQuota,delta 为负即扣减。
//
// 只允许在主库事务提交之后调用,且必须用增量而非整体用户快照覆盖 —— 用快照会把
// 并发的其他额度变更冲掉。
func QyApplyUserQuotaCacheDelta(userId int, delta int64) error {
	return cacheIncrUserQuota(userId, delta)
}

// QyCommonGroupCol 等暴露方言相关的列名与布尔字面量。
// 扩展在 users / logs 上做原生 SQL 查询时必须用它们,否则 PostgreSQL 与 MySQL
// 的引号规则不同会导致语法错误。
func QyCommonGroupCol() string { return commonGroupCol }
func QyLogGroupCol() string    { return logGroupCol }
func QyCommonTrueVal() string  { return commonTrueVal }
func QyCommonFalseVal() string { return commonFalseVal }

// QyLogDB 暴露日志库句柄。
func QyLogDB() *gorm.DB { return LOG_DB }

// QyLogDBSharesMainDB 表示日志库与主库是否为同一个连接。
// 未配置 LOG_SQL_DSN 时两者相同,此时可以把 logs 与主库表放进同一个查询;
// 分库时则不能 join,必须分别查询后在内存里合并。
func QyLogDBSharesMainDB() bool { return LOG_DB == DB }

// ────────────────── 2. 主库 outbox:跨库两阶段的唯一精确探针 ──────────────────

// QyFundOutbox 与资金变更写在同一个主库事务里,是补偿任务判定
// 「主库副作用是否已经生效」的唯一权威依据。
//
// 为什么不能用 logs 代替:model.RecordLog 写的是 LOG_DB,无法加入主库事务。
// 把单号写进日志只解决"运营能看见",不解决"进程在 commit 与写日志之间崩溃"——
// 那种情况下钱已经动了,但没有任何记录能证明这一点。
//
// 附带收益:order_no 的唯一索引让主库侧操作本身幂等。补偿任务重跑事务时,
// 插入冲突即代表"此前已应用过",可以安全跳过资金变更。
type QyFundOutbox struct {
	Id        int64  `gorm:"primaryKey;autoIncrement"`
	OrderNo   string `gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_outbox_no"`
	Kind      string `gorm:"type:varchar(32);not null"`
	UserId    int    `gorm:"not null;index:idx_qy_outbox_user"`
	PeerId    int    `gorm:"not null;default:0"`
	Amount    int64  `gorm:"not null"`
	CreatedAt int64  `gorm:"not null;index:idx_qy_outbox_created"`
}

func (QyFundOutbox) TableName() string { return "qy_fund_outbox" }

// QyEnsureFundOutbox 建表。只在主节点执行;调用方需自行做跨节点 DDL 互斥。
func QyEnsureFundOutbox() error {
	if !common.IsMasterNode {
		return nil
	}
	return DB.AutoMigrate(&QyFundOutbox{})
}

// QyClaimFundOutbox 在调用方的主库事务内登记单号。
//
// 返回 false 表示该单号此前已登记,即主库副作用已经生效 ——
// 调用方必须跳过资金变更直接返回成功,否则就是重复扣款。
func QyClaimFundOutbox(tx *gorm.DB, row *QyFundOutbox) (bool, error) {
	if row == nil || row.OrderNo == "" {
		return false, errors.New("qy: 资金单号为空")
	}
	if row.CreatedAt == 0 {
		row.CreatedAt = common.GetTimestamp()
	}
	res := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "order_no"}},
		DoNothing: true,
	}).Create(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// QyProbeFundOutbox 供补偿任务判定主库侧是否已生效。
func QyProbeFundOutbox(orderNo string) (bool, error) {
	var cnt int64
	if err := DB.Model(&QyFundOutbox{}).Where("order_no = ?", orderNo).Count(&cnt).Error; err != nil {
		return false, err
	}
	return cnt > 0, nil
}

// QyScanFundOutbox 按主键游标读回一批早于 before 的探针行,供清理任务逐行判定。
//
// 刻意只读不删:一行探针到底能不能删,取决于它对应的资金单在**扩展库**里的状态,
// 而这个包只认识主库。把"选哪些行"和"删哪些行"拆开,清理任务才有机会在中间
// 插入那道判定 —— 合起来写成一条按 created_at 的 DELETE,就等于按时间销毁证据。
//
// 用 SELECT + 主键 IN 删,不能写成 DB.Where(...).Limit(batch).Delete(...):
// GORM 的 LIMIT 子句只有在方言把 "LIMIT" 列进 DeleteClauses 时才会被渲染,
// 而这只有 MySQL 驱动做了 —— postgres 与 sqlite 的 DeleteClauses 里没有 LIMIT,
// 它们会静默生成一条不带 LIMIT 的 DELETE 把整段历史一次删光。主库可以是这三种
// 中的任何一种(见 AGENTS.md 的跨库要求),在全平台业务库上产生长事务、
// 大量 WAL 与锁等待,而且没有任何报错提示。
//
// SELECT 上的 Limit 三种方言都支持,因此这个写法在哪个主库上都真正按批执行。
// afterId 是主键游标:留下不能删的行之后,下一批必须从它之后继续,否则每一轮
// 都会重新捞到同一批"删不掉的行",清理再也推进不下去。
func QyScanFundOutbox(before, afterId int64, batch int) ([]QyFundOutbox, error) {
	if batch <= 0 {
		batch = 200
	}
	var rows []QyFundOutbox
	err := DB.Model(&QyFundOutbox{}).
		Where("created_at < ? AND id > ?", before, afterId).
		Order("id").Limit(batch).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// QyDeleteFundOutbox 按主键删除指定的探针行。
//
// 只接受主键列表:调用方必须已经逐行确认过"这一行的证据使命已经结束"。
func QyDeleteFundOutbox(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := DB.Where("id IN ?", ids).Delete(&QyFundOutbox{})
	return res.RowsAffected, res.Error
}

// ─────────────── 3. 带单号的账本日志(写 LOG_DB,供运营与对账检索) ───────────────

// QyRecordLedgerLog 写一条 logs 记录,把扩展单号放进 request_id
// (该列有 idx_logs_request_id 索引,可直接按单号检索),结构化信息放进 other。
//
// 仅管理员可见的内容请放进 other["admin_info"] —— 上游在返回给普通用户时会自动剥离。
func QyRecordLedgerLog(userId int, logType int, content string, orderNo string, other map[string]interface{}) {
	username, _ := GetUsernameById(userId, false)
	payload := make(map[string]interface{}, len(other)+1)
	for k, v := range other {
		payload[k] = v
	}
	payload["qy_order_no"] = orderNo

	entry := &Log{
		UserId:    userId,
		Username:  username,
		CreatedAt: common.GetTimestamp(),
		Type:      logType,
		Content:   content,
		RequestId: orderNo,
		Other:     common.MapToJsonStr(payload),
	}
	if err := createLog(entry); err != nil {
		common.SysError("qy: 写入账本日志失败: " + err.Error())
	}
}

// ───────────── 4. Hook 变量(上游文件里一行挂载,连 import 都不用改) ─────────────
//
// 默认实现是空操作,因此上游调用点不需要 nil 判断,只需一行普通调用。
// 赋值时机为 qianye.Init(),它在 main.go 的 InitResources() 内执行,
// 早于任何 HTTP 请求与后台协程,因此不存在并发读写窗口 —— 也正因如此,
// 运行期禁止改写这些变量。
//
// 所有 hook 的实现体必须走 guard.Hot / guard.HotAsync:吞 panic、限时、
// 失败只记日志,绝不影响主流程。

var (
	// QyOnConsumeLog 在 RecordConsumeLog 入口触发,用于消费返佣。
	//
	// 必须挂在 common.LogConsumeEnabled 早退判断之前,否则关闭了消费日志的部署
	// 将完全收不到返佣事件。
	QyOnConsumeLog = func(c *gin.Context, userId int, params RecordConsumeLogParams) {}

	// QyOnRedeemSuccess 在兑换码成功兑换后触发,用于充值返佣。
	QyOnRedeemSuccess = func(userId int, redemptionId int, quota int) {}
)

// QyDistinctUserGroups 返回**在册用户**用到的全部用户分组名,已排序。
//
// 它是「用户分组下拉」的唯一口径。上游 controller/group.go 的 GetGroups 从
// options.GroupRatio 的键返回,那是**模型分组**的清单 —— 用它喂用户编辑下拉,
// 管理员看到的是一堆渠道池子的名字,而真正在用的用户分组只要没被配过倍率就消失了。
//
// deleted_at IS NULL:users 是软删除,漏掉它会让早已注销账号的历史分组一直挂在下拉里。
func QyDistinctUserGroups() ([]string, error) {
	var rows []struct {
		Grp string `gorm:"column:grp"`
	}
	err := DB.Raw("SELECT DISTINCT " + commonGroupCol + " AS grp FROM users WHERE deleted_at IS NULL").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Grp != "" {
			out = append(out, row.Grp)
		}
	}
	sort.Strings(out)
	return out, nil
}

// QyPlanUserGroupsInUse 返回**现存套餐已经在用**的升降级分组名,已排序去重。
//
// 它存在的唯一理由是让存量行可编辑:一个历史套餐的 upgrade_group 指向的分组可能
// 已经一个用户都没有了(那三个人到期降级或注销),而管理员接下来只是想改这个套餐的
// **价格** —— PUT 会带上原有的 upgrade_group,于是一条与他正在做的事毫无关系的
// 分组校验把他挡住,错误文案还指向分组。校验只该防新的手误,不该把存量数据锁死。
func QyPlanUserGroupsInUse() ([]string, error) {
	var rows []struct {
		Up   string `gorm:"column:upgrade_group"`
		Down string `gorm:"column:downgrade_group"`
	}
	if err := DB.Model(&SubscriptionPlan{}).
		Select("upgrade_group", "downgrade_group").Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		for _, name := range []string{row.Up, row.Down} {
			if name != "" {
				seen[name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}
