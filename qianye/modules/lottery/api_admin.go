package lottery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin.go —— 管理端 CRUD。
//
// # 管理端**没有**的两个按钮,以及为什么
//
//	提前截止:封盘时刻 close_at 在 publish 时进承诺哈希,由定时任务自动执行。
//	立即开奖:draw_at 同理。
//
// 结果 = f(seed, 冻结名单, 规则) 是确定函数,**与执行时刻完全无关**;
// 但"谁来决定什么时候封盘"却能被用来挑名单 —— 管理员只要能提前截止,
// 就能在看到某个人报名之后立刻关门。给他这两个按钮等于把选时攻击重新引进来。
// 管理员剩下的唯一动作是整场取消,而取消必然全额退款、必然公示、必然写审计:
// 他只能"不开",不能"挑一个开"。

// ─────────────────────────── 请求体 ───────────────────────────

type prizeInput struct {
	Tier        int    `json:"tier"`
	Name        string `json:"name"`
	AmountQuota int64  `json:"amount_quota"`
	Count       int    `json:"count"`
	// PrizeType 为空按 quota 处理,让存量前端与脚本不必改。
	PrizeType string `json:"prize_type"`
	// WinPpm 只在 draw_mode=prob 下有意义。
	WinPpm int `json:"win_ppm"`
	// TextDesc 是**会公开展示**的履行说明,绝不能写兑换码。
	TextDesc string `json:"text_desc"`

	// RedMatch / BlueMatch / PoolShareBps 只在 draw_mode=ball 下有意义:
	// 前两个是这一奖级要求的最少命中数,后者 > 0 时本级是浮动奖(占池比例),
	// 与固定额度互斥。
	RedMatch     int `json:"red_match"`
	BlueMatch    int `json:"blue_match"`
	PoolShareBps int `json:"pool_share_bps"`
}

type optionInput struct {
	OptNo      int    `json:"opt_no"`
	Label      string `json:"label"`
	IsCatchAll bool   `json:"is_catch_all"`
}

type activityInput struct {
	Kind string `json:"kind"`
	// DrawMode 只对抽奖有意义,为空按 rank 处理。
	DrawMode string `json:"draw_mode"`
	Title    string `json:"title"`
	Intro    string `json:"intro"`
	// CoverUrl / CoverRef 是卡片背景图的两种来源,互斥。归一化与互斥判定走
	// resolveCoverInput —— 创建、修改、单独换图三条路共用同一份判定。
	CoverUrl string `json:"cover_url"`
	CoverRef string `json:"cover_ref"`

	StakeQuota  int64 `json:"stake_quota"`
	BetMinQuota int64 `json:"bet_min_quota"`
	BetMaxQuota int64 `json:"bet_max_quota"`

	OpenAt         int64 `json:"open_at"`
	CloseAt        int64 `json:"close_at"`
	DrawAt         int64 `json:"draw_at"`
	SettleDeadline int64 `json:"settle_deadline"`

	AllowMultiWin bool `json:"allow_multi_win"`
	// FeeBps 为 nil 时取运营配置里的默认值。用指针而不是 0:竞猜"手续费为 0"
	// 是一个合法且有意义的取值(公益场),不能与"没填"混为一谈。
	FeeBps           *int `json:"fee_bps"`
	MinEntriesToHold int  `json:"min_entries_to_hold"`

	// SeriesNo 只在 draw_mode=ball 下必填:一期双色球必须属于某个期次系列,
	// 号池、投注入池比例与累计发行上限全部由那个系列决定。
	SeriesNo string `json:"series_no"`

	Rules   Rules         `json:"rules"`
	Prizes  []prizeInput  `json:"prizes"`
	Options []optionInput `json:"options"`
}

type activityWriteResult struct {
	ActNo string `json:"act_no"`
	// PrizeTotalQuota / BreakEvenEntries 是运营最需要的那两个数:
	// 抽奖是"平台收参与费、平台出奖品",两边不守恒是正常的,派奖对用户额度
	// 是**净增发**。把"要多少人参加才不亏"摆在发布按钮之前,
	// 是拦住配置事故最便宜的一道。
	PrizeTotalQuota  int64 `json:"prize_total_quota"`
	BreakEvenEntries int64 `json:"break_even_entries"`
	// WorstCaseNetIssue 是最坏情况下净增发多少额度(一个人都没来但仍开奖)。
	//
	// 概率制下它**与名次制一模一样**:超募时某档的预算由全部中签者均分,
	// 支出上界恒为 Σ(count × amount)。这是"概率模式不引入任何新发行风险"
	// 的全部理由,任何后续改动不得动摇它。
	WorstCaseNetIssue int64 `json:"worst_case_net_issue"`
	// ExpectPayoutQuota 是概率制下按**全场坐满**估算的期望支出。
	// rank 模式恒等于 WorstCaseNetIssue(一定发满)。
	ExpectPayoutQuota int64 `json:"expect_payout_quota"`
	// ExpectWinners 是概率制下按全场坐满估算的期望中奖人次。
	ExpectWinners int64 `json:"expect_winners"`
	// WorstCaseTextGrants 是最坏情况下要人工履行多少份文本奖。
	//
	// 概率制下文本奖不摊薄(兑换码劈不开),所以理论上全场每个人都可能中 ——
	// 这个数必须摆在发布按钮上面,否则运营会在"1% 概率发 10 份"的心理预期下
	// 配出一个要发几百份的活动。
	WorstCaseTextGrants int `json:"worst_case_text_grants"`
}

// ─────────────────────────── 创建 ───────────────────────────

// handleCreateActivity 创建草稿活动。
//
// 草稿期可以随意改内容,因为**此刻还没有任何承诺**。种子与两个盐在这一刻就
// 生成并写进 qy_lot_seed:API 不接受任何种子入参,管理员没有输入通道。
func handleCreateActivity(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	var in activityInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "lottery.activity.create", "", qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	act, prizes, options, err := buildActivity(ctx, &in, c.GetInt("id"))
	if err != nil {
		writeAdminAudit(c, "lottery.activity.create", "", qymodel.ResultFail,
			auditReason(err), "", snapText(activitySnapshot(nil, &in)))
		respondErr(c, err)
		return
	}

	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(act).Error; err != nil {
			return err
		}
		// 认领封面必须与建活动同事务:分开做的话,建活动失败会留下一张
		// 已经指着这场活动、而这场活动并不存在的封面 —— 它既不算孤儿
		// (act_id 非 0),也不属于任何活动,只有"活动已删除"那条口径能捞到它。
		if err := bindCover(tx, act.Id, act.ActNo, "", act.CoverRef, c.GetInt("id")); err != nil {
			return err
		}
		// 种子与两个盐在草稿期就落库。绝不等到 publish ——
		// publish 那一刻要算 commit_hash,种子必须已经存在且不可能被本次请求影响。
		if err := tx.Create(&Seed{
			ActId:     act.Id,
			Seed:      newSecret(),
			RefSalt:   newSecret(),
			IpSalt:    newSecret(),
			CreatedAt: act.CreatedAt,
		}).Error; err != nil {
			return err
		}
		for i := range prizes {
			prizes[i].ActId = act.Id
		}
		if len(prizes) > 0 {
			if err := tx.Create(&prizes).Error; err != nil {
				return err
			}
		}
		for i := range options {
			options[i].ActId = act.Id
		}
		if len(options) > 0 {
			if err := tx.Create(&options).Error; err != nil {
				return err
			}
		}
		return writeActivityEvent(tx, act.Id, "", StatusDraft, ActionCreate, qymodel.ActorAdmin, c.GetInt("id"), map[string]any{
			"kind":  act.Kind,
			"title": act.Title,
		})
	})
	if err != nil {
		// 封面换绑会在这个事务里抛出 errCoverNotFound —— 那是一条该回给运营的
		// 400("这张图不存在或已被别的活动用了"),无条件包成 500 会让他
		// 收到"处理失败,请稍后重试",而重试一万次都是同一个结果。
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("创建活动", err)
		}
		writeAdminAudit(c, "lottery.activity.create", "", qymodel.ResultFail,
			auditReason(err), "", snapText(activitySnapshot(act, &in)))
		respondErr(c, err)
		return
	}

	writeAdminAudit(c, "lottery.activity.create", act.ActNo, qymodel.ResultOK, "",
		"", snapText(activitySnapshot(act, &in)))
	respondOK(c, writeResultOf(act, prizes))
}

