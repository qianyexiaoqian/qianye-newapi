package apiaddr

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func idParams(id string) gin.Params {
	return gin.Params{{Key: "id", Value: id}}
}

// 新建走完整链路:入库、追加到末尾、默认启用、留一条成功审计。
//
// 默认启用是业务规则(见 upsertReq.Enabled 的说明),而不是 GORM 的 default tag ——
// 这条断言正是防止有人"顺手"把它改回 `gorm:"default:true"`(那会让 MySQL 与
// PostgreSQL 在每次重启时反复 ALTER TABLE)。
func TestAdminCreateAppendsEnabledAddressAndAudits(t *testing.T) {
	gdb := newTestDB(t)

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses",
		`{"name":"主线路","remark":"默认","url":"https://api.example.com/"}`, nil, adminCreate)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	var rows []Address
	require.NoError(t, gdb.Order(orderClause).Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "主线路", rows[0].Name)
	assert.Equal(t, "https://api.example.com", rows[0].URL, "尾部斜杠必须在入库前去掉")
	assert.True(t, rows[0].Enabled, "新建的地址必须默认启用,否则建完立刻不可用")
	assert.Equal(t, sortStep, rows[0].SortOrder)
	assert.Equal(t, 9001, rows[0].CreatedBy)

	assert.Equal(t, []string{"api_address.create:ok"}, auditActions(t, gdb))
}

// 显式 enabled:false 必须被尊重 —— 指针语义的另一半。
func TestAdminCreateHonoursExplicitDisabled(t *testing.T) {
	gdb := newTestDB(t)

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses",
		`{"name":"备用","url":"https://backup.example.com","enabled":false}`, nil, adminCreate)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	var row Address
	require.NoError(t, gdb.Take(&row).Error)
	assert.False(t, row.Enabled)
}

