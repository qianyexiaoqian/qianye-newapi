package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// auditOther 是本 fork 管理审计落在 model.Log.Other 里的形状:op 由
// recordManageAudit 写,audit_info 由 finishAdminAudit 的兜底写。
type auditOther struct {
	Op struct {
		Action string `json:"action"`
		Params struct {
			Count int64 `json:"count"`
			Total int   `json:"total"`
			IDs   []int `json:"requested_redemption_ids"`
		} `json:"params"`
	} `json:"op"`
	AuditInfo struct {
		Success bool `json:"success"`
	} `json:"audit_info"`
}

func TestDeleteRedemptionBatch(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver, logDriver gorm.Dialector
			dbType := common.DatabaseTypeSQLite
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
				logDriver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
				logDSN := os.Getenv("TEST_MYSQL_LOG_DSN")
				if logDSN == "" {
					logDSN = dsn
				}
				logDriver = mysql.Open(logDSN)
				dbType = common.DatabaseTypeMySQL
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
				logDSN := os.Getenv("TEST_POSTGRES_LOG_DSN")
				if logDSN == "" {
					logDSN = dsn
				}
				logDriver = postgres.Open(logDSN)
				dbType = common.DatabaseTypePostgreSQL
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)

			logDB, err := gorm.Open(logDriver, &gorm.Config{})
			require.NoError(t, err)
			logSQL, err := logDB.DB()
			require.NoError(t, err)
			logSQL.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, logSQL.Close()) })
			previousDB, previousLogDB := model.DB, model.LOG_DB
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			previousRedis := common.RedisEnabled
			model.DB, model.LOG_DB = db, logDB
			common.SetDatabaseTypes(dbType, dbType)
			common.RedisEnabled = false
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMain, previousLog)
				common.RedisEnabled = previousRedis
			})
			for _, table := range []any{&model.User{}, &model.Redemption{}} {
				require.False(t, db.Migrator().HasTable(table), "use an empty test database")
				require.NoError(t, db.AutoMigrate(table))
				t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(table)) })
			}
			// 本 fork 的管理审计写在主日志表(model.Log, type=LogTypeManage)里,
			// 没有上游那张 AuditLog 表 —— 那是 rc.34/rc.35 安全重构带的,本仓未合。
			require.False(t, logDB.Migrator().HasTable(&model.Log{}), "use an empty test log database")
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			t.Cleanup(func() { require.NoError(t, logDB.Migrator().DropTable(&model.Log{})) })
			// 被拒的请求由 middleware/audit.go 的 finishAdminAudit 兜底记录,而那一条
			// 是 gopool.Go 异步写的 —— 断言必须等它落库,不能读一次就下结论。
			manageAudits := func() []model.Log {
				var events []model.Log
				if err := logDB.Where("type = ?", model.LogTypeManage).Order("id").Find(&events).Error; err != nil {
					return nil
				}
				return events
			}
			awaitAudits := func(t *testing.T, want int) []model.Log {
				t.Helper()
				require.Eventually(t, func() bool { return len(manageAudits()) == want }, 5*time.Second, 10*time.Millisecond)
				return manageAudits()
			}
			clearAudits := func(t *testing.T) {
				t.Helper()
				require.NoError(t, logDB.Where("type = ?", model.LogTypeManage).Delete(&model.Log{}).Error)
			}
			token := "redemption-audit-test-token"
			admin := model.User{Username: "redemption-audit-admin", Password: "unused", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AccessToken: &token}
			require.NoError(t, db.Create(&admin).Error)
			// UserId 必须落上:生产里 AddRedemption 就是这么写的(controller/redemption.go
			// 的 `UserId: c.GetInt("id")`),而删除路径按发码人分桶。不落的话这批码
			// 谁都删不掉,测的就不是真实形状了。
			codes := make([]model.Redemption, 16)
			for index := range codes {
				codes[index] = model.Redemption{UserId: admin.Id, Name: "selected", Key: fmt.Sprintf("%032d", index+1), Quota: 100, Status: common.RedemptionCodeStatusEnabled}
			}
			codes[1].Status = common.RedemptionCodeStatusUsed
			codes[15].Name = "unselected"
			codes[15].Status = common.RedemptionCodeStatusDisabled
			require.NoError(t, model.DB.Create(&codes).Error)
			router := gin.New()
			router.Use(middleware.RequestId())
			router.POST("/api/redemption/batch", middleware.AdminAuth(), DeleteRedemptionBatch)

			overLimit := make([]int, 1001)
			for index := range overLimit {
				overLimit[index] = codes[0].Id
			}
			oversized, err := common.Marshal(map[string]any{"ids": overLimit})
			require.NoError(t, err)
			for _, body := range []string{"{}", `{"ids":[]}`, `{"ids":null}`, `{"ids":[0]}`, `{"ids":[1,-1]}`, `{"ids":["1"]}`, "{", string(oversized)} {
				t.Run("invalid_"+body[:min(len(body), 30)], func(t *testing.T) {
					clearAudits(t)
					response := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, "/api/redemption/batch", bytes.NewBufferString(body))
					request.Header.Set("Authorization", "Bearer "+token)
					router.ServeHTTP(response, request)
					var result struct {
						Success bool `json:"success"`
					}
					require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
					assert.False(t, result.Success)
					var count int64
					require.NoError(t, model.DB.Model(&model.Redemption{}).Count(&count).Error)
					assert.EqualValues(t, 16, count)
					events := awaitAudits(t, 1)
					var other auditOther
					require.NoError(t, common.UnmarshalJsonStr(events[0].Other, &other))
					assert.Equal(t, "redemption.delete_batch", other.Op.Action)
					assert.False(t, other.AuditInfo.Success)
				})
			}
			_, err = model.BatchDeleteRedemptions(0, nil)
			require.Error(t, err)

			// 发码人分桶:非 root 管理员只能删自己发的码。上游没有这道闸,
			// 从上游合批量删除时补的,判据与单条删除的 requireOwnRedemption 一致。
			otherAdminToken := "redemption-scope-other-admin"
			otherAdmin := model.User{Username: "redemption-scope-other", Password: "unused", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AccessToken: &otherAdminToken, AffCode: "scopeoth"}
			require.NoError(t, db.Create(&otherAdmin).Error)
			scoped, err := model.BatchDeleteRedemptions(otherAdmin.Id, []int{codes[0].Id, codes[2].Id})
			require.NoError(t, err)
			assert.Zero(t, scoped, "别的管理员发的码,非 root 一张都删不掉")
			var stillThere int64
			require.NoError(t, model.DB.Model(&model.Redemption{}).Where("id IN ?", []int{codes[0].Id, codes[2].Id}).Count(&stillThere).Error)
			assert.EqualValues(t, 2, stillThere)
			requestedIDs := make([]int, 0, 17)
			for _, code := range codes[:15] {
				requestedIDs = append(requestedIDs, code.Id)
			}
			requestedIDs = append(requestedIDs, codes[0].Id, 999999)
			payload, err := common.Marshal(map[string]any{"ids": requestedIDs})
			require.NoError(t, err)
			for _, expectedCount := range []int64{15, 0} {
				clearAudits(t)
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/redemption/batch", bytes.NewReader(payload))
				request.Header.Set("Authorization", "Bearer "+token)
				router.ServeHTTP(response, request)
				assert.Equal(t, http.StatusOK, response.Code)
				var result struct {
					Success bool  `json:"success"`
					Data    int64 `json:"data"`
				}
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
				assert.True(t, result.Success)
				assert.Equal(t, expectedCount, result.Data)
				// handler 自己记了一条,recordManageAudit 会置 ContextKeyAuditLogged,
				// 兜底那条因此不写 —— 等够时间再数,能同时证明「没有第二条」。
				events := awaitAudits(t, 1)
				require.Len(t, events, 1, "one operation event, without a duplicate single-delete fallback")
				event := events[0]
				assert.Equal(t, fmt.Sprintf("Batch deleted %d redemption codes", expectedCount), event.Content)
				assert.Equal(t, admin.Id, event.UserId)
				var other auditOther
				require.NoError(t, common.UnmarshalJsonStr(event.Other, &other))
				assert.Equal(t, "redemption.delete_batch", other.Op.Action)
				assert.Equal(t, expectedCount, other.Op.Params.Count)
				assert.Equal(t, len(requestedIDs), other.Op.Params.Total)
				assert.Equal(t, requestedIDs, other.Op.Params.IDs)
				encoded, err := common.Marshal(event)
				require.NoError(t, err)
				assert.NotContains(t, string(encoded), token)
				for _, code := range codes {
					assert.NotContains(t, string(encoded), code.Key)
				}
			}
			var active []model.Redemption
			require.NoError(t, model.DB.Find(&active).Error)
			require.Len(t, active, 1)
			assert.Equal(t, codes[15], active[0])
			var all []model.Redemption
			require.NoError(t, model.DB.Unscoped().Order("id").Find(&all).Error)
			require.Len(t, all, 16)
			for _, code := range all[:15] {
				assert.True(t, code.DeletedAt.Valid)
			}
			assert.False(t, all[15].DeletedAt.Valid)
		})
	}
}