// handleUpdateActivity 修改草稿活动。
//
// 只有 draft 能改:published 之后条件、奖档、选项、四个时刻、费率全部进了
// commit_hash,改任何一项都会让已经公开的承诺变成谎言。
// 条件 UPDATE 的 WHERE status='draft' 是这条约束的唯一执行点。
func handleUpdateActivity(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	actNo := c.Param("act_no")
	var in activityInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "lottery.activity.update", actNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	old, err := loadActivityAny(ctx, gdb, actNo)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.update", actNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if old.Status != StatusDraft {
		writeAdminAudit(c, "lottery.activity.update", actNo, qymodel.ResultFail,
			"活动已发布,内容已被承诺哈希冻结", snapText(activitySnapshot(old, nil)), "")
		// 不是 errStatusConflict:后者在说"刷新一下再试",而这里的真相是
		// "这件事从此不可能做到"。理由见 errUpdateNotDraft。
		respondErr(c, errUpdateNotDraft)
		return
	}

	// 沿用原来的 act_no、创建者与创建时间:它们不是本次提交的内容,
	// 而 act_no 已经出现在草稿链接里。
	next, prizes, options, err := buildActivity(ctx, &in, old.CreatedBy)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.update", actNo, qymodel.ResultFail,
			auditReason(err), snapText(activitySnapshot(old, nil)), "")
		respondErr(c, err)
		return
	}
	next.Id = old.Id
	next.ActNo = old.ActNo
	next.CreatedAt = old.CreatedAt
	next.CreatedBy = old.CreatedBy

	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 锁内复核状态,而不是拿 Updates 的 RowsAffected 当闸门:管理员保存一份
		// **没改动过**的草稿(双击保存、客户端重投)时 draftUpdates 的每一列都
		// 等于原值,MySQL 回 0 行,一次完全正常的保存被回绝成"活动状态已变化"。
		// 理由与口径见 lockActivityStatus。
		status, err := lockActivityStatus(tx, old.Id)
		if err != nil {
			return err
		}
		if status != StatusDraft {
			return errStatusConflict
		}
		// WHERE 上的 status 留着当第二道保险:行锁已经握在手里,它永远成立,
		// 但它让这条语句单独读起来也是安全的。
		if err := tx.Model(&Activity{}).
			Where("id = ? AND status = ?", old.Id, StatusDraft).
			Updates(draftUpdates(next)).Error; err != nil {
			return err
		}
		// 封面的换绑与活动行的 UPDATE 同事务:被换下来的那一张要在同一刻拿到
		// detached_at,否则一次回滚之后活动行上仍指着它、而它已经被标成垃圾,
		// 回收任务会在宽限期后把一张**正在用**的图从磁盘上删掉。
		if err := bindCover(tx, old.Id, old.ActNo, old.CoverRef, next.CoverRef, c.GetInt("id")); err != nil {
			return err
		}
		// 奖档与选项整体替换。草稿期还没有任何参与,不存在引用完整性问题;
		// 逐条 diff 只会多一套要维护的合并逻辑。
		if err := tx.Where("act_id = ?", old.Id).Delete(&Prize{}).Error; err != nil {
			return err
		}
		if err := tx.Where("act_id = ?", old.Id).Delete(&Option{}).Error; err != nil {
			return err
		}
		for i := range prizes {
			prizes[i].ActId = old.Id
		}
		if len(prizes) > 0 {
			if err := tx.Create(&prizes).Error; err != nil {
				return err
			}
		}
		for i := range options {
			options[i].ActId = old.Id
		}
		if len(options) > 0 {
			if err := tx.Create(&options).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// 判据是"它是不是一条可以安全回给用户的业务错误",而不是"它是不是
		// errStatusConflict":封面换绑会在这个事务里抛出 errCoverNotFound,
		// 按单值比对的话它会被包成 500,而它本该是一条明确的 400。
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("修改活动", err)
		}
		writeAdminAudit(c, "lottery.activity.update", actNo, qymodel.ResultFail,
			auditReason(err), snapText(activitySnapshot(old, nil)), "")
		respondErr(c, err)
		return
	}

	writeAdminAudit(c, "lottery.activity.update", actNo, qymodel.ResultOK, "",
		snapText(activitySnapshot(old, nil)), snapText(activitySnapshot(next, &in)))
	res := writeResultOf(next, prizes)
	res.ActNo = actNo
	respondOK(c, res)
}

// ─────────────────────────── 发布(承诺生成)───────────────────────────

