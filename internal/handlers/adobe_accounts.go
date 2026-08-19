package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"chatgpt-register/internal/adobeproducer"
	"chatgpt-register/internal/adobereg"
	"chatgpt-register/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type adobeAccountInput struct {
	Email      string `json:"email" binding:"required"`
	Password   string `json:"password"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	BirthYear  int    `json:"birth_year"`
	BirthMonth int    `json:"birth_month"`
	Country    string `json:"country"`
	Note       string `json:"note"`
}

type adobeAccountExportInput struct {
	IDs []uint `json:"ids" binding:"required"`
}

func (h *Handler) AdobeAccountList(c *gin.Context) {
	var rows []models.AdobeRegistration
	q := h.DB.Model(&models.AdobeRegistration{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if keyword := strings.TrimSpace(c.Query("q")); keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("email LIKE ? OR first_name LIKE ? OR last_name LIKE ? OR note LIKE ?", like, like, like, like)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	var total int64
	q.Count(&total)
	if err := q.Order("created_at desc, id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for i := range rows {
		rows[i].Password = ""
		rows[i].Log = ""
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": total, "page": page, "size": size})
}

func (h *Handler) AdobeAccountCreate(c *gin.Context) {
	var in adobeAccountInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	row, err := adobeRow(in)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.DB.Create(&row).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	row.Password = ""
	c.JSON(http.StatusCreated, row)
}

func (h *Handler) AdobeAccountImport(c *gin.Context) {
	var in struct {
		Lines string `json:"lines" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created, skipped := 0, 0
	for _, email := range strings.Fields(in.Lines) {
		firstName, lastName := adobereg.RandomName()
		input := adobeAccountInput{
			Email: email, FirstName: firstName, LastName: lastName,
			BirthYear: 1994, BirthMonth: 6, Country: "SG",
		}
		row, err := adobeRow(input)
		if err != nil || h.DB.Create(&row).Error != nil {
			skipped++
			continue
		}
		created++
	}
	c.JSON(http.StatusOK, gin.H{"created": created, "skipped": skipped})
}

func normalizeAdobeExportIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	result := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func adobeExportEmails(rows []models.AdobeRegistration) ([]string, bool) {
	emails := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Status != "registered" {
			return nil, false
		}
		emails = append(emails, row.Email)
	}
	return emails, true
}

func (h *Handler) AdobeAccountExportPreview(c *gin.Context) {
	var in adobeAccountExportInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要出库的账号"})
		return
	}
	ids := normalizeAdobeExportIDs(in.IDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要出库的账号"})
		return
	}
	var rows []models.AdobeRegistration
	if err := h.DB.Select("id", "email", "status").Where("id IN ?", ids).Order("id asc").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(rows) != len(ids) {
		c.JSON(http.StatusConflict, gin.H{"error": "所选账号不存在或已被删除"})
		return
	}
	emails, ok := adobeExportEmails(rows)
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "仅已注册账号可以出库"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "count": len(emails), "emails": emails,
		"text": strings.Join(emails, "\n") + "\n",
	})
}

func (h *Handler) AdobeAccountExport(c *gin.Context) {
	var in adobeAccountExportInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要出库的账号"})
		return
	}
	ids := normalizeAdobeExportIDs(in.IDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要出库的账号"})
		return
	}

	tx := h.DB.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	defer tx.Rollback()
	var rows []models.AdobeRegistration
	if err := tx.Select("id", "email", "status").Where("id IN ?", ids).Order("id asc").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(rows) != len(ids) {
		c.JSON(http.StatusConflict, gin.H{"error": "所选账号不存在或已被删除"})
		return
	}
	emails, ok := adobeExportEmails(rows)
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "仅已注册账号可以出库"})
		return
	}
	result := tx.Model(&models.AdobeRegistration{}).
		Where("id IN ? AND status = ?", ids, "registered").
		Updates(map[string]any{"status": "exported", "note": ""})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}
	if result.RowsAffected != int64(len(ids)) {
		c.JSON(http.StatusConflict, gin.H{"error": "账号状态已变化，请刷新后重试"})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	text := strings.Join(emails, "\n") + "\n"
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": len(emails), "emails": emails, "text": text})
}

func (h *Handler) AdobeAccountUpdate(c *gin.Context) {
	var row models.AdobeRegistration
	if err := h.DB.First(&row, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if row.Status == "registering" {
		c.JSON(http.StatusConflict, gin.H{"error": "注册中的账号暂不可编辑"})
		return
	}
	var in adobeAccountInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := adobeRow(in)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updates := map[string]any{
		"email": updated.Email, "first_name": updated.FirstName, "last_name": updated.LastName,
		"birth_year": updated.BirthYear, "birth_month": updated.BirthMonth,
		"country": updated.Country, "note": updated.Note,
	}
	if in.Password != "" {
		updates["password"] = in.Password
	}
	if err := h.DB.Model(&row).Updates(updates).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdobeAccountDelete(c *gin.Context) {
	result := h.DB.Where("id = ? AND status <> ?", c.Param("id"), "registering").Delete(&models.AdobeRegistration{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "账号不存在或正在注册"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdobeAccountRetry(c *gin.Context) {
	result := h.DB.Model(&models.AdobeRegistration{}).
		Where("id = ? AND status = ?", c.Param("id"), "register_failed").
		Updates(map[string]any{"status": "pending", "note": ""})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "仅注册失败的账号可重试"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdobeAccountStop(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "账号 ID 不正确"})
		return
	}
	if !h.AdobeProducer.StopAccount(uint(id)) {
		c.JSON(http.StatusConflict, gin.H{"error": "该账号当前未在运行"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdobeAccountLog(c *gin.Context) {
	var row models.AdobeRegistration
	if err := h.DB.First(&row, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"email": row.Email, "status": row.Status, "note": row.Note,
		"log": row.Log, "has_shot": len(row.Shot) > 0,
	})
}

func (h *Handler) AdobeAccountShot(c *gin.Context) {
	var row models.AdobeRegistration
	if err := h.DB.Select("shot").First(&row, c.Param("id")).Error; err != nil || len(row.Shot) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "暂无异常截图"})
		return
	}
	c.Data(http.StatusOK, "image/png", row.Shot)
}

func (h *Handler) AdobeProduce(c *gin.Context) {
	var in adobeproducer.StartOptions
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.AdobeProducer.Start(in); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

func (h *Handler) AdobeProduceStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.AdobeProducer.Snapshot())
}

func (h *Handler) AdobeProduceStop(c *gin.Context) {
	h.AdobeProducer.Stop()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func adobeRow(in adobeAccountInput) (models.AdobeRegistration, error) {
	in.Email = strings.TrimSpace(in.Email)
	if !strings.Contains(in.Email, "@") {
		return models.AdobeRegistration{}, gorm.ErrInvalidData
	}
	if in.BirthYear == 0 {
		in.BirthYear = 1994
	}
	if in.BirthMonth == 0 {
		in.BirthMonth = 6
	}
	if in.BirthYear < 1900 || in.BirthYear > 2007 || in.BirthMonth < 1 || in.BirthMonth > 12 {
		return models.AdobeRegistration{}, gorm.ErrInvalidData
	}
	return models.AdobeRegistration{
		Email: in.Email, Password: in.Password,
		FirstName: strings.TrimSpace(in.FirstName), LastName: strings.TrimSpace(in.LastName),
		BirthYear: in.BirthYear, BirthMonth: in.BirthMonth,
		Country: strings.ToUpper(strings.TrimSpace(in.Country)),
		Status:  "pending", Note: strings.TrimSpace(in.Note),
	}, nil
}
