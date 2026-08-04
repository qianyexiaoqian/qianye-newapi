package ticket

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/service/imagestore"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 工单图片附件。
//
// 落盘/取回/清理的机制全部来自 qianye/service/imagestore —— 它与提现凭证
// 共用同一份实现,而不是照着抄一遍。那个包的包注释逐条列了这条路上的每一道
// 防线以及漏掉任何一条的后果;这里只写工单自己的三件事:
// 什么时候允许传、一张图归哪条消息、什么时候该从磁盘上消失。

const (
	// attachmentDirName 是落盘目录名,与配置文件同级(即 Docker/本地下的 data/)。
	attachmentDirName = "qy-ticket-images"

	// attachmentOrphanSeconds 是"上传了却一直没提交消息"的容忍窗口。
	//
	// 固化在代码里而不是新增配置项:它的语义完全依附于"用户写一条工单要多久",
	// 24 小时对任何人都绰绰有余,单独放出来只会变成又一个"定义了却没人调"的旋钮
	// (那正是 config/selfcheck.go 里记着的四次翻车)。
	attachmentOrphanSeconds = 24 * 3600
)

// attachmentStore 是工单图片的落盘目录。
var attachmentStore = imagestore.New(attachmentDirName)

// ImageAcceptMimes 供 /ticket/config 下发给前端做 accept 属性。
// 前端的 accept 只是体验,真正的判定是魔数。
func ImageAcceptMimes() []string { return imagestore.AcceptMimes() }

// pendingUploadMax 是单个用户同时挂着的未绑定上传数上限。
//
// 从 image_max_per_message 派生("最多两条消息的量"),而不是再开一个配置项,
// 也不是拍一个常数:站点把单条消息的图片数调到 20 之后,一个写死的 10 会让
// 正常用户在传第 11 张时被拒,而那道闸本来是给"只上传、不提交"准备的。
//
// 这道闸必须存在:没有它,一个登录用户可以用"只传图、不发消息"把磁盘打满 ——
// 而工单本身的那几道闸(daily_max_count / max_open_per_user)一个都拦不到
// 这条路径,因为它压根没有创建工单。
func pendingUploadMax() int {
	n := config.Get().Ticket.ImageMaxPerMessage
	if n <= 0 {
		n = 1
	}
	return n * 2
}

// acceptImageUpload 落盘一张图片并登记元数据。
//
// 顺序是【先写库行、再写文件】。反过来的话,写完文件而插库失败会留下一个
// 没有任何行指向它的孤儿文件 —— 磁盘上永远清不掉,因为清理任务是按库行扫的。
// 现在这个方向最坏只会留下一行指向不存在文件的元数据,而下载与清理都能优雅处理。
func acceptImageUpload(c *gin.Context, userId int) (*Attachment, error) {
	cfg := config.Get().Ticket
	if !cfg.ImageOn() {
		return nil, errImageDisabled
	}
	maxBytes := cfg.ImageMaxBytes
	if maxBytes <= 0 || maxBytes > config.MaxTicketImageBytes {
		// 校验器已经挡过一次,这里是第二道:配置热更新之后仍要有确定的上界。
		maxBytes = config.MaxTicketImageBytes
	}

	data, kind, err := imagestore.Accept(c, "file", maxBytes)
	if err != nil {
		return nil, uploadError(err)
	}

	storedName, err := imagestore.NewStoredName(kind.Ext)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	row := &Attachment{
		Ref:        strings.ReplaceAll(common.GetUUID(), "-", ""),
		UserId:     userId,
		StoredName: storedName,
		MimeType:   kind.Mime,
		Size:       int64(len(data)),
		Sha256:     hex.EncodeToString(sum[:]),
		CreatedAt:  common.GetTimestamp(),
	}

	// 计数与插入同事务:分开做的话,并发提交会双双读到旧计数一起通过,
	// 上限形同虚设。
	limit := pendingUploadMax()
	quota := cfg.ImageUserQuotaBytes
	err = db.Get().Transaction(func(tx *gorm.DB) error {
		var cnt int64
		if err := tx.Model(&Attachment{}).
			Where("user_id = ? AND ticket_id = 0 AND purged_at = 0", userId).
			Count(&cnt).Error; err != nil {
			return err
		}
		if cnt >= int64(limit) {
			return errImagePending
		}
		// 单人磁盘总量闸。pendingUploadMax 只管"未绑定"那一档 —— 图片一旦随
		// 消息提交,那个计数就归零,于是它对"已经落进工单里的字节"完全没有约束。
		// 而工单的其他几道闸也拦不到这条路:自动关闭只碰 replied,一个每次收到
		// 客服回复就再补一句的人可以让自己的单永远不进入可清理状态,他的图片
		// 因此永远不会被 pruneAttachments 扫到。没有这一条,单个账号即可在宿主机
		// 上钉死数 GiB 且没有任何后台任务能回收。
		if quota > 0 {
			var used int64
			if err := tx.Model(&Attachment{}).
				Where("user_id = ? AND purged_at = 0", userId).
				Select("COALESCE(SUM(size), 0)").Scan(&used).Error; err != nil {
				return err
			}
			if used+row.Size > quota {
				return errImageQuota
			}
		}
		return tx.Create(row).Error
	})
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}

	if err := attachmentStore.Write(storedName, data); err != nil {
		// 文件没写成,这一行就是纯垃圾。删不掉也不要紧:它的 ticket_id 恒为 0,
		// 孤儿清理会在窗口到期后连同"文件本就不存在"一起收掉。
		if delErr := db.Get().Where("id = ?", row.Id).Delete(&Attachment{}).Error; delErr != nil {
			db.MarkFailure(delErr)
		}
		common.SysError("qianye/ticket: 图片落盘失败: " + err.Error())
		return nil, errImageStore
	}
	return row, nil
}

