package groupmatrix

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 「孤儿令牌修复」这个出口必须真的只修孤儿。
//
// 它的文档第一句写的是「把一条**孤儿**令牌的分组置空」,而此前唯一的前置条件
// 是 tk.Group != "" —— 没有任何一处判断这条令牌是不是孤儿。于是 role=10 可以
// 把任意用户(含 role=100)一条分组完全正常的令牌的分组清掉:令牌分组为空时
// UsingGroup 回落到 users.group,可用模型范围与倍率随之改变。
// 前端只从孤儿清单的样本里取 token_id,但那是客户端约束,服务端零校验。
//
// 判据必须与 buildOrphanReport 同源,所以这里逐格覆盖它的两个桶与那个反例。
func TestTokenRepairReasonOnlyAcceptsRealOrphans(t *testing.T) {
	mainDB := newRepairMainDB(t)
	require.NoError(t, mainDB.Create(&model.User{
		Id: 90001, Username: "qy-repair-owner", Password: "x",
		Group: "vipgrp", Status: common.UserStatusEnabled,
	}).Error)

	// 白名单里只有 vipgrp 这一档,而分组倍率表里额外有一个 other-grp ——
	// 于是 other-grp「在倍率表里但不在属主可选清单里」,正好是 orphan 那个桶。
	useUpstreamGroups(t, map[string]string{"vipgrp": "本组"},
		map[string]float64{"vipgrp": 1, "other-grp": 1})

	cases := []struct {
		name     string
		group    string
		isOrphan bool
	}{
		{"分组不在分组倍率表里(deprecated 桶)", "long-gone-group", true},
		{"分组在倍率表里但不在属主可选清单里(orphan 桶)", "other-grp", true},
		{"分组完全正常 —— 这一条必须被拒", "vipgrp", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, orphan := tokenRepairReason(model.Token{Id: 1, UserId: 90001, Group: tc.group})
			assert.Equal(t, tc.isOrphan, orphan)
		})
	}
}

// 属主账号已经不在了:那条令牌无论如何都用不了,按可修处理。
func TestTokenRepairReasonTreatsMissingOwnerAsRepairable(t *testing.T) {
	newRepairMainDB(t)
	useUpstreamGroups(t, map[string]string{"vipgrp": "本组"}, map[string]float64{"vipgrp": 1})

	_, orphan := tokenRepairReason(model.Token{Id: 1, UserId: 777777, Group: "vipgrp"})
	assert.True(t, orphan)
}

func newRepairMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&model.User{}, &model.Token{}))
	prev := model.DB
	prevType := common.MainDatabaseType()
	model.DB = gdb
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = prev
		common.SetMainDatabaseType(prevType)
	})
	return gdb
}