// URL 判重与行数上限都要落一条失败审计。
//
// 失败审计是这个模块审计口径的一半:"有人试过、被挡下了"与"没人动过"
// 在事后是完全不同的两件事。
func TestAdminCreateAuditsRejectedAttempts(t *testing.T) {
	gdb := newTestDB(t)
	seedAddress(t, gdb, "主线路", "https://api.example.com", sortStep, true)

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses",
		`{"name":"重复的","url":"https://api.example.com/"}`, nil, adminCreate)
	assert.Equal(t, http.StatusConflict, res.Code)
	assert.Equal(t, errDuplicateURL.Code, codeOf(t, res),
		"归一化之后是同一个地址,必须按重复处理而不是静默建出第二条")

	assert.Equal(t, []string{"api_address.create:fail"}, auditActions(t, gdb))

	var count int64
	require.NoError(t, gdb.Model(&Address{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

// 纯输入校验失败**不写**审计:那是管理员在输入框里打字的过程,
// 一次编辑会产生十几条,把真正有价值的那几条冲掉。
func TestAdminCreateDoesNotAuditPureValidationFailures(t *testing.T) {
	gdb := newTestDB(t)

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses",
		`{"name":"没协议","url":"api.example.com"}`, nil, adminCreate)
	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Equal(t, errURLScheme.Code, codeOf(t, res))

	assert.Empty(t, auditActions(t, gdb), "输入框里的打字过程不该进审计")
}

// 编辑路径的审计口径必须与新建一致:输入框里的打字过程不进审计。
//
// # 这条断言守的是什么
//
// 分界线是"有没有碰到库里的既有状态"。校验一旦跑在 loadRow 之后(updateAddress
// 内部才 applyTo 就是这个形状),管理员每按一次保存就多一条 result=fail 的
// api_address.update —— 反复修正一个 URL 格式的过程会产生十几条,把真正有价值
// 的那几条(超上限 / URL 重复 / 并发重排被拒)冲掉。新建路径有
// TestAdminCreateDoesNotAuditPureValidationFailures 守着,编辑路径缺了对称的
// 那一条,于是两端口径可以静默漂移。
func TestAdminUpdateDoesNotAuditPureValidationFailures(t *testing.T) {
	gdb := newTestDB(t)
	row := seedAddress(t, gdb, "主线路", "https://api.example.com", 70, true)

	res := call(t, http.MethodPut, "/api/qy/admin/api-addresses/1",
		`{"name":"","url":"https://api.example.com"}`,
		idParams(strconv.Itoa(row.Id)), adminUpdate)
	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.Equal(t, errNameRequired.Code, codeOf(t, res))

	assert.Empty(t, auditActions(t, gdb), "输入框里的打字过程不该进审计")
}

// 编辑绝不能把排序覆盖掉。
//
// # 这条断言守的是什么
//
// 排序有独立入口(adminReorder)。如果编辑走 Save 或 Updates(struct),
// sort_order 会跟着请求体里的零值一起写回 —— 表现为"先拖好顺序,再去改一条的
// 备注,顺序就乱了",而且乱的是别人刚排好的那份。列清单是唯一的防线。
func TestAdminUpdateNeverTouchesSortOrder(t *testing.T) {
	gdb := newTestDB(t)
	row := seedAddress(t, gdb, "主线路", "https://api.example.com", 70, true)

	res := call(t, http.MethodPut, "/api/qy/admin/api-addresses/1",
		`{"name":"主线路(改名)","remark":"海外优先","url":"https://api.example.com"}`,
		idParams(strconv.Itoa(row.Id)), adminUpdate)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	var after Address
	require.NoError(t, gdb.Where("id = ?", row.Id).Take(&after).Error)
	assert.Equal(t, "主线路(改名)", after.Name)
	assert.Equal(t, "海外优先", after.Remark)
	assert.Equal(t, 70, after.SortOrder, "编辑不该动排序 —— 排序归 adminReorder 管")
	assert.True(t, after.Enabled, "请求里没带 enabled 时必须保持原样")

	assert.Equal(t, []string{"api_address.update:ok"}, auditActions(t, gdb))
}

// 改到别人已占用的 URL 上要被拒,并留一条失败审计。
func TestAdminUpdateRejectsDuplicateURL(t *testing.T) {
	gdb := newTestDB(t)
	first := seedAddress(t, gdb, "主线路", "https://a.example.com", 10, true)
	second := seedAddress(t, gdb, "备用", "https://b.example.com", 20, true)

	res := call(t, http.MethodPut, "/api/qy/admin/api-addresses/2",
		`{"name":"备用","url":"https://a.example.com"}`, idParams(strconv.Itoa(second.Id)), adminUpdate)
	assert.Equal(t, http.StatusConflict, res.Code)
	assert.Equal(t, errDuplicateURL.Code, codeOf(t, res))
	assert.Equal(t, []string{"api_address.update:fail"}, auditActions(t, gdb))

	var unchanged Address
	require.NoError(t, gdb.Where("id = ?", second.Id).Take(&unchanged).Error)
	assert.Equal(t, "https://b.example.com", unchanged.URL)
	assert.Equal(t, "https://a.example.com", mustURL(t, gdb, first.Id))
}

// 改回自己原来的 URL 不算重复 —— 否则"只想改个备注"会被自己挡住。
func TestAdminUpdateAllowsKeepingOwnURL(t *testing.T) {
	gdb := newTestDB(t)
	row := seedAddress(t, gdb, "主线路", "https://a.example.com", 10, true)

	res := call(t, http.MethodPut, "/api/qy/admin/api-addresses/1",
		`{"name":"主线路","remark":"只改备注","url":"https://a.example.com"}`,
		idParams(strconv.Itoa(row.Id)), adminUpdate)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())
}

// 删除留下 before 快照 —— 那是事后唯一能回答"删的是哪一条"的东西。
func TestAdminDeleteRemovesRowAndAudits(t *testing.T) {
	gdb := newTestDB(t)
	row := seedAddress(t, gdb, "备用", "https://b.example.com", 20, true)

	res := call(t, http.MethodDelete, "/api/qy/admin/api-addresses/1", "",
		idParams(strconv.Itoa(row.Id)), adminDelete)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	var count int64
	require.NoError(t, gdb.Model(&Address{}).Count(&count).Error)
	assert.EqualValues(t, 0, count)
	assert.Equal(t, []string{"api_address.delete:ok"}, auditActions(t, gdb))
}

// 删一条不存在的行:404 + 一条失败审计。
func TestAdminDeleteMissingRowAudits(t *testing.T) {
	gdb := newTestDB(t)

	res := call(t, http.MethodDelete, "/api/qy/admin/api-addresses/42", "",
		idParams("42"), adminDelete)
	assert.Equal(t, http.StatusNotFound, res.Code)
	assert.Equal(t, errNotFound.Code, codeOf(t, res))
	assert.Equal(t, []string{"api_address.delete:fail"}, auditActions(t, gdb))
}

// 整表重排:提交完整顺序,库里的展示顺序跟着变。
func TestAdminReorderRewritesWholeTable(t *testing.T) {
	gdb := newTestDB(t)
	a := seedAddress(t, gdb, "A", "https://a.example.com", 10, true)
	b := seedAddress(t, gdb, "B", "https://b.example.com", 20, true)
	c := seedAddress(t, gdb, "C", "https://c.example.com", 30, true)
	require.Equal(t, []int{a.Id, b.Id, c.Id}, orderedIds(t, gdb))

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses/reorder",
		`{"ids":[3,1,2]}`, nil, adminReorder)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	assert.Equal(t, []int{c.Id, a.Id, b.Id}, orderedIds(t, gdb))
	assert.Equal(t, []string{"api_address.reorder:ok"}, auditActions(t, gdb))
}

