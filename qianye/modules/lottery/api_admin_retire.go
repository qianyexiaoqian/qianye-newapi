package lottery

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_retire.go —— 历史活动的「下架」与「彻底删除」。
//
// # 这是两个动作,不是一个
//
//	下架(hide):把这一场从用户端的活动大厅撤下。一个字节的资金都不动、
//	            一条记录都不删、随时可以撤回。
//	删除(delete):把这一场的全部行从库里抹掉。不可逆。
//
// 它们与既有的 cancel(整场取消)也不是一回事,而这一点必须在界面上分得开:
// **cancel 会退钱**(把活动推进 settling 并触发逐笔全额退款,只能在终态之前用),
// 下架与删除都**不动任何人的余额**。运营把"关闭"理解成"取消"会平白退掉一整场
// 已经结清的活动,反过来把"取消"理解成"下架"会以为只是藏起来 —— 两个方向的
// 误解都真的会花钱,所以三个动作的弹窗各自把代价写在第一行。
//
// # 删除为什么不是一次普通的 CRUD
//
// 本模块的核心价值是**可验证的公正性**:commit-reveal、匿名可访问的
// GET /lottery/public/:act_no/proof、仓库自带的 lottery-verify.py。
// 删掉一场活动 = 那一场的公正性从此无法被任何人验证,而资金侧派奖对用户额度
// 是净增发,活动行上还挂着 pool/fee/payout 三个对账口径。
//
// 因此删除被夹在三层之间:
//
//	① 硬闸门(checkActivityDeletable),事务外走一遍给出精确文案,
//	   事务内再走一遍作为真正的执行点;
//	② 审计**先于**删除写入、且与删除同生共死(audit.WriteTx 是事务里的第一条
//	   写语句)。审计关闭时直接拒绝删除 —— 删完之后,那一行审计是唯一还能证明
//	   这一场存在过的东西;
//	③ 二次确认在**服务端**校验(必须原样回填 act_no + 必填理由),而不是只在
//	   前端弹一个"确定吗"。
//
// # 上面那三层说的是「已结束的活动」。草稿是另一档
//
// 项目方原话:「草稿活动为什么改不了删不了,你弄一下,超级管理员拥有最高权限的。」
//
// 那六道闸门回答的全是同一个问题:「这一场结束了,但它还欠着谁什么吗」。
// 草稿一条都不适用 —— 它从没对外公布过承诺(commit_hash 恒为空串)、大厅不列它、
// 详情页对它 404、证据链在封盘之前根本不下发,它连一条报名都收不到。删掉它
// 不会让任何一段证据链消失。
//
// 而其中两道在草稿上**恒为真**,于是"草稿删不了"根本不是一条设计取舍,是判据
// 被套用在了它们从没打算覆盖的状态上(逐条理由写在 checkActivityDeletable 上)。
//
// 所以草稿走 checkDraftDeletable 的三条正向断言,确认强度也跟着降一档:
// 不要求回填活动编号、不强制填理由。**代价不同的动作,仪式就该不同** ——
// 给一个零代价的动作套上和"抹掉一场发过钱的活动"同样的仪式,只会训练运营
// 对确认框整体失去敏感,而真正需要读完的那一个就在同一排按钮上。
// 不变的是:仍然不可逆、仍然写审计(审计关闭时同样拒绝)、仍然一次清掉九张表。
//
// # 删除**不碰**的东西
//
//	qy_fund_orders(跨库资金单)与主库 logs:它们是这笔钱真实发生过的账,
//	不归本模块管(见 model.go 顶部),删掉活动不等于这笔钱没发生过。
//	qy_lot_series 上的 seed_total_quota / paid_total_quota:那是累计发行上限的
//	计数器,调小它等于凭空还回一截 headroom —— 永远只增不减。
//	qy_lot_spend_daily:它按用户聚合、与活动无关。

// ─────────────────────────── 错误 ───────────────────────────

