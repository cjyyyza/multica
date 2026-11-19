package notify

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	PopoVerificationRelayURLEnv   = "MULTICA_POPO_VERIFICATION_RELAY_URL"
	PopoVerificationRelayTokenEnv = "MULTICA_POPO_VERIFICATION_RELAY_TOKEN"
	popoVerificationRelayPath     = "/v1/verification-code"
)

type PopoVerificationCodeSender interface {
	SendVerificationCode(ctx context.Context, email, code string) error
}

var (
	popoVerificationRelayTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,}$`)
	popoVerificationCodePattern       = regexp.MustCompile(`^[0-9]{6}$`)
	errPopoVerificationRelayToken     = errors.New(PopoVerificationRelayTokenEnv + " must be a random URL-safe token of at least 32 characters")
)

type popoVerificationRelayRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type popoVerificationRelaySender struct {
	endpoint  string
	token     string
	client    *http.Client
	configErr error
}

func NewPopoVerificationRelaySender(origin, token string) PopoVerificationCodeSender {
	endpoint, err := popoVerificationRelayEndpoint(origin, token)
	if err != nil {
		slog.Error("invalid POPO verification relay configuration", "error", err)
	}
	return &popoVerificationRelaySender{
		endpoint:  endpoint,
		token:     token,
		configErr: err,
		client: &http.Client{
			Timeout: 55 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func popoVerificationRelayEndpoint(origin, token string) (string, error) {
	if !popoVerificationRelayTokenPattern.MatchString(token) {
		return "", errPopoVerificationRelayToken
	}
	base, err := url.Parse(origin)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil ||
		base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return "", errors.New(PopoVerificationRelayURLEnv + " must be an HTTPS origin")
	}
	base.Path = popoVerificationRelayPath
	base.RawPath = ""
	return base.String(), nil
}

func (s *popoVerificationRelaySender) SendVerificationCode(ctx context.Context, email, code string) error {
	if s.configErr != nil {
		return s.configErr
	}
	body, err := json.Marshal(popoVerificationRelayRequest{Email: email, Code: code})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("send code via POPO verification relay: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("POPO verification relay returned HTTP %d", response.StatusCode)
	}
	return nil
}

// NewPopoVerificationRelayHandler serves the deployment's verification sender.
// The CLI starts it on a dedicated HTTPS listener under the POPO user's account.
func NewPopoVerificationRelayHandler(token string, sender PopoVerificationCodeSender) (http.Handler, error) {
	if !popoVerificationRelayTokenPattern.MatchString(token) {
		return nil, errPopoVerificationRelayToken
	}
	if sender == nil {
		return nil, errors.New("POPO verification sender is required")
	}
	authorization := []byte("Bearer " + token)
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+popoVerificationRelayPath, func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), authorization) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		var payload popoVerificationRelayRequest
		if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid verification request", http.StatusBadRequest)
			return
		}
		if !accountPattern.MatchString(payload.Email) || !popoVerificationCodePattern.MatchString(payload.Code) {
			http.Error(w, "invalid verification request", http.StatusBadRequest)
			return
		}
		if err := sender.SendVerificationCode(r.Context(), payload.Email, payload.Code); err != nil {
			slog.Warn("POPO verification relay send failed", "error", strings.ReplaceAll(err.Error(), payload.Code, "[redacted]"))
			http.Error(w, "POPO submission not confirmed", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux, nil
}