// uploadError 把 imagestore 的哨兵错误翻译成工单自己的业务 code。
func uploadError(err error) error {
	switch {
	case errors.Is(err, imagestore.ErrTooLarge):
		return errImageTooLarge
	case errors.Is(err, imagestore.ErrType):
		return errImageType
	default:
		return errImageRequired
	}
}

// bindAttachments 把若干张已上传的图片挂到刚落库的消息上。
//
// 每一张都是一条带条件的 UPDATE,而不是"先查出来看看是不是本人的、是不是
// 还没用过、再更新":先读后写在并发下必然出现同一张图被两条消息同时认领。
// 四个条件缺一不可 ——
//
//	ref        这一张
//	user_id    只能是上传者本人的(越权的唯一防线,不能放到取回来之后再比)
//	ticket_id  必须还没被别的消息认领(一张图一条消息)
//	purged_at  已被清理的图片不能再被引用
//
// 必须与消息插入在同一个事务里:提交失败时图片要跟着回到未使用状态,
// 否则用户重试一次就会被告知"图片不存在",而他明明刚传过。
func bindAttachments(tx *gorm.DB, t *Ticket, m *Message, refs []string) error {
	for _, ref := range refs {
		res := tx.Model(&Attachment{}).
			Where("ref = ? AND user_id = ? AND ticket_id = 0 AND purged_at = 0", ref, m.AuthorId).
			Updates(map[string]any{
				"ticket_id":  t.Id,
				"message_id": m.Id,
				"ticket_no":  t.TicketNo,
				"bound_at":   common.GetTimestamp(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errImageNotFound
		}
	}
	return nil
}

// loadAttachmentsOfTicket 取出一张工单下全部未清理的图片,按消息分组。
//
// 一次查询取全,而不是每条消息各查一次:一张工单可以有上百条消息,
// 逐条查是 N+1,而这张表按 ticket_id 有索引、每张单的行数是几十的量级。
func loadAttachmentsOfTicket(ticketId int64) (map[int64][]Attachment, error) {
	rows := make([]Attachment, 0, 8)
	err := db.Get().Where("ticket_id = ? AND purged_at = 0", ticketId).
		Order("id asc").Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	out := make(map[int64][]Attachment, len(rows))
	for i := range rows {
		out[rows[i].MessageId] = append(out[rows[i].MessageId], rows[i])
	}
	return out, nil
}

// loadAttachment 按 ref 取一行元数据。
//
// **不在这里判 purged_at**:410"已按保留期清理"与 400"没有这张图"是可区分的
// 两个回答,把它放在取行这一步意味着它会在任何鉴权之前返回 —— 于是任意登录用户
// 拿一个别人工单里的过期 ref 就能确认"这个 ref 确实存在过"。到期判定属于
// 【鉴权之后】的展示层,由调用方在确认归属之后调 purged() 完成。
func loadAttachment(ref string) (*Attachment, error) {
	var row Attachment
	err := db.Get().Where("ref = ?", ref).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errImageNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return &row, nil
}

// userCanSeeAttachment 判定一张图片能否回给这个普通用户。
//
// 两条口径,顺序是先 A 后 B(A 不需要额外查库):
//
//	A. 这张图是我传的(还没绑定到任何工单时,只有这一条能成立)
//	B. 这张图挂在我的工单上的一条**对我可见**的消息里
//
// B 的后半句不能省。用户端详情接口在 SQL 层滤掉了内部备注的正文,但附图挂在
// 同一条备注上、走的却是另一条接口 —— 只判"这张单是我的"的话,客服贴在内部
// 备注里的截图(风控画像、别人的对账截图)对被投诉的那个用户就是可取回的,
// 唯一的屏障是 ref 猜不到,而"猜不到"从来不是授权。
func userCanSeeAttachment(userId int, a *Attachment) (bool, error) {
	if a.UserId == userId {
		return true, nil
	}
	if a.TicketId == 0 {
		return false, nil
	}
	if _, err := loadUserTicket(userId, a.TicketId); err != nil {
		if errors.Is(err, errNotFound) {
			return false, nil
		}
		return false, err
	}
	var visible int64
	err := db.Get().Model(&Message{}).
		Where("id = ? AND ticket_id = ? AND internal = ?", a.MessageId, a.TicketId, false).
		Count(&visible).Error
	if err != nil {
		db.MarkFailure(err)
		return false, err
	}
	return visible > 0, nil
}

// discardPendingUpload 丢弃一张【自己上传且尚未随任何消息提交】的图片。
//
// 四个条件全部写进 WHERE:
//
//	ref / user_id   只能丢自己的
//	ticket_id = 0   已经进了对话的图片不可撤回 —— 消息是 append-only 的,
//	                它的附图同样是"当时到底附了什么"的一部分
//	purged_at = 0   已清理的行留着当证据,不再删
//
// 先删库行、再删文件:反过来的话,文件删了而库行还在,serveAttachment 会把
// 一张"存在但打不开"的图报成已清理;而现在这个方向最坏留下一个没有行指向的
// 文件,由下面那次 Remove 负责,失败也只是多占一份磁盘并留日志。
func discardPendingUpload(userId int, ref string) error {
	var row Attachment
	err := db.Get().Where("ref = ? AND user_id = ? AND ticket_id = 0 AND purged_at = 0", ref, userId).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errImageNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	res := db.Get().Where("id = ? AND ticket_id = 0 AND purged_at = 0", row.Id).Delete(&Attachment{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 这一瞬间它被别的请求绑定到了一条消息上。
		return errImageNotFound
	}
	if err := attachmentStore.Remove(row.StoredName); err != nil {
		common.SysError("qianye/ticket: 丢弃图片时删除文件失败: " + err.Error())
	}
	return nil
}

// serveAttachment 把图片回给**已鉴权**的调用者。鉴权在调用方完成。
func serveAttachment(c *gin.Context, a *Attachment) {
	full, err := attachmentStore.Locate(a.StoredName)
	if errors.Is(err, imagestore.ErrMalformedName) {
		common.SysError("qianye/ticket: 图片元数据里的文件名形状非法: " + a.StoredName)
		respondErr(c, errImageNotFound)
		return
	}
	if err != nil {
		// 库里有行、磁盘上没有文件:多节点各存各的时最常见的一种表现,
		// 也可能是落盘失败留下的残行。不回 500 —— 它不是服务端故障。
		respondErr(c, errImagePurged)
		return
	}
	imagestore.Serve(c, full, a.MimeType, downloadName(a))
}

// downloadName 是回给浏览器的文件名。
// 由 ref + 服务端扩展名拼出,永远不含用户提供的任何字符串。
func downloadName(a *Attachment) string {
	_, ext, _ := strings.Cut(a.StoredName, ".")
	return "ticket-" + a.Ref + "." + ext
}
