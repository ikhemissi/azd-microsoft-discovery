package discovery

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSafeHTTPDiagnosticsAndClassification(t *testing.T) {
	client := NewClient(credential{})
	client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
		return response(request, 403,
			`{"error":{"code":"AuthorizationFailed","message":"secret-body-must-not-escape"}}`,
			http.Header{"X-Ms-Request-Id": []string{"fixture-request-123"}}), nil
	})
	_, err := client.Resource(t.Context(), "/fixture")
	var failure *HTTPError
	if !errors.As(err, &failure) || !failure.Rejected() || failure.Code != "AuthorizationFailed" ||
		failure.RequestID != "fixture-request-123" || strings.Contains(err.Error(), "secret-body") ||
		!strings.Contains(err.Error(), "/fixture") {
		t.Fatalf("safe diagnostic information lost or leaked: %v", err)
	}
	client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
		return response(request, 500, `{"error":{"code":"unsafe\nmultiline"}}`,
			http.Header{"X-Ms-Request-Id": []string{"unsafe\nmultiline"}}), nil
	})
	_, err = client.Request(t.Context(), http.MethodPost, ARMEndpoint+"/fixture", nil)
	if !errors.As(err, &failure) || failure.Rejected() ||
		failure.Code != "unavailable" || failure.RequestID != "unavailable" {
		t.Fatalf("untrusted diagnostic identifiers accepted: %v", err)
	}
}