var (
	errDeleteNotFinished = newBizError(http.StatusConflict, "qy_lot_delete_not_finished",
		"只有草稿或已结束的活动才能删除:published/locked/settling 三段里用户的参与费已经真的扣走了,"+
			"删掉之后那笔钱就没有任何去向记录 —— 要止损请用「整场取消」,它会全额退款并公示")
	errDeleteFundsOpen = newBizError(http.StatusConflict, "qy_lot_delete_funds_open",
		"本场还有未落定的出款(计划中/发放中/失败/转人工),删除会让这笔平台还欠着的钱失去唯一的收款依据")
	errDeleteTextPending = newBizError(http.StatusConflict, "qy_lot_delete_text_pending",
		"本场还有未履行的文本奖:删除会让中奖者手里那一档凭据凭空消失")
	errDeleteEntryOpen = newBizError(http.StatusConflict, "qy_lot_delete_entry_open",
		"本场还有未结算的参与明细,删除会让这些用户的扣费失去归属")
	errDeleteFlagOpen = newBizError(http.StatusConflict, "qy_lot_delete_flag_open",
		"本场还有未处理的对账异常:异常指向的正是这些即将被删掉的行,请先处理完再删")
	errDeleteSeriesLive = newBizError(http.StatusConflict, "qy_lot_delete_series_live",
		"这是双色球的一期,它的滚存仍被本系列后续期次依赖:请先关闭系列,并从最新一期开始往前删")
	errDeleteEvidenceBroken = newBizError(http.StatusConflict, "qy_lot_delete_evidence_broken",
		"读不出本场的随机源,无法把证据链指纹写进审计;在库被修好之前拒绝删除")
	errDeleteConfirmMismatch = newBizError(http.StatusBadRequest, "qy_lot_delete_confirm",
		"二次确认没有通过:请原样输入活动编号")
	errDeleteAuditOff = newBizError(http.StatusConflict, "qy_lot_delete_audit_off",
		"审计已关闭。删除不可逆,而删除之后审计行是唯一还能证明这一场存在过的东西,因此审计关闭时一律拒绝删除")

	errHideNotFinished = newBizError(http.StatusConflict, "qy_lot_hide_not_finished",
		"只有已结束的活动才能下架:下架一场还在进行的活动等于一次隐蔽的提前截止")
	errHideAlready   = newBizError(http.StatusConflict, "qy_lot_hide_already", "该活动已经下架")
	errHideNotHidden = newBizError(http.StatusConflict, "qy_lot_hide_not_hidden",
		"该活动当前就在架上")
)

// errDeleteDraftDirty 是"这份草稿上挂着不该存在的行"的统一形状。
//
// 三种触发条件(参与明细 / 出款 / 承诺痕迹)在库结构正常时**一条都不可能成立**:
// 报名的原子 UPDATE 带着 `status='published'`,出款只在揭示事务里登记,
// commit_hash / published_at / issue_no 只在 publish 那一次写。所以走到这里
// 只意味着一件事 —— 库被直接改过。此时更不该抹掉现场。
//
// 三种情况共用一个 code(前端把 code 映射成一句固定文案),具体是哪一种写在
// message 里,并由 auditReason 原样落进审计。
func errDeleteDraftDirty(what string) *bizError {
	return newBizError(http.StatusConflict, "qy_lot_delete_draft_dirty",
		"这份草稿上挂着"+what+"。草稿从不下发给用户(大厅不列、详情 404、证据链未就绪),"+
			"出现这些行说明库被直接改过 —— 在查清之前拒绝删除")
}

// ─────────────────────────── 硬闸门 ───────────────────────────

