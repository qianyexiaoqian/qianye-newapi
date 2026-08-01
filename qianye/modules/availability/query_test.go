package availability

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 分组交集为空时绝不能退化成「不过滤」—— 那等于把全站分组的运营数据下发给
// 一个什么权限都没有的用户。这是本模块唯一的权限边界,必须单测钉死。
func TestQueryCellsRefusesEmptyGroupSet(t *testing.T) {
	out, err := queryCells(timeRange{StartTs: 0, EndTs: 1}, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, out, "空分组集合必须返回空结果,且不得访问数据库")

	points, err := querySeriesPoints(timeRange{StartTs: 0, EndTs: 1}, "gpt-5", nil)
	require.NoError(t, err)
	assert.Empty(t, points)
}

// 可用率看板是给全体登录用户的;管理端总览 /admin/availability/stats 已按项目方
// 要求整体移除。这条断言现在钉两件事:
//  1. 用户端两个接口必须还在 —— 「移除管理页」不能顺手把用户功能一起拿走;
//  2. 管理端不得再挂出任何 availability 路由,尤其不能是那个不做分组裁剪的
//     stats(它会把全站分组名、含隐藏分组一起下发)。
//
// 注意断言的是 engine.Routes() 的实际结果,而不是「Mod 有没有实现
// RegisterAdminRoutes」:module.Base 的空实现让漏删一行调用也能编译通过,
// 只有看最终路由表才抓得住。
func TestRoutesKeepUserScopeAndDropAdminScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mod{}.RegisterUserRoutes(engine.Group("/user"))
	// 与 qianye/router.go 一致:管理端路由组照常调用,Mod 不实现即什么都不挂。
	Mod{}.RegisterAdminRoutes(engine.Group("/admin"))

	paths := map[string]struct{}{}
	for _, r := range engine.Routes() {
		paths[r.Path] = struct{}{}
	}
	assert.Contains(t, paths, "/user/availability/matrix", "矩阵必须对全体登录用户开放")
	assert.Contains(t, paths, "/user/availability/series")
	assert.NotContains(t, paths, "/admin/availability/stats",
		"管理端可用率总览已移除,不得再挂出")
	assert.NotContains(t, paths, "/user/availability/stats",
		"管理端总览不做分组裁剪,挂到用户组等于下发全站分组名")
	for p := range paths {
		assert.NotContains(t, p, "/admin/availability",
			"管理端不得残留任何 availability 路由")
	}
}

func TestIntersectGroups(t *testing.T) {
	visible := map[string]string{"default": "默认", "vip": "VIP"}

	assert.Equal(t, []string{"default", "vip"}, intersectGroups(nil, visible),
		"不传 groups 表示全部可见分组")
	assert.Equal(t, []string{"vip"}, intersectGroups([]string{"vip"}, visible))
	assert.Empty(t, intersectGroups([]string{"internal"}, visible),
		"请求无权分组必须被整条剔除,而不是放行")
	assert.Empty(t, intersectGroups([]string{"default"}, nil),
		"可见集合为空时不得回退成全量")
	assert.Empty(t, intersectGroups(nil, nil))
}

// 时间范围是这个只读端点唯一的成本闸门:不夹紧就等于开放一次全表扫描。
func TestResolveRangeClamping(t *testing.T) {
	now := common.GetTimestamp()

	r := resolveRange(0, 0, 0)
	assert.Equal(t, int64(24*3600), r.EndTs-r.StartTs, "默认 24 小时")

	r = resolveRange(0, 0, 10000)
	assert.Equal(t, int64(maxRangeHours*3600), r.EndTs-r.StartTs)

	r = resolveRange(now-100*24*3600, now, 0)
	assert.Equal(t, int64(maxRangeHours*3600), r.EndTs-r.StartTs)

	r = resolveRange(now-3600, now+86400, 0)
	assert.LessOrEqual(t, r.EndTs, now+1, "结束时间不得超过当前时刻")

	// start >= end 属于非法输入,退回按 hours 计算而不是产生一个倒置区间。
	r = resolveRange(now, now-3600, 6)
	assert.Equal(t, int64(6*3600), r.EndTs-r.StartTs)
}

