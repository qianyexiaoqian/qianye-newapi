package transfer

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// basegroup.go —— 「划转门槛只认用户组,套餐不许影响」的唯一解析点。
//
// ═══════════════════════════ 项目方口径 ═══════════════════════════
//
// 「余额划转只针对用户组,套餐关他们什么事情?」
//
// 门槛分档(grouplimit.go)原先按此刻的 users.group 解析,而 users.group 是
// **用户自己花钱就能改的** —— 任何一个 upgrade_group 指向别处、又允许余额支付的
// 套餐都能改它。两条实测跑通的路径:
//
//	① 当天日额度/日笔数用满 → 花 5000 quota($0.01)买一个升组套餐 →
//	   同一秒转走 6000 万,而当天的计数一个都不重置,只是上限换了一档;
//	② 注册 496 秒的号被 new_account_freeze_hours 挡住 → 买同一个套餐 →
//	   换进一个该项更松的档 → 立刻放行,users.created_at 一个字节没变。
//
// ═══════════════════════ 剥离在哪一层做 ═══════════════════════
//
// 判据由 model.QyBaseUserGroup 给出(定义与四条边界写在那里,不在这里重复):
// 名下还站着一条升组链时取链根,否则取 users.group。绝大多数用户名下一条升组
// 订阅都没有,那条路径返回的就是 users.group 本身 —— **行为一个字节不变**。
//
// 剥离**只作用于门槛**(qy_transfer_group_limits 的七项:单笔上下限、日额度、
// 日笔数、冷却、收款方入账笔数、新账号冻结期),发起方与收款方两侧同时剥离 ——
// 收款方那一档要是不剥离,攻击者只需给汇集账号买一档最松的,
// receiver_daily_max_in_count 这道闸门就等于不存在(见 evaluateRisk)。
//
// **分组规则(grouprule.go,谁能转给谁)刻意不剥离**,仍按 users.group 判:
// 那是一张"允许 / 禁止"的白名单,「买了 VIP 才能划转」是运营完全正当的产品
// 形态;把它也改成基准分组等于把这个卖法废掉。它与门槛的区别是方向 ——
// 规则决定"这条边通不通",门槛决定"能走多少钱",项目方要拦的是后者。
//
// ═══════════════════════ 读失败必须失败关闭 ═══════════════════════
//
// 回落 users.group 就是回落到套餐给的那个付费组,主库抖一下等于把这道剥离
// 静默关掉 —— 与 loadGroupRules / loadTiers 「读失败绝不回落成不限制」同一条口径。

// errBaseUserGroup 表示这一刻判不出发起方/收款方的基准用户组。
//
// 用 503 而不是 400:这是服务端读取状态的问题,不是用户请求写错了,重试有意义。
// code 一旦发布就不能改(前端按 code 映射 i18n;未登记的 code 会回落到后端
// 原始文案,与 qy_transfer_settings_invalid 同一档处理)。
var errBaseUserGroup = newBizError("qy_transfer_base_group_unavailable",
	"暂时无法确定用户组,请稍后再试", http.StatusServiceUnavailable)

// baseUserGroup 返回这个用户在划转门槛上作数的分组。
//
// 与 model.QyBaseUserGroup 之间隔一层不是为了转发:这里承担把主库读失败翻译成
// **失败关闭**的那个决定,以及把它记进 SysError —— 少了这一行,一次持续的主库
// 抖动会让整站的划转按付费档放行而日志上一片安静。
func baseUserGroup(u *model.User) (string, error) {
	groups, err := baseUserGroups(u)
	if err != nil {
		return "", err
	}
	return groups[0], nil
}

// baseUserGroups 返回这一笔必须**一起**作数的全部基准分组,调用方逐项取严。
//
// 绝大多数用户只有一个(users.group 本身)。第二个只在一种情况下出现:用户此刻
// 坐的分组是某张已到期套餐的显式 downgrade_group 落点 —— 那是套餐留下的东西,
// 不是他自己的身份。判据与理由写在 model.QyBaseUserGroups,不在这里重复。
func baseUserGroups(u *model.User) ([]string, error) {
	if u == nil {
		return nil, errBaseUserGroup
	}
	groups, err := model.QyBaseUserGroups(u.Id, u.Group)
	if err != nil || len(groups) == 0 {
		reason := "返回了空清单"
		if err != nil {
			reason = err.Error()
		}
		common.SysError("qianye/transfer: 读取用户 " + strconv.Itoa(u.Id) +
			" 的基准用户组失败,本次划转按失败关闭处理: " + reason)
		return nil, errBaseUserGroup
	}
	return groups, nil
}
