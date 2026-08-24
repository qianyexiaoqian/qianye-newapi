package controller

import (
	"context"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/serverday"

	"github.com/gin-gonic/gin"
)

// UserTokenTodayUsage 返回当前用户**每一把** API 密钥今天的消费额。
//
// ─────────────── 为什么是一条接口、一次查询 ───────────────
//
// 项目方原话:「用户key密钥这里显示一个,今日消耗。」
//
// 密钥页一页 20 行。逐行去查一次聚合,就是打开一次页面往主库上打 20 条
// GROUP BY —— 而 logs 是本站最大的表(备份库 447 万行)。所以这条接口按
// user_id **一次**聚合出该用户今天用过的全部令牌,页面拿 map 查表。
// 聚合与它依赖的覆盖索引见 commission.TokenDayUsage 的文件头。
//
// ─────────────── 「今日」是哪一段 ───────────────
//
// 项目方原话:「api 密钥的今日消耗以服务器的时间为准,即:0 点到 23 点 59 分
// 59 秒是今日的消耗。」所以日界走**服务器本地时区的自然日**,实现在
// qianye/serverday —— 与提现/划转的日限额窗口是同一份代码,不是第二份。
//
// 这与返佣的「消费日」(commission.day_offset_minutes)**不是**同一段时间:
// 后者是配置里写死的固定偏移,日消费明细、计佣分桶、日封顶都走它。演示机上
// day_offset_minutes=0(UTC)而机器本地是 PST,两者差 7 小时 —— 也就是说
// 本页的「今日」与日消费明细的「今日」会对不上,而两个数都是对的、两边都
// 不会报错。所以 day_start / day_end / timezone / utc_offset_minutes 一并下发,
// 不是给前端算用的,是给它**写给用户看**用的。
//
// ─────────────── 零值口径 ───────────────
//
//	今天没消费的令牌  → 不在 usage 里。前端渲染 0,不是 "-",也不是"加载中"。
//	SUM 恰好为 0      → 在 usage 里,值为 0。与"缺席"在用户眼里是同一件事。
//	今日消耗为负      → 原样下发(退款类日志走 type=6 不计入,但 type=2 的
//	                    单条 quota 理论上可以是负数),不夹到 0 —— 夹了就是
//	                    在界面上编一个库里没有的数。
//
// ─────────────── 为什么不走 requireCore ───────────────
//
// 与同目录 UserSessionStats 同一理由:它只读主库 logs 与进程内配置,
// 一个字节都不碰扩展库。扩展库不可用时让这一列 503,是纯粹的负收益。
func UserTokenTodayUsage(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "unauthorized"})
		return
	}

	dayStart, dayEnd := serverday.Range(common.GetTimestamp())
	zoneName, zoneOffsetMinutes := serverday.Zone(dayStart)

	ctx, cancel := context.WithTimeout(c.Request.Context(), commission.TokenDayUsageTimeout())
	defer cancel()

	usage, err := commission.TokenDayUsage(ctx, userId, dayStart, dayEnd)
	if err != nil {
		common.SysError("qianye: 密钥今日消耗聚合失败: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"code":    "qy_token_usage_failed",
			"message": "今日消耗统计暂时不可用,请稍后重试",
		})
		return
	}

	// map 的键必须是字符串:JSON 对象的键本来就只能是字符串,显式转一次
	// 是为了让前端那一侧的类型(Record<string, number>)与这里逐字对上,
	// 而不是依赖 encoding/json 对整数键的隐式转换。
	out := make(map[string]int64, len(usage))
	for tokenId, quota := range usage {
		out[strconv.Itoa(tokenId)] = quota
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"day_start": dayStart,
			"day_end":   dayEnd,
			// 时区必须如实下发,而且缩写与偏移要一起给:缩写单看有歧义
			// ("CST" 既是中国标准时间也是美国中部标准时间),而容器里 TZ
			// 常常没设、tzdata 常常没装 —— 那时进程认的「本地」就是 UTC,
			// 运营心里的却是他自己所在地。界面上把这两个字段写出来,
			// 是让这件事看得见的唯一办法。
			"timezone":           zoneName,
			"utc_offset_minutes": zoneOffsetMinutes,
			// index_ready 让「这一列今天为什么转圈」有一个可以直接看的答案。
			// 它只是显示位,判据是查询自己的超时 —— 见 logs_index.go。
			"index_ready": commission.TokenDayUsageIndexReady(),
			"usage":       out,
		},
	})
}
