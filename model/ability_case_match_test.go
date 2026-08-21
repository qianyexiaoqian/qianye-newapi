package model

import (
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// ability_case_match_test.go —— 渠道选路必须按「分组名与模型名逐字相等」收敛。
//
// 这条判据守的是一个跨库分歧:`WHERE group = ? AND model = ?` 在 MySQL 上按
// utf8mb4_0900_ai_ci 比(大小写不敏感),在 PostgreSQL 与 SQLite 上逐字节比
// (大小写敏感)。同一份 abilities、同一个请求,一边 200 计费成功、一边 503
// 「无可用渠道」—— 迁库当天表现成一片"渠道挂了"。
//
// 收敛方向选严格:内存缓存选路走 Go map(本来就逐字节比)、定价查 Go map、
// 上游模型 ID 本身大小写敏感。松的那一档还制造过"渠道找得到、价格找不到"的
// 400 —— 一个从未被配过价的模型名被 ci 排序规则路由了出去。

func TestFilterAbilitiesRequiresExactGroupAndModel(t *testing.T) {
	candidates := []Ability{
		{Group: "default", Model: "QY-CASE-MODEL", ChannelId: 1, Enabled: true},
		{Group: "default", Model: "qy-case-model", ChannelId: 2, Enabled: true},
		{Group: "VIP", Model: "qy-case-model", ChannelId: 3, Enabled: true},
	}

	cases := []struct {
		name       string
		group      string
		model      string
		wantChanId []int
	}{
		{
			name:       "逐字相等才留下",
			group:      "default",
			model:      "qy-case-model",
			wantChanId: []int{2},
		},
		{
			name:       "模型名只差大小写:MySQL 的 ci 排序规则会把它捞出来,这里必须丢掉",
			group:      "default",
			model:      "Qy-Case-Model",
			wantChanId: []int{},
		},
		{
			name:       "分组名只差大小写同样丢掉:VIP 与 vip 不是同一个池子",
			group:      "vip",
			model:      "qy-case-model",
			wantChanId: []int{},
		},
		{
			name:       "分组名逐字相等时正常返回",
			group:      "VIP",
			model:      "qy-case-model",
			wantChanId: []int{3},
		},
		{
			name:       "同名不同大小写的两行同时在库里时,只认送进来的那一个",
			group:      "default",
			model:      "QY-CASE-MODEL",
			wantChanId: []int{1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterAbilitiesByExactGroupAndModel(candidates, tc.group, tc.model)
			ids := make([]int, 0, len(got))
			for _, ability := range got {
				ids = append(ids, ability.ChannelId)
			}
			assert.Equal(t, tc.wantChanId, ids)
		})
	}
}

// 真库判据:只有在**大小写不敏感**的库上才看得见这个缺陷,所以必须打 MySQL。
// 不设 QY_TEST_MYSQL_DSN 就干净 SKIP(sqlite 与 PostgreSQL 本来就是严格的,
// 在它们上面跑这条用例证明不了任何事)。
func TestGetChannelRejectsCaseVariantModelOnMySQL(t *testing.T) {
	dsn := os.Getenv("QY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 QY_TEST_MYSQL_DSN,跳过(大小写不敏感的排序规则只有真 MySQL 有)")
	}

	gdb, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&Ability{}, &Channel{}))

	prevDB := DB
	prevMain, prevLog := common.MainDatabaseType(), common.LogDatabaseType()
	DB = gdb
	common.SetDatabaseTypes(common.DatabaseTypeMySQL, prevLog)
	InitCol()
	t.Cleanup(func() {
		gdb.Exec("DELETE FROM abilities WHERE model LIKE 'qy-case-%'")
		gdb.Exec("DELETE FROM channels WHERE id = ?", 990771)
		DB = prevDB
		common.SetDatabaseTypes(prevMain, prevLog)
		InitCol()
	})

	require.NoError(t, gdb.Exec("DELETE FROM abilities WHERE model LIKE 'qy-case-%'").Error)
	priority := int64(0)
	require.NoError(t, gdb.Create(&Channel{Id: 990771, Name: "qy-case-probe", Priority: &priority}).Error)
	require.NoError(t, gdb.Create(&Ability{
		Group: "default", Model: "QY-CASE-MODEL", ChannelId: 990771,
		Enabled: true, Priority: &priority, Weight: 0,
	}).Error)

	// ① 先证明这个库确实是大小写不敏感的 —— 否则本用例什么也没验到。
	var ciHits int64
	require.NoError(t, gdb.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and enabled = ?", "default", "qy-case-model", true).
		Count(&ciHits).Error)
	require.Equal(t, int64(1), ciHits,
		"这个 MySQL 的排序规则不是 ci,本用例证明不了任何事")

	// ② 逐字相等的请求照常路由。
	channel, err := GetChannel("default", "QY-CASE-MODEL", 0, "")
	require.NoError(t, err)
	require.NotNil(t, channel, "逐字相等的模型名必须能路由到渠道")
	assert.Equal(t, 990771, channel.Id)

	// ③ 只差大小写的请求必须落空,与 PostgreSQL / SQLite 同口径。
	channel, err = GetChannel("default", "qy-case-model", 0, "")
	require.NoError(t, err)
	assert.Nil(t, channel,
		"模型名只差大小写时不得路由:PostgreSQL 与 SQLite 上它是 503,MySQL 不能给出 200")

	// ④ 分组名那一半同理。
	channel, err = GetChannel("DEFAULT", "QY-CASE-MODEL", 0, "")
	require.NoError(t, err)
	assert.Nil(t, channel, "分组名只差大小写时不得路由:VIP 与 vip 不是同一个池子")
}
