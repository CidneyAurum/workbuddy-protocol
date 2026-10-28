package upstream

import "testing"

func TestGatewayAuthRequiresNoBusinessVerdict(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{401, "<html>401 Authorization Required</html>", true},
		{401, "", true},
		{403, "<html>Forbidden</html>", false},
		{401, `{"code":12153,"msg":"Offline user session not found"}`, false},
		{401, `{"code" : 12153, "msg" : "Offline user session not found"}`, false},
		{403, `{"code":11140,"msg":"request illegal"}`, false},
	} {
		if got := IsGatewayUnauthorized(tc.status, tc.body); got != tc.want {
			t.Errorf("status=%d body=%q: got %v want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestDefinitiveRefreshRejection(t *testing.T) {
	for _, tc := range []struct {
		err  *Error
		want bool
	}{
		{nil, false},
		{&Error{Status: 401, Msg: "<html>Authorization Required</html>"}, false},
		{&Error{Status: 403, Msg: "Forbidden"}, false},
		{&Error{Status: 429, Msg: "invalid_grant"}, false},
		{&Error{Status: 550, Msg: "invalid_grant"}, false},
		{&Error{Status: 400, Msg: "invalid_grant: session is invalid"}, true},
		{&Error{Status: 401, Kind: ErrSessionDead, Msg: "code=12153"}, true},
		{&Error{Status: 200, Kind: ErrSessionDead, Msg: "code=12153"}, true},
	} {
		if got := IsDefinitiveRefreshRejection(tc.err); got != tc.want {
			t.Errorf("err=%v got %v want %v", tc.err, got, tc.want)
		}
	}
}
