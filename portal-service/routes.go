package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *PortalHandler) {
	r.POST("/lookup", h.HandleLookup)
	r.GET("/healthz", h.HandleHealth)
}