// 并发重排必须被拒,而不是把别人新加的那条静默挤到末尾。
//
// # 这条断言守的是什么
//
// 两个管理员各自打开列表:甲加了一条 D,乙(手上还是 A/B/C 那份)拖完顺序点保存。
// 如果只按提交的 id 逐条写序号,D 会保留它自己的旧序号 —— 落在哪全看巧合,
// 而乙完全不知道自己刚刚参与了一次冲突。"全集比对 + 整表提交"把它变成一次
// 显式的乐观锁失败,前端提示刷新重来。
func TestAdminReorderRejectsStaleIdSet(t *testing.T) {
	gdb := newTestDB(t)
	seedAddress(t, gdb, "A", "https://a.example.com", 10, true)
	seedAddress(t, gdb, "B", "https://b.example.com", 20, true)
	// 甲刚加的那条,乙手上的列表里没有。
	seedAddress(t, gdb, "D", "https://d.example.com", 30, true)

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses/reorder",
		`{"ids":[2,1]}`, nil, adminReorder)
	assert.Equal(t, http.StatusConflict, res.Code)
	assert.Equal(t, errOrderStale.Code, codeOf(t, res))
	assert.Equal(t, []string{"api_address.reorder:fail"}, auditActions(t, gdb))

	assert.Equal(t, []int{1, 2, 3}, orderedIds(t, gdb), "被拒的重排不该改动任何一行")
}

// id 数量对得上但内容对不上(有人删了一条、又加了一条)同样要被拒。
func TestAdminReorderRejectsSameLengthDifferentIds(t *testing.T) {
	gdb := newTestDB(t)
	seedAddress(t, gdb, "A", "https://a.example.com", 10, true)
	seedAddress(t, gdb, "B", "https://b.example.com", 20, true)

	res := call(t, http.MethodPost, "/api/qy/admin/api-addresses/reorder",
		`{"ids":[1,99]}`, nil, adminReorder)
	assert.Equal(t, http.StatusConflict, res.Code)
	assert.Equal(t, errOrderStale.Code, codeOf(t, res))
}

// 管理端列表要带上 max —— 前端据此决定「新增」按钮能不能点,自己抄一份上限
// 就是同一常量的第二份拷贝。
func TestAdminListCarriesMaxAlongsideItems(t *testing.T) {
	gdb := newTestDB(t)
	seedAddress(t, gdb, "A", "https://a.example.com", 10, false)

	data := dataOf(t, call(t, http.MethodGet, "/api/qy/admin/api-addresses", "", nil, adminList))
	items, ok := data["items"].([]any)
	require.True(t, ok, "响应里没有 items 数组")
	assert.Len(t, items, 1, "管理端必须能看到已停用的地址")
	assert.EqualValues(t, maxAddresses, data["max"])
}

func mustURL(t *testing.T, gdb *gorm.DB, id int) string {
	t.Helper()
	var row Address
	require.NoError(t, gdb.Where("id = ?", id).Take(&row).Error)
	return row.URL
}
