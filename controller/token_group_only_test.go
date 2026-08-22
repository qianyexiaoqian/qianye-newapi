package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// token_group_only_test.go —— 密钥列表里「分组行内快切」那条写入路径。
//
// 被守的东西只有一件,但它是资产级的:`PUT /api/token/` 默认是**整表替换**。
// 行内快切手上只有 id 与新分组,一旦它走上默认路径,用户的令牌名会被清空、
// 有效期会被写成 0(= 立刻过期)、额度会被清零 —— 而请求返回 200。
//
// 所以这里的每一条断言都在回答同一个问题:「我只想换个分组,别的东西还在吗」。

// seedRichToken 造一把**每个字段都非零**的令牌。
//
// 全非零是有意的:group_only 的失败方式是把某个字段写回零值,而零值字段
// 在断言里与"没被改"长得一模一样。
func seedRichToken(t *testing.T, db *gorm.DB, userID int, group string) *model.Token {
	t.Helper()
	allowIps := "1.2.3.4"
	token := &model.Token{
		UserId:             userID,
		Name:               "qy-keep-me",
		Key:                "qy-group-only-" + group,
		Status:             common.TokenStatusEnabled,
		CreatedTime:        11,
		AccessedTime:       22,
		ExpiredTime:        -1,
		RemainQuota:        12345,
		UnlimitedQuota:     false,
		ModelLimitsEnabled: true,
		ModelLimits:        "gpt-4",
		AllowIps:           &allowIps,
		UsedQuota:          777,
		Group:              group,
		CrossGroupRetry:    true,
		AutoGroups:         `["alpha","beta"]`,
	}
	require.NoError(t, db.Create(token).Error)
	return token
}

func reloadToken(t *testing.T, db *gorm.DB, id int) *model.Token {
	t.Helper()
	var got model.Token
	require.NoError(t, db.First(&got, "id = ?", id).Error)
	return &got
}

// callUpdateToken 以 userID 的身份跑一次 UpdateToken。
func callUpdateToken(t *testing.T, target string, body map[string]any, userID int) tokenAPIResponse {
	t.Helper()
	ctx, rec := newAuthenticatedContext(t, http.MethodPut, target, body, userID)
	UpdateToken(ctx)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeAPIResponse(t, rec)
}

// TestUpdateTokenGroupOnlyKeepsEveryOtherField 是行内快切的主用例。
//
// 期望(独立算出来的):除 group 之外**一个字段都不变**;因为新分组不是 auto,
// cross_group_retry 与 auto_groups 按既有规则清掉(与整表替换那条路径一致)。
//
// 变异验证见文件末尾。
func TestUpdateTokenGroupOnlyKeepsEveryOtherField(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedRichToken(t, db, 9001, "auto")

	resp := callUpdateToken(t, "/api/token/?group_only=true",
		map[string]any{"id": token.Id, "group": "vip"}, 9001)
	require.True(t, resp.Success, resp.Message)

	got := reloadToken(t, db, token.Id)
	assert.Equal(t, "vip", got.Group, "分组必须真的改了")
	assert.Equal(t, "qy-keep-me", got.Name, "令牌名不能被清空")
	assert.EqualValues(t, -1, got.ExpiredTime, "有效期不能被写成 0(那等于立刻过期)")
	assert.Equal(t, 12345, got.RemainQuota, "剩余额度不能被清零")
	assert.False(t, got.UnlimitedQuota)
	assert.True(t, got.ModelLimitsEnabled, "模型限制开关不能被关掉")
	assert.Equal(t, "gpt-4", got.ModelLimits)
	require.NotNil(t, got.AllowIps)
	assert.Equal(t, "1.2.3.4", *got.AllowIps, "IP 白名单不能被清空")
	assert.Equal(t, common.TokenStatusEnabled, got.Status)
	// 非 auto 分组上这两项没有意义,与整表替换那条路径同一规则。
	assert.False(t, got.CrossGroupRetry)
	assert.Equal(t, "", got.AutoGroups)
}