// checkActivityDeletable 是"这一场能不能被彻底删除"的**唯一**判据。
//
// 它被调用两次:事务外一次(为了给运营一句精确的话,而不是一个笼统的 409),
// 事务内一次(那才是执行点 —— 事务外那次与真正的删除之间隔着网络与调度,
// 一笔转人工的出款完全可能恰好在这中间被管理员重试成功)。
//
// 判据只写在这一个函数里,是因为写两份必然漂移,而这里漂移一次的代价是
// 一场还欠着钱的活动被抹掉。
//
// # 两套闸门,不是一套
//
// 下面那六道全部在回答同一个问题:「这一场结束了,但它还欠着谁什么吗」。
// 草稿一个都不适用 —— 它没有参与者、没有资金单、没有对外公布过任何承诺,
// 而这六道里有两道(⑤对账异常、⑥双色球结转)在草稿上**恒为真**,于是
// 「草稿删不掉」不是一条设计取舍,是这两道闸门被套用在了它们从没打算覆盖的
// 状态上:
//
//	⑤ auditSpecDrift 扫的是 `kind='draw' AND spec_hash<>''`,**不看状态** ——
//	  草稿在创建那一刻就有 spec_hash,它与奖档行的两次读之间没有事务,
//	  一次并发的草稿保存就足以让它落下一条 spec_drift。那条 flag 从此把
//	  这份草稿钉死。
//	⑥ issue_no 要到 publish 那一刻才由 claimSeriesPool 分配,草稿恒为 0,
//	  于是 `issue_no > 0` 对系列里任何一个已发布期次都成立 ——
//	  一份绑了系列的双色球草稿,无论系列开着还是关着,永远拿一个
//	  errDeleteSeriesLive。
//
// 所以草稿走 checkDraftDeletable:它换成三条**正向**断言(没有参与、没有出款、
// 没有承诺痕迹),而不是把六道逐条放宽 —— 放宽出来的那一份必然与本体漂移。
func checkActivityDeletable(ctx context.Context, gdb *gorm.DB, act *Activity) error {
	if act.Status == StatusDraft {
		return checkDraftDeletable(ctx, gdb, act)
	}
	// ① 除草稿外只有 finished 能删。published/locked/settling 一律不行 ——
	// settling 里正躺着一批还没发出去的 payout 计划,而 published/locked 里
	// 用户的参与费已经真的扣走了。
	if act.Status != StatusFinished {
		return errDeleteNotFinished
	}

	// ② 资金必须全部落定。终态只有两个:paid(额度真的到账了)与
	// granted(文本奖的中奖位已登记)。held 尤其要拦 —— finishIfDone 只把
	// planned/paying/failed 当作"未完成",held 是被当作终态放行的,
	// 于是一场 finished 的活动完全可能还挂着一笔"平台欠着、只是发不出去"的钱。
	var n int64
	err := gdb.WithContext(ctx).Model(&Payout{}).
		Where("act_id = ? AND status NOT IN ?", act.Id, []string{PayoutPaid, PayoutGranted}).
		Count(&n).Error
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("统计未落定出款", err)
	}
	if n > 0 {
		return errDeleteFundsOpen
	}

	// ③ 文本奖的履行是"钱之外还欠着的东西"。granted 是资金终态,但
	// fulfilled_at = 0 意味着中奖者还在「我的参与」里等那串码。
	err = gdb.WithContext(ctx).Model(&Payout{}).
		Where("act_id = ? AND kind = ? AND fulfilled_at = 0", act.Id, PayoutText).
		Count(&n).Error
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("统计未履行文本奖", err)
	}
	if n > 0 {
		return errDeleteTextPending
	}

	// ④ 参与明细必须全部结清。pending 是扣费还在途,excluded 是封盘后才落定、
	// 退款由资金单终态驱动 —— 两者都代表"这个人的钱还没归位"。
	err = gdb.WithContext(ctx).Model(&Entry{}).
		Where("act_id = ? AND status IN ?", act.Id, []string{EntryPending, EntryExcluded}).
		Count(&n).Error
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("统计未结算参与", err)
	}
	if n > 0 {
		return errDeleteEntryOpen
	}

	// ⑤ 未解决的对账异常。raiseFlag 落下的每一条都在说"这一场的某个数对不上",
	// 而它指向的行正是即将被删掉的那些 —— 先删掉证据再关掉告警,顺序恰好反了。
	err = gdb.WithContext(ctx).Model(&Flag{}).
		Where("act_id = ? AND resolved = ?", act.Id, false).
		Count(&n).Error
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("统计未处理对账异常", err)
	}
	if n > 0 {
		return errDeleteFlagOpen
	}

	// ⑥ 双色球期次的结转链。
	//
	// 一期的 pool_carry_quota 是"上一期没派出去的部分",而系列行上的
	// seed_total / paid_total 是跨全部期次的累计。删掉中间任何一期,
	// series.go 顶部那条"Σ净增发 ≤ Σpool_seed ≤ issue_cap"的论证就再也
	// 复算不出来了。只留一条合法路径:先关闭系列(滚存作废、不再开新期),
	// 再从最新一期往前删。
	if act.SeriesId == 0 {
		return nil
	}
	var s Series
	switch err := gdb.WithContext(ctx).Where("id = ?", act.SeriesId).Take(&s).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// Series 永不清理。系列行不见了说明库被直接改过,这时任何关于结转的
		// 判断都没有依据 —— 拒绝。
		return errDeleteSeriesLive
	case err != nil:
		db.MarkFailure(err)
		return wrapInternal("读取期次系列", err)
	case s.Status != SeriesClosed:
		return errDeleteSeriesLive
	}
	err = gdb.WithContext(ctx).Model(&Activity{}).
		Where("series_id = ? AND issue_no > ?", act.SeriesId, act.IssueNo).
		Count(&n).Error
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("统计后续期次", err)
	}
	if n > 0 {
		return errDeleteSeriesLive
	}
	return nil
}