// 跨度决定读哪张表与返回粒度:读错表会让 30 天查询扫掉百万行 5 分钟桶。
func TestRangeTableSelection(t *testing.T) {
	short := resolveRange(0, 0, hourTableSpanHours)
	assert.False(t, short.useHourTable())
	assert.Equal(t, bucketTable, short.tableName())
	assert.Equal(t, bucketSeconds(), short.granularitySeconds())

	long := resolveRange(0, 0, hourTableSpanHours+1)
	assert.True(t, long.useHourTable())
	assert.Equal(t, hourTable, long.tableName())
	assert.Equal(t, int64(3600), long.granularitySeconds())
	assert.Equal(t, "1h", granularityLabel(long))
}

// 监控页默认要先看最烂的:排序把无数据的模型压到最后,
// 否则一屏全是没人用的模型,真正故障的那个反而翻不到。
func TestSortModelsWorstFirst(t *testing.T) {
	d := definitionOf(config.Availability{})
	groups := []string{"default", "vip"}
	cells := map[cellKey]*Bucket{
		{group: "default", model: "healthy"}: {ReqTotal: 100, SuccessCount: 100},
		{group: "vip", model: "healthy"}:     {ReqTotal: 100, SuccessCount: 100},
		{group: "default", model: "broken"}:  {ReqTotal: 100, SuccessCount: 100},
		{group: "vip", model: "broken"}:      {ReqTotal: 100, SuccessCount: 40, FailUpstream: 60},
		{group: "default", model: "idle"}:    {},
	}
	names := []string{"healthy", "idle", "broken"}
	sortModels(names, cells, groups, d, "")
	assert.Equal(t, []string{"broken", "healthy", "idle"}, names)

	sortModels(names, cells, groups, d, "model_asc")
	assert.Equal(t, []string{"broken", "healthy", "idle"}, names)
}

func TestSortCellsPutsNoDataLast(t *testing.T) {
	ninety := 90.0
	hundred := 100.0
	cells := []cell{
		{Model: "a", Group: "g", Availability: nil, State: StateNoData},
		{Model: "b", Group: "g", Availability: &hundred},
		{Model: "c", Group: "g", Availability: &ninety},
	}
	sortCells(cells, "availability_asc")
	assert.Equal(t, []string{"c", "b", "a"}, []string{cells[0].Model, cells[1].Model, cells[2].Model})
}

// 无数据的格子必须带 no_data 而不是 0%,并且 availability 字段是 null。
func TestBuildCellNoDataStates(t *testing.T) {
	d := definitionOf(config.Availability{})

	offered := buildCell(cellKey{group: "vip", model: "gpt-5"}, nil, true, d)
	assert.Equal(t, StateNoData, offered.State)
	assert.Nil(t, offered.Availability)
	assert.Equal(t, int64(0), offered.Counted)

	notOffered := buildCell(cellKey{group: "vip", model: "gpt-5"}, nil, false, d)
	assert.Equal(t, StateNotOffered, notOffered.State)
	assert.Nil(t, notOffered.Availability)
	assert.False(t, notOffered.HasChannel)
}

// 原 TestPaginate 已随本地 paginate 一起收敛到 httpq.Slice:
// 切片分页的正常路径由 qianye/httpq/httpq_test.go 覆盖,越界与溢出路径由
// 本包 api_test.go 的 TestPaginateNeverPanicsOnOutOfRangePage 覆盖。

func TestSplitCSV(t *testing.T) {
	assert.Nil(t, splitCSV("  ", 0))
	assert.Equal(t, []string{"a", "b"}, splitCSV(" a , b ,", 0))
	assert.Equal(t, []string{"a", "b"}, splitCSV("a,b,c,d", 2), "超出上限的部分必须被截断")
}

// SELECT 投影由列清单派生,漏列会让某个计数在查询结果里恒为 0。
func TestSelectSumsCoversEveryCounter(t *testing.T) {
	projection := selectSums("model_name, group_name")
	for _, col := range counterColumns() {
		assert.Containsf(t, projection, "SUM("+col+") AS "+col, "投影缺少列 %s", col)
	}
}
