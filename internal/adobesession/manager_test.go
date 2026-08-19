package adobesession

import "testing"

func TestPublicURLRemovesDynamicQuery(t *testing.T) {
	got := publicURL("https://auth.example.test/deeplink.html?relay=TOKEN&state=TOKEN#/signup")
	want := "https://auth.example.test/deeplink.html#/signup"
	if got != want {
		t.Fatalf("publicURL() = %q, want %q", got, want)
	}
}

func TestStartRejectsInvalidInputBeforeLaunch(t *testing.T) {
	m := New()
	if err := m.Start(StartInput{Email: "bad", Password: "value"}); err == nil {
		t.Fatal("expected invalid email error")
	}
	if err := m.Start(StartInput{Email: "valid@example.test"}); err == nil {
		t.Fatal("expected empty password error")
	}
	if err := m.Start(StartInput{Email: "valid@example.test", Password: "value", StartURL: "http://example.test"}); err == nil {
		t.Fatal("expected insecure start URL error")
	}
}
