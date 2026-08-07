package lottery

// residue.go —— 本模块对「用户分组名」这个键的处置声明。
//
// ══════════════ 它藏在一个 grep 不到列名的地方 ══════════════
//
// 参与条件 Rules.AllowGroups / DenyGroups 是**用户分组**名单(判定时逐字比
// users.group,见 GroupAllowed),但它们不是一列 —— 它们住在 qy_lot_activity.rules_text
// 这个 JSON 文本里。按列名扫库表的那条守卫看不见它,而漏掉的后果与 transfer 的
// to_groups 逐字相同:一条挂在死名字上的白名单,在这个名字被将来某次新建重新
// 用上的时候,会让一批毫不相干的新用户凭空获得参与资格。
//
// ══════════════ 为什么这一处既不能 clean 也不能 rewrite ══════════════
//
// rules_text 是**发布时冻结的承诺**:它进 rules_hash、rules_hash 进 commit_hash,
// 而 commit_hash 在活动页上公开、第三方拿公开数据复算。改动其中一个字节
// (哪怕只是把分组名从 A 换成 B)会让每一个外部验证者算出不同的哈希,
// 而那与"平台偷偷改了规则"在证据上完全无法区分 —— 那正是这套承诺协议
// 唯一要防的东西。所以:
//
//	已结束的活动(finished)  keep  —— 与 commission 的 rate_group 同一个理由:
//	                                 它是"当时按哪份名单开的奖"这个事实。
//	未结束的活动             block —— 名单动不了,而这批人正要被迁走 / 改名。
//	                                 结果只有两种:他们在一场进行中的活动里
//	                                 突然失去参与资格(allow 名单),或者突然
//	                                 获得资格(deny 名单)。两种都是运营必须
//	                                 先自己处理掉的事,而不是由一次"删分组"
//	                                 替他决定。
//
// 处理办法是等这场活动结束、或者先把它取消(取消走 settling→finished 的
// 全额退款路径),而不是在这里替运营改一份已经公示出去的规则。
//
// ══════════════ 第三处:它**不**在探测清单里,以及为什么 ══════════════
//
// qy_lot_entry.eligibility_snapshot 里也冻着报名那一刻的 users.group。它同样是
// keep —— 主库 users 没有历史版本,这份快照是争议仲裁的唯一凭据,改它等于
// 让平台自己修改证据。它不单列成一行探测结果,理由是这一行**不影响任何决定**:
// 处置恒为保留、行数为几都不改变运营该按哪个键,而准确的行数要在每次打开删除
// 弹窗时全表扫本模块最大的一张表(每一次报名一行,永不清理)。
// 那是拿一次实打实的库压力换一个装饰性的数字。

import (
	"strings"

	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module: "lottery",
		Probe:  probeResidue,
		Sweep:  sweepResidue,
	})
}

// probeResidue 全表扫活动行的 rules_text。
//
// 不做 SQL 侧的 LIKE 预筛:分组名是运营自由输入的字符串,里面可以有 % 与 _,
// 而三种方言的 ESCAPE 写法各不相同。一次预筛写错的表现是**漏掉**一条命中,
// 也就是这条守卫存在的理由被静默取消掉。活动表只在开活动时长一行,
// 而这是管理端删除弹窗的冷路径,整表读四列可以接受。
func probeResidue(gdb *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	if gdb == nil {
		return nil, nil
	}
	var rows []Activity
	if err := gdb.Select("id", "act_no", "title", "status", "rules_text").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	live := make([]string, 0, 2)
	var finished int64
	for _, act := range rows {
		rules, err := ParseRules(act.RulesText)
		if err != nil {
			// 解析不了的活动照样要报出来:算不清影响面就不许删,
			// 这是唯一安全的方向(与 ProbeResidues 的整体口径一致)。
			return nil, err
		}
		if !listsGroup(rules, userGroup) {
			continue
		}
		if act.Status == StatusFinished {
			finished++
			continue
		}
		live = append(live, act.ActNo+" "+act.Title)
	}

	out := []groupns.Residue{{
		Module: "lottery", Table: Activity{}.TableName(),
		Label: "**进行中**的活动把它写进了参与名单", Rows: int64(len(live)),
		Disposition: groupns.ResidueBlock,
		Detail: "命中的活动:" + strings.Join(live, "、") +
			"。参与条件在发布时已经冻结进 commit_hash 并对外公示,改一个字节就会让" +
			"所有外部验证者算出不同的哈希 —— 与「平台偷偷改了规则」在证据上无法区分。" +
			"请先让这些活动结束或取消,再删/改这个分组",
	}, {
		Module: "lottery", Table: Activity{}.TableName(),
		Label: "已结束的活动里冻结的参与名单", Rows: finished,
		Disposition: groupns.ResidueKeep,
		Detail: "**保留**。它是「当时按哪份名单开的奖」这个事实," +
			"改掉等于让公示出去的 commit_hash 再也验不过",
	}}
	if len(live) == 0 {
		out[0].Detail = "没有任何进行中的活动把它写进 allow_groups / deny_groups"
	}
	return out, nil
}

// listsGroup 判断一份参与条件是不是点名了这个用户分组。
//
// 两个名单都要看:allow 名单里的名字消失 ⇒ 这批人当场失去资格;
// deny 名单里的名字消失 ⇒ 一批本来被挡在门外的人当场获得资格。
// 后者更隐蔽,也更贵 —— 它是一道被静默拆掉的闸门。
func listsGroup(rules Rules, userGroup string) bool {
	for _, name := range rules.AllowGroups {
		if name == userGroup {
			return true
		}
	}
	for _, name := range rules.DenyGroups {
		if name == userGroup {
			return true
		}
	}
	return false
}

// sweepResidue 什么都不做,而且**必须**什么都不做。
//
// 走到这里时 Probe 报告的 block 行数一定是 0(删除与改名两条路径都在调用
// SweepResidues 之前复核过 blocking 残留),剩下的全是已结束活动里的冻结名单 ——
// 那一份的处置是 keep。写成空实现而不是干脆不注册这个模块:不注册的话,
// TestEveryUserGroupKeyedTableDeclaresItsDisposition 里那条"这个模块声明过没有"
// 的断言就没有对象,而"没人想过这件事"与"想过并决定不动"在代码里必须是两个样子。
func sweepResidue(_ *gorm.DB, _, _ string, _ bool) error { return nil }
