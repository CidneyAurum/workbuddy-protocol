package upstream

import (
	"net/http"
	"regexp"
	"strings"
)

// IsGatewayUnauthorized distinguishes an APISIX/OpenResty rejection from a
// business response such as code 12153. A bare 401 is not proof of a ban.
func IsGatewayUnauthorized(status int, body string) bool {
	return status == http.StatusUnauthorized && !hasBusinessEnvelope(body)
}

var businessEnvelopeField = regexp.MustCompile(`"(?:code|msg)"\s*:`)

func hasBusinessEnvelope(body string) bool { return businessEnvelopeField.MatchString(body) }

// IsDefinitiveRefreshRejection requires an application-level auth verdict.
// HTML, WAF responses, transport failures, throttling and 5xx are inconclusive.
func IsDefinitiveRefreshRejection(err *Error) bool {
	if err == nil || err.Status < 200 || err.Status >= 500 || err.Status == http.StatusTooManyRequests {
		return false
	}
	body := strings.ToLower(err.Msg)
	if strings.Contains(body, "<html") || strings.Contains(body, "<!doctype") {
		return false
	}
	return err.Kind == ErrSessionDead || strings.Contains(body, "invalid_grant") ||
		strings.Contains(body, "invalid refresh token") || strings.Contains(body, "refresh token expired")
}