// checkDraftDeletable 是草稿那一支的判据。**三条正向断言,不是六道的放宽版。**
//
// 「草稿一定是干净的」不是一个可以假设的前提,它是一个必须被断言的结论:
//
//	参与明细:报名那条原子 UPDATE 带着 `status='published'`(entry.go),
//	         草稿收不到一条。**任何状态**的 entry 出现在草稿上都是异常 ——
//	         这里不像 ④ 那样只数 pending/excluded,而是一条都不许有。
//	出款:    payout 只在揭示/退款事务里成批登记,草稿没有那条路径。
//	         同理一条都不许有(文本奖 granted 也在其中,所以 ③ 被这条覆盖)。
//	承诺痕迹:commit_hash / published_at / issue_no 三个只在 publish 的同一次
//	         UPDATE 里写。它们非零意味着这一场发布过 —— 那时 status 就不该
//	         还是 draft,而"发布过的活动"绝不能按草稿的宽松口径删掉。
//	         issue_no 这一格同时替掉了 ⑥:它为 0 就证明 claimSeriesPool 从没
//	         为这份草稿取走过系列池,后续期次的结转链上没有它的位置。
//
// 刻意**不**查对账异常(⑤):草稿上的 flag 只可能来自 auditSpecDrift 的并发误报
// (见 checkActivityDeletable 的说明),它指向的是一份从未对外公开过的规格,
// 没有任何人拿着它去验证过什么。purgeActivityRows 会把它一并删掉。
// 用它挡住删除,等于用一条误报把唯一的处置路径也堵死。
func checkDraftDeletable(ctx context.Context, gdb *gorm.DB, act *Activity) error {
	gdb = gdb.WithContext(ctx)

	var n int64
	if err := gdb.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&n).Error; err != nil {
		db.MarkFailure(err)
		return wrapInternal("统计草稿参与明细", err)
	}
	if n > 0 {
		return errDeleteDraftDirty("参与明细(报名只在 published 才收得下)")
	}
	if err := gdb.Model(&Payout{}).Where("act_id = ?", act.Id).Count(&n).Error; err != nil {
		db.MarkFailure(err)
		return wrapInternal("统计草稿出款", err)
	}
	if n > 0 {
		return errDeleteDraftDirty("出款记录(派奖计划只在揭示事务里登记)")
	}
	if act.CommitHash != "" || act.PublishedAt != 0 || act.IssueNo != 0 {
		return errDeleteDraftDirty("承诺痕迹(commit_hash / published_at / issue_no 三者只在发布那一刻写)")
	}
	return nil
}

// ─────────────────────────── 删除 ───────────────────────────

type deleteActivityRequest struct {
	// ConfirmActNo 必须与路径上的活动号逐字相同 —— **只对已结束的活动**。
	// 二次确认在**服务端**校验:只在前端弹一个"确定吗"的话,任何一次脚本化的
	// 误调用都会直接生效。草稿不要求它,理由见 draftDeleteConfirmation。
	ConfirmActNo string `json:"confirm_act_no"`
	Reason       string `json:"reason"`
}

// draftDeleteReason 是草稿删除时运营没填理由的兜底文案。
//
// 它写进审计的 Reason 列而不是留空:留空之后那一行长得像"埋点漏了字段",
// 而"这次是运营主动没写"与"这次代码忘了传"在事后是两件必须分得开的事。
const draftDeleteReason = "草稿删除(未填理由)"

