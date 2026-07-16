package remote

import (
	"context"
	"testing"

	"k8s.io/client-go/rest"
)

func TestCallerCredential(t *testing.T) {
	t.Run("client certificate", func(t *testing.T) {
		cred, err := callerCredential(context.Background(), &rest.Config{
			TLSClientConfig: rest.TLSClientConfig{
				CertData: []byte("CERT"),
				KeyData:  []byte("KEY"),
			},
		})
		if err != nil {
			t.Fatalf("callerCredential: %v", err)
		}
		if cred.token != "" {
			t.Errorf("expected cert credential, got token")
		}
		if string(cred.certPEM) != "CERT" || string(cred.keyPEM) != "KEY" {
			t.Errorf("cert/key not forwarded: %q %q", cred.certPEM, cred.keyPEM)
		}
	})

	t.Run("bearer token", func(t *testing.T) {
		cred, err := callerCredential(context.Background(), &rest.Config{BearerToken: "abc"})
		if err != nil {
			t.Fatalf("callerCredential: %v", err)
		}
		if cred.token != "abc" {
			t.Errorf("token = %q, want abc", cred.token)
		}
	})

	t.Run("certificate preferred over token", func(t *testing.T) {
		cred, err := callerCredential(context.Background(), &rest.Config{
			BearerToken:     "abc",
			TLSClientConfig: rest.TLSClientConfig{CertData: []byte("CERT"), KeyData: []byte("KEY")},
		})
		if err != nil {
			t.Fatalf("callerCredential: %v", err)
		}
		if cred.token != "" {
			t.Errorf("expected cert credential to win, got token %q", cred.token)
		}
	})

	t.Run("no credential fails closed", func(t *testing.T) {
		if _, err := callerCredential(context.Background(), &rest.Config{}); err == nil {
			t.Fatal("expected error for missing credential")
		}
	})
}
