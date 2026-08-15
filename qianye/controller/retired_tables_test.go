package controller

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/qianye/modules/groupmatrix"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRetiredTablesMatchTheDoc 对账 /admin/health 的退役表清单与 retired_tables.md。
//
// # 为什么这条对账值得存在
//
// 退役表清单存在的**全部**理由是可信:半年后在扩展库里看到一张不在任何 Tables()
// 里的表,人只有两条路 —— 查这份清单,或者猜。清单漏了一张,他猜的结论就是
// 「漏了一次迁移」,而下一步动作是 DROP,那一步不可逆。
//
// 两份清单分别服务不同的读者(运行时排障 / 事前评审),都会有人只看其中一份,
// 所以它们必须一致。这条对账只查**表名集合**,不查措辞 —— 措辞会自然演化,
// 而集合分叉才是那个会让人删错表的缺陷。
func TestRetiredTablesMatchTheDoc(t *testing.T) {
	doc, err := os.ReadFile("../docs/retired_tables.md")
	require.NoError(t, err, "退役表清单文档必须存在:它是 /admin/health 那一段唯一的解释")

	for _, row := range retiredTables {
		name, ok := row["table"].(string)
		require.Truef(t, ok && name != "", "每一条退役登记都必须有 table 字段")
		assert.Containsf(t, string(doc), name,
			"/admin/health 报了退役表 %s,但 qianye/docs/retired_tables.md 里找不到它。\n"+
				"文档是那张表的来历、观察期与手工 DROP 语句的唯一出处 —— "+
				"少了它,看到这张表的人只能猜,而猜错的下一步是不可逆的 DROP", name)
	}

	// 反向:文档里以表格行形式登记的表,必须在运行时也报得出来。
	// 只认 `qy_` 开头、被反引号包住的名字,避免把正文里的普通提及当成登记。
	reported := make(map[string]bool, len(retiredTables))
	for _, row := range retiredTables {
		reported[row["table"].(string)] = true
	}
	// groupmatrix 自报的那几张不在本清单里(它的模块还在,只是表退役了)。
	//
	// 豁免必须**从 Health() 里读**,不能在这里抄一份名字:抄下来的名单会让反向
	// 对账对这几张表整段失效 —— 文档登记了、接口其实不报,而这正是这条测试存在
	// 的唯一理由。读实际下发的那一份,豁免与被豁免的东西就永远是同一个来源。
	gmRetired, ok := groupmatrix.Health()["retired_tables"].([]string)
	require.True(t, ok,
		"groupmatrix.Health() 必须以 []string 报出它自报的退役表 —— "+
			"那是这几张表在运行时唯一的说明")
	for _, name := range gmRetired {
		reported[name] = true
	}

	for _, line := range strings.Split(string(doc), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "| `qy_") {
			continue
		}
		name := strings.Trim(strings.Fields(strings.TrimSpace(line))[1], "`")
		assert.Truef(t, reported[name],
			"qianye/docs/retired_tables.md 登记了退役表 %s,而 /admin/health 不报它。\n"+
				"排障的人看的是接口,不是仓库里的 markdown", name)
	}
}