// handleDeleteActivity 彻底删除一场活动。**不可逆。**
//
// 用 FlagCore 而不是 FlagLottery:与管理端的读接口同一档 —— 功能被临时关停时,
// 管理员仍然必须能管理历史记录。
//
// # 确认强度按代价分两档
//
// 已结束的活动:必须原样回填活动编号 + 必填理由。敲编号的那几秒正是让人真的
// 读完"证据链从此无法被任何人验证"那段话的唯一办法。
//
// 草稿:两条都不要求(理由仍然可填,填了就进审计)。草稿从没对外公布过承诺、
// 没有一个用户见过它、删掉它不损失任何一段证据链 —— 给一个零代价的动作套上
// 和"抹掉一场发过钱的活动"同样的仪式,只会训练运营对确认框整体失去敏感,
// 而真正需要读完的那一个就在同一排按钮上。这是**刻意的强度差**,不是漏写。
func handleDeleteActivity(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	actNo := c.Param("act_no")
	var req deleteActivityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.activity.delete", actNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	// 长度上界与状态无关:它约束的是写进审计的那一列,不是确认强度。
	if utf8.RuneCountInString(reason) > 200 {
		writeAdminAudit(c, "lottery.activity.delete", actNo, qymodel.ResultFail, "删除理由过长", "", "")
		respondErr(c, errBadRequest("删除理由不超过 200 个字"))
		return
	}
	// 审计是这次删除的**前置条件**,不是副作用:审计关掉之后再删,库里与台账里
	// 会同时不存在这一场,谁都无法证明它存在过。这条判定在这里(能给出精确文案)
	// 与事务里(audit.WriteTx 的写入本身)各有一次。
	if !config.Get().Audit.On() {
		respondErr(c, errDeleteAuditOff)
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	// 活动要先读出来才知道该用哪一档确认强度。顺带把"活动号根本不存在"这种
	// 情况回成 404 而不是"二次确认没通过" —— 后者是一条误导性的线索。
	act, err := loadActivityAny(ctx, gdb, actNo)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.delete", actNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if act.Status == StatusDraft {
		if reason == "" {
			reason = draftDeleteReason
		}
	} else {
		if reason == "" {
			writeAdminAudit(c, "lottery.activity.delete", actNo, qymodel.ResultFail, "未填写删除理由",
				snapText(activitySnapshot(act, nil)), "")
			respondErr(c, errBadRequest("必须填写删除理由,它会留在审计里"))
			return
		}
		if strings.TrimSpace(req.ConfirmActNo) != actNo {
			writeAdminAudit(c, "lottery.activity.delete", actNo, qymodel.ResultFail,
				"二次确认的活动编号不匹配", snapText(activitySnapshot(act, nil)), "")
			respondErr(c, errDeleteConfirmMismatch)
			return
		}
	}
	if err := checkActivityDeletable(ctx, gdb, act); err != nil {
		writeAdminAudit(c, "lottery.activity.delete", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}

	evidence, err := buildDeleteEvidence(ctx, gdb, act)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.delete", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}
	before := snapText(evidence)

	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先拿活动行的写锁,再拿锁内那一份真实状态复核。
		//
		// 这里曾经是"UPDATE … SET updated_at=now WHERE status=finished,断言
		// RowsAffected==1"。那条 UPDATE 只写 updated_at,同一秒内有过任何一次
		// 管理端写(换封面、下架、上一次删除失败后立刻重试)就会一列都没改到,
		// MySQL 回 0 行,一场完全可删的活动被回绝成"活动状态已变化"。
		// 理由与口径写在 lockActivityStatus 上,不在这里复述。
		status, err := lockActivityStatus(tx, act.Id)
		if err != nil {
			return err
		}
		// 与**加载时读到的那个状态**比,不是与某个常量比:两档确认强度、
		// 走哪一支闸门、purgeActivityRows 最后那条 DELETE 的 WHERE,三者都是
		// 按 act.Status 决定的。一份在这中间被别人发布掉的草稿必须在这里停住,
		// 否则它会带着"草稿口径"的宽松闸门被删掉。
		if status != act.Status {
			return errStatusConflict
		}
		// 闸门的真正执行点。事务外那一次只负责说人话。
		if err := checkActivityDeletable(ctx, tx, act); err != nil {
			return err
		}
		// **审计先于删除写入**,且与删除同生共死:事务一回滚两者一起消失,
		// 提交之后审计行永久留下(qy_lot_* 那十一张表已经不在了)。
		err = audit.WriteTx(tx, audit.Entry{
			TraceNo:     act.ActNo,
			Category:    auditCategory,
			Action:      "lottery.activity.delete",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: c.GetInt("id"),
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			Reason:      audit.Truncate(reason, 500),
			BeforeSnap:  before,
			AfterSnap:   snapText(map[string]any{"deleted": true, "act_no": act.ActNo}),
		})
		if err != nil {
			return err
		}
		return purgeActivityRows(tx, act)
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("删除活动", err)
		}
		writeAdminAudit(c, "lottery.activity.delete", actNo, qymodel.ResultFail,
			auditReason(err), before, "")
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"act_no": actNo, "deleted": true})
}

// purgeSecretHistChunk 是按 payout_no 批删兑换码履历时单条 IN 的行数上限。
// 一场活动的条目上限由 lottery.max_total_entries_hard 兜住,但那可以配到很大,
// 而几千个占位符的 IN 在 MySQL 上会把解析开销顶上去。
const purgeSecretHistChunk = 500

