package adobereg

import (
	"image"
	"image/color"
	"testing"
)

func TestInferNameDefaultsForCompactAddress(t *testing.T) {
	first, last := InferName("loganfoster257@example.test")
	if first != "Alex" || last != "Morgan" {
		t.Fatalf("got %s %s", first, last)
	}
}

func TestInferNameWithSeparator(t *testing.T) {
	first, last := InferName("sabrina.khan709@example.test")
	if first != "Sabrina" || last != "Khan" {
		t.Fatalf("got %s %s", first, last)
	}
}

func TestGenPassword(t *testing.T) {
	p := GenPassword(18)
	if len(p) != 18 {
		t.Fatalf("length=%d", len(p))
	}
}

func TestAccountConsoleURLChecksHostname(t *testing.T) {
	if !isAccountConsoleURL("https://account.adobe.com/") {
		t.Fatal("expected account console URL")
	}
	if isAccountConsoleURL("https://auth.example.test/?redirect_uri=https://account.adobe.com/") {
		t.Fatal("redirect_uri must not be treated as the console hostname")
	}
}

func TestAccountAlreadyExistsText(t *testing.T) {
	for _, text := range []string{
		"An account already exists with this email address.",
		"This Adobe account exists. Sign in instead.",
		"Adobe 账户已存在",
		"此账号已存在，请登录",
	} {
		if !isAccountAlreadyExistsText(text) {
			t.Fatalf("expected existing-account text to match: %q", text)
		}
	}
	if isAccountAlreadyExistsText("Create an Adobe account") {
		t.Fatal("registration heading must not be treated as an existing account")
	}
}

func TestRandomName(t *testing.T) {
	first, last := RandomName()
	if first == "" || last == "" {
		t.Fatalf("empty random name: %q %q", first, last)
	}
}

func TestNormalizeArrowQuestionUsesVisibleProgress(t *testing.T) {
	got := normalizeArrowQuestion("Use the arrows to move the person until they're standing on the same icon in the left image (2 of 5) Match This! Reload Challenge Submit", 2)
	want := "Use the arrows to move the person until they're standing on the same icon in the left image (2 of 5)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFinalCaptchaQuestion(t *testing.T) {
	if isFinalCaptchaQuestion("Use the arrows (4 of 5)") {
		t.Fatal("fourth question must not be final")
	}
	if !isFinalCaptchaQuestion("Use the arrows (5 of 5)") {
		t.Fatal("fifth question should be final")
	}
}

func TestAdobeConsoleTextRecognizesWelcomeAndAccount(t *testing.T) {
	text := "Welcome to your account, Quinn Adobe free membership tristan-leong300@code2alita.com"
	if !isAdobeConsoleText(text, "tristan-leong300@code2alita.com") {
		t.Fatal("expected Adobe console text to be recognized")
	}
	if isAdobeConsoleText(text, "someone-else@example.test") {
		t.Fatal("different account identity must not be accepted")
	}
}

func TestCaptchaQuestionAdvanceUsesProgressNotContainerText(t *testing.T) {
	previous := "Use the arrows to move the person (1 of 5)"
	stale := "Use the arrows to move the person (1 of 5) Match This! Reload Challenge Submit"
	transition := "Use the arrows to move the person Match This! Reload Challenge Submit"
	next := "Use the arrows to move the person (2 of 5) Match This! Reload Challenge Submit"
	if captchaQuestionAdvanced(previous, stale) {
		t.Fatal("same question number must not advance")
	}
	if captchaQuestionAdvanced(previous, transition) {
		t.Fatal("transition text without progress must not advance")
	}
	if !captchaQuestionAdvanced(previous, next) {
		t.Fatal("next question number should advance")
	}
}

func TestCaptchaFailurePrompt(t *testing.T) {
	for _, text := range []string{"Match This!", "That was not quite right."} {
		if !isCaptchaFailurePrompt(text) {
			t.Fatalf("expected failure prompt: %q", text)
		}
	}
	if isCaptchaFailurePrompt("Use the arrows (1 of 5)") {
		t.Fatal("active question must not be treated as a failure prompt")
	}
}

func TestFindTryAgainButton(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 800))
	for y := 500; y < 545; y++ {
		for x := 465; x < 800; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 115, B: 230, A: 255})
		}
	}
	x, y, ok := findTryAgainButton(img)
	if !ok || x < 620 || x > 645 || y < 515 || y > 530 {
		t.Fatalf("unexpected button detection: x=%v y=%v ok=%v", x, y, ok)
	}
}

func TestAdobeFingerprintCanBeReproducedFromSeed(t *testing.T) {
	a := adobeFingerprintFromSeed(123456789)
	b := adobeFingerprintFromSeed(123456789)
	c := adobeFingerprintFromSeed(987654321)
	if *a != *b {
		t.Fatal("same test seed should reproduce the same fingerprint")
	}
	if *a == *c {
		t.Fatal("different test seeds should produce different fingerprints")
	}
}

func TestAdobeFingerprintUsesNonzeroRandomSeed(t *testing.T) {
	if fingerprint := newAdobeFingerprint(); fingerprint.seed == 0 {
		t.Fatal("random fingerprint seed must be nonzero")
	}
}

func TestCloakFingerprintSeedRange(t *testing.T) {
	for _, raw := range []uint64{0, 1, 89999, 90000, ^uint64(0)} {
		seed := 10000 + raw%90000
		if seed < 10000 || seed > 99999 {
			t.Fatalf("seed %d is outside CloakBrowser's supported range", seed)
		}
	}
}

func TestParseProxySupportsCompactCredentials(t *testing.T) {
	server, user, pass, err := parseProxy("proxy.example.test:8080:user:secret")
	if err != nil {
		t.Fatal(err)
	}
	if server != "http://proxy.example.test:8080" || user != "user" || pass != "secret" {
		t.Fatalf("server=%q user=%q pass=%q", server, user, pass)
	}
}
