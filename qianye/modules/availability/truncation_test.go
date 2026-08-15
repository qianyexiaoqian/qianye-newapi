package availability

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// truncationGroups 是两个可见分组。两个就够了:截断与分组数无关,
// 只与「一行的宽度 × 行数」和 max_series_per_query 的关系有关。
var truncationGroups = []string{"default", "vip"}

type truncationBody struct {
	Success bool `json:"success"`
	Data    struct {
		Models        []string `json:"models"`
		CoveredModels []string `json:"covered_models"`
		Truncated     bool     `json:"truncated"`
		Cells         []struct {
			Group string `json:"group"`
			Model string `json:"model"`
			State string `json:"state"`
		} `json:"cells"`
		Overall struct {
			ReqTotal int64 `json:"req_total"`
		} `json:"overall"`
	} `json:"data"`
}

// newTruncationEnv 搭起一个「两个分组 × 七个模型」的矩阵,并把序列上限设成 maxSeries。
//
// 每个 (分组, 模型) 都灌同样的数据:排序稳定,断言可以直接按模型名下标表达。
func newTruncationEnv(t *testing.T, maxSeries int) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Bucket{}))

	now := common.GetTimestamp()
	ts := alignBucket(now-600, bucketSeconds())
	for _, name := range matrixModels {
		for _, g := range truncationGroups {
			require.NoError(t, gdb.Create(&Bucket{
				BucketTs: ts, GroupName: g, ModelName: name,
				ReqTotal: 10, SuccessCount: 9, FailUpstream: 1, UpdatedAt: now,
			}).Error)
		}
	}

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled: true,
		Availability: config.Availability{
			Enabled: true, MaxSeriesPerQuery: maxSeries,
		},
	})

	// 可见分组白名单必须显式铺开:visibleGroups 走的是全站 setting,
	// 不是测试里的常量,少了这一步交集为空、矩阵直接返回空。
	prevGroups := setting.UserUsableGroups2JSONString()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(
		`{"default":"默认分组","vip":"vip分组"}`))

	offered := map[string]struct{}{}
	for _, name := range matrixModels {
		offered[name] = struct{}{}
	}
	channelModelMu.Lock()
	for _, g := range truncationGroups {
		channelModelCache[g] = channelModelEntry{models: offered, expireAt: time.Now().Add(time.Hour)}
	}
	channelModelMu.Unlock()

	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = setting.UpdateUserUsableGroupsByJSONString(prevGroups)
		channelModelMu.Lock()
		for _, g := range truncationGroups {
			delete(channelModelCache, g)
		}
		channelModelMu.Unlock()
		_ = sqlDB.Close()
	})
}

func callMatrix(t *testing.T, query string) truncationBody {
	t.Helper()
	gin.SetMode(gin.TestMode)
	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/qy/availability/matrix?sort=model_asc&"+query, nil)
	c.Set("id", 1)
	c.Set("group", "default")

	getMatrix(c)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	var body truncationBody
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success)
	return body
}

// 截断必须是可分辨的,而且必须按整行切。
//
// 修复前的形状:逐格截断到 max_series_per_query 就 break,响应里只有一个
// truncated 布尔。于是被截掉的格子在 cells 里缺席 —— 而"该分组不提供这个模型"
// 在 cells 里同样缺席,两者不可分辨,前端把没查过的格子渲染成了"未提供"。
// 默认 max_series_per_query=200、前端一页 30 个模型,≥7 个可见分组就必然每页命中。
//
// 修复后:covered_models 显式说明哪些模型真的被查了,而且被覆盖的模型一定是
// **整行**都在,前端据此把剩下的格子降级成"未知"。
func TestMatrixTruncationReportsCoverageAndNeverSplitsARow(t *testing.T) {
	// 一行 2 个格子,上限 5 → 只有 2 行放得下(4 格),第 3 行需要 6 格,放不下。
	newTruncationEnv(t, 5)
	body := callMatrix(t, "page=1&page_size=7")

	assert.True(t, body.Data.Truncated, "命中上限却不上报,前端无从降级")
	assert.Equal(t, matrixModels, body.Data.Models, "行标题仍是整页模型,不能悄悄少几行")
	assert.Equal(t, matrixModels[:2], body.Data.CoveredModels,
		"covered_models 必须精确说明查了哪些模型")

	covered := map[string]struct{}{}
	for _, name := range body.Data.CoveredModels {
		covered[name] = struct{}{}
	}
	seen := map[cellKey]struct{}{}
	for _, cl := range body.Data.Cells {
		assert.Contains(t, covered, cl.Model, "cells 里出现了不在 covered_models 的模型")
		seen[cellKey{group: cl.Group, model: cl.Model}] = struct{}{}
	}
	// 半行是最坏的一种截断:留下的空缺与 not_offered 完全不可分辨。
	for name := range covered {
		for _, g := range truncationGroups {
			assert.Contains(t, seen, cellKey{group: g, model: name},
				"被覆盖的模型必须整行都在:%s / %s 缺失", name, g)
		}
	}
	assert.Len(t, body.Data.Cells, len(covered)*len(truncationGroups))
	assert.LessOrEqual(t, len(body.Data.Cells), 5, "格子数不得越过 max_series_per_query")
}

// 没有截断时 covered_models 必须等于 models —— 否则前端会把好端端的格子
// 也降级成"未知",把一个不存在的问题画到脸上。
func TestMatrixCoversEveryModelWhenUnderLimit(t *testing.T) {
	newTruncationEnv(t, defaultMaxSeries)
	body := callMatrix(t, "page=1&page_size=7")

	assert.False(t, body.Data.Truncated)
	assert.Equal(t, matrixModels, body.Data.CoveredModels)
	assert.Len(t, body.Data.Cells, len(matrixModels)*len(truncationGroups))
}

// KPI 只能汇总真正查过的模型。
//
// 拿被截掉的模型去凑总数,页面上就会出现"整体可用率的分母比矩阵里所有格子
// 加起来还大"——用户看得见对不上,却查不出为什么。
func TestMatrixOverallCountsOnlyCoveredModels(t *testing.T) {
	newTruncationEnv(t, 5)
	body := callMatrix(t, "page=1&page_size=7")

	require.Len(t, body.Data.CoveredModels, 2)
	// 每个 (分组, 模型) 灌了 10 次请求,覆盖 2 个模型 × 2 个分组 = 40。
	assert.Equal(t, int64(40), body.Data.Overall.ReqTotal)
}

// 分组多到一行都放不下时,诚实地返回空覆盖 + truncated,而不是给出半行。
func TestMatrixReportsZeroCoverageWhenOneRowExceedsLimit(t *testing.T) {
	newTruncationEnv(t, 1)
	body := callMatrix(t, "page=1&page_size=7")

	assert.True(t, body.Data.Truncated)
	assert.Empty(t, body.Data.CoveredModels)
	assert.Empty(t, body.Data.Cells)
	assert.NotNil(t, body.Data.CoveredModels, "covered_models 必须是 [] 而不是 null")
}
