package availability

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

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

func TestPaginate(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e"}
	assert.Equal(t, []string{"a", "b"}, paginate(names, 1, 2))
	assert.Equal(t, []string{"e"}, paginate(names, 3, 2))
	assert.Empty(t, paginate(names, 9, 2))
	assert.Equal(t, names, paginate(names, 1, 50))
}

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
