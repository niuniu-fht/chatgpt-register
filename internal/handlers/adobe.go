package handlers

import (
	"net/http"

	"chatgpt-register/internal/adobesession"

	"github.com/gin-gonic/gin"
)

func (h *Handler) AdobeStart(c *gin.Context) {
	var in adobesession.StartInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.Adobe.Start(in); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

func (h *Handler) AdobeStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.Adobe.Snapshot())
}

func (h *Handler) AdobeStop(c *gin.Context) {
	h.Adobe.Stop()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdobeFocus(c *gin.Context) {
	if err := h.Adobe.Focus(); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
