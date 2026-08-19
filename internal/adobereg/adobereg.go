package adobereg

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var (
	ErrAccountAlreadyExists = errors.New("Adobe 账户已存在")
	ErrProxyRejected        = errors.New("Adobe 代理节点不可用")
	ErrCloakSeatBusy        = errors.New("CloakBrowser 并发席位已占用")
)

type Input struct {
	Email        string
	Password     string
	FirstName    string
	LastName     string
	BirthYear    int
	BirthMonth   int
	Country      string
	Proxy        string
	BrowserBin   string
	CloakBrowser bool
	Headless     bool
	// ExtensionCaptcha indicates that a browser extension owns CAPTCHA solving.
	// In this mode the registration flow waits for the extension instead of
	// handing an empty classifier to the API solver.
	ExtensionCaptcha bool
	DryRun           bool
	Captcha          CaptchaClassifier

	Log      func(format string, args ...any)
	SaveShot func(png []byte)
}

type CaptchaClassifier interface {
	Classify(ctx context.Context, images [][]byte, question string) ([]int, error)
}

type Result struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	BirthYear  int    `json:"birth_year"`
	BirthMonth int    `json:"birth_month"`
	Country    string `json:"country"`
	Status     string `json:"status"`
}

func Register(ctx context.Context, in Input) (*Result, error) {
	if !strings.Contains(in.Email, "@") {
		return nil, fmt.Errorf("邮箱格式不正确")
	}
	if in.Password == "" {
		in.Password = GenPassword(18)
	}
	if in.FirstName == "" || in.LastName == "" {
		first, last := InferName(in.Email)
		if in.FirstName == "" {
			in.FirstName = first
		}
		if in.LastName == "" {
			in.LastName = last
		}
	}
	if in.BirthYear == 0 {
		in.BirthYear = 1994
	}
	if in.BirthMonth < 1 || in.BirthMonth > 12 {
		in.BirthMonth = 6
	}

	return registerBrowser(ctx, in)
}

var trailingDigits = regexp.MustCompile(`\d+$`)

func InferName(email string) (string, string) {
	local := strings.SplitN(email, "@", 2)[0]
	local = trailingDigits.ReplaceAllString(local, "")
	parts := regexp.MustCompile(`[^a-zA-Z]+`).Split(local, -1)
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		return title(parts[0]), title(parts[1])
	}
	// Compact mailbox names have no reliable separator. Callers should pass
	// explicit names; these defaults keep fixture runs deterministic.
	return "Alex", "Morgan"
}

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

func GenPassword(length int) string {
	if length < 12 {
		length = 12
	}
	const letters = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			panic(err)
		}
		b[i] = letters[n.Int64()]
	}
	return "A9!" + string(b[3:])
}

var firstNames = []string{
	"Avery", "Blake", "Cameron", "Casey", "Dylan", "Elliot", "Emerson", "Harper",
	"Hayden", "Jordan", "Logan", "Morgan", "Parker", "Quinn", "Reese", "Riley",
	"Rowan", "Sawyer", "Taylor", "Alexis",
}

var lastNames = []string{
	"Anderson", "Bennett", "Brooks", "Carter", "Collins", "Cooper", "Davis", "Edwards",
	"Foster", "Gray", "Hayes", "Hughes", "Kelly", "Morgan", "Parker", "Reed",
	"Scott", "Turner", "Walker", "Ward",
}

// RandomName returns a browser-form-friendly English profile name.
func RandomName() (string, string) {
	return firstNames[secureIndex(len(firstNames))], lastNames[secureIndex(len(lastNames))]
}

func secureIndex(size int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(size)))
	if err != nil {
		panic(err)
	}
	return int(n.Int64())
}

func (in Input) logf(format string, args ...any) {
	if in.Log != nil {
		in.Log(format, args...)
	}
}
