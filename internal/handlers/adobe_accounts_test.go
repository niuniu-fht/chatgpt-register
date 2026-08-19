package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"chatgpt-register/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newAdobeExportTest(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "export.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.AutoMigrate(&models.AdobeRegistration{}); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &Handler{DB: database}
	router.POST("/export/preview", h.AdobeAccountExportPreview)
	router.POST("/export", h.AdobeAccountExport)
	return database, router
}

func TestAdobeAccountExportPreviewDoesNotChangeStatus(t *testing.T) {
	database, router := newAdobeExportTest(t)
	row := models.AdobeRegistration{Email: "preview@example.test", Status: "registered"}
	if err := database.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"ids": []uint{row.ID}})
	req := httptest.NewRequest(http.MethodPost, "/export/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if err := database.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "registered" {
		t.Fatalf("preview changed status to %q", row.Status)
	}
}

func postAdobeExport(t *testing.T, router http.Handler, ids []uint) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"ids": ids})
	req := httptest.NewRequest(http.MethodPost, "/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestAdobeAccountExportMarksRowsExported(t *testing.T) {
	database, router := newAdobeExportTest(t)
	rows := []models.AdobeRegistration{
		{Email: "first@example.test", Status: "registered"},
		{Email: "second@example.test", Status: "registered"},
	}
	if err := database.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	response := postAdobeExport(t, router, []uint{rows[1].ID, rows[0].ID, rows[0].ID})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Count int    `json:"count"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 || result.Text != "first@example.test\nsecond@example.test\n" {
		t.Fatalf("unexpected export: %+v", result)
	}
	var count int64
	if err := database.Model(&models.AdobeRegistration{}).Where("status = ?", "exported").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("exported count=%d", count)
	}
}

func TestAdobeAccountExportRollsBackMixedStatuses(t *testing.T) {
	database, router := newAdobeExportTest(t)
	registered := models.AdobeRegistration{Email: "ready@example.test", Status: "registered"}
	pending := models.AdobeRegistration{Email: "pending@example.test", Status: "pending"}
	if err := database.Create(&registered).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	response := postAdobeExport(t, router, []uint{registered.ID, pending.ID})
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if err := database.First(&registered, registered.ID).Error; err != nil {
		t.Fatal(err)
	}
	if registered.Status != "registered" {
		t.Fatalf("registered row changed to %q", registered.Status)
	}
}
