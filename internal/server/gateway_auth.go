package server

import (
	"errors"
	"log"
	"strings"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/logfmt"
	"workbuddy2api/internal/upstream"
)

// recoverGatewayAuth verifies an edge 401 with refresh before treating a
// credential as dead. The caller rotates normally and never exposes auth errors.
func (h *Handler) recoverGatewayAuth(a *auth.Auth, status int, body string) bool {
	if !upstream.IsGatewayUnauthorized(status, body) {
		return false
	}
	// Claim recovery before network I/O. max_in_flight permits concurrent chat
	// requests for one account; a burst must neither reuse the same refresh token
	// in parallel nor turn one upstream incident into three session-dead strikes.
	h.gatewayAuthMu.Lock()
	if h.gatewayAuthInFlight[a.UID] {
		h.gatewayAuthMu.Unlock()
		return true
	}
	if h.gatewayAuthInFlight == nil {
		h.gatewayAuthInFlight = map[string]bool{}
	}
	h.gatewayAuthInFlight[a.UID] = true
	h.gatewayAuthMu.Unlock()
	defer func() {
		h.gatewayAuthMu.Lock()
		delete(h.gatewayAuthInFlight, a.UID)
		h.gatewayAuthMu.Unlock()
	}()
	// The pool permits a fallback probe while every account is cooling. Such
	// probes may test chat again, but must not rotate the refresh token again.
	if st, ok := h.cfg.Pool.Status(a.UID); ok && st.Cooling && strings.HasPrefix(st.Reason, "gateway 401") {
		return true
	}
	a.Lock()
	hasRefresh := strings.TrimSpace(a.RefreshToken) != ""
	a.Unlock()
	reason := "gateway 401 (refresh unavailable)"
	if hasRefresh {
		err := h.cfg.Upstream.RefreshToken(a)
		if err == nil {
			if saveErr := a.SaveAtomic(); saveErr != nil {
				log.Printf("ERR: [server] gateway 401 refresh uid=%s: save auth failed: %v", logfmt.UID8(a.UID), saveErr)
			}
			h.cfg.Pool.ClearSessionDead(a.UID)
			reason = "gateway 401 (refresh succeeded)"
		} else {
			var ue *upstream.Error
			if errors.As(err, &ue) && upstream.IsDefinitiveRefreshRejection(ue) {
				if h.cfg.Pool.NoteSessionDead(a.UID) {
					return true
				}
				reason = "gateway 401 (refresh rejected, re-login may be required)"
			} else {
				reason = "gateway 401 (refresh inconclusive)"
			}
		}
	}
	h.cfg.Pool.CooldownSoftRate(a.UID, h.cfg.SoftCooldown, time.Time{}, reason)
	return true
}
