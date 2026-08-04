package ticket

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	_ "unsafe" // go:linkname

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/imagestore"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本模块的业务函数一律走 db.Get() 自取句柄(与生产代码一致),所以测试必须
// 真的把测试库接上去,而不是把 *gorm.DB 传进被测函数 —— 后者测的是一条
// 生产代码里不存在的调用形态。链接到 db 包的私有句柄是本仓既有做法
// (见 modules/withdraw/pagination_handler_test.go)。

//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

// newTestDB 建一个只承载工单相关表的内存库。
//
// 本模块的缺陷面全都住在 WHERE 条件里(内部备注的可见性、四道闸门的 0 语义、
// 图片保留期只对已关闭工单生效),只有真的让数据库执行一遍那条查询才能验证。
// mock 掉 GORM 等于把被测对象换成了测试自己写的假设。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Ticket{}, &Message{}, &Attachment{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// newEnv 接上测试库并加载一份可用配置,返回句柄。
func newEnv(t *testing.T, extraTicketYAML string) *gorm.DB {
	t.Helper()
	loadTicketConfig(t, extraTicketYAML)
	gdb := newTestDB(t)
	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
	})
	return gdb
}

// loadTicketConfig 走真实的 config.Load(),于是 config.Path() 指向 t.TempDir()
// 下的临时文件,attachmentStore.Dir() 也就自然落在临时目录里 ——
// 测试之间天然隔离,不需要任何全局替身。
func loadTicketConfig(t *testing.T, extra string) {
	t.Helper()
	yaml := `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
ticket:
  enabled: true
` + extra
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
}

// seedTicket 插入一张工单。单号从 no 派生,调用方只要给不同的单号就不会冲突。
func seedTicket(t *testing.T, gdb *gorm.DB, no string, mutate func(*Ticket)) *Ticket {
	t.Helper()
	now := common.GetTimestamp()
	tk := &Ticket{
		TicketNo:    no,
		UserId:      1,
		Username:    "alice",
		Title:       "标题 " + no,
		Priority:    PriorityNormal,
		Status:      StatusOpen,
		LastReplyAt: now,
		LastReplyBy: qymodel.ActorUser,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if mutate != nil {
		mutate(tk)
	}
	require.NoError(t, gdb.Create(tk).Error)
	return tk
}

// seedAttachment 插入一行图片元数据并把文件真的写到磁盘上。
//
// 必须真的落盘:清理路径的正确性有一半在"文件到底删没删",只写库行的话
// purgeBatch 里那句 Remove 的返回值永远是"文件本就不存在",测不出任何东西。
func seedAttachment(t *testing.T, gdb *gorm.DB, ref string, mutate func(*Attachment)) *Attachment {
	t.Helper()
	name, err := newStoredNameForTest()
	require.NoError(t, err)
	a := &Attachment{
		Ref:        ref,
		UserId:     1,
		StoredName: name,
		MimeType:   "image/png",
		Size:       int64(len(pngBytes)),
		CreatedAt:  common.GetTimestamp(),
	}
	if mutate != nil {
		mutate(a)
	}
	require.NoError(t, attachmentStore.Write(a.StoredName, pngBytes))
	require.NoError(t, gdb.Create(a).Error)
	return a
}

// pngBytes 只造出足以通过魔数判定的最小前缀:被测代码刻意不解码图片
// (解码才是解压炸弹的风险面),后面是什么内容无关紧要。
var pngBytes = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("payload")...)

func newStoredNameForTest() (string, error) { return imagestore.NewStoredName("png") }

// attachmentExistsOnDisk 判断一张图是否还躺在磁盘上。
func attachmentExistsOnDisk(t *testing.T, storedName string) bool {
	t.Helper()
	_, err := attachmentStore.Locate(storedName)
	return err == nil
}
