package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

type fakePopoVerificationCodeSender struct {
	email string
	code  string
	err   error
}

func (f *fakePopoVerificationCodeSender) SendVerificationCode(_ context.Context, email, code string) error {
	f.email = email
	f.code = code
	return f.err
}

func TestSendCodeThroughPopoCLI(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sendErr      error
		wantHTTPCode int
	}{
		{name: "delivered", wantHTTPCode: http.StatusOK},
		{name: "failed", sendErr: errors.New("popo-cli failed"), wantHTTPCode: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakePopoVerificationCodeSender{err: tc.sendErr}
			h := *testHandler
			h.cfg = Config{AllowSignup: true}
			h.EmailService = nil
			h.PopoVerificationCodes = sender
			email := "popo-cli-auth-" + tc.name + "@corp.netease.com"
			dbfx.Cleanup(t, `DELETE FROM "user" WHERE email = $1`, email)
			dbfx.Cleanup(t, `DELETE FROM verification_code WHERE email = $1`, email)

			req := testutil.JSONRequest(http.MethodPost, "/auth/send-code", map[string]string{
				"email": "  " + strings.ToUpper(email) + "  ",
			})
			testutil.Call(t, h.SendCode, req).Want(tc.wantHTTPCode)
			if sender.email != email {
				t.Fatalf("POPO receiver = %q, want %q", sender.email, email)
			}
			stored, err := h.Queries.GetLatestVerificationCode(context.Background(), email)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored.Code) != 6 || stored.Code != sender.code {
				t.Fatalf("POPO code does not match the stored random code")
			}
			if tc.wantHTTPCode != http.StatusOK {
				return
			}
			testutil.Call(t, h.VerifyCode, testutil.JSONRequest(http.MethodPost, "/auth/verify-code", map[string]string{
				"email": email,
				"code":  stored.Code,
			})).Want(http.StatusOK)
			if n := dbfx.Count(t, `SELECT count(*) FROM "user" WHERE email = $1`, email); n != 1 {
				t.Fatalf("created users = %d, want 1", n)
			}
		})
	}
}
