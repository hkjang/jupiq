package auth

import "testing"

func TestOIDCExternalIdentityIncludesIssuerAndNormalizesSlash(t *testing.T) {
	a := oidcExternalIdentity("https://keycloak.example/realms/a/", "subject-1")
	if a != oidcExternalIdentity("https://keycloak.example/realms/a", "subject-1") {
		t.Fatal("equivalent issuer URLs produced different identities")
	}
	if a == oidcExternalIdentity("https://keycloak.example/realms/b", "subject-1") {
		t.Fatal("same subject from a different issuer collided")
	}
	if a == oidcExternalIdentity("https://keycloak.example/realms/a", "subject-2") {
		t.Fatal("different subjects collided")
	}
}
