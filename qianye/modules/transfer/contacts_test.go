package transfer

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// contacts_test.go —— 划转联系人簿(需求 3-C)的行为级回归。
//
// # 断言一律从 HTTP 处理器进
//
// 本仓反复出现的第一种缺陷形状是**断链**:纯函数写对了,调度层/收尾层没接上。
// 联系人尤其容易踩两处:
//
//   - 添加接口自己写一遍"查一下这个用户在不在",于是 recipient_lookup 开关、
//     反枚举日志、按用户限流三道防线一道都没走 —— 而单测 saveContact 照样全绿;
//   - 脱敏函数写对了,列表却直接把 model.User 序列化下去。
//
// 所以下面每个用例都打真正的 handler,并且断言的是**响应体的字节**里
// 有没有出现真实用户名/邮箱,而不是"某个字段等于脱敏值"。

// ───────────────────────────── 脚手架 ─────────────────────────────

// contactTables 是本文件用到的扩展库表。
// GroupRule 必须建:handleAddContact 会先 loadGroupRules(),表不存在就读失败。
func contactTables() []any {
	return []any{&Contact{}, &LookupLog{}, &GroupRule{}, &Order{}, &UserState{}}
}

// newContactsExtDB 建扩展库替身并接到 db.Get()。
//
// 用文件库而不是 ":memory:":handler 里的多条语句不共用同一个连接,
// ":memory:" 按连接隔离会让它们各看到一个空库。
func newContactsExtDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 扩展库固定是 MySQL,db.LockForUpdate 无条件挂 FOR UPDATE,而 sqlite 不认。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(contactTables()...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		_ = sqlDB.Close()
	})
	return gdb
}

// contactConfig 是本文件统一的划转配置:收款人只认纯数字 ID
// (config.RecipientLookupID 是默认档,也是"邮箱必须被拒"那条断言的前提)。
func contactConfig(t *testing.T) {
	t.Helper()
	cfg := baseConfig()
	cfg.NewAccountFreezeHours = 0
	prev := qyConfig.Swap(&config.Config{Enabled: true, Transfer: cfg})
	t.Cleanup(func() { qyConfig.Store(prev) })
}

// seedContactUser 往主库替身里塞一个用户。
//
// 与 grouppolicy 那份 seedMainUser 分开写,是因为这里必须能指定用户名、邮箱
// 与状态 —— 本文件一半的断言就是"这三个值有没有漏出去 / 状态变了列表怎么显示"。
func seedContactUser(t *testing.T, gdb *gorm.DB, id int, username, email string, status int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.User{
		Id: id, Username: username, Email: email,
		AffCode: "aff" + strconv.Itoa(id), Group: "default",
		Quota: 1000, Status: status,
	}).Error)
}

// callContact 驱动一个联系人处理器。userId 就是登录态里的 c.GetInt("id")。
func callContact(t *testing.T, method, target, body string, userId int,
	params gin.Params, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", userId)
	h(c)
	return rec
}

// addContactVia 走真正的 POST 接口加一个联系人,返回响应。
//
// 请求体用 common.Marshal 拼而不是字符串拼接:用例里的备注名故意带控制字符,
// 而 JSON 要求控制字符必须写成 \u00XX —— 手拼会先在解析这一步就 400,
// 那样测的就不是清洗逻辑了。
func addContactVia(t *testing.T, userId int, identifier, alias string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := common.Marshal(addContactRequest{Identifier: identifier, Alias: alias})
	require.NoError(t, err)
	return callContact(t, http.MethodPost, "/transfer/contacts", string(body), userId, nil, handleAddContact)
}

// bodyCode 取出响应体里的业务 code(成功时为空串)。
func bodyCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &out))
	return out.Code
}

