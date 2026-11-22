package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	firebase "firebase.google.com/go/v4"
	firebaseauth "firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"
)

type errorVerifier struct{ err error }

func (v errorVerifier) VerifyIDToken(context.Context, string) (*firebaseauth.Token, error) {
	return nil, v.err
}

func TestRequireIdentitySeparatesInfrastructureFailures(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"credential", errors.New("token expired"), http.StatusUnauthorized, "unauthorized"},
		{"unavailable", errors.Join(ErrVerifierUnavailable, errors.New("keys unreachable at https://internal.example")), http.StatusServiceUnavailable, "auth_unavailable"},
		{"internal", errors.Join(ErrVerifierFailed, errors.New("project id not available")), http.StatusInternalServerError, "internal_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			handler := RequireIdentity(errorVerifier{err: c.err}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("request reached the handler")
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer secret-token-value")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			body := rec.Body.String()
			if rec.Code != c.status || !strings.Contains(body, `"code":"`+c.code+`"`) {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if strings.Contains(body, "secret-token-value") || strings.Contains(body, "internal.example") || strings.Contains(body, "project id") {
				t.Fatalf("response leaks details: %s", body)
			}
			if (c.status == http.StatusUnauthorized) != (rec.Header().Get("WWW-Authenticate") != "") {
				t.Fatalf("WWW-Authenticate must only accompany a 401: %v", rec.Header())
			}
		})
	}
}

// A real SDK client without credentials rejects a malformed token locally,
// without network access; that must stay a credential error (401).
func TestClassifyVerifyErrorKeepsSDKCredentialErrors(t *testing.T) {
	ctx := context.Background()
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: "classification-test"}, option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	client, err := app.Auth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"not-a-jwt", "a.b.c"} {
		_, err := firebaseVerifier{client: client}.VerifyIDToken(ctx, token)
		if err == nil || errors.Is(err, ErrVerifierUnavailable) || errors.Is(err, ErrVerifierFailed) {
			t.Fatalf("%q: err=%v; want a credential error", token, err)
		}
	}
}

func TestClassifyVerifyErrorIsConservative(t *testing.T) {
	if err := classifyVerifyError(context.DeadlineExceeded); !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("deadline: %v", err)
	}
	if err := classifyVerifyError(errors.New("something new")); !errors.Is(err, ErrVerifierFailed) || errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("unknown errors are internal, never transient: %v", err)
	}
}