// handlePublishActivity 发布活动并生成承诺哈希。**不可逆,管理员只点一次。**
//
// 这是全模块最关键的一条写路径:rules_hash / spec_hash / commit_hash 在这一刻
// 算出并冻结,此后条件、奖档、选项、四个时刻、费率全部只读。
//
// 审计走 audit.WriteTx —— 与状态转移同生共死。事务一回滚,审计也必须消失,
// 否则台账里会出现一条"发布成功"而库里还是草稿。AfterSnap 含三个哈希,
// **绝不含 seed**。
func handlePublishActivity(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
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
		writeAdminAudit(c, "lottery.activity.publish", actNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if act.Status != StatusDraft {
		writeAdminAudit(c, "lottery.activity.publish", actNo, qymodel.ResultFail,
			"活动不在草稿状态", snapText(activitySnapshot(act, nil)), "")
		respondErr(c, errStatusConflict)
		return
	}
	// 玩法被隐藏时**不允许发布**。草稿可以照常备着 —— 草稿本来就不下发给用户,
	// 拦在这里不妨碍运营提前把下一期做好。
	//
	// 拦的是发布这一刻:发布是不可逆的(承诺哈希一经生成,条件与时刻全部只读),
	// 而发布一场用户在大厅里看不到、点进去也报不了名的活动,得到的是一场只会
	// 走到"参与人数不足流局"的空活动 —— 那正是"实现了但界面上点不到"的翻版。
	//
	// 位置在时刻校验**之前**:它是最便宜也最决定性的一条。放在后面的表现是
	// 运营先被"截止时间已过"顶回来、改完时间再提交,才发现这个玩法压根没开 ——
	// 两趟往返,而第一趟给出的理由是个假线索。
	if !effectiveCtx(ctx).playShown(playOf(act.Kind, act.DrawMode)) {
		writeAdminAudit(c, "lottery.activity.publish", actNo, qymodel.ResultFail,
			"该玩法当前已隐藏,请先在娱乐配置里打开",
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, errPlayHidden)
		return
	}
	// 时刻必须相对**现在**仍然成立:草稿可能躺了两天,close_at 早就过去了。
	if err := validateSchedule(act, common.GetTimestamp()); err != nil {
		writeAdminAudit(c, "lottery.activity.publish", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}
	if err := checkActiveCap(ctx, gdb); err != nil {
		writeAdminAudit(c, "lottery.activity.publish", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}
	if err := checkAlgoPublishable(act); err != nil {
		writeAdminAudit(c, "lottery.activity.publish", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}

	// 种子在事务外读一次:双色球要在事务内(取走系列池、定下期号之后)才算得出
	// 承诺哈希,而承诺的每一个分量都必须与最终落库的那一份逐字节一致。
	seedHex, err := loadSeedForReveal(ctx, gdb, act.Id)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.publish", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}

	now := common.GetTimestamp()
	algo := publishAlgo(act)
	var commitHash string
	after := map[string]any{}
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot := *act
		snapshot.Algo = algo

		// 双色球:把系列池整块取走冻结成本期开局池,并分配期号。
		// **必须在算承诺之前**:pool_open / pool_seed / pool_carry / issue_no
		// 全部进 commit 原像,而承诺一旦落库就不可再改。
		var series *SeriesSnapshot
		if snapshot.Kind == KindDraw && snapshot.DrawMode == DrawModeBall {
			if err := claimSeriesPool(tx, &snapshot); err != nil {
				return err
			}
			if err := checkBallPoolCovers(tx, &snapshot); err != nil {
				return err
			}
			series = seriesSnapshotOf(&snapshot)
		}
		commitHash = CommitHashFor(&snapshot, series, seedHex)

		for k, v := range map[string]any{
			"act_no": act.ActNo, "kind": act.Kind, "algo": algo, "draw_mode": act.DrawMode,
			"rules_hash": act.RulesHash, "spec_hash": act.SpecHash, "commit_hash": commitHash,
			"open_at": act.OpenAt, "close_at": act.CloseAt, "draw_at": act.DrawAt,
		} {
			after[k] = v
		}

		updates := map[string]any{
			"status":       StatusPublished,
			"algo":         algo,
			"commit_hash":  commitHash,
			"chain_head":   commitHash, // chain_0 = commit_hash
			"published_at": now,
			"updated_at":   now,
		}
		if series != nil {
			after["series_no"] = series.SeriesNo
			after["issue_no"] = series.IssueNo
			after["pool_open_quota"] = series.PoolOpenQuota
			updates["series_no"] = snapshot.SeriesNo
			updates["issue_no"] = snapshot.IssueNo
			updates["pool_seed_quota"] = snapshot.PoolSeedQuota
			updates["pool_carry_quota"] = snapshot.PoolCarryQuota
			updates["pool_open_quota"] = snapshot.PoolOpenQuota
			updates["pool_share_bps"] = snapshot.PoolShareBps
			updates["ball_red_pool"] = snapshot.BallRedPool
			updates["ball_red_pick"] = snapshot.BallRedPick
			updates["ball_blue_pool"] = snapshot.BallBluePool
			updates["ball_blue_pick"] = snapshot.BallBluePick
		}

		res := tx.Model(&Activity{}).
			Where("id = ? AND status = ?", act.Id, StatusDraft).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errStatusConflict
		}
		if err := writeActivityEvent(tx, act.Id, StatusDraft, StatusPublished, ActionPublish,
			qymodel.ActorAdmin, c.GetInt("id"), after); err != nil {
			return err
		}
		return audit.WriteTx(tx, audit.Entry{
			TraceNo:     act.ActNo,
			Category:    auditCategory,
			Action:      "lottery.activity.publish",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: c.GetInt("id"),
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			BeforeSnap:  snapText(activitySnapshot(act, nil)),
			AfterSnap:   snapText(after),
		})
	})
	if err != nil {
		if _, biz := AsBizError(err); !biz && !errors.Is(err, errStatusConflict) {
			db.MarkFailure(err)
			err = wrapInternal("发布活动", err)
		}
		// 事务里的那条审计已经随回滚消失了,必须在事务外补一条失败记录 ——
		// "有人在这一刻试图发布、失败了"是最需要留痕的事实之一。
		writeAdminAudit(c, "lottery.activity.publish", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}

	respondOK(c, gin.H{
		"act_no":      act.ActNo,
		"status":      StatusPublished,
		"commit_hash": commitHash,
		"rules_hash":  act.RulesHash,
		"spec_hash":   act.SpecHash,
		"algo":        algo,
		"draw_mode":   act.DrawMode,
	})
}

// computeCommit 算出承诺哈希。
//
// 哈希的是**落库的那份字节**(rules_text / spec_text),不重新序列化 ——
// Go struct 的序列化顺序与第三方不一致,重序列化一次就会让所有外部验证者
// 算出不同的哈希。
func computeCommit(ctx context.Context, gdb *gorm.DB, act *Activity) (string, error) {
	seed, err := loadSeedForReveal(ctx, gdb, act.Id)
	if err != nil {
		return "", err
	}
	snapshot := *act
	snapshot.Algo = publishAlgo(act)
	return CommitHashFor(&snapshot, nil, seed), nil
}

// publishAlgo 决定这一场用哪个协议版本发布。
//
// 草稿行上写的是什么就是什么:本轮之前建的草稿仍是 lot-v1,发布后它的历史公正
// 查询与当年完全一致;本轮之后建的草稿是 lot-v2。**绝不按"当前最新版本"发布**
// —— 那会让一份躺了两天的草稿在发布瞬间换掉原像形状。
// 空串是本轮之前更早的存量草稿,按 lot-v1 处理;其余值原样返回,由
// checkAlgoPublishable 判定认不认。**绝不把一个不认识的版本号静默降级成 v1**
// —— 那会用 v1 的原像去覆盖一份按别的版本算好的 spec_hash,而错位只有
// 用户手里的验证脚本会发现。
func publishAlgo(act *Activity) string {
	if act.Algo == "" {
		return AlgoV1
	}
	return act.Algo
}

// checkAlgoPublishable 是发布闸门:验证脚本没跟上就不许发布。
//
// 公正性承诺一旦在用户侧不可执行,它就等于不存在。一个还没有对应验证器分支的
// 协议版本被发布出去,用户拿到的是一份**没人能验**的证据链 ——
// 那比不做这个功能更糟,因为它看起来像是可验证的。
func checkAlgoPublishable(act *Activity) error {
	switch publishAlgo(act) {
	case AlgoV1:
		return nil
	case AlgoV2:
		if act.Kind == KindDraw {
			// 白名单而不是黑名单:rank / prob / ball 三个分支在
			// qianye/docs/lottery-verify.py 里都有对应的复算实现,并各有一组
			// 黄金向量。再加一种玩法时,**先补验证脚本再放开这里** ——
			// 默认放行的写法会让某一天新加的玩法在没人注意的情况下发出去,
			// 而它的证据链没有任何人能验。
			switch act.DrawMode {
			case "", DrawModeRank, DrawModeProb, DrawModeBall:
			default:
				return errBadRequest("该定档方式尚未开放发布:离线验证脚本的对应分支还没有合入")
			}
			if act.DrawMode == DrawModeBall && act.SeriesId == 0 {
				// 双色球必须挂在一个期次系列上:号池、投注入池比例与**累计发行上限**
				// 全都在系列行上,没有它就没有任何东西封顶平台的净增发。
				return errBadRequest("双色球活动必须绑定一个期次系列")
			}
		}
		return nil
	}
	return errBadRequest("未知的公正性协议版本,拒绝发布")
}

// ─────────────────────────── 取消 ───────────────────────────

type cancelRequest struct {
	Reason string `json:"reason"`
}

// handleCancelActivity 整场取消。
//
// 这是管理员唯一能改变活动结局的动作,而它必然全额退款、必然公示、必然写审计:
// 他只能"不开",不能"挑一个开"。
//
// 取消不直接登记退款:退款由参与单的**资金单终态**驱动(见 lifecycle.go)。
// 在这里投机性地登记退款,会在资金单最终判定为 Failed(主库根本没扣钱)时
// 退一笔从没收过的钱。
func handleCancelActivity(c *gin.Context) {
	// 用 FlagCore 而不是 FlagLottery:功能被临时关停时,管理员仍然必须能把
	// 进行中的活动收尾并退款 —— 那正是最需要取消的时刻。
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	actNo := c.Param("act_no")
	var req cancelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.activity.cancel", actNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 200 {
		writeAdminAudit(c, "lottery.activity.cancel", actNo, qymodel.ResultFail, "未填写取消原因", "", "")
		respondErr(c, errBadRequest("必须填写取消原因,它会对参与者公示"))
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
		writeAdminAudit(c, "lottery.activity.cancel", actNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}

	// 草稿不走取消。理由写在 errCancelDraft 上:它对草稿没有任何止损作用
	// (草稿上不可能有参与、出款或要退的钱),却会把一份从没公布过的活动推进
	// 「已结束」的公开大厅、并把它的随机种子经匿名证据链下发出去,同时让本轮
	// 新做的零仪式草稿删除对它失效。取消曾经是草稿唯一的处置路径,现在不是了。
	if act.Status == StatusDraft {
		writeAdminAudit(c, "lottery.activity.cancel", actNo, qymodel.ResultFail,
			auditReason(errCancelDraft), snapText(activitySnapshot(act, nil)), "")
		respondErr(c, errCancelDraft)
		return
	}

	now := common.GetTimestamp()
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 只有还没进入结算的活动能被取消。settling 之后已经有 payout 计划行,
		// 再叠一层取消会让同一张票同时挂着派奖与退款两条计划。
		//
		// 名单里**没有 StatusDraft**:上面那道前置已经把草稿挡掉,这里再列一次
		// 就等于留了一条"读出来是 published、加锁时已经被别人退回草稿"的旁路。
		res := tx.Model(&Activity{}).
			Where("id = ? AND status IN (?)", act.Id, []string{StatusPublished, StatusLocked}).
			Updates(map[string]any{
				"status":        StatusSettling,
				"outcome":       OutcomeCancelled,
				"cancel_reason": audit.Truncate(reason, 255),
				"updated_at":    now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errStatusConflict
		}
		// 取消可以从 published 直接跳到 settling,于是它绕过了封盘 ——
		// 而封盘是**唯一**把在途参与标成 excluded 的地方。不在这里补一次同样的
		// 清扫,那些 pending 条目就永远没人收敛:convergeExcluded 只看 excluded,
		// finishIfDone 又把 pending 计入未结算,活动会永久停在 settling。
		if _, err := excludePendingEntries(tx, act.Id, now); err != nil {
			return err
		}
		// 封盘同样是**唯一**冻结名单快照的地方,取消绕过它的后果与上面那条对称:
		// roster_hash 留空,而任何第三方按公开条目重算出来的都是一个非空哈希 ——
		// 于是每一场被取消的活动在自家验证脚本第 3 步就判 FAIL。
		hash, count, err := freezeRosterOnCancel(ctx, tx, act, now)
		if err != nil {
			return err
		}
		return writeActivityEvent(tx, act.Id, act.Status, StatusSettling, ActionCancel,
			qymodel.ActorAdmin, c.GetInt("id"), map[string]any{
				"reason": reason, "roster_hash": hash, "roster_count": count,
			})
	})
	if err != nil {
		if !errors.Is(err, errStatusConflict) {
			db.MarkFailure(err)
			err = wrapInternal("取消活动", err)
		}
		writeAdminAudit(c, "lottery.activity.cancel", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}

	writeAdminAudit(c, "lottery.activity.cancel", actNo, qymodel.ResultOK, reason,
		snapText(activitySnapshot(act, nil)),
		snapText(map[string]any{"status": StatusSettling, "outcome": OutcomeCancelled}))
	respondOK(c, gin.H{"act_no": actNo, "status": StatusSettling, "outcome": OutcomeCancelled})
}

// freezeRosterOnCancel 在整场取消时补上封盘那一步没走到的名单冻结。
//
// 取消可以从 published 直接跳到 settling,而 lockActivity 是**唯一**写
// roster_hash 的地方。少了这一步,活动行上的 roster_hash 永远是空串,而
// 证据链下发的条目仍然完整 —— 任何第三方(包括本仓自带的 lottery-verify.py)
// 按公开条目重算出的都是一个非空哈希,对不上就直接停在第 3 步,连"本应中奖
// 名单"那段判断"管理员是不是看了结果才决定不开"的材料都算不出来。
// 一个参与者都没有的场次同样会 FAIL:空名单的哈希是 H(域, act_no, commit, "0", "")
// 而不是空串。
//
// 与封盘同一条纪律:必须在 pending→excluded 清扫**之后**、同一个事务内读名单。
// 已经封过盘的活动(roster_hash 非空)绝不覆盖 —— 那份快照可能早已被人抓走。
// 草稿没有承诺哈希,名单原像无从谈起,跳过。
func freezeRosterOnCancel(ctx context.Context, tx *gorm.DB, act *Activity, now int64) (string, int, error) {
	// 判据取事务内回读的那一行,不是进 handler 时读到的那一份:上面那次状态 CAS
	// 已经在活动行上取得了锁,而并发的封盘任务恰好可能在这两次读之间落盘。
	var cur Activity
	if err := tx.Where("id = ?", act.Id).Take(&cur).Error; err != nil {
		return "", 0, err
	}
	if cur.CommitHash == "" || cur.RosterHash != "" {
		return cur.RosterHash, cur.RosterCount, nil
	}
	roster, err := loadRoster(ctx, tx, act.Id)
	if err != nil {
		return "", 0, err
	}
	hash, count := RosterHashFor(cur.Algo, cur.ActNo, cur.CommitHash, rosterLines(roster))
	err = tx.Model(&Activity{}).Where("id = ?", act.Id).Updates(map[string]any{
		"roster_hash":  hash,
		"roster_count": count,
		"updated_at":   now,
	}).Error
	if err != nil {
		return "", 0, err
	}
	return hash, count, nil
}

// ─────────────────────────── 竞猜结果 ───────────────────────────

type guessResultRequest struct {
	OptNo int `json:"opt_no"`
	// Evidence 是必填的外部依据(URL/文本/哈希),对用户公开。
	Evidence string `json:"evidence"`
}

// handleSetGuessResult 录入竞猜结果。
//
// # 这是全模块最大的信任缺口,必须诚实地写出来
//
// 竞猜的公正性问题不是随机数,是"winning_option 是不是真按外部事实定的"。
// 任何密码学都证不了世界杯谁赢了。能做的只有三条:
//
//	① 选项集合与费率在 publish 进承诺,事后加选项/改费率会自证不一致;
//	② 结果录入强制附 evidence 并对用户公开;
//	③ 结果一经写入不可修改(CAS locked→settling),录错只能整场作废 +
//	   全额退款 + 公示。
//
// 这把作弊面从"悄悄地反复调整"压缩到"一次性地公开撒谎"。原样写在活动规则页上。
func handleSetGuessResult(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	actNo := c.Param("act_no")
	var req guessResultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	evidence := strings.TrimSpace(req.Evidence)
	if evidence == "" || utf8.RuneCountInString(evidence) > 400 {
		writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultFail, "未填写结果依据", "", "")
		respondErr(c, errBadRequest("必须填写结果依据,它会对全部参与者公开"))
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
		writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if act.Kind != KindGuess {
		writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultFail, "该活动不是竞猜", "", "")
		respondErr(c, errBadRequest("该活动不是竞猜"))
		return
	}
	if act.Status != StatusLocked {
		writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultFail,
			"活动尚未封盘或已结算", snapText(activitySnapshot(act, nil)), "")
		respondErr(c, errStatusConflict)
		return
	}
	if act.WinOptionId != 0 {
		writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultFail, "结果已录入", "", "")
		respondErr(c, errResultLocked)
		return
	}

	var opt Option
	if err := gdb.WithContext(ctx).
		Where("act_id = ? AND opt_no = ?", act.Id, req.OptNo).Take(&opt).Error; err != nil {
		writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultFail, "选项不存在", "", "")
		respondErr(c, errBadOption)
		return
	}

	// 真正的分配计算与 payout 计划落在结算路径里(单个扩展库事务,一分钱不动)。
	// 这里只负责把"结果是什么"钉死。
	if err := settleGuessResult(ctx, act, &opt, evidence, c.GetInt("id")); err != nil {
		writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultFail, auditReason(err),
			snapText(activitySnapshot(act, nil)), "")
		respondErr(c, err)
		return
	}

	writeAdminAudit(c, "lottery.guess.result", actNo, qymodel.ResultOK, evidence,
		snapText(activitySnapshot(act, nil)),
		snapText(map[string]any{"win_opt_no": opt.OptNo, "label": opt.Label, "evidence": evidence}))
	respondOK(c, gin.H{"act_no": actNo, "win_opt_no": opt.OptNo, "status": StatusSettling})
}

