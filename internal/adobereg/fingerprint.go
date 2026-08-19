package adobereg

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"math/rand"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

type adobeFingerprint struct {
	screenW, screenH int
	windowW, windowH int
	cores, memory    int
	vendor, renderer string
	platformVersion  string
	seed             uint64
}

func newAdobeFingerprint() *adobeFingerprint {
	var raw [8]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		binary.LittleEndian.PutUint64(raw[:], uint64(rand.Int63()))
	}
	seed := binary.LittleEndian.Uint64(raw[:])
	return adobeFingerprintFromSeed(seed)
}

func adobeFingerprintFromSeed(seed uint64) *adobeFingerprint {
	r := rand.New(rand.NewSource(int64(seed)))
	screens := [][2]int{{1920, 1080}, {1536, 864}, {1366, 768}, {1600, 900}, {1440, 900}}
	gpus := [][2]string{
		{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
		{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce GTX 1660 Ti Direct3D11 vs_5_0 ps_5_0, D3D11)"},
		{"Google Inc. (Intel)", "ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
		{"Google Inc. (Intel)", "ANGLE (Intel, Intel(R) Iris(R) Xe Graphics Direct3D11 vs_5_0 ps_5_0, D3D11)"},
		{"Google Inc. (AMD)", "ANGLE (AMD, AMD Radeon RX 6600 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	}
	s := screens[r.Intn(len(screens))]
	g := gpus[r.Intn(len(gpus))]
	windowW, windowH := s[0]-40-r.Intn(100), s[1]-110-r.Intn(100)
	if windowW < 1000 {
		windowW = 1000
	}
	if windowH < 700 {
		windowH = 700
	}
	cores := []int{4, 6, 8, 12, 16}
	memory := []int{8, 16}
	platforms := []string{"10.0.0", "15.0.0", "19.0.0"}
	return &adobeFingerprint{
		screenW: s[0], screenH: s[1], windowW: windowW, windowH: windowH,
		cores: cores[r.Intn(len(cores))], memory: memory[r.Intn(len(memory))],
		vendor: g[0], renderer: g[1], platformVersion: platforms[r.Intn(len(platforms))],
		seed: seed,
	}
}

func (f *adobeFingerprint) windowSize() string {
	return fmt.Sprintf("%d,%d", f.windowW, f.windowH)
}

func (f *adobeFingerprint) apply(page *rod.Page, browser *rod.Browser, acceptLanguage string) (string, error) {
	major, full := adobeBrowserVersion(browser)
	ua := fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36", major)
	metadata := &proto.EmulationUserAgentMetadata{
		Brands: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Chromium", Version: major}, {Brand: "Google Chrome", Version: major}, {Brand: "Not_A Brand", Version: "24"},
		},
		FullVersionList: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Chromium", Version: full}, {Brand: "Google Chrome", Version: full}, {Brand: "Not_A Brand", Version: "24.0.0.0"},
		},
		Platform: "Windows", PlatformVersion: f.platformVersion,
		Architecture: "x86", Bitness: "64", Mobile: false,
	}
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: ua, AcceptLanguage: acceptLanguage, Platform: "Win32", UserAgentMetadata: metadata,
	}); err != nil {
		return "", err
	}
	if _, err := page.EvalOnNewDocument(f.script()); err != nil {
		return "", err
	}
	return full, nil
}

func adobeBrowserVersion(browser *rod.Browser) (major, full string) {
	major, full = "131", "131.0.0.0"
	if version, err := (proto.BrowserGetVersion{}).Call(browser); err == nil && version != nil {
		if slash := strings.LastIndex(version.Product, "/"); slash >= 0 {
			full = version.Product[slash+1:]
		}
		if dot := strings.Index(full, "."); dot > 0 {
			major = full[:dot]
		}
	}
	return major, full
}

func (f *adobeFingerprint) script() string {
	return fmt.Sprintf(`(() => {
  const define = (obj, key, value) => { try { Object.defineProperty(obj, key, {get: () => value, configurable: true}); } catch (_) {} };
  define(navigator, 'hardwareConcurrency', %d);
  define(navigator, 'deviceMemory', %d);
  define(screen, 'width', %d); define(screen, 'height', %d);
  define(screen, 'availWidth', %d); define(screen, 'availHeight', %d);
  const vendor=%q, renderer=%q;
  const patch = proto => { if (!proto) return; const original=proto.getParameter; proto.getParameter=function(key){
    if (key===37445 || key===7936) return vendor;
    if (key===37446 || key===7937) return renderer;
    return original.apply(this, arguments);
  }; };
  patch(window.WebGLRenderingContext && WebGLRenderingContext.prototype);
  patch(window.WebGL2RenderingContext && WebGL2RenderingContext.prototype);
})()`,
		f.cores, f.memory, f.screenW, f.screenH, f.screenW, f.screenH-40, f.vendor, f.renderer)
}
