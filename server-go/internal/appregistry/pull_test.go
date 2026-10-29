package appregistry

import "testing"

func TestPullCredential_IsStablePerProjectAndVerifies(t *testing.T) {
	issuer, _ := testIssuer(t)
	a1, a2, b := issuer.PullCredential("proj-a"), issuer.PullCredential("proj-a"), issuer.PullCredential("proj-b")
	expectAll(t, map[string]bool{
		"same project, same password, on every replica": a1 == a2,
		"another project, another password":             a1.Password != b.Password,
		"named after the project":                       a1.Username == "proj-a",
		"long":                                          len(a1.Password) >= 43,
		"verifies":                                      issuer.VerifyPull("proj-a", a1.Password),
		"not another project's":                         !issuer.VerifyPull("proj-b", a1.Password),
		"not empty":                                     !issuer.VerifyPull("proj-a", ""),
	})
	other, _ := testIssuer(t)
	if other.PullCredential("proj-a") == a1 {
		t.Error("a new key must change every password")
	}
}