// TestUpdateTokenWithoutGroupOnlyOverwritesEverything 是上面那条为什么必须存在。
//
// 它钉住的是上游 UpdateToken 的**整表替换**语义:同一份"只有 id 和 group"的
// 请求体,不带 group_only 时会把令牌名清空、把有效期写成 0。
// 这不是在给缺陷背书 —— 它是行内快切不能复用默认路径的唯一证据,
// 而且哪天上游把 PUT 改成局部更新,这条会立刻变红提醒我们重新评估。
func TestUpdateTokenWithoutGroupOnlyOverwritesEverything(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedRichToken(t, db, 9002, "default")

	resp := callUpdateToken(t, "/api/token/",
		map[string]any{"id": token.Id, "group": "vip"}, 9002)
	require.True(t, resp.Success, resp.Message)

	got := reloadToken(t, db, token.Id)
	assert.Equal(t, "vip", got.Group)
	assert.Equal(t, "", got.Name, "整表替换会把没写的字段按零值覆盖")
	assert.EqualValues(t, 0, got.ExpiredTime, "0 在这里就是「已过期」")
	assert.Equal(t, 0, got.RemainQuota)
}

// TestUpdateTokenGroupOnlyIgnoresStatusInTheBody 守「只改分组」的边界。
//
// 请求体里带一个 status 是很容易发生的(前端把整行对象直接 POST 出去)。
// group_only 必须既不写 status,也不走"启用一把已耗尽令牌"的那两道校验 ——
// 否则用户在一把被停用的密钥上换分组会收到一句「已耗尽的令牌无法启用」,
// 而他根本没点启用。
//
// 变异验证:把 group_only 分支挪到那两道 status 校验之后,并让它顺手写
// cleanToken.Status = token.Status → 两条断言全红。
func TestUpdateTokenGroupOnlyIgnoresStatusInTheBody(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedRichToken(t, db, 9003, "default")
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).
		Updates(map[string]any{
			"status":          common.TokenStatusExhausted,
			"remain_quota":    0,
			"unlimited_quota": false,
		}).Error)

	resp := callUpdateToken(t, "/api/token/?group_only=1",
		map[string]any{"id": token.Id, "group": "vip", "status": common.TokenStatusEnabled}, 9003)
	require.True(t, resp.Success, resp.Message)

	got := reloadToken(t, db, token.Id)
	assert.Equal(t, "vip", got.Group, "已耗尽的令牌一样可以换分组")
	assert.Equal(t, common.TokenStatusExhausted, got.Status, "group_only 不许顺手改状态")
}

// TestUpdateTokenGroupOnlyRefusesGroupsTheUserCannotSelect 守写入侧的可选性判据。
//
// 行内快切与编辑抽屉必须共用同一处判据(service.QyCheckTokenGroupChange)。
// 两套判据分家的表现是「抽屉里存不下的分组、行内能存下」,而那正是
// 孤儿令牌(保存得下、一发请求就 403)的来源。
//
// 变异验证:把 group_only 分支里那次 QyCheckTokenGroupChange 调用删掉
// → 分组被写成 forbidden,两条断言红。
func TestUpdateTokenGroupOnlyRefusesGroupsTheUserCannotSelect(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedRichToken(t, db, 9004, "default")

	prev := service.QyCheckTokenGroupChange
	t.Cleanup(func() { service.QyCheckTokenGroupChange = prev })
	var sawOld, sawNew string
	service.QyCheckTokenGroupChange = func(_ *gin.Context, oldGroup, newGroup string) error {
		sawOld, sawNew = oldGroup, newGroup
		return assertDeniedGroup
	}

	resp := callUpdateToken(t, "/api/token/?group_only=true",
		map[string]any{"id": token.Id, "group": "forbidden"}, 9004)
	assert.False(t, resp.Success, "被判据拒绝时不能报成功")
	assert.Equal(t, assertDeniedGroup.Error(), resp.Message)

	got := reloadToken(t, db, token.Id)
	assert.Equal(t, "default", got.Group, "被拒之后库里必须原样不动")
	assert.Equal(t, "default", sawOld, "旧分组必须取自库里的值,不是请求体")
	assert.Equal(t, "forbidden", sawNew)
}

