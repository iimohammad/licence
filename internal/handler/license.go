package handler

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/pkg/apperr"
	"github.com/tabloy/keygate/pkg/response"
)

const (
	maxIdentifierLen = 256
	maxLabelLen      = 256 // characters
)

// cleanIdentifier trims the identifier and checks it, writing a 400 and
// returning false when it is unusable. One made only of spaces passed
// the required check and then took an activation slot as "".
func cleanIdentifier(c *gin.Context, raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	if id == "" {
		response.BadRequest(c, "identifier is required")
		return "", false
	}
	if len(id) > maxIdentifierLen {
		response.BadRequest(c, "identifier too long")
		return "", false
	}
	return id, true
}

type LicenseHandler struct {
	svc *service.LicenseService
}

func NewLicenseHandler(svc *service.LicenseService) *LicenseHandler {
	return &LicenseHandler{svc: svc}
}

func (h *LicenseHandler) Activate(c *gin.Context) {
	var req struct {
		LicenseKey     string `json:"license_key" binding:"required"`
		Identifier     string `json:"identifier" binding:"required"`
		IdentifierType string `json:"identifier_type"`
		Label          string `json:"label"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_key and identifier are required")
		return
	}
	var ok bool
	if req.Identifier, ok = cleanIdentifier(c, req.Identifier); !ok {
		return
	}
	// The column takes only these two; anything else used to reach the
	// database and come back as a 500.
	switch req.IdentifierType {
	case "", "device", "user":
	default:
		response.BadRequest(c, "identifier_type must be device or user")
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if utf8.RuneCountInString(req.Label) > maxLabelLen {
		response.BadRequest(c, "label too long (max 256 characters)")
		return
	}

	productID, _ := c.Get("product_id")
	result, err := h.svc.Activate(c.Request.Context(), service.ActivateInput{
		LicenseKey:     req.LicenseKey,
		Identifier:     req.Identifier,
		IdentifierType: req.IdentifierType,
		Label:          req.Label,
		IPAddress:      c.ClientIP(),
		ProductID:      str(productID),
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	response.OK(c, result)
}

func (h *LicenseHandler) Verify(c *gin.Context) {
	var req struct {
		LicenseKey string `json:"license_key" binding:"required"`
		Identifier string `json:"identifier" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_key and identifier are required")
		return
	}
	var ok bool
	if req.Identifier, ok = cleanIdentifier(c, req.Identifier); !ok {
		return
	}

	productID, _ := c.Get("product_id")
	result, err := h.svc.Verify(c.Request.Context(), service.VerifyInput{
		LicenseKey: req.LicenseKey,
		Identifier: req.Identifier,
		ProductID:  str(productID),
		IPAddress:  c.ClientIP(),
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	response.OK(c, result)
}

func (h *LicenseHandler) Deactivate(c *gin.Context) {
	var req struct {
		LicenseKey string `json:"license_key" binding:"required"`
		Identifier string `json:"identifier" binding:"required"`
		// TargetIdentifier optionally releases another device than the
		// caller's own (issue #34); needs the product's opt-in and the
		// licence's own key.
		TargetIdentifier string `json:"target_identifier"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_key and identifier are required")
		return
	}
	var ok bool
	if req.Identifier, ok = cleanIdentifier(c, req.Identifier); !ok {
		return
	}
	if req.TargetIdentifier != "" {
		if req.TargetIdentifier, ok = cleanIdentifier(c, req.TargetIdentifier); !ok {
			return
		}
	}

	productID, _ := c.Get("product_id")
	err := h.svc.Deactivate(c.Request.Context(), service.DeactivateInput{
		LicenseKey:        req.LicenseKey,
		Identifier:        req.Identifier,
		TargetIdentifier:  req.TargetIdentifier,
		ProductID:         str(productID),
		IPAddress:         c.ClientIP(),
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	response.OK(c, gin.H{"status": "deactivated"})
}

// ListActivations answers a shipped client's "which devices hold this
// license" (issue #34): identifiers, labels and times, so an app can
// offer "free the old laptop" at activation time. Opt-in per product —
// POST /api/v1/license/activations {license_key}.
func (h *LicenseHandler) ListActivations(c *gin.Context) {
	var req struct {
		LicenseKey string `json:"license_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_key is required")
		return
	}
	productID, _ := c.Get("product_id")
	lic, rows, max, err := h.svc.ListDeviceActivations(c.Request.Context(), service.ListActivationsInput{
		LicenseKey: req.LicenseKey,
		ProductID:  str(productID),
		IPAddress:  c.ClientIP(),
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	out := make([]portalActivationView, 0, len(rows))
	for _, a := range rows {
		out = append(out, portalActivationView{
			ID:             a.ID,
			Identifier:     a.Identifier,
			IdentifierType: a.IdentifierType,
			Label:          a.Label,
			IPAddress:      a.IPAddress,
			CreatedAt:      a.CreatedAt,
			LastVerified:   a.LastVerified,
			SeatID:         a.SeatID,
			MemberEmail:    a.MemberEmail,
		})
	}
	response.OK(c, gin.H{
		"license_id": lic.ID,
		"status":     lic.Status,
		"max":        max,
		"count":      len(out),
		"activations": response.Array(out),
	})
}

// writeAppErr translates an AppError into a consistent API response.
func writeAppErr(c *gin.Context, err error) {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		if ae.Details != nil {
			// A refusal that says how long to wait says it in the
			// header too, so a client that reads neither the body nor
			// our error codes still backs off.
			if d, ok := ae.Details.(map[string]any); ok {
				if secs, ok := d["retry_after"].(int); ok && secs > 0 {
					c.Header("Retry-After", strconv.Itoa(secs))
				}
			}
			response.ErrWithDetails(c, ae.Status, ae.Code, ae.Message, ae.Details)
		} else {
			response.Err(c, ae.Status, ae.Code, ae.Message)
		}
		// A 500 carries a cause the client never sees. It was being
		// dropped here, which left every SDK-facing failure —
		// activate, verify, usage, floating — as a bare status code
		// with nothing in the log to explain it. The 4xx answers are
		// self-explanatory and stay quiet.
		if ae.Status >= 500 && ae.Cause != nil {
			response.LogInternal(c, ae.Cause)
		}
		return
	}
	response.Internal(c, err)
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