// purgeActivityRows 按外键顺序抹掉一场活动的全部行。**必须在调用方的事务里。**
//
// 顺序不是随便排的:先删指向 payout 的履历,再删 payout 与 entry,最后才是
// 活动行本身。留半截的表现是用户端还能查到孤儿记录 ——
// 而最容易漏的那一张恰恰是 qy_lot_prize_secret_hist:它挂在 payout_no 上
// 而不是 act_id 上,漏了它,一场被删掉的活动会在库里留下一堆无主的兑换码明文。
//
// 以 act_id 为键的从表由 TestPurgeCoversEveryActIdKeyedTable 用反射兜底:
// 以后再给活动加从表,不改这里那条用例就会红。**不以 act_id 为键的从表兜不住**,
// 目前有两张,各有各的归属:
//
//	qy_lot_prize_secret_hist 挂在 payout_no 上,由上面那段显式处理;
//	qy_lot_covers(Activity.CoverRef 指向的封面)**刻意不在这里删**。
//	  cover.go 的 pruneCovers 有一条专门的批次扫 act_id 指向已不存在活动的行,
//	  由它在事务外先删磁盘文件、再标记回收。在这个事务里删掉封面行的话,
//	  磁盘文件会失去唯一指向它的记录 —— 那正是 cover.go 反复解释的
//	  "库里有、盘上没有"的镜像版本。
func purgeActivityRows(tx *gorm.DB, act *Activity) error {
	payoutNos := make([]string, 0, 64)
	if err := tx.Model(&Payout{}).Where("act_id = ?", act.Id).
		Pluck("payout_no", &payoutNos).Error; err != nil {
		return err
	}
	for start := 0; start < len(payoutNos); start += purgeSecretHistChunk {
		end := start + purgeSecretHistChunk
		if end > len(payoutNos) {
			end = len(payoutNos)
		}
		if err := tx.Where("payout_no IN ?", payoutNos[start:end]).
			Delete(&PrizeSecretHist{}).Error; err != nil {
			return err
		}
	}
	// 剩下的六张都以 act_id 为条件。Seed 的主键就是 act_id,同一条件同样成立。
	for _, model := range []any{
		&Payout{}, &Entry{}, &Event{}, &Option{}, &Prize{}, &Flag{}, &Seed{},
	} {
		if err := tx.Where("act_id = ?", act.Id).Delete(model).Error; err != nil {
			return err
		}
	}
	// 封面是唯一一张**只解绑、不删行**的从表(qy_lot_covers)。
	//
	// 它的库行是磁盘上那张图的唯一指针,回收任务按库行扫 —— 在这里把行删掉,
	// 那个文件就再也没有任何东西指向它,永久留在盘上。而文件本身又不能在这个
	// 事务里删:磁盘操作不参与回滚,一次回滚之后活动还在、封面却已经没了。
	//
	// 所以改成打 detached_at 并解开 act_id,交给 lottery.cover_prune 在宽限期
	// 之后删文件、标 purged_at(见 cover.go)。act_id 归零同时让本函数满足
	// "以 act_id 为键的从表,删完之后一行都不该剩"这条守卫。
	if err := tx.Model(&Cover{}).Where("act_id = ?", act.Id).
		Updates(map[string]any{
			"act_id":      0,
			"detached_at": common.GetTimestamp(),
		}).Error; err != nil {
		return err
	}
	// 活动行最后删,并且再带一次状态条件:走到这里状态仍然必须是**调用方读到的
	// 那一个**。写死 finished 的话草稿这一支会恒 0 行,一场刚被清空九张从表的
	// 草稿会以 errStatusConflict 整体回滚 —— 表面看是"活动状态已变化",
	// 实际上是判据抄错了状态。
	//
	// 只认这两个状态:别的状态一律不该走到这里(checkActivityDeletable 会先拦),
	// 而万一被绕过,让它在这一步硬停,而不是删掉一场 published 的活动。
	if act.Status != StatusDraft && act.Status != StatusFinished {
		return errStatusConflict
	}
	res := tx.Where("id = ? AND status = ?", act.Id, act.Status).Delete(&Activity{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errStatusConflict
	}
	return nil
}

// buildDeleteEvidence 拼出写进审计的那份"这一场存在过"的证据。
//
// 删完之后这份快照是唯一的遗物,所以它必须同时装下三类东西:
//
//	身份与规模:act_no / 标题 / 玩法 / 参与人数 / 投注总额;
//	资金口径:  派奖总额 / 退款总额 / 平台抽成 / 已付合计;
//	证据链指纹:algo / commit_hash / rules_hash / spec_hash / roster_hash /
//	           chain_head / **seed**。
//
// 种子是这里最关键的一位:它一旦随活动行消失,连"当初公布的 commit_hash 到底
// 是不是这个种子算出来的"都再也没人能验。对一场已结束的活动来说它本来就已经
// 在 proof 里公开过,写进只有管理员能看的审计表不构成任何新的泄漏面。
// 读不出来就拒绝删除 —— 那说明库已经被人动过,而这时更不该抹掉现场。
func buildDeleteEvidence(ctx context.Context, gdb *gorm.DB, act *Activity) (map[string]any, error) {
	seed, err := loadSeedForReveal(ctx, gdb, act.Id)
	if err != nil {
		// 草稿这一支不拿种子当前置条件。
		//
		// 上面那段话("连当初公布的 commit_hash 是不是这个种子算出来的都没人能验")
		// 的前提是**有一个已经公布过的 commit_hash**。草稿的 commit_hash 恒为空串
		// (checkDraftDeletable 刚断言过),没有任何一份对外承诺需要被这个种子
		// corroborate。这时把删除卡住,换来的不是一份被保住的证据,而是一份
		// 谁都清不掉的草稿 —— 那正是这一轮要修的形状。
		if act.Status != StatusDraft {
			return nil, errDeleteEvidenceBroken
		}
		seed = ""
	}

	gdb = gdb.WithContext(ctx)
	var entryRows, successRows, distinctUsers, payoutRows, eventRows int64
	if err := gdb.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&entryRows).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计参与明细", err)
	}
	if err := gdb.Model(&Entry{}).Where("act_id = ? AND status = ?", act.Id, EntrySuccess).
		Count(&successRows).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计有效参与", err)
	}
	if err := gdb.Model(&Entry{}).Distinct("user_id").
		Where("act_id = ? AND status = ?", act.Id, EntrySuccess).
		Count(&distinctUsers).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计参与人数", err)
	}
	var betTotal, paidTotal int64
	if err := gdb.Model(&Entry{}).Select("COALESCE(SUM(amount), 0)").
		Where("act_id = ? AND status = ?", act.Id, EntrySuccess).
		Scan(&betTotal).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计投注总额", err)
	}
	if err := gdb.Model(&Payout{}).Select("COALESCE(SUM(amount_quota), 0)").
		Where("act_id = ? AND status = ?", act.Id, PayoutPaid).
		Scan(&paidTotal).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计已付出款", err)
	}
	if err := gdb.Model(&Payout{}).Where("act_id = ?", act.Id).Count(&payoutRows).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计出款行", err)
	}
	if err := gdb.Model(&Event{}).Where("act_id = ?", act.Id).Count(&eventRows).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计事件流", err)
	}

	// 刻意不放 intro / rules_text / spec_text:审计快照有 SnapshotMaxBytes
	// (默认 4096 字节)的截断,而截断是从尾巴切的 —— 塞一段几 KB 的规则正文
	// 进去,会把后面那些真正不可再生的哈希整段切掉。规则文本的哈希在,
	// 而规则文本本身在事件流与用户手里的截图里都有。
	return map[string]any{
		"act_no":    act.ActNo,
		"title":     act.Title,
		"kind":      act.Kind,
		"draw_mode": act.DrawMode,
		"status":    act.Status,
		"outcome":   act.Outcome,

		"algo":         act.Algo,
		"commit_hash":  act.CommitHash,
		"rules_hash":   act.RulesHash,
		"spec_hash":    act.SpecHash,
		"roster_hash":  act.RosterHash,
		"roster_count": act.RosterCount,
		"chain_head":   act.ChainHead,
		"seed":         seed,

		"entry_rows":     entryRows,
		"entry_success":  successRows,
		"distinct_users": distinctUsers,
		"entry_seq":      act.EntrySeq,
		"payout_rows":    payoutRows,
		"event_rows":     eventRows,

		"stake_quota":        act.StakeQuota,
		"bet_total_quota":    betTotal,
		"pool_quota":         act.PoolQuota,
		"payout_quota":       act.PayoutQuota,
		"refund_quota":       act.RefundQuota,
		"platform_fee_quota": act.PlatformFeeQuota,
		"paid_total_quota":   paidTotal,
		"text_grant_count":   act.TextGrantCount,

		"series_no":        act.SeriesNo,
		"issue_no":         act.IssueNo,
		"pool_open_quota":  act.PoolOpenQuota,
		"pool_carry_quota": act.PoolCarryQuota,
		"ball_result":      act.BallResult,

		"created_by":   act.CreatedBy,
		"created_at":   act.CreatedAt,
		"published_at": act.PublishedAt,
		"revealed_at":  act.RevealedAt,
		"settled_at":   act.SettledAt,
		"hidden_at":    act.HiddenAt,
	}, nil
}