// ─────────────────────────── 重试出款 ───────────────────────────

// handleRetryPayout 把一笔卡住的出款重新放回队列。
//
// **绝不新建 payout 行**:那会让同一张票挂上两笔出款,而 uk(act_id, entry_id,
// kind) 正是为了让这件事在结构上不可能发生。这里只做两件事:
// held → planned,以及把 next_attempt_at 归零。
func handleRetryPayout(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	payoutNo := c.Param("payout_no")
	if payoutNo == "" {
		writeAdminAudit(c, "lottery.payout.retry", "", qymodel.ResultFail, "缺少出款单号", "", "")
		respondErr(c, errBadRequest("缺少出款单号"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	var p Payout
	if err := gdb.WithContext(ctx).Where("payout_no = ?", payoutNo).Take(&p).Error; err != nil {
		writeAdminAudit(c, "lottery.payout.retry", payoutNo, qymodel.ResultFail, "出款记录不存在", "", "")
		respondErr(c, errPayoutNotFound)
		return
	}
	// 路由是 /lottery/activities/:act_no/payouts/:payout_no/retry,而这个 handler
	// 原先一次都不读 :act_no —— URL 里那段作用域是装饰品。实测用一个纯属虚构的
	// act_no 去 retry 别的活动下的出款,整条写路径跑完并真的划了额度。
	// 后果不是越权(本模块管理端对所有活动一视同仁),而是:一次拼错/复制错
	// act_no 的写调用不会被拒绝反而会成功;任何按 URL 路径段做作用域限制的外部
	// 手段(反代规则、按活动分派的脚本)被静默绕过;错误语义也失真(拿到的是
	// 409 状态冲突而不是 404)。
	if actNo := c.Param("act_no"); actNo != "" {
		var act Activity
		if err := gdb.WithContext(ctx).Where("id = ?", p.ActId).Take(&act).Error; err != nil || act.ActNo != actNo {
			writeAdminAudit(c, "lottery.payout.retry", payoutNo, qymodel.ResultFail, "出款不属于该活动", "", "")
			respondErr(c, errPayoutNotFound)
			return
		}
	}
	if p.Status == PayoutPaid {
		writeAdminAudit(c, "lottery.payout.retry", payoutNo, qymodel.ResultFail, "该笔已到账", "", "")
		respondErr(c, errStatusConflict)
		return
	}

	if err := RetryPayout(ctx, payoutNo); err != nil {
		writeAdminAudit(c, "lottery.payout.retry", payoutNo, qymodel.ResultFail, auditReason(err),
			snapText(payoutSnapshot(&p)), "")
		respondErr(c, err)
		return
	}

	// 回读真实终态,绝不假定是 planned。RetryPayout 有一条分支(payout.go 里
	// "该幂等键的资金单已经是 success")只调 markPayoutPaid 就返回 —— 那一笔
	// 的真实终态是 paid,从未回到 planned。写死 planned 会在审计表里留下一次
	// 从未发生的状态迁移,排障时让人以为一笔已到账的钱正在排队重发。
	after := Payout{Status: PayoutPlanned}
	if err := db.Get().WithContext(ctx).Where("payout_no = ?", payoutNo).Take(&after).Error; err != nil {
		db.MarkFailure(err)
	}
	writeAdminAudit(c, "lottery.payout.retry", payoutNo, qymodel.ResultOK, "",
		snapText(payoutSnapshot(&p)),
		snapText(map[string]any{"status": after.Status, "next_attempt_at": after.NextAttemptAt}))
	respondOK(c, gin.H{"payout_no": payoutNo, "status": after.Status})
}

// ─────────────────────────── 只读列表 ───────────────────────────

// handleAdminListActivities 返回活动列表。
//
// 用 FlagCore 而不是 FlagLottery:功能被临时关停时,管理员仍然必须能查历史账。
func handleAdminListActivities(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb = gdb.WithContext(c.Request.Context())

	q := gdb.Model(&Activity{})
	if v := c.Query("kind"); v != "" {
		q = q.Where("kind = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("act_no"); v != "" {
		q = q.Where("act_no = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计活动", err))
		return
	}
	// 下发给前端的数组一律显式初始化,理由见 qianye/json_array_guard_test.go。
	rows := make([]Activity, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询活动", err))
		return
	}

	// 未解决的异常条数一次分组查出来。列表页的红点直读它 —— 逐行各查一次
	// 就是一页 20 次往返,而这一页恰恰是运营每天都要刷的那一页。
	flagCounts := make(map[int64]int, len(rows))
	if len(rows) > 0 {
		ids := make([]int64, 0, len(rows))
		for _, a := range rows {
			ids = append(ids, a.Id)
		}
		counts := make([]struct {
			ActId int64
			Cnt   int
		}, 0, len(ids))
		err := gdb.Model(&Flag{}).Select("act_id, COUNT(*) AS cnt").
			Where("act_id IN (?) AND resolved = ?", ids, false).
			Group("act_id").Scan(&counts).Error
		if err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("统计对账异常", err))
			return
		}
		for _, r := range counts {
			flagCounts[r.ActId] = r.Cnt
		}
	}

	items := make([]adminActivityBrief, 0, len(rows))
	for _, a := range rows {
		items = append(items, adminActivityBrief{
			ActNo: a.ActNo, Kind: a.Kind, Status: a.Status, Outcome: a.Outcome,
			Title: a.Title, StakeQuota: a.StakeQuota,
			OpenAt: a.OpenAt, CloseAt: a.CloseAt, DrawAt: a.DrawAt,
			ActiveCount: a.ActiveCount, PoolQuota: a.PoolQuota,
			PayoutQuota: a.PayoutQuota, RefundQuota: a.RefundQuota,
			PlatformFeeQuota: a.PlatformFeeQuota,
			DrawMode:         a.DrawMode,
			IssueNo:          a.IssueNo,
			OpenFlagCount:    flagCounts[a.Id],
			CreatedAt:        a.CreatedAt,
			HiddenAt:         a.HiddenAt,
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// adminActivityBrief 是管理端列表行。
//
// 显式 DTO 而不是整行 Activity:列表用不上 rules_text / spec_text,几十行加起来
// 就是几百 KB;更要紧的是"红点"需要一个活动行上没有的量(未解决异常数),
// 而整行返回的形状根本放不下它。
type adminActivityBrief struct {
	ActNo   string `json:"act_no"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Outcome string `json:"outcome"`
	Title   string `json:"title"`

	StakeQuota int64 `json:"stake_quota"`
	OpenAt     int64 `json:"open_at"`
	CloseAt    int64 `json:"close_at"`
	DrawAt     int64 `json:"draw_at"`

	ActiveCount      int   `json:"active_count"`
	PoolQuota        int64 `json:"pool_quota"`
	PayoutQuota      int64 `json:"payout_quota"`
	RefundQuota      int64 `json:"refund_quota"`
	PlatformFeeQuota int64 `json:"platform_fee_quota"`

	// DrawMode / IssueNo 让列表能把双色球与普通抽奖分开。两者的 kind 都是 draw,
	// 不给这两位的话运营在列表上只能靠标题猜,而双色球那一行的 pool_quota
	// (本期投注额)与它真正的奖池根本不是一回事。
	DrawMode string `json:"draw_mode"`
	IssueNo  int    `json:"issue_no"`

	OpenFlagCount int   `json:"open_flag_count"`
	CreatedAt     int64 `json:"created_at"`

	// HiddenAt > 0 = 这一场已从用户端大厅撤下。它必须出现在**列表**上:
	// 下架之后这一行在管理端看起来与在架的完全一样,运营会反复问
	// "到底关掉了没有",然后再关一次。
	HiddenAt int64 `json:"hidden_at"`
}

// handleAdminGetActivity 返回单个活动的完整视图(含奖档/选项与收支)。
func handleAdminGetActivity(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	prizes := make([]Prize, 0, 8)
	if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).Order("tier asc").Find(&prizes).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询奖档", err))
		return
	}
	options := make([]Option, 0, 8)
	if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).Order("opt_no asc").Find(&options).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询选项", err))
		return
	}
	// 转人工的那部分钱必须单独摆出来。payout_quota / refund_quota 在收尾时
	// 只聚合 paid,而 held 同时被当作终态放行 —— 不显式给出 held 的合计,
	// 一笔"平台还欠着、只是发不出去"的钱就会从"本场收支"里彻底消失,
	// 运营据此判断的亏损额比真实值小。
	var held int64
	if err := gdb.WithContext(ctx).Model(&Payout{}).
		Select("COALESCE(SUM(amount_quota), 0)").
		Where("act_id = ? AND status = ?", act.Id, PayoutHeld).
		Scan(&held).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计待人工出款", err))
		return
	}

	total := prizeTotalRows(prizes)
	respondOK(c, gin.H{
		"activity": act,
		"prizes":   prizes,
		"options":  options,
		"economics": gin.H{
			"prize_total_quota":  total,
			"break_even_entries": breakEven(total, act.StakeQuota),
			"income_quota":       act.PoolQuota,
			"payout_quota":       act.PayoutQuota,
			"refund_quota":       act.RefundQuota,
			"platform_fee_quota": act.PlatformFeeQuota,
			"held_quota":         held,
			"net_quota":          activityNetQuota(act, held),
		},
	})
}

// activityNetQuota 是「本场收支」的净值:收进来的参与费 − 发出去的奖 − 退回去的
// 钱 − 还欠着没发出去的那部分。可以是负数 —— 平台出奖品是净增发,两边不守恒是
// 正常的,所以前端带符号显示。
//
// platform_fee_quota **不进这个式子**。它是从 pool 里切出来的那一块,不是池子之外
// 的第二笔收入:竞猜在 SplitPool 结尾断言 Σpay + fee == pool(commit.go),双色球的
// fee = PoolQuota − ballPoolIn(lifecycle.go),两条路都是 pool 的真子集,而
// income_quota 就是 pool。原先的式子把它又加了一遍 —— 竞猜恒有 pool = payout + fee,
// 于是真实净值(恰等于 fee)被显示成 2×fee,误差 100%;亏损场则被少报一个 fee 的
// 亏损,一场明确在亏的期次看起来没那么亏。rank/prob 的 fee 恒为 0,所以这个错误只在
// 有手续费的玩法上显形,一直没被发现。管理端列表页(admin-lottery/index.tsx)算的
// 一直是 pool − payout − refund,与这里修正后的口径一致;修之前同一场活动在列表页
// 和详情页会显示两个不同的「净值」。
func activityNetQuota(act *Activity, held int64) int64 {
	return act.PoolQuota - act.PayoutQuota - act.RefundQuota - held
}

// handleAdminListEntries 返回参与明细。
//
// Entry 上的 idem_key / eligibility_snapshot / ip_hash / ua_hash 都带 json:"-",
// 因此整行下发不会泄漏这些字段。
func handleAdminListEntries(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := gdb.WithContext(ctx).Model(&Entry{}).Where("act_id = ?", act.Id)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := httpq.Int(c, "user_id", 0); v > 0 {
		q = q.Where("user_id = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计参与明细", err))
		return
	}
	rows := make([]Entry, 0, size)
	if err := q.Order("seq asc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询参与明细", err))
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size})
}

// handleAdminListPayouts 返回出款明细与按状态分桶的进度。
func handleAdminListPayouts(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := gdb.WithContext(ctx).Model(&Payout{}).Where("act_id = ?", act.Id)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计出款", err))
		return
	}
	rows := make([]Payout, 0, size)
	if err := q.Order("id asc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询出款", err))
		return
	}

	// 空结果集时 Scan 到 nil 切片会序列化成 JSON null,前端 .find() 直接崩。
	buckets := make([]struct {
		Status string `json:"status"`
		Cnt    int64  `json:"cnt"`
		Amount int64  `json:"amount"`
	}, 0, 5)
	err = gdb.WithContext(ctx).Model(&Payout{}).
		Select("status, COUNT(*) AS cnt, COALESCE(SUM(amount_quota), 0) AS amount").
		Where("act_id = ?", act.Id).Group("status").Scan(&buckets).Error
	if err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计出款分桶", err))
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size, "buckets": buckets})
}

// handleAdminListEvents 返回活动的事件流。它对用户也可见,属于证据链。
func handleAdminListEvents(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	rows := make([]Event, 0, 32)
	if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).
		Order("id asc").Limit(500).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询事件流", err))
		return
	}
	// total 与别处的列表口径保持一致,即使这里不分页:前端的分页组件对
	// "没有 total 的列表"会退化成不显示条数,而事件流恰恰是要一眼看出
	// "这场活动一共发生过几件事"。
	respondOK(c, gin.H{"items": rows, "total": len(rows)})
}

// handleAdminListFlags 返回对账任务发现的异常,管理端红点直读。
func handleAdminListFlags(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb = gdb.WithContext(c.Request.Context())

	q := gdb.Model(&Flag{})
	// 默认只看未解决的:异常列表是给红点用的,把历史一起翻出来会让真正要处理的
	// 那几条淹掉。显式传 ?resolved= 才看全部。
	if c.Query("resolved") == "" {
		q = q.Where("resolved = ?", false)
	}
	if v := c.Query("act_no"); v != "" {
		var act Activity
		if err := gdb.Model(&Activity{}).Select("id").
			Where("act_no = ?", v).Take(&act).Error; err == nil {
			q = q.Where("act_id = ?", act.Id)
		}
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计异常", err))
		return
	}
	rows := make([]Flag, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询异常", err))
		return
	}
	respondOK(c, gin.H{"items": rows, "total": total, "p": page, "page_size": size})
}

// resolveFlagRequest 是标记异常已处理的请求体。
type resolveFlagRequest struct {
	Note string `json:"note"`
}

// handleAdminResolveFlag 把一条对账异常标记为已处理。
//
// 没有它,qy_lot_flag 是一张只写不改的表:raiseFlag 按 (act_id, code, resolved=false)
// 去重,所以第一条落下之后,**这场活动这一类的检出就永久哑火**。对 published/locked
// 的活动还能靠活动自己走完生命周期来收场;对 finished 就是永久的 —— 而
// auditFinishedChains 恰恰把 finished 也纳入了持续复核,历史公正查询的全部内容
// 都在那里。运营在产品里必须能关掉一条已经处理完的异常,否则只能去改库。
func handleAdminResolveFlag(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	// 路径 ID 走 httpq:上界是解析的一部分,strconv.Atoi 的上界是 MaxInt64。
	id, ok := httpq.PathInt64(c, "id")
	if !ok {
		respondErr(c, errBadRequest("异常编号不合法"))
		return
	}
	var req resolveFlagRequest
	// 备注可选:请求体允许为空。
	_ = c.ShouldBindJSON(&req)
	note := strings.TrimSpace(req.Note)
	if err := rejectControlChars(note, "处理备注"); err != nil {
		respondErr(c, err)
		return
	}

	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	adminId := c.GetInt("id")
	traceNo := "qy_lot_flag:" + strconv.FormatInt(id, 10)

	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var flag Flag
		if e := tx.Where("id = ?", id).Take(&flag).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return errFlagNotFound
			}
			return e
		}
		// resolved 上的 CAS 就是这里唯一需要的并发保障,不必再加行锁:两个管理员
		// 同时点,只有一个的 UPDATE 命中 1 行,另一个拿到"已处理",
		// resolved_by 不会被后点的那个人覆盖。
		res := tx.Model(&Flag{}).
			Where("id = ? AND resolved = ?", id, false).
			Updates(map[string]any{
				"resolved":    true,
				"resolved_by": adminId,
				"resolved_at": common.GetTimestamp(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errFlagAlreadyResolved
		}
		return audit.WriteTx(tx, audit.Entry{
			TraceNo:     traceNo,
			Category:    auditCategory,
			Action:      "lottery.flag.resolve",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: adminId,
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			Reason:      note,
		})
	})
	if err != nil {
		writeAdminAudit(c, "lottery.flag.resolve", traceNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"id": id, "resolved": true})
}

// ─────────────────────────── 构造与校验 ───────────────────────────

// maxScheduleHorizonSeconds 是四个时刻距当前时间的最大跨度(366 天)。
//
// 不新增 YAML 键:这不是一个需要按部署调的运营参数,而是"活动必须在可预见的
// 时间内收场"这条不变量的兜底。真要办一年后的活动,到时候再建草稿。
const maxScheduleHorizonSeconds int64 = 366 * 86400

// rejectControlChars 拒绝任何控制字符。
//
// # 为什么是拒绝而不是过滤,以及为什么它是资金安全的一部分
//
// 奖档名称、文本奖履行说明、竞猜选项文案会被 SEP(0x1F)拼成 spec 原像
// (PrizeSpecLineV2 / OptionSpecLine),再哈希成 spec_hash 进 commit_hash。
// 这套编码的全部安全性建立在 commit.go 顶部那句断言上:「SEP 不会出现在业务
// 串里」。运营只要在奖档名里塞一个 0x1F,两张**结构不同**的奖档表就能拼出
// 逐字节相同的 spec_text —— 于是发布之后可以直接换掉 qy_lot_prize 整表,而
// checkSpecIntegrity 的 spec_hash 与 spec_text 两条比对**双双通过**、承诺哈希
// 复算通过、一条 flag 都不落、连仓库自带的验证脚本都会输出「全部通过」。
// 实测:注入档发布时的承诺支出上界是 5000,换表后实发 6000,内外零告警。
//
// 过滤(照 transfer 的 sanitizeRemark 静默剔除)在这里是错的:运营看到的字符串
// 与落库的不是同一个,而落库的那个才进承诺。宁可 400。
//
// 顺带把 title 也纳进来:它随匿名证据链下发,而 lottery-verify.py 会把它原样
// print 到终端 —— 带 ANSI 转义的标题可以改写验证器自己的输出。
func rejectControlChars(field, s string) error {
	for _, r := range s {
		if unicode.IsControl(r) {
			return errBadRequest(field + "不能包含控制字符")
		}
	}
	return nil
}

// rejectControlCharsAllowingBreaks 用于多行文本:换行与制表符是正当排版,
// 其余控制字符一律拒绝。这类字段不进任何哈希原像,只怕日志与终端注入。
func rejectControlCharsAllowingBreaks(field, s string) error {
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return errBadRequest(field + "不能包含控制字符")
		}
	}
	return nil
}

// buildActivity 把管理端提交的内容翻译成待落库的活动、奖档与选项。
//
// 这里做的是**全部**业务校验:发布时只再核对一次时刻与活动数上限,
// 因为草稿可能躺了两天。校验分散到两处会立刻长出两套口径。
func buildActivity(ctx context.Context, in *activityInput, createdBy int) (*Activity, []Prize, []Option, error) {
	cfg := config.Get().Lottery
	set := effectiveCtx(ctx)

	if in.Kind != KindDraw && in.Kind != KindGuess {
		return nil, nil, nil, errBadRequest("活动类型只能是 draw 或 guess")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || utf8.RuneCountInString(title) > 60 {
		return nil, nil, nil, errBadRequest("活动标题必填且不超过 60 个字")
	}
	if err := rejectControlChars("活动标题", title); err != nil {
		return nil, nil, nil, err
	}
	if utf8.RuneCountInString(in.Intro) > 2000 {
		return nil, nil, nil, errBadRequest("活动说明不超过 2000 个字")
	}
	if err := rejectControlCharsAllowingBreaks("活动说明", in.Intro); err != nil {
		return nil, nil, nil, err
	}
	// 封面在这里只做**形状**校验(协议、长度、互斥);"这个 ref 是不是我传的、
	// 是不是还没被别的活动认领"要在事务里用一条带条件的 UPDATE 回答,
	// 先查后写在并发下会让同一张图被两场活动同时认领。
	coverURL, coverRef, err := resolveCoverInput(coverInput{
		CoverUrl: in.CoverUrl, CoverRef: in.CoverRef,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	// 免费场在 v1 明确不做:twophase 的入口强制 0 < amount ≤ MaxQuota。
	if in.StakeQuota <= 0 || in.StakeQuota > cfg.MaxStakeQuota ||
		in.StakeQuota > int64(common.MaxQuota) {
		return nil, nil, nil, errBadRequest(fmt.Sprintf("参与费必须落在 (0, %d]", cfg.MaxStakeQuota))
	}

	rules := in.Rules.Normalize()
	if err := checkSpendWindow(rules, cfg); err != nil {
		return nil, nil, nil, err
	}
	if rules.MaxTotalEntries < 0 || rules.MaxTotalEntries > cfg.MaxTotalEntriesHard {
		return nil, nil, nil, errBadRequest(fmt.Sprintf("全场参与上限不得超过 %d", cfg.MaxTotalEntriesHard))
	}
	// 每人的两个上限必须落在锁内那次读取的窗口之内(checkCaps 一次读回本人最近
	// 的 perUserCapHard 条)。配一个比窗口还大的上限,它会在用户攒够那么多条
	// 之后**静默失效** —— 界面上写着"每人 1000 次",实际执行的是"无上限"。
	if rules.MaxEntriesPerUser > perUserCapHard || rules.MaxAttemptsPerUser > perUserCapHard {
		return nil, nil, nil, errBadRequest(fmt.Sprintf("每人参与/尝试上限不得超过 %d", perUserCapHard))
	}
	if rules.MaxTotalEntries == 0 {
		// 名单冻结要在单个事务里流式算完,必须有上界。运营没填就用硬上限,
		// 而不是"不限制"。
		rules.MaxTotalEntries = cfg.MaxTotalEntriesHard
	}
	rulesText, err := rules.CanonicalText()
	if err != nil {
		return nil, nil, nil, wrapInternal("序列化参与条件", err)
	}

	act := &Activity{
		ActNo:              newActNo(),
		Kind:               in.Kind,
		Status:             StatusDraft,
		Outcome:            OutcomeNone,
		Title:              title,
		Intro:              in.Intro,
		CoverUrl:           coverURL,
		CoverRef:           coverRef,
		StakeQuota:         in.StakeQuota,
		OpenAt:             in.OpenAt,
		CloseAt:            in.CloseAt,
		DrawAt:             in.DrawAt,
		SettleDeadline:     in.SettleDeadline,
		RulesText:          rulesText,
		RulesHash:          RulesHash(rulesText),
		Algo:               AlgoV2,
		AllowMultiWin:      in.AllowMultiWin,
		MinEntriesToHold:   in.MinEntriesToHold,
		MaxEntriesPerUser:  rules.MaxEntriesPerUser,
		MaxAttemptsPerUser: rules.MaxAttemptsPerUser,
		MaxTotalEntries:    rules.MaxTotalEntries,
		MaxTotalUsers:      rules.MaxTotalUsers,
		MaxPerInviter:      rules.MaxPerInviter,
		CooldownSeconds:    rules.CooldownSeconds,
		DedupIp:            rules.DedupIp,
		CreatedBy:          createdBy,
		CreatedAt:          common.GetTimestamp(),
		UpdatedAt:          common.GetTimestamp(),
	}
	if act.MinEntriesToHold < 0 {
		return nil, nil, nil, errBadRequest("最低成场人数不能为负")
	}
	if err := validateSchedule(act, common.GetTimestamp()); err != nil {
		return nil, nil, nil, err
	}

	var (
		prizes  []Prize
		options []Option
		lines   []string
	)
	switch in.Kind {
	case KindDraw:
		if act.DrawMode, err = normalizeDrawMode(in.DrawMode); err != nil {
			return nil, nil, nil, err
		}
		// 抽奖没有手续费这回事(platform_fee_quota 只由双色球的入池比例写)。
		// 与 normalizeWinPpm 对 rank/ball 填 win_ppm 的处理同一个口径:
		// 填了却不生效是最坏的一种界面谎言,直接拒绝而不是静默丢弃。
		if in.FeeBps != nil && *in.FeeBps != 0 {
			return nil, nil, nil, errBadRequest("只有竞猜(kind=guess)才能设置手续费")
		}
		// 概率制与双色球下 allow_multi_win 强制为真(即不去重)。
		//
		// 按 user_ref 去重会保留票面最小的那张 —— 也就是最可能落进低位中奖区间
		// 的那一张 —— 使多票用户的单票概率变成 1-(1-p)^k。数学上自洽,但无法用
		// 一句话对用户讲清,也让期望支出依赖每人的票数分布,而"每张票独立、
		// 概率严格等于公示值"正是概率制的全部主张。限流改用 max_entries_per_user,
		// 零协议成本。这个字段仍然原样进 commit 原像。
		if act.DrawMode != DrawModeRank {
			act.AllowMultiWin = true
		}
		prizes, lines, err = buildPrizes(in.Prizes, cfg, set, act)
		if err == nil && act.DrawMode == DrawModeBall {
			// 双色球的奖级条件(红蓝命中数、占池比例)也在 spec 原像里,
			// 因此这一步会**整体重算**逐行,而不是往已经算好的行上补字段。
			prizes, lines, err = applyBallSpec(ctx, act, in, prizes)
		}
	case KindGuess:
		options, lines, err = buildOptions(in.Options, cfg)
		if err == nil {
			act.FeeBps, err = resolveFeeBps(in.FeeBps, set)
		}
		if err == nil {
			err = applyBetBounds(act, in, cfg)
		}
	}
	if err != nil {
		return nil, nil, nil, err
	}

	// spec_text 落库的就是参与哈希的那份字节。
	act.SpecText = strings.Join(lines, SEP)
	act.SpecHash = SpecHashFor(act.Algo, lines)
	return act, prizes, options, nil
}

// normalizeDrawMode 归一化定档方式。空串按 rank 处理,让存量前端与脚本不必改。
func normalizeDrawMode(in string) (string, error) {
	switch strings.TrimSpace(in) {
	case "", DrawModeRank:
		return DrawModeRank, nil
	case DrawModeProb:
		return DrawModeProb, nil
	case DrawModeBall:
		return DrawModeBall, nil
	}
	return "", errBadRequest("定档方式只能是 rank、prob 或 ball")
}

// buildPrizes 校验奖档并生成 spec 原像的逐行。
//
// Σ(amount × count) ≤ max_total_prize_quota 是**唯一**能拦住"奖品金额多写一个
// 零"的闸门:抽奖是平台收参与费、平台出奖品,派奖对用户额度是净增发,
// 下游没有任何环节会因为金额过大而失败。
// 文本奖(prize_type=text)不占用任何额度,因此它**完全不参与**
// Σ(count × amount) ≤ max_total_prize_quota 这道闸门 —— amount 恒为 0。
// 它的成本闸门是另一件事:最坏履行份数,由 worstCaseTextGrants 摆到发布按钮上面。
func buildPrizes(in []prizeInput, cfg config.Lottery, set opSettings, act *Activity) ([]Prize, []string, error) {
	if len(in) == 0 || len(in) > cfg.MaxPrizeTiers {
		return nil, nil, errBadRequest(fmt.Sprintf("奖档数量必须落在 [1, %d]", cfg.MaxPrizeTiers))
	}
	// entriesCap 是本场理论上可能出现的最大有效票数。buildActivity 已经把
	// max_total_entries 归一化成"没填就用硬上限",所以它一定是正数。
	entriesCap := act.MaxTotalEntries
	if entriesCap <= 0 {
		entriesCap = cfg.MaxTotalEntriesHard
	}

	rows := make([]Prize, 0, len(in))
	seen := make(map[int]bool, len(in))
	var total int64
	var ppmSum int64
	for _, p := range in {
		name := strings.TrimSpace(p.Name)
		if p.Tier <= 0 || seen[p.Tier] {
			return nil, nil, errBadRequest("奖档等级必须是互不相同的正整数")
		}
		seen[p.Tier] = true
		if name == "" || utf8.RuneCountInString(name) > 40 {
			return nil, nil, errBadRequest("奖档名称必填且不超过 40 个字")
		}
		// 奖档名进 spec 原像,控制字符会让两张不同的奖档表撞出同一个 spec_hash。
		if err := rejectControlChars("奖档名称", name); err != nil {
			return nil, nil, err
		}
		if p.Count <= 0 || p.Count > cfg.MaxTotalEntriesHard {
			return nil, nil, errBadRequest("奖品数量必须大于 0")
		}

		prizeType, textDesc, err := normalizePrizeType(p)
		if err != nil {
			return nil, nil, err
		}
		// 双色球的浮动奖(占池比例 > 0)是额度奖的一种,但它的额度**必须为 0**:
		// 那一档发多少由期次池现算,写死一个数与占池比例互斥(checkBallTierInput)。
		// 因此这里必须先认出它,否则下面那条 `amount > 0` 会把
		// 「一等奖 = 池子的 X%」这个双色球的核心玩法在结构上创建不出来。
		//
		// 它的支出上界不由 max_total_prize_quota 管,而由期次池封顶:
		// checkBallPoolCovers 在发布期证明 fixed + open×Σshare/10000 ≤ open。
		// 把一个恒为 0 的额度累加进 Σ(amount×count) 只会让那道闸门形同虚设地通过,
		// 所以浮动奖**完全不参与**这条累加,与文本奖同一个理由。
		floatingBallTier := act.DrawMode == DrawModeBall && p.PoolShareBps > 0
		if prizeType == PrizeTypeQuota && !floatingBallTier {
			if p.AmountQuota <= 0 || p.AmountQuota > int64(common.MaxQuota) {
				return nil, nil, errBadRequest("奖品额度必须大于 0 且不超过系统上限")
			}
			// 先判单档再累加:两个各自合法的档相乘也可能溢出。
			if p.AmountQuota > set.MaxTotalPrizeQuota/int64(p.Count) {
				return nil, nil, errPrizeCapExceeded
			}
			total += p.AmountQuota * int64(p.Count)
			if total > set.MaxTotalPrizeQuota {
				return nil, nil, errPrizeCapExceeded
			}
		}
		if floatingBallTier && p.AmountQuota != 0 {
			// 与 checkBallTierInput 同一条规则,在这里也说一遍:走到 applyBallSpec
			// 之前就拒绝,报错信息才指得准是哪一档。
			return nil, nil, errBadRequest(fmt.Sprintf("奖级 %d 是浮动奖(占池比例 > 0),额度必须为 0", p.Tier))
		}

		winPpm, err := normalizeWinPpm(act.DrawMode, p, prizeType, entriesCap)
		if err != nil {
			return nil, nil, err
		}
		ppmSum += int64(winPpm)
		if ppmSum > PpmDen {
			// 超过 100% 意味着有两档的摇号区间重叠,而"一张票同时中两档"在派奖层
			// 会撞唯一键、静默丢掉第二个奖。三处校验(这里、揭示、验证脚本)
			// 全都直接拒绝,不猜。
			return nil, nil, errBadRequest(fmt.Sprintf(
				"各档中奖概率之和不得超过 100%%(当前已累计 %d ppm)", ppmSum))
		}

		rows = append(rows, Prize{
			Tier: p.Tier, Name: name, AmountQuota: p.AmountQuota, Count: p.Count,
			PrizeType: prizeType, WinPpm: winPpm, TextDesc: textDesc,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Tier < rows[j].Tier })

	if set.LargePrizeAlertQuota > 0 && total >= set.LargePrizeAlertQuota {
		// 不阻断 —— 运营确实可能办大活动。但必须喊出来:这是净增发。
		common.SysError(fmt.Sprintf(
			"qianye/lottery: 正在创建奖品总额度 %d 的抽奖(告警阈值 %d,保本参与人数 %d)—— "+
				"抽奖派奖是对用户额度的净增发,请确认这不是多写了一个零",
			total, set.LargePrizeAlertQuota, breakEven(total, act.StakeQuota)))
	}

	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, prizeSpecLineOf(act.Algo, r))
	}
	return rows, lines, nil
}

// prizeSpecLineOf 按算法版本生成奖档在 spec 原像里的一行。
func prizeSpecLineOf(algo string, r Prize) string {
	if algo != AlgoV2 {
		return prizeSpecLineV1(r.Tier, r.Name, r.AmountQuota, r.Count)
	}
	return PrizeSpecLineV2(PrizeSpec{
		Tier: r.Tier, Name: r.Name, PrizeType: r.Type(),
		AmountQuota: r.AmountQuota, Count: r.Count, WinPpm: r.WinPpm, TextDesc: r.TextDesc,
		RedMatch: r.RedMatch, BlueMatch: r.BlueMatch, PoolShareBps: r.PoolShareBps,
	})
}

// normalizePrizeType 校验"额度奖 / 文本奖"这条单选,并返回归一化后的两个字段。
//
// 不允许一档既给额度又给文本:混合会让派奖在同一行里分叉(一条腿走跨库资金单、
// 一条腿只落一行记录),要两者就配两档。
func normalizePrizeType(p prizeInput) (string, string, error) {
	desc := strings.TrimSpace(p.TextDesc)
	switch strings.TrimSpace(p.PrizeType) {
	case "", PrizeTypeQuota:
		if desc != "" {
			return "", "", errBadRequest("额度奖不能填写文本说明;要发文本奖请把奖品类型改成 text")
		}
		return PrizeTypeQuota, "", nil
	case PrizeTypeText:
		if p.AmountQuota != 0 {
			return "", "", errBadRequest("文本奖的额度必须为 0;要同时发额度请另配一个奖档")
		}
		if desc == "" || utf8.RuneCountInString(desc) > 500 {
			return "", "", errBadRequest("文本奖必须填写履行说明,且不超过 500 个字")
		}
		// 履行说明同样进 spec 原像(PrizeSpecLineV2 的第 7 位)。
		if err := rejectControlChars("文本奖履行说明", desc); err != nil {
			return "", "", err
		}
		return PrizeTypeText, desc, nil
	}
	return "", "", errBadRequest("奖品类型只能是 quota 或 text")
}

// normalizeWinPpm 校验中奖概率,并把"哪种模式允许填它"这条规则钉死在一处。
//
// prob 模式下额度档还要多守一条:`count × amount ≥ entriesCap`。
//
// 这一条堵的是均分制唯一的新失败态 —— 预算摊到人均不足 1 额度时会有人分到 0,
// 而 PlanPayouts 会跳过 amount<=0 的计划,结果是**一个真中了奖的人被静默漏发**。
// 保证每人至少分到 1 额度,守恒式就恒精确成立。
func normalizeWinPpm(drawMode string, p prizeInput, prizeType string, entriesCap int) (int, error) {
	if drawMode == DrawModeRank || drawMode == DrawModeBall {
		// rank 按名次切片、ball 按号码匹配定档,两者都不读 win_ppm。
		// 填了却不生效是最坏的一种界面谎言,所以直接拒绝而不是静默忽略。
		if p.WinPpm != 0 {
			return 0, errBadRequest("只有概率制(draw_mode=prob)的奖档才能设置中奖概率")
		}
		return 0, nil
	}
	if p.WinPpm <= 0 || p.WinPpm > PpmDen {
		return 0, errBadRequest("概率制下每一档的中奖概率必须落在 (0, 1000000] ppm")
	}
	// count 已被 MaxTotalEntriesHard 夹住、amount 已被 MaxQuota(int32)夹住,
	// 乘积最多在 1e14 量级,int64 上不会溢出。
	if prizeType == PrizeTypeQuota && p.AmountQuota*int64(p.Count) < int64(entriesCap) {
		return 0, errBadRequest(fmt.Sprintf(
			"概率制下本档预算(数量 %d × 额度 %d)必须不小于全场参与上限 %d,"+
				"否则超募时会有中奖者被摊薄到 0 额度而拿不到钱", p.Count, p.AmountQuota, entriesCap))
	}
	return p.WinPpm, nil
}

// buildOptions 校验竞猜选项并生成 spec 原像的逐行。
func buildOptions(in []optionInput, cfg config.Lottery) ([]Option, []string, error) {
	if len(in) < 2 || len(in) > cfg.MaxOptions {
		return nil, nil, errBadRequest(fmt.Sprintf("竞猜选项数量必须落在 [2, %d]", cfg.MaxOptions))
	}
	rows := make([]Option, 0, len(in))
	seen := make(map[int]bool, len(in))
	seenLabel := make(map[string]bool, len(in))
	catchAll := 0
	for _, o := range in {
		label := strings.TrimSpace(o.Label)
		if o.OptNo <= 0 || seen[o.OptNo] {
			return nil, nil, errBadRequest("选项编号必须是互不相同的正整数")
		}
		seen[o.OptNo] = true
		if label == "" || utf8.RuneCountInString(label) > 40 {
			return nil, nil, errBadRequest("选项文案必填且不超过 40 个字")
		}
		// 选项文案进 spec 原像(OptionSpecLine 的第 2 位),同奖档名的理由。
		if err := rejectControlChars("选项文案", label); err != nil {
			return nil, nil, err
		}
		// 文案不许重复。这不是洁癖:用户在界面上看到的**只有文案**,而投注落在
		// opt_no 上。两个都叫「甲队赢」的选项意味着有一半人押中了正确的结果
		// 却拿的是另一个编号,开奖时全额亏掉 —— 而他在界面上无从分辨自己点的是
		// 哪一个。前端早有这条判定(qy_lot_v_option_dup),后端一直缺,于是任何
		// 一次脚本化的创建/改草稿都能绕过它。
		if seenLabel[label] {
			return nil, nil, errBadRequest("选项文案不能重复:用户看到的只有文案,两个同名选项会让押对结果的人拿到另一个编号")
		}
		seenLabel[label] = true
		if o.IsCatchAll {
			catchAll++
		}
		rows = append(rows, Option{OptNo: o.OptNo, Label: label, IsCatchAll: o.IsCatchAll})
	}
	if catchAll > 1 {
		return nil, nil, errBadRequest("兜底项(以上都不是)最多只能有一个")
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].OptNo < rows[j].OptNo })

	if catchAll == 0 {
		// 不阻断,但要留痕:没有兜底项时"全部猜错"会频繁发生,
		// 届时全场退款、平台零收益。管理端表单要在删除兜底项时二次确认。
		common.SysError("qianye/lottery: 正在创建没有兜底项(以上都不是)的竞猜 —— " +
			"「全部猜错」会频繁发生,届时全场退款、平台零收益")
	}

	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, OptionSpecLine(r.OptNo, r.Label, r.IsCatchAll))
	}
	return rows, lines, nil
}

func resolveFeeBps(in *int, set opSettings) (int, error) {
	if in == nil {
		return set.DefaultGuessFeeBps, nil
	}
	if *in < 0 || *in > set.MaxGuessFeeBps {
		return 0, errBadRequest(fmt.Sprintf("手续费万分比必须落在 [0, %d]", set.MaxGuessFeeBps))
	}
	return *in, nil
}

// applyBetBounds 校验竞猜的单注上下限。
//
// 不设上限时一个大户可以在封盘前几秒压满获胜选项吃掉奖池,散户期望收益归零;
// 不设下限则会有 1 单位的骚扰投注刷名单。
func applyBetBounds(act *Activity, in *activityInput, cfg config.Lottery) error {
	minQ, maxQ := in.BetMinQuota, in.BetMaxQuota
	if minQ < 0 || maxQ < 0 {
		return errBadRequest("单注上下限不能为负")
	}
	if maxQ > 0 && maxQ > cfg.MaxStakeQuota {
		return errBadRequest(fmt.Sprintf("单注上限不得超过 %d", cfg.MaxStakeQuota))
	}
	if maxQ > 0 && minQ > maxQ {
		return errBadRequest("单注下限不得大于上限")
	}
	act.BetMinQuota = minQ
	act.BetMaxQuota = maxQ
	return nil
}

// validateSchedule 校验四个时刻。
//
// reveal_delay_seconds 是硬约束:名单哈希必须先于种子公开,中间要留出足够时间
// 让任何人抓到一份平台无法否认的快照。间隔为 0 等于整个协议退化成
// "平台自己说它没改"。
func validateSchedule(act *Activity, now int64) error {
	cfg := config.Get().Lottery
	if act.OpenAt <= 0 || act.CloseAt <= 0 || act.DrawAt <= 0 {
		return errBadRequest("开始、截止、开奖三个时刻都必须填写")
	}
	if act.CloseAt <= act.OpenAt {
		return errBadRequest("截止时间必须晚于开始时间")
	}
	if act.CloseAt <= now {
		return errBadRequest("截止时间必须晚于当前时间")
	}
	// 四个时刻必须有绝对上界,理由有两条,而且都不是洁癖:
	//
	//  1. 资金可用性。竞猜的 settle_deadline 是"管理员不能无限期扣着奖池不结算"
	//     的唯一兜底(runVoidExpired 到点全额退款),把它填成 2100 年就原样恢复了
	//     那个风险;抽奖更彻底 —— 它连逾期兜底都没有,close_at 在 2200 年的活动
	//     会正常收钱然后永远停在 published,不封盘、不开奖、不退款。
	//  2. 溢出旁路。下面那条 `DrawAt < CloseAt + RevealDelaySeconds` 在 close_at
	//     接近 int64 上界时会静默溢出成负数,判据恒假 —— 实测提交
	//     close_at=2^63-1 / draw_at=now+100 可以绕过它。先夹住地平线,
	//     那条加法就再也溢不出去。
	horizon := now + maxScheduleHorizonSeconds
	for _, ts := range []struct {
		name string
		at   int64
	}{{"开始时间", act.OpenAt}, {"截止时间", act.CloseAt}, {"开奖时间", act.DrawAt}, {"结算截止时间", act.SettleDeadline}} {
		if ts.at > horizon {
			return errBadRequest(fmt.Sprintf("%s不得晚于当前时间之后 %d 天", ts.name, maxScheduleHorizonSeconds/86400))
		}
	}
	// 受理窗口必须比 grace 更长,否则活动一开放就已经进入停止受理的窗口。
	if act.CloseAt-act.OpenAt <= int64(cfg.EntryCloseGraceSeconds) {
		return errBadRequest(fmt.Sprintf("开放时长必须大于停止受理提前量(%d 秒)", cfg.EntryCloseGraceSeconds))
	}
	if act.DrawAt < act.CloseAt+int64(cfg.RevealDelaySeconds) {
		return errBadRequest(fmt.Sprintf(
			"开奖时间必须比截止时间至少晚 %d 秒 —— 名单哈希要先于随机种子公开,"+
				"中间的间隔就是任何人抓取快照的窗口", cfg.RevealDelaySeconds))
	}
	if act.SettleDeadline > 0 && act.SettleDeadline < act.DrawAt {
		return errBadRequest("结算截止时间不得早于开奖时间")
	}
	if act.Kind == KindGuess && act.SettleDeadline <= 0 {
		// 没有这一条,管理员可以无限期扣着奖池不结算 ——
		// 这是竞猜最现实的资损形状。
		return errBadRequest("竞猜必须填写结算截止时间;逾期未录入结果将自动作废并全额退款")
	}
	return nil
}

// checkSpendWindow 把"近 N 日消费"的冷启动误拒挡在配置阶段。
//
// 上线时日桶表是空的,条件会全员误拒;而用户看到的只是"你不满足条件",
// 完全无从判断问题出在平台这边。
func checkSpendWindow(r Rules, cfg config.Lottery) error {
	if r.RecentSpendQuota <= 0 || r.RecentSpendDays <= 0 {
		return nil
	}
	if r.RecentSpendDays > cfg.SpendMaxLookbackDays {
		return errBadRequest(fmt.Sprintf("「近 N 日消费」的天数不得超过 %d", cfg.SpendMaxLookbackDays))
	}
	ready := SpendReadyFrom()
	if ready == 0 {
		return errSpendNotReady
	}
	if dayBucket(common.GetTimestamp()-int64(r.RecentSpendDays-1)*86400) < ready {
		return errSpendNotReady
	}
	return nil
}

// checkActiveCap 限制同时进行中的活动数。
func checkActiveCap(ctx context.Context, gdb *gorm.DB) error {
	set := effectiveCtx(ctx)
	if set.MaxActiveActivities <= 0 {
		return nil
	}
	var n int64
	err := gdb.WithContext(ctx).Model(&Activity{}).
		Where("status IN (?)", []string{StatusPublished, StatusLocked, StatusSettling}).
		Count(&n).Error
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("统计进行中活动", err)
	}
	if n >= int64(set.MaxActiveActivities) {
		return errActiveCapExceeded
	}
	return nil
}

// draftUpdates 列出草稿可改的列。
//
// 显式白名单而不是整行 Updates(&act):后者会把 entry_seq、chain_head、
// active_count 这些运行期状态一起覆盖回零值。草稿期它们确实都是零,
// 但这段代码离"有人复制它去改已发布活动"只有一步。
func draftUpdates(a *Activity) map[string]any {
	return map[string]any{
		"kind":  a.Kind,
		"title": a.Title,
		"intro": a.Intro,
		// 封面两列一起写。少写一列的后果是"从上传图改回外链"之后两列同时非空,
		// 而显示哪一张只能由前端的判断顺序回答。
		"cover_url":       a.CoverUrl,
		"cover_ref":       a.CoverRef,
		"stake_quota":     a.StakeQuota,
		"bet_min_quota":   a.BetMinQuota,
		"bet_max_quota":   a.BetMaxQuota,
		"open_at":         a.OpenAt,
		"close_at":        a.CloseAt,
		"draw_at":         a.DrawAt,
		"settle_deadline": a.SettleDeadline,
		"rules_text":      a.RulesText,
		"rules_hash":      a.RulesHash,
		"spec_text":       a.SpecText,
		"spec_hash":       a.SpecHash,
		// algo 必须与 spec_hash **同一次**写进去。
		//
		// buildActivity 是按 a.Algo 决定用哪一版 spec 原像算 spec_hash 的
		// (v2 的奖档行有十个字段,v1 只有四个)。漏掉这一列的后果是:一份
		// 本轮之前建的草稿被改一次之后,库里存着 v2 形状的 spec_hash 却标着
		// algo=lot-v1 —— 发布时承诺照样能算出来、开奖也照常,只有拿着
		// verify.py 的用户会算出一个对不上的 spec_hash,而那时活动已经开完了。
		"algo":                  a.Algo,
		"allow_multi_win":       a.AllowMultiWin,
		"fee_bps":               a.FeeBps,
		"min_entries_to_hold":   a.MinEntriesToHold,
		"max_entries_per_user":  a.MaxEntriesPerUser,
		"max_attempts_per_user": a.MaxAttemptsPerUser,
		"max_total_entries":     a.MaxTotalEntries,
		"max_total_users":       a.MaxTotalUsers,
		"max_per_inviter":       a.MaxPerInviter,
		"cooldown_seconds":      a.CooldownSeconds,
		"dedup_ip":              a.DedupIp,
		"updated_at":            common.GetTimestamp(),

		// 定档方式与双色球的号池绑定也要能在草稿期改。池子那三个数**不在这里**:
		// 它们要到 publish 那一刻才从系列行上原子取走,草稿期写进去的任何值都
		// 只是一个会过期的快照。
		"draw_mode":      a.DrawMode,
		"series_id":      a.SeriesId,
		"series_no":      a.SeriesNo,
		"pool_share_bps": a.PoolShareBps,
		"ball_red_pool":  a.BallRedPool,
		"ball_red_pick":  a.BallRedPick,
		"ball_blue_pool": a.BallBluePool,
		"ball_blue_pick": a.BallBluePick,
	}
}

func prizeTotal(rows []Prize) int64 { return prizeTotalRows(rows) }

func prizeTotalRows(rows []Prize) int64 {
	var total int64
	for _, r := range rows {
		total += r.AmountQuota * int64(r.Count)
	}
	return total
}

// writeResultOf 算出创建/修改活动之后要摆在管理员面前的那几个数。
//
// 最坏支出必须与发布按钮在同一屏,而不是藏在某个折叠面板里 —— 这是拦住
// "奖品金额多写一个零"最便宜的一道,而概率制新增的"最坏履行份数"同理:
// 运营会在"1% 概率发 10 份"的心理预期下配出一个要人工发几百份兑换码的活动。
func writeResultOf(act *Activity, prizes []Prize) activityWriteResult {
	total := prizeTotal(prizes)
	out := activityWriteResult{
		ActNo:             act.ActNo,
		PrizeTotalQuota:   total,
		BreakEvenEntries:  breakEven(total, act.StakeQuota),
		WorstCaseNetIssue: total,
		ExpectPayoutQuota: total,
	}
	if act.DrawMode != DrawModeProb {
		for _, p := range prizes {
			out.ExpectWinners += int64(p.Count)
			if p.Type() == PrizeTypeText {
				out.WorstCaseTextGrants += p.Count
			}
		}
		return out
	}

	// 概率制:按全场坐满估算期望。支出的**上界**仍然是 Σ(count × amount) ——
	// 超募时该档预算由全部中签者均分,一分钱都不会多发。
	cap64 := int64(act.MaxTotalEntries)
	var expectPayout, expectWinners int64
	hasText := false
	for _, p := range prizes {
		hit := cap64 * int64(p.WinPpm) / PpmDen
		expectWinners += hit
		if p.Type() == PrizeTypeText {
			hasText = true
			continue
		}
		budget := p.AmountQuota * int64(p.Count)
		spend := hit * p.AmountQuota
		if spend > budget {
			spend = budget
		}
		expectPayout += spend
	}
	out.ExpectWinners = expectWinners
	out.ExpectPayoutQuota = expectPayout
	if hasText {
		// 文本奖不摊薄(兑换码劈不开),所以最坏情况下全场每个人都可能中。
		out.WorstCaseTextGrants = act.MaxTotalEntries
	}
	return out
}

// breakEven 是保本参与人数 = ceil(奖品总额度 / 参与费)。
// 这是运营最需要的那个数,把痛点前移到发布之前。
func breakEven(prizeTotal, stake int64) int64 {
	if stake <= 0 {
		return 0
	}
	return (prizeTotal + stake - 1) / stake
}
