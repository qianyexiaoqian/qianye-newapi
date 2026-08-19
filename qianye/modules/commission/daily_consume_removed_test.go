package commission

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDailyConsumeMarksAccountsThatAreGone 钉住"这一行的账号已经没了"必须说出口。
//
// logs 是永久的,users 不是:管理端的删除按钮走软删(model.User 带 gorm.DeletedAt),
// 硬删则整行消失。补名字那条查询没有 Unscoped 时 GORM 会自动补 deleted_at IS NULL,
// 于是一个被删掉的账号在报表里渲染成用户名/分组/邮箱/上线四列全空的一行,与
// "确实没有上线的正常用户"完全分不开;按 id 搜还搜不出来,运营连下钻自查的路
// 都没有。数字全对,缺的是解释 —— 而这张表的整个用途就是"两个数不一样时
// 当场知道为什么",文件头列了七条原因,"账号已被删除"是没被列进去的第八条。
func TestDailyConsumeMarksAccountsThatAreGone(t *testing.T) {
	useConfig(t, &config.Config{Enabled: true, Commission: config.Commission{Enabled: true}})
	newTestDB(t)
	mainDB := useMainDB(t, &model.User{})
	logDB := useLogDB(t)

	const day = "20260803"
	at := dayTs(t, day, 3600)

	require.NoError(t, mainDB.Create(&model.User{
		Id: 601, Username: "qy-alive", DisplayName: "qy-alive", Group: "default", AffCode: "aff601",
	}).Error)
	require.NoError(t, mainDB.Create(&model.User{
		Id: 602, Username: "qy-softdeleted", DisplayName: "qy-softdeleted", Group: "vip", AffCode: "aff602",
	}).Error)
	// 软删 = 管理端删除按钮走的那条路。
	require.NoError(t, mainDB.Delete(&model.User{}, 602).Error)
	var visible int64
	require.NoError(t, mainDB.Model(&model.User{}).Where("id = ?", 602).Count(&visible).Error)
	require.EqualValues(t, 0, visible, "前提不成立:软删之后默认作用域仍然看得见这一行")

	seedLog(t, logDB, 601, at, 1000, model.LogTypeConsume)
	seedLog(t, logDB, 602, at, 50428, model.LogTypeConsume)
	seedLog(t, logDB, 603, at, 20805, model.LogTypeConsume) // users 里根本没有这一行

	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/commission/daily-consume?start_date="+day+"&sort=user_id&order=asc", "",
		adminListDailyConsume)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items []dailyConsumeRow `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Items, 3)

	byId := map[int]dailyConsumeRow{}
	for _, r := range resp.Data.Items {
		byId[r.UserId] = r
	}

	alive := byId[601]
	assert.False(t, alive.AccountRemoved)
	assert.Equal(t, "qy-alive", alive.Username)

	soft := byId[602]
	assert.True(t, soft.AccountRemoved, "软删的账号必须被标出来")
	assert.Equal(t, "qy-softdeleted", soft.Username,
		"标出来还不够:名字要一起给出来,否则运营手上只有一个 id")
	assert.Equal(t, "vip", soft.UserGroup)
	assert.EqualValues(t, 50428, soft.ConsumeQuota)
	assert.EqualValues(t, 50428, soft.UncountedQuota)

	hard := byId[603]
	assert.True(t, hard.AccountRemoved, "users 里已经没有的账号同样必须被标出来")
	assert.Empty(t, hard.Username)
}

// TestDailyConsumeKeywordFindsARemovedAccount 钉住"报表上看得见就必须搜得出来"。
//
// 关键词解析与补名字用的是同一套软删过滤。运营在表里看到一行没有名字的大额
// 消费,第一个动作就是按 id 搜它;搜出空表意味着他连自查的路都没有。
func TestDailyConsumeKeywordFindsARemovedAccount(t *testing.T) {
	useConfig(t, &config.Config{Enabled: true, Commission: config.Commission{Enabled: true}})
	newTestDB(t)
	mainDB := useMainDB(t, &model.User{})
	logDB := useLogDB(t)

	const day = "20260803"
	at := dayTs(t, day, 3600)
	require.NoError(t, mainDB.Create(&model.User{
		Id: 742, Username: "qy-removed", DisplayName: "qy-removed", AffCode: "aff742",
	}).Error)
	require.NoError(t, mainDB.Delete(&model.User{}, 742).Error)
	seedLog(t, logDB, 742, at, 50428, model.LogTypeConsume)

	ids, err := resolveKeywordUserIds(t.Context(), "742")
	require.NoError(t, err)
	assert.Equal(t, []int{742}, ids, "按 id 搜一个已删除账号必须命中")

	ids, err = resolveKeywordUserIds(t.Context(), "qy-removed")
	require.NoError(t, err)
	assert.Equal(t, []int{742}, ids, "按用户名搜同理")
}