// TestUpdateTokenGroupOnlyCannotTouchAnotherUsersToken 守越权。
//
// 加载走的是 GetTokenByIds(id, userId),越权时它 ErrRecordNotFound。
// 变异验证:把它换成 model.GetTokenById(id) → 别人的令牌被改,两条断言红。
func TestUpdateTokenGroupOnlyCannotTouchAnotherUsersToken(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedRichToken(t, db, 9005, "default")

	resp := callUpdateToken(t, "/api/token/?group_only=true",
		map[string]any{"id": token.Id, "group": "vip"}, 9006)
	assert.False(t, resp.Success)

	got := reloadToken(t, db, token.Id)
	assert.Equal(t, "default", got.Group, "别人的令牌一个字段都不能动")
}

// assertDeniedGroup 是判据被打桩时返回的固定错误,单独提出来是为了让
// 断言比对的是同一个字面量,而不是各写一遍中文。
var assertDeniedGroup = &deniedGroupError{}

type deniedGroupError struct{}

func (*deniedGroupError) Error() string { return "无权访问 forbidden 分组" }

// TestUpdateTokenGroupOnlySwitchingToAutoMatchesTheDrawer 钉住「同一个动作,
// 两个入口必须落同一份库值」。
//
// 「把令牌切到 auto」在界面上有两条路:编辑抽屉与列表行内快切。抽屉在选中
// auto 时会 form.setValue('cross_group_retry', true)
// (web/src/features/keys/components/api-keys-mutate-drawer.tsx),而
// api-key-form.ts 的 getApiKeyFormDefaultValues 同样把新建 auto 令牌的默认
// 写成 `cross_group_retry: group === 'auto'`。行内快切此前刻意什么都不写,
// 落库是 false。
//
// 这一位不是装饰:cross_group_retry 决定 auto 在组内重试用尽之后要不要换下
// 一个分组接着重试(service/channel_select.go),也就是这次请求最终落在哪个
// 分组、按哪个分组的倍率计费。两个入口给出两种行为,而界面上两处的说明文案
// 是同一句 —— 用户没有任何办法看出自己拿到的是哪一份。
//
// auto_groups 仍然留空,那是「跟随全站 auto 顺序」,与抽屉里新建 auto 令牌
// 的默认一致;要指定顺序仍然只能走抽屉。
func TestUpdateTokenGroupOnlySwitchingToAutoMatchesTheDrawer(t *testing.T) {
	db := setupTokenControllerTestDB(t)

	// 先从一个非 auto 分组出发,并且把 cross_group_retry 造成 false ——
	// 断言里的 true 只能来自这次切换,不可能是"本来就是 true 没被动过"。
	token := seedRichToken(t, db, 9101, "default")
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).
		Updates(map[string]any{"cross_group_retry": false, "auto_groups": ""}).Error)

	resp := callUpdateToken(t, "/api/token/?group_only=true",
		map[string]any{"id": token.Id, "group": "auto"}, 9101)
	require.True(t, resp.Success, resp.Message)

	got := reloadToken(t, db, token.Id)
	assert.Equal(t, "auto", got.Group)
	assert.True(t, got.CrossGroupRetry,
		"行内快切切到 auto 必须与编辑抽屉落同一份库值(抽屉是 true);"+
			"两个入口分家的表现是同一个动作产生两种 relay 行为")
	assert.Equal(t, "", got.AutoGroups,
		"auto_groups 留空 = 跟随全站 auto 顺序,与抽屉里新建 auto 令牌的默认一致")
	// 其余字段照旧一个都不许动。
	assert.Equal(t, "qy-keep-me", got.Name)
	assert.EqualValues(t, -1, got.ExpiredTime)
	assert.Equal(t, 12345, got.RemainQuota)
}
