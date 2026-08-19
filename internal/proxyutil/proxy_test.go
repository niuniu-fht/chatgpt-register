package proxyutil

import "testing"

func TestServerAndAuthSupportsCompactFormat(t *testing.T) {
	server, user, pass, err := ServerAndAuth("proxy.example.test:8080:user:secret")
	if err != nil {
		t.Fatal(err)
	}
	if server != "http://proxy.example.test:8080" || user != "user" || pass != "secret" {
		t.Fatalf("server=%q user=%q pass=%q", server, user, pass)
	}
}

func TestRedactedRemovesCredentials(t *testing.T) {
	got := Redacted("socks5://user:secret@proxy.example.test:1080")
	if got != "socks5://proxy.example.test:1080" {
		t.Fatalf("redacted=%q", got)
	}
}

func TestParseRejectsUnknownProtocol(t *testing.T) {
	if _, err := Parse("ftp://proxy.example.test:21"); err == nil {
		t.Fatal("expected unknown protocol to fail")
	}
}