// contactItems 解析列表接口的 items。
func contactItems(t *testing.T, rec *httptest.ResponseRecorder) []contactView {
	t.Helper()
	var out struct {
		Data struct {
			Items []contactView `json:"items"`
			Max   int           `json:"max"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, maxContactsPerUser, out.Data.Max,
		"前端要靠 max 渲染\"还能加几个\",漏了它列表会显示成可以无限加")
	return out.Data.Items
}

func lookupLogs(t *testing.T, gdb *gorm.DB) []LookupLog {
	t.Helper()
	var rows []LookupLog
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	return rows
}

func contactRows(t *testing.T, gdb *gorm.DB) []Contact {
	t.Helper()
	var rows []Contact
	require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
	return rows
}

// failMainUserQuery 让主库上所有打到 users 的查询报错。
// 模拟的是"主库抖了一下",而不是"用户没了" —— 这两件事在列表里必须显示成不同的状态。
func failMainUserQuery(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	const name = "test:contacts_fail_user_query"
	require.NoError(t, gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "users" {
			tx.AddError(errors.New("Error 1040: Too many connections"))
		}
	}))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(name) })
}

// ───────────────── 隐私:添加过程必须复用反枚举那一套 ─────────────────

// TestAddContactReusesRecipientLookupDefences 是本条需求最重要的一条断言。
//
// 「输入一个标识,看它返回存在还是不存在」就是用户枚举。/transfer/preview
// 为此挂了三道防线,添加联系人必须走同一条路径而不是自己再查一遍库:
//
//	① recipient_lookup 开关:配成 id 时,邮箱必须被拒(否则联系人接口就成了
//	   一条绕过开关的邮箱探测器);
//	② 每次解析都落一条 qy_transfer_lookup_logs,命中与未命中都要落
//	   —— 慢速枚举只能靠这张表事后发现,限流看不见它;
//	③ 按用户 ID 限流(SearchRateLimit,挂在 module.go 的路由上)。
//
// 回滚验证:把 handleAddContact 里的 resolveRecipient 换成直接调 findRecipient,
// ①②两条断言同时变红(邮箱被接受、日志表为空)。
func TestAddContactReusesRecipientLookupDefences(t *testing.T) {
	ext := newContactsExtDB(t)
	main := newMainDB(t)
	contactConfig(t)
	seedContactUser(t, main, 1, "owner1", "owner1@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 2, "zhangsanfeng", "zhang@example.com", common.UserStatusEnabled)

	rec := addContactVia(t, 1, "2", "老张")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	logs := lookupLogs(t, ext)
	require.Len(t, logs, 1, "添加联系人必须与预览一样落一条反枚举日志")
	assert.Equal(t, 1, logs[0].UserId)
	assert.Equal(t, "2", logs[0].Identifier)
	assert.Equal(t, lookupByID, logs[0].ByType)
	assert.True(t, logs[0].Hit)

	// recipient_lookup = id 时,邮箱不是"查不到",而是压根不该被当成一次查询。
	rec = addContactVia(t, 1, "zhang@example.com", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "qy_contact_user_not_found", bodyCode(t, rec))

	logs = lookupLogs(t, ext)
	require.Len(t, logs, 2, "未命中的解析同样要留痕:只记成功等于把枚举者的痕迹全抹了")
	assert.False(t, logs[1].Hit)
	assert.Len(t, contactRows(t, ext), 1, "被拒的那次绝不能落库")
}

// TestContactListNeverLeaksRealIdentity 钉死脱敏口径。
//
// 断言的是**响应体字节**里不含真实用户名与邮箱,而不是"某个字段等于脱敏值":
// 后者挡不住"多加了一个 email 字段"这种最常见的泄漏形状。
//
// 回滚验证:把 hydrateContacts 里的 maskUsername(u.Username) 换成 u.Username,
// 或给 contactView 加一个 masked_email 字段,断言立刻变红。
func TestContactListNeverLeaksRealIdentity(t *testing.T) {
	newContactsExtDB(t)
	main := newMainDB(t)
	contactConfig(t)
	seedContactUser(t, main, 1, "owner1", "owner1@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 2, "zhangsanfeng", "zhang@example.com", common.UserStatusEnabled)

	created := addContactVia(t, 1, "2", "老张")
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	listed := callContact(t, http.MethodGet, "/transfer/contacts", "", 1, nil, handleListContacts)
	require.Equal(t, http.StatusOK, listed.Code)

	for _, rec := range []*httptest.ResponseRecorder{created, listed} {
		body := rec.Body.String()
		assert.NotContains(t, body, "zhangsanfeng", "真实用户名不得下发")
		assert.NotContains(t, body, "zhang@example.com", "邮箱不得下发")
		// 连脱敏邮箱都不给:确认"是不是这个人"发生在 preview 那一步(有日志、
		// 有限流),列表只需要区分自己存的这几个人。
		assert.NotContains(t, body, "@example.com", "脱敏后的域名同样是对方的标识信息")
	}

	items := contactItems(t, listed)
	require.Len(t, items, 1)
	assert.Equal(t, maskUsername("zhangsanfeng"), items[0].MaskedUsername)
	assert.Equal(t, 2, items[0].UserId)
	assert.Equal(t, "老张", items[0].Alias)
	assert.Equal(t, contactStatusActive, items[0].Status)
}

// ───────────────── 对方注销/封禁后列表怎么显示 ─────────────────

// TestContactSurvivesCounterpartyBanAndDeletion 钉死"记录不会凭空消失"。
//
// 三种情况必须显示成三种不同的状态,且**行始终都在**:
//
//	封禁   → disabled(用户还在,只是转不了)
//	注销   → gone(用户没了,退回添加当时的脱敏快照)
//	主库抖 → unknown(不是"人没了",别吓用户)
//
// 直接把行过滤掉是最容易写出来的实现,而它的后果是用户以为自己的数据丢了。
//
// 回滚验证:让 hydrateContacts 跳过主库里读不到的行 → gone/unknown 两段变红;
// 把 unknown 并进 gone → 第三段变红。
func TestContactSurvivesCounterpartyBanAndDeletion(t *testing.T) {
	ext := newContactsExtDB(t)
	main := newMainDB(t)
	contactConfig(t)
	seedContactUser(t, main, 1, "owner1", "owner1@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 2, "zhangsanfeng", "zhang@example.com", common.UserStatusEnabled)
	require.Equal(t, http.StatusOK, addContactVia(t, 1, "2", "老张").Code)

	list := func() []contactView {
		return contactItems(t, callContact(t, http.MethodGet, "/transfer/contacts",
			"", 1, nil, handleListContacts))
	}

	t.Run("封禁后仍在列表里,标记为不可用", func(t *testing.T) {
		require.NoError(t, main.Model(&model.User{}).Where("id = ?", 2).
			Update("status", common.UserStatusDisabled).Error)
		items := list()
		require.Len(t, items, 1)
		assert.Equal(t, contactStatusDisabled, items[0].Status)
		assert.Equal(t, maskUsername("zhangsanfeng"), items[0].MaskedUsername)
	})

	t.Run("注销后仍在列表里,退回脱敏快照", func(t *testing.T) {
		require.NoError(t, main.Delete(&model.User{}, 2).Error)
		items := list()
		require.Len(t, items, 1, "对方注销不该让用户自己的联系人记录消失")
		assert.Equal(t, contactStatusGone, items[0].Status)
		assert.Equal(t, maskUsername("zhangsanfeng"), items[0].MaskedUsername,
			"读不到对方时要退回添加当时的脱敏快照,否则这一行只剩一串数字 ID")
		assert.Equal(t, 2, items[0].UserId)
	})

	t.Run("主库读失败标 unknown 而不是 gone", func(t *testing.T) {
		failMainUserQuery(t, main)
		items := list()
		require.Len(t, items, 1)
		assert.Equal(t, contactStatusUnknown, items[0].Status,
			"主库抖一下就把整簿子显示成\"已注销\",用户看到的是\"我存的人全没了\"")
	})

	assert.Len(t, contactRows(t, ext), 1, "全程不该有任何行被删掉")
}

// ───────────────── 自己、去重、上限 ─────────────────

// TestAddContactRejectsSelfDuplicateAndOverLimit 锁住三条受理约束。
//
// 回滚验证:去掉 saveContact 里的 ownerId == contactUserId 判定 → 第一段变红;
// 去掉事务内的 dup 预检(并让唯一索引兜底翻译也失效)→ 第二段变红;
// 把 cnt >= maxContactsPerUser 改成 cnt > maxContactsPerUser → 第三段变红。
func TestAddContactRejectsSelfDuplicateAndOverLimit(t *testing.T) {
	ext := newContactsExtDB(t)
	main := newMainDB(t)
	contactConfig(t)
	seedContactUser(t, main, 1, "owner1", "owner1@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 2, "zhangsanfeng", "zhang@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 3, "lisi", "lisi@example.com", common.UserStatusEnabled)

	t.Run("自己不能加自己", func(t *testing.T) {
		rec := addContactVia(t, 1, "1", "我")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "qy_contact_self", bodyCode(t, rec))
		assert.Empty(t, contactRows(t, ext))
	})

	t.Run("同一个对方只能存一条", func(t *testing.T) {
		require.Equal(t, http.StatusOK, addContactVia(t, 1, "2", "老张").Code)
		rec := addContactVia(t, 1, "2", "另一个老张")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "qy_contact_duplicate", bodyCode(t, rec))
		assert.Len(t, contactRows(t, ext), 1)
	})

	t.Run("超过上限拒绝", func(t *testing.T) {
		// 直接补齐到上限:这一段验的是计数闸门,不是添加流程。
		now := common.GetTimestamp()
		for i := 2; i <= maxContactsPerUser; i++ {
			require.NoError(t, ext.Create(&Contact{
				OwnerUserId: 1, ContactUserId: 1000 + i, CreatedAt: now,
			}).Error)
		}
		require.Len(t, contactRows(t, ext), maxContactsPerUser)

		rec := addContactVia(t, 1, "3", "李四")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "qy_contact_limit", bodyCode(t, rec))
		assert.Len(t, contactRows(t, ext), maxContactsPerUser, "被拒的那次绝不能落库")
	})
}

// ───────────────── 备注名清洗 ─────────────────

// TestAcceptAliasStripsUnsafeRunes 锁住备注名的清洗规则。
//
// 备注名与脱敏用户名并排渲染在选择列表里,而它是 owner 自己输入的自由文本:
// 双向覆盖能让一行在视觉上显示成另一个名字,零宽字符能造出两条肉眼完全相同
// 的记录 —— 在一个"点一下就把收款人填好"的控件上,这两件事都会让人转错人。
//
// 回滚验证:删掉 unicode.In(r, unicode.Cf, ...) 那一支,前三个用例同时变红。
func TestAcceptAliasStripsUnsafeRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "双向覆盖被剥掉", in: "老张‮kcatta", want: "老张kcatta"},
		{name: "零宽字符被剥掉", in: "老​张‍", want: "老张"},
		{name: "行分隔符被剥掉", in: "老张 李四", want: "老张李四"},
		{name: "控制字符被剥掉", in: "老张\x01\x7f\n", want: "老张"},
		{name: "非法 UTF-8 的替换符被剥掉", in: "老张" + string([]byte{0xff}), want: "老张"},
		{name: "两侧空白被裁掉", in: "   老张   ", want: "老张"},
		{name: "超长按 rune 截断", in: strings.Repeat("张", maxAliasRunes+10),
			want: strings.Repeat("张", maxAliasRunes)},
		{name: "空备注是合法的", in: "  ", want: ""},
		{name: "正常内容原样保留", in: "老张(工作)", want: "老张(工作)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, acceptAlias(tc.in))
		})
	}
}

// TestAddContactSanitizesAliasEndToEnd 是上一条的断链防护。
//
// acceptAlias 写对了但 handler 没调用,单测照样全绿 —— 这里从 HTTP 进,
// 断言落库与回执里的备注名都是清洗后的值。
//
// 回滚验证:把 handleAddContact 里的 acceptAlias(req.Alias) 换成 req.Alias,
// 本用例变红,而 TestAcceptAliasStripsUnsafeRunes 全绿 —— 这正是"断链"的形状。
func TestAddContactSanitizesAliasEndToEnd(t *testing.T) {
	ext := newContactsExtDB(t)
	main := newMainDB(t)
	contactConfig(t)
	seedContactUser(t, main, 1, "owner1", "owner1@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 2, "zhangsanfeng", "zhang@example.com", common.UserStatusEnabled)

	rec := addContactVia(t, 1, "2", "老张‮​kcatta\x01")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows := contactRows(t, ext)
	require.Len(t, rows, 1)
	assert.Equal(t, "老张kcatta", rows[0].Alias, "落库的备注名必须已清洗")
	assert.NotContains(t, rec.Body.String(), "‮", "回执里也不能带回双向覆盖")

	// 改名走的是另一个 handler,同样要清洗 —— 两个入口漏一个等于没洗。
	renamed := callContact(t, http.MethodPut, "/transfer/contacts/1",
		`{"alias":"新‮名"}`, 1, gin.Params{{Key: "id", Value: "1"}}, handleRenameContact)
	require.Equal(t, http.StatusOK, renamed.Code, renamed.Body.String())
	rows = contactRows(t, ext)
	require.Len(t, rows, 1)
	assert.Equal(t, "新名", rows[0].Alias)
}

// ───────────────── 越权 ─────────────────

// TestContactMutationsAreScopedToOwner 钉死 owner 归属。
//
// 只按 id 查、再在应用层比对 owner,是本仓最常见的越权形状 ——
// 少写一个 if 就变成任意改/删别人的数据。这里的写法是把 owner_user_id
// 放进 WHERE,因此"别人的行"在 SQL 层面就不存在。
//
// 回滚验证:把 renameContact / deleteContact 的 WHERE 里的 owner_user_id 去掉,
// 两段断言立刻变红(user 2 能改掉、能删掉 user 1 的记录)。
func TestContactMutationsAreScopedToOwner(t *testing.T) {
	ext := newContactsExtDB(t)
	main := newMainDB(t)
	contactConfig(t)
	seedContactUser(t, main, 1, "owner1", "owner1@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 2, "intruder", "intruder@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 3, "zhangsanfeng", "zhang@example.com", common.UserStatusEnabled)

	require.Equal(t, http.StatusOK, addContactVia(t, 1, "3", "老张").Code)
	rows := contactRows(t, ext)
	require.Len(t, rows, 1)
	id := strconv.FormatInt(rows[0].Id, 10)
	params := gin.Params{{Key: "id", Value: id}}

	t.Run("别人不能改我的备注", func(t *testing.T) {
		rec := callContact(t, http.MethodPut, "/transfer/contacts/"+id,
			`{"alias":"hacked"}`, 2, params, handleRenameContact)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "qy_contact_not_found", bodyCode(t, rec))
		assert.Equal(t, "老张", contactRows(t, ext)[0].Alias)
	})

	t.Run("别人不能删我的联系人", func(t *testing.T) {
		rec := callContact(t, http.MethodDelete, "/transfer/contacts/"+id,
			"", 2, params, handleDeleteContact)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Len(t, contactRows(t, ext), 1)
	})

	t.Run("别人的簿子也读不到", func(t *testing.T) {
		assert.Empty(t, contactItems(t, callContact(t, http.MethodGet,
			"/transfer/contacts", "", 2, nil, handleListContacts)))
	})

	t.Run("本人可以删", func(t *testing.T) {
		rec := callContact(t, http.MethodDelete, "/transfer/contacts/"+id,
			"", 1, params, handleDeleteContact)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, contactRows(t, ext))
	})
}

// TestRenameContactSucceedsWhenDriverReportsZeroRows 复现 MySQL 的行数陷阱。
//
// MySQL 对"新值与旧值相同"的 UPDATE 返回 affected_rows = 0
// (go-sql-driver 默认不开 CLIENT_FOUND_ROWS)。如果 renameContact 用
// RowsAffected == 0 判"找不到",用户把备注改成和原来一样的字就会收到 404,
// 而记录明明在。SQLite 在这种情况下返回 1,所以这条差异在测试库上不会自然
// 复现 —— 这里用 GORM 回调把 RowsAffected 强制置 0 把它造出来。
//
// 回滚验证:在 renameContact 的 UPDATE 后面加回
// `if res.RowsAffected == 0 { return errContactNotFound }`,本用例变红。
func TestRenameContactSucceedsWhenDriverReportsZeroRows(t *testing.T) {
	ext := newContactsExtDB(t)
	main := newMainDB(t)
	contactConfig(t)
	seedContactUser(t, main, 1, "owner1", "owner1@example.com", common.UserStatusEnabled)
	seedContactUser(t, main, 2, "zhangsanfeng", "zhang@example.com", common.UserStatusEnabled)
	require.Equal(t, http.StatusOK, addContactVia(t, 1, "2", "老张").Code)

	const name = "test:contacts_zero_rows"
	require.NoError(t, ext.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "qy_transfer_contacts" {
			tx.RowsAffected = 0
		}
	}))
	t.Cleanup(func() { _ = ext.Callback().Update().Remove(name) })

	rows := contactRows(t, ext)
	require.Len(t, rows, 1)
	id := strconv.FormatInt(rows[0].Id, 10)

	rec := callContact(t, http.MethodPut, "/transfer/contacts/"+id,
		`{"alias":"新名"}`, 1, gin.Params{{Key: "id", Value: id}}, handleRenameContact)
	require.Equal(t, http.StatusOK, rec.Code, "驱动报 0 行不代表记录不存在")
	assert.Equal(t, "新名", contactRows(t, ext)[0].Alias)

	// 改成与现值相同的备注同样必须成功(这一条走的是提前返回那一支)。
	rec = callContact(t, http.MethodPut, "/transfer/contacts/"+id,
		`{"alias":"新名"}`, 1, gin.Params{{Key: "id", Value: id}}, handleRenameContact)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestContactIdPathParamIsRejectedWhenNotPositiveInteger 锁住路径参数口径。
//
// 走 httpq.PathInt64 而不是自己 strconv:非数字、负号、0、超过 MaxInt64
// 一律 400,而不是被解析成一个能命中别人记录的数。
func TestContactIdPathParamIsRejectedWhenNotPositiveInteger(t *testing.T) {
	newContactsExtDB(t)
	newMainDB(t)
	contactConfig(t)

	for _, raw := range []string{"0", "-1", "abc", "1e3", "9223372036854775808", ""} {
		t.Run("id="+raw, func(t *testing.T) {
			params := gin.Params{{Key: "id", Value: raw}}
			rec := callContact(t, http.MethodDelete, "/transfer/contacts/"+raw,
				"", 1, params, handleDeleteContact)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "qy_invalid_param", bodyCode(t, rec))
		})
	}
}