// ─────────────────────────── 下架 / 重新上架 ───────────────────────────

type hideActivityRequest struct {
	Reason string `json:"reason"`
}

// handleHideActivity 把一场已结束的活动从用户端的活动大厅撤下。**可逆,不动钱。**
//
// # 为什么只有 finished 能下架
//
// 下架一场 published 的活动,对没打开过它的人等于提前截止,对手里有链接的人
// 却照常可以参与 —— 那正是本模块从头到尾在防的选时/挑人攻击,只是换了个入口
// (见 api_admin.go 顶部"管理端没有提前截止按钮"那一段)。进行中的止损只有一条
// 路径:整场取消 + 全额退款 + 公示。
//
// # 下架**不**遮住什么
//
// 活动详情、我的参与、匿名证据链一律照常可达。公正性一旦公布过就不能被运营
// 收回,而参与过的人必须还能查到自己那一票 —— 让"我的参与"里的链接指向 404,
// 就是"删完还能被用户看见半截"的镜像版本。
func handleHideActivity(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	actNo := c.Param("act_no")
	var req hideActivityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.activity.hide", actNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 200 {
		writeAdminAudit(c, "lottery.activity.hide", actNo, qymodel.ResultFail, "未填写下架理由", "", "")
		respondErr(c, errBadRequest("必须填写下架理由"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, actNo)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.hide", actNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if act.Status != StatusFinished {
		writeAdminAudit(c, "lottery.activity.hide", actNo, qymodel.ResultFail,
			auditReason(errHideNotFinished), snapText(activitySnapshot(act, nil)), "")
		respondErr(c, errHideNotFinished)
		return
	}

	now := common.GetTimestamp()
	res := gdb.WithContext(ctx).Model(&Activity{}).
		Where("id = ? AND status = ? AND hidden_at = 0", act.Id, StatusFinished).
		Updates(map[string]any{
			"hidden_at":     now,
			"hidden_by":     c.GetInt("id"),
			"hidden_reason": audit.Truncate(reason, 255),
			"updated_at":    now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		writeAdminAudit(c, "lottery.activity.hide", actNo, qymodel.ResultFail,
			auditReason(res.Error), snapText(activitySnapshot(act, nil)), "")
		respondErr(c, wrapInternal("下架活动", res.Error))
		return
	}
	if res.RowsAffected != 1 {
		writeAdminAudit(c, "lottery.activity.hide", actNo, qymodel.ResultFail,
			auditReason(errHideAlready), snapText(activitySnapshot(act, nil)), "")
		respondErr(c, errHideAlready)
		return
	}

	writeAdminAudit(c, "lottery.activity.hide", actNo, qymodel.ResultOK, reason,
		snapText(activitySnapshot(act, nil)),
		snapText(map[string]any{"act_no": actNo, "hidden_at": now}))
	respondOK(c, gin.H{"act_no": actNo, "hidden_at": now})
}

// handleUnhideActivity 把下架的活动放回活动大厅。
//
// 它存在的理由与 flags/:id/resolve 相同:一个只能单向按下去的开关,
// 运营按错之后只能去改库。下架本来就是可逆的,不给撤回入口等于把它变成不可逆。
func handleUnhideActivity(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	actNo := c.Param("act_no")

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, actNo)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.unhide", actNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}

	now := common.GetTimestamp()
	res := gdb.WithContext(ctx).Model(&Activity{}).
		Where("id = ? AND hidden_at > 0", act.Id).
		Updates(map[string]any{
			"hidden_at":     0,
			"hidden_by":     0,
			"hidden_reason": "",
			"updated_at":    now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		writeAdminAudit(c, "lottery.activity.unhide", actNo, qymodel.ResultFail,
			auditReason(res.Error), snapText(activitySnapshot(act, nil)), "")
		respondErr(c, wrapInternal("上架活动", res.Error))
		return
	}
	if res.RowsAffected != 1 {
		writeAdminAudit(c, "lottery.activity.unhide", actNo, qymodel.ResultFail,
			auditReason(errHideNotHidden), snapText(activitySnapshot(act, nil)), "")
		respondErr(c, errHideNotHidden)
		return
	}

	writeAdminAudit(c, "lottery.activity.unhide", actNo, qymodel.ResultOK, "",
		snapText(activitySnapshot(act, nil)),
		snapText(map[string]any{"act_no": actNo, "hidden_at": 0}))
	respondOK(c, gin.H{"act_no": actNo, "hidden_at": 0})
}
