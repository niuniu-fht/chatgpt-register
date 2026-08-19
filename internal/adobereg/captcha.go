package adobereg

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

func solveArkoseCaptcha(ctx context.Context, root *rod.Page, in Input) error {
	in.logf("检测到 Arkose 验证，启动 YesCaptcha 图像识别")
	restoreDOM := root.EnableDomain(&proto.DOMEnable{})
	defer restoreDOM()
	if captchaQuestion(root) == "" {
		start, err := deepSearchFirst(root, "Start puzzle", 15*time.Second)
		if err != nil {
			return fmt.Errorf("等待 Start puzzle 按钮: %w", err)
		}
		if _, err := start.Eval(`() => {
  const node = this.nodeType === 3 ? this.parentElement : this;
  const button = node && node.closest ? node.closest('button') : null;
  (button || node).click();
}`); err != nil {
			return fmt.Errorf("点击 Start puzzle: %w", err)
		}
		in.logf("已点击 Start puzzle")
	} else {
		in.logf("验证码题目已显示，继续当前验证会话")
	}
	time.Sleep(2 * time.Second)

	challenge := 1
	for action := 1; action <= 30; action++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if isConsoleReady(root) {
			in.logf("Arkose 验证已完成")
			return nil
		}
		if clicked, retryErr := clickTryAgain(root, 3*time.Second); retryErr != nil {
			return retryErr
		} else if clicked {
			in.logf("验证码第 %d 组未通过，已点击 Try again，开始第 %d 组", challenge, challenge+1)
			challenge++
			time.Sleep(2 * time.Second)
			continue
		}
		question := captchaQuestion(root)
		if clicked, retryErr := clickTryAgain(root, 3*time.Second); retryErr != nil {
			return retryErr
		} else if clicked {
			in.logf("验证码第 %d 组未通过，已点击 Try again，开始第 %d 组", challenge, challenge+1)
			challenge++
			time.Sleep(2 * time.Second)
			continue
		}
		if question == "" {
			question = "Use the arrows to move the person until they're standing on the same icon in the left image"
		}
		if strings.HasPrefix(strings.ToLower(question), "use the arrows") {
			questionNumber, total, ok := captchaProgress(question)
			if !ok {
				if action > 1 {
					clicked, waitErr := waitForTryAgain(ctx, root, 90*time.Second)
					if waitErr != nil {
						return waitErr
					}
					if clicked {
						in.logf("验证码第 %d 组未通过，已点击 Try again，开始第 %d 组", challenge, challenge+1)
						challenge++
						time.Sleep(2 * time.Second)
						continue
					}
					return nil
				}
				questionNumber, total = action, 5
			}
			label := fmt.Sprintf("第 %d 组 %d/%d", challenge, questionNumber, total)
			images, err := captureArrowStates(root)
			if err != nil {
				return err
			}
			in.logf("验证码%s，题目: %s，已采集 %d 个方向状态", label, truncate(question, 180), len(images))
			classificationQuestion := normalizeArrowQuestion(question, questionNumber)
			objects, err := classifyWithRetry(ctx, in, images, classificationQuestion, label)
			if err != nil {
				return err
			}
			in.logf("YesCaptcha %s返回索引 %v", label, objects)
			if err := applyArrowAnswer(root, objects); err != nil {
				return err
			}
			in.logf("验证码%s已点击 Submit，等待本题结果", label)
			advance, err := waitForCaptchaAdvance(ctx, root, question)
			if err != nil {
				return err
			}
			switch advance {
			case captchaComplete:
				in.logf("Arkose 题窗已关闭，等待 Adobe 账户控制台")
				return nil
			case captchaRetry:
				in.logf("验证码第 %d 组未通过，已点击 Try again，开始第 %d 组", challenge, challenge+1)
				challenge++
			}
			continue
		}
		if isCaptchaFailurePrompt(question) {
			clicked, waitErr := waitForTryAgain(ctx, root, 90*time.Second)
			if waitErr != nil {
				return waitErr
			}
			if clicked {
				in.logf("验证码第 %d 组未通过，已点击 Try again，开始第 %d 组", challenge, challenge+1)
				challenge++
				time.Sleep(2 * time.Second)
				continue
			}
			return nil
		}
		question, imageElement, imageBytes, err := captureCaptchaRound(root)
		if err != nil {
			return err
		}
		label := fmt.Sprintf("第 %d 组第 %d 题", challenge, action)
		in.logf("验证码%s，题目: %s", label, truncate(question, 180))
		objects, err := classifyWithRetry(ctx, in, [][]byte{imageBytes}, question, label)
		if err != nil {
			return err
		}
		in.logf("YesCaptcha %s返回索引 %v", label, objects)
		if err := clickCaptchaObjects(root, imageElement, objects); err != nil {
			return err
		}
		time.Sleep(2500 * time.Millisecond)
	}
	return fmt.Errorf("验证码轮次超过上限")
}

func isCaptchaFailurePrompt(question string) bool {
	question = strings.ToLower(strings.TrimSpace(question))
	return question == "match this!" || strings.Contains(question, "that was not quite right")
}

func classifyWithRetry(ctx context.Context, in Input, images [][]byte, question, label string) ([]int, error) {
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		objects, err := in.Captcha.Classify(ctx, images, question)
		if err == nil {
			return objects, nil
		}
		lastErr = err
		message := strings.ToLower(err.Error())
		transient := strings.Contains(message, "error_service_unavaliable") ||
			strings.Contains(message, "error_service_unavailable") || strings.Contains(message, "服务暂时不可用")
		if !transient || attempt == 4 {
			return nil, err
		}
		in.logf("YesCaptcha %s服务暂时繁忙，3 秒后进行第 %d 次识别", label, attempt+1)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return nil, lastErr
}

func captureArrowStates(page *rod.Page) ([][]byte, error) {
	states := make([][]byte, 0, 6)
	for i := 0; i < 6; i++ {
		png, err := page.CancelTimeout().Timeout(5*time.Second).Screenshot(false, nil)
		if err != nil {
			return nil, fmt.Errorf("截取方向题第 %d 个状态: %w", i+1, err)
		}
		cropped, err := cropArrowChallenge(png)
		if err != nil {
			return nil, err
		}
		states = append(states, cropped)
		if err := clickArrowControl(page, true); err != nil {
			return nil, err
		}
		time.Sleep(450 * time.Millisecond)
	}
	return states, nil
}

func cropArrowChallenge(png []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return nil, fmt.Errorf("解析方向题截图: %w", err)
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	left, right := w/2-174, w/2+160
	top, bottom := int(float64(h)*0.306), int(float64(h)*0.556)
	if left < 0 {
		left = 0
	}
	if right > w {
		right = w
	}
	crop := image.NewRGBA(image.Rect(0, 0, right-left, bottom-top))
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			crop.Set(x-left, y-top, img.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, crop, &jpeg.Options{Quality: 78}); err != nil {
		return nil, fmt.Errorf("编码方向题截图: %w", err)
	}
	return out.Bytes(), nil
}

func clickArrowControl(page *rod.Page, right bool) error {
	png, err := page.CancelTimeout().Timeout(5*time.Second).Screenshot(false, nil)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return err
	}
	w, h := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
	x := w/2 - 20
	if right {
		x = w/2 + 140
	}
	if err := page.Mouse.MoveTo(proto.Point{X: x, Y: h * 0.531}); err != nil {
		return fmt.Errorf("移动到方向按钮: %w", err)
	}
	if err := page.Mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击方向按钮: %w", err)
	}
	return nil
}

func applyArrowAnswer(page *rod.Page, objects []int) error {
	if len(objects) == 0 || objects[0] < 0 || objects[0] > 5 {
		return fmt.Errorf("方向题返回索引不正确: %v", objects)
	}
	for i := 0; i < objects[0]; i++ {
		if err := clickArrowControl(page, true); err != nil {
			return err
		}
		time.Sleep(350 * time.Millisecond)
	}
	clicked, err := clickVisibleDeepButton(page, "Submit", 5*time.Second)
	if err == nil && clicked {
		return nil
	}

	// Arkose can replace its nested document after the direction controls are used,
	// invalidating CDP's DOM search state. The button has a stable centered layout.
	png, shotErr := page.CancelTimeout().Timeout(5*time.Second).Screenshot(false, nil)
	if shotErr != nil {
		return fmt.Errorf("截取 Submit 按钮位置: %w", shotErr)
	}
	img, _, decodeErr := image.Decode(bytes.NewReader(png))
	if decodeErr != nil {
		return fmt.Errorf("解析 Submit 按钮位置: %w", decodeErr)
	}
	w, h := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
	if moveErr := page.Mouse.MoveTo(proto.Point{X: w / 2, Y: h * 0.679}); moveErr != nil {
		return fmt.Errorf("移动到 Submit 按钮: %w", moveErr)
	}
	if clickErr := page.Mouse.Click(proto.InputMouseButtonLeft, 1); clickErr != nil {
		return fmt.Errorf("点击 Submit 按钮: %w", clickErr)
	}
	return nil
}

type captchaAdvance int

const (
	captchaNext captchaAdvance = iota
	captchaComplete
	captchaRetry
)

func waitForCaptchaAdvance(ctx context.Context, page *rod.Page, previousQuestion string) (captchaAdvance, error) {
	deadline := time.Now().Add(90 * time.Second)
	emptySince := time.Time{}
	for time.Now().Before(deadline) {
		if isConsoleReady(page) {
			return captchaComplete, nil
		}
		if clicked, retryErr := clickTryAgain(page, 2*time.Second); retryErr != nil {
			return captchaNext, fmt.Errorf("查找 Try again: %w", retryErr)
		} else if clicked {
			time.Sleep(2 * time.Second)
			return captchaRetry, nil
		}
		current := captchaQuestion(page)
		previousNumber, previousTotal, previousOK := captchaProgress(previousQuestion)
		currentNumber, currentTotal, currentOK := captchaProgress(current)
		if previousOK && currentOK && previousNumber == previousTotal &&
			currentNumber == 1 && currentTotal == previousTotal {
			return captchaRetry, nil
		}
		if captchaQuestionAdvanced(previousQuestion, current) {
			return captchaNext, nil
		}
		if current == "" {
			if isFinalCaptchaQuestion(previousQuestion) {
				return captchaComplete, nil
			}
			if emptySince.IsZero() {
				emptySince = time.Now()
			} else if time.Since(emptySince) >= 80*time.Second {
				return captchaComplete, nil
			}
		} else {
			emptySince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return captchaNext, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return captchaNext, fmt.Errorf("点击 Submit 后题号未变化")
}

func waitForTryAgain(ctx context.Context, page *rod.Page, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isConsoleReady(page) {
			return false, nil
		}
		if clicked, err := clickTryAgain(page, 3*time.Second); err != nil {
			return false, fmt.Errorf("点击 Try again: %w", err)
		} else if clicked {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return false, fmt.Errorf("等待 Try again 超时")
}

func clickTryAgain(page *rod.Page, timeout time.Duration) (bool, error) {
	element, err := deepSearchFirst(page, "Try again", timeout)
	if err != nil {
		return clickTryAgainByPosition(page)
	}
	result, err := element.Eval(`() => {
  const node = this.nodeType === 3 ? this.parentElement : this;
  const button = node && node.closest ? node.closest('button, [role="button"], input[type="button"], input[type="submit"]') : null;
  if (!button) return false;
  const text = String(button.innerText || button.textContent || button.value || '').trim().toLowerCase();
  if (!text.includes('try again')) return false;
  button.click();
  return true;
}`)
	if err != nil {
		return false, err
	}
	return result.Value.Bool(), nil
}

func clickTryAgainByPosition(page *rod.Page) (bool, error) {
	png, err := page.CancelTimeout().Timeout(5*time.Second).Screenshot(false, nil)
	if err != nil {
		return false, nil
	}
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return false, nil
	}
	x, y, ok := findTryAgainButton(img)
	if !ok {
		return false, nil
	}
	if err := page.Mouse.MoveTo(proto.Point{X: x, Y: y}); err != nil {
		return false, err
	}
	if err := page.Mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return false, err
	}
	return true, nil
}

func findTryAgainButton(img image.Image) (x, y float64, ok bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	minX, minY, maxX, maxY, count := w, h, 0, 0, 0
	for py := b.Min.Y + h*58/100; py < b.Min.Y+h*72/100; py++ {
		for px := b.Min.X + w*25/100; px < b.Min.X+w*75/100; px++ {
			r16, g16, b16, _ := img.At(px, py).RGBA()
			r, g, blue := int(r16>>8), int(g16>>8), int(b16>>8)
			if blue < 160 || g < 60 || blue < r+70 || blue < g+40 {
				continue
			}
			count++
			if px < minX {
				minX = px
			}
			if px > maxX {
				maxX = px
			}
			if py < minY {
				minY = py
			}
			if py > maxY {
				maxY = py
			}
		}
	}
	if count < 800 || maxX-minX < w/5 || maxY-minY < 24 {
		return 0, 0, false
	}
	return float64(minX+maxX) / 2, float64(minY+maxY) / 2, true
}

func captchaQuestionAdvanced(previous, current string) bool {
	if current == "" {
		return false
	}
	previousNumber, previousTotal, previousOK := captchaProgress(previous)
	currentNumber, currentTotal, currentOK := captchaProgress(current)
	if previousOK {
		// Arkose briefly renders an instruction without the progress suffix after
		// Submit. Keep waiting until a real numbered question replaces it.
		return currentOK && (previousNumber != currentNumber || previousTotal != currentTotal)
	}
	if currentOK {
		return true
	}
	return current != previous
}

func isFinalCaptchaQuestion(question string) bool {
	current, total, ok := captchaProgress(question)
	if !ok {
		return false
	}
	return total > 0 && current == total
}

func normalizeArrowQuestion(question string, round int) string {
	const base = "Use the arrows to move the person until they're standing on the same icon in the left image"
	current, total := round, 5
	if parsedCurrent, parsedTotal, ok := captchaProgress(question); ok {
		current, total = parsedCurrent, parsedTotal
	}
	return fmt.Sprintf("%s (%d of %d)", base, current, total)
}

func captchaProgress(question string) (int, int, bool) {
	for start := strings.Index(question, "("); start >= 0; {
		var current, total int
		if _, err := fmt.Sscanf(question[start:], "(%d of %d)", &current, &total); err == nil {
			return current, total, true
		}
		next := strings.Index(question[start+1:], "(")
		if next < 0 {
			break
		}
		start += next + 1
	}
	return 0, 0, false
}

func deepSearchFirst(page *rod.Page, query string, timeout time.Duration) (*rod.Element, error) {
	_ = proto.DOMEnable{}.Call(page)
	result, err := page.CancelTimeout().Timeout(timeout).Search(query)
	if err != nil {
		return nil, err
	}
	defer result.Release()
	if result.First == nil {
		return nil, fmt.Errorf("未找到 %s", query)
	}
	return result.First.CancelTimeout(), nil
}

func deepSearchAll(page *rod.Page, query string, timeout time.Duration) ([]*rod.Element, error) {
	_ = proto.DOMEnable{}.Call(page)
	result, err := page.CancelTimeout().Timeout(timeout).Search(query)
	if err != nil {
		return nil, err
	}
	defer result.Release()
	elements, err := result.All()
	if err != nil {
		return nil, err
	}
	for i := range elements {
		elements[i] = elements[i].CancelTimeout()
	}
	return elements, nil
}

func clickVisibleDeepButton(page *rod.Page, query string, timeout time.Duration) (bool, error) {
	_ = proto.DOMEnable{}.Call(page)
	result, err := page.CancelTimeout().Timeout(timeout).Search(query)
	if err != nil {
		return false, nil
	}
	defer result.Release()
	elements, err := result.All()
	if err != nil {
		return false, err
	}
	for _, element := range elements {
		value, evalErr := element.Eval(`(query) => {
  const node = this.nodeType === 3 ? this.parentElement : this;
  const target = node && node.matches && node.matches('button, [role="button"], input[type="button"], input[type="submit"]')
    ? node : (node && node.closest ? node.closest('button, [role="button"], input[type="button"], input[type="submit"]') : null);
  if (!target) return false;
  const text = String(target.innerText || target.textContent || target.value || '').trim().toLowerCase();
  const rect = target.getBoundingClientRect();
  const style = getComputedStyle(target);
  const visible = rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden' && Number(style.opacity || 1) > 0;
  if (!visible || !text.includes(String(query).toLowerCase())) return false;
  target.click();
  return true;
}`, query)
		if evalErr == nil && value.Value.Bool() {
			return true, nil
		}
	}
	return false, nil
}

func findCaptchaPage(page *rod.Page, depth int) *rod.Page {
	if depth > 4 {
		return nil
	}
	pg := page.CancelTimeout().Timeout(1200 * time.Millisecond)
	if body, err := pg.Element("body"); err == nil {
		if text, textErr := body.Text(); textErr == nil && captchaText(text) {
			return page
		}
	}
	if info, err := pg.Info(); err == nil && captchaText(info.URL) {
		return page
	}
	frames, _ := page.CancelTimeout().Timeout(1200 * time.Millisecond).Elements("iframe")
	for _, element := range frames {
		src, _ := element.Attribute("src")
		title, _ := element.Attribute("title")
		marked := captchaText(valueOrEmpty(src) + " " + valueOrEmpty(title))
		visible, _ := element.Visible()
		if !marked && !visible {
			continue
		}
		child, err := element.Frame()
		if err == nil {
			if found := findCaptchaPage(child, depth+1); found != nil {
				return found
			}
			if marked {
				return child
			}
		}
	}
	return nil
}

func captureCaptchaRound(page *rod.Page) (string, *rod.Element, []byte, error) {
	pg := page.CancelTimeout().Timeout(8 * time.Second)
	question := captchaQuestion(pg)
	if question == "" {
		question = "Use the arrows to move the person until they're standing on the same icon in the left image"
	}
	if challenge, challengeErr := deepSearchFirst(pg, "#game_children_challenge", 2*time.Second); challengeErr == nil {
		if image, shotErr := challenge.Screenshot(proto.PageCaptureScreenshotFormatPng, 100); shotErr == nil && len(image) > 0 {
			return question, challenge, image, nil
		}
	}
	elements, err := deepSearchAll(pg, "img, canvas", 8*time.Second)
	if err != nil {
		return "", nil, nil, fmt.Errorf("查找验证码题图: %w", err)
	}
	type candidate struct {
		el   *rod.Element
		area float64
	}
	var candidates []candidate
	for _, element := range elements {
		shape, shapeErr := element.Shape()
		if shapeErr != nil || shape.Box() == nil {
			continue
		}
		box := shape.Box()
		if box.Width >= 180 && box.Height >= 120 {
			candidates = append(candidates, candidate{el: element, area: box.Width * box.Height})
		}
	}
	if len(candidates) == 0 {
		return "", nil, nil, fmt.Errorf("未找到验证码题图")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].area > candidates[j].area })
	imageElement := candidates[0].el
	image, err := imageElement.Resource()
	if err != nil || len(image) == 0 {
		image, err = imageElement.Screenshot(proto.PageCaptureScreenshotFormatPng, 100)
	}
	if err != nil || len(image) == 0 {
		return "", nil, nil, fmt.Errorf("截取验证码题图: %w", err)
	}
	return question, imageElement, image, nil
}

func captchaQuestion(page *rod.Page) string {
	queries := []struct {
		text    string
		timeout time.Duration
	}{
		{"Use the arrows", 6 * time.Second}, {"Pick the", 1500 * time.Millisecond},
		{"Pick one", 1500 * time.Millisecond}, {"Select the", 1500 * time.Millisecond},
		{"Select an", 1500 * time.Millisecond}, {"Choose the", 1500 * time.Millisecond},
		{"Match the", 1500 * time.Millisecond},
	}
	for _, query := range queries {
		text, err := deepSearchText(page, query.text, query.timeout)
		if err != nil {
			continue
		}
		if question := extractCaptchaQuestion(text); question != "" {
			return question
		}
	}
	selectors := []string{
		"[class*='instruction']", "[class*='prompt']", "[class*='question']",
		"#game_children_challenge h2", "#game_children_challenge h3", "h1", "h2", "h3", "p",
	}
	for _, selector := range selectors {
		elements, _ := deepSearchAll(page, selector, 1200*time.Millisecond)
		for _, element := range elements {
			visible, _ := element.Visible()
			if !visible {
				continue
			}
			text := deepNodeText(element)
			if question := extractCaptchaQuestion(text); question != "" {
				return question
			}
		}
	}
	if body, err := page.Element("body"); err == nil {
		text, _ := body.Text()
		return extractCaptchaQuestion(text)
	}
	return ""
}

func deepSearchText(page *rod.Page, query string, timeout time.Duration) (string, error) {
	result, err := page.CancelTimeout().Timeout(timeout).Search(query)
	if err != nil {
		return "", err
	}
	defer result.Release()
	if result.First == nil {
		return "", fmt.Errorf("未找到 %s", query)
	}
	elements, allErr := result.All()
	if allErr != nil {
		return "", allErr
	}
	bestText := ""
	bestProgress := -1
	for _, element := range elements {
		value, evalErr := element.Eval(`(query) => {
  let node = this.nodeType === 3 ? this.parentElement : this;
  const needle = String(query).toLowerCase();
  let best = '';
  for (let depth = 0; node && depth < 5; depth++, node = node.parentElement) {
    const text = String(node.innerText || node.textContent || '').replace(/\s+/g, ' ').trim();
    const rect = node.getBoundingClientRect ? node.getBoundingClientRect() : null;
    const style = node.nodeType === 1 ? getComputedStyle(node) : null;
    let ancestorsVisible = true;
    for (let parent = node; parent && parent.nodeType === 1; parent = parent.parentElement) {
      const parentStyle = getComputedStyle(parent);
      if (parentStyle.display === 'none' || parentStyle.visibility === 'hidden' || Number(parentStyle.opacity || 1) === 0) { ancestorsVisible = false; break; }
    }
    const visible = ancestorsVisible && rect && rect.width > 0 && rect.height > 0 && rect.bottom > 0 && rect.right > 0 && rect.top < innerHeight && rect.left < innerWidth && (!style || (style.display !== 'none' && style.visibility !== 'hidden'));
    if (visible && text.toLowerCase().includes(needle) && text.length <= 300 && text.length > best.length) best = text;
  }
  return best;
}`, query)
		if evalErr == nil && value.Value.Str() != "" {
			candidate := value.Value.Str()
			progress, _, hasProgress := captchaProgress(candidate)
			if (hasProgress && progress > bestProgress) ||
				(hasProgress && progress == bestProgress && (bestText == "" || len(candidate) < len(bestText))) ||
				(!hasProgress && bestProgress < 0 && len(candidate) > len(bestText)) {
				bestText = candidate
				if hasProgress {
					bestProgress = progress
				}
			}
		}
	}
	if bestText != "" {
		return bestText, nil
	}
	text := deepNodeText(result.First)
	if text == "" {
		return "", fmt.Errorf("节点文本为空")
	}
	return text, nil
}

func deepNodeText(element *rod.Element) string {
	result, err := element.Eval(`() => {
  if (this.nodeType === 3) return this.textContent || '';
  return this.innerText || this.textContent || '';
}`)
	if err != nil {
		return ""
	}
	return result.Value.Str()
}

func extractCaptchaQuestion(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.Join(strings.Fields(line), " "))
		if isCaptchaQuestion(line) {
			return line
		}
	}
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if isCaptchaQuestion(text) {
		return text
	}
	return ""
}

func isCaptchaQuestion(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if len(lower) < 8 || len(lower) > 300 {
		return false
	}
	for _, prefix := range []string{"pick ", "select ", "use ", "choose ", "match ", "please pick", "please select"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func clickCaptchaObjects(page *rod.Page, image *rod.Element, objects []int) error {
	if len(objects) == 0 {
		return fmt.Errorf("验证码识别结果为空")
	}
	buttons, _ := deepSearchAll(page, "button, [role='button'], [aria-label]", 3*time.Second)
	imageShape, _ := image.Shape()
	var imageBox *proto.DOMRect
	if imageShape != nil {
		imageBox = imageShape.Box()
	}
	type choice struct {
		el     *rod.Element
		x, y   float64
		width  float64
		height float64
	}
	var choices []choice
	for _, button := range buttons {
		visible, _ := button.Visible()
		shape, err := button.Shape()
		if !visible || err != nil || shape.Box() == nil {
			continue
		}
		box := shape.Box()
		centerX, centerY := box.X+box.Width/2, box.Y+box.Height/2
		insideImage := imageBox != nil && centerX >= imageBox.X && centerX <= imageBox.X+imageBox.Width &&
			centerY >= imageBox.Y && centerY <= imageBox.Y+imageBox.Height
		if insideImage && box.Width >= 28 && box.Height >= 28 &&
			box.Width <= imageBox.Width*0.75 && box.Height <= imageBox.Height*0.75 {
			choices = append(choices, choice{button, box.X, box.Y, box.Width, box.Height})
		}
	}
	sort.Slice(choices, func(i, j int) bool {
		if abs(choices[i].y-choices[j].y) > 20 {
			return choices[i].y < choices[j].y
		}
		return choices[i].x < choices[j].x
	})
	maxIndex := objects[0]
	for _, index := range objects[1:] {
		if index > maxIndex {
			maxIndex = index
		}
	}
	if len(choices) >= 2 && maxIndex < len(choices) {
		for _, index := range objects {
			if index < 0 || index >= len(choices) {
				return fmt.Errorf("验证码索引 %d 越界", index)
			}
			if err := choices[index].el.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return fmt.Errorf("点击验证码选项 %d: %w", index, err)
			}
		}
		return nil
	}
	for _, index := range objects {
		if index < 0 {
			return fmt.Errorf("验证码索引 %d 越界", index)
		}
		_, err := image.Eval(`(index) => {
  const rect = this.getBoundingClientRect();
  const cols = 3, rows = 2;
  const x = rect.left + (index % cols + 0.5) * rect.width / cols;
  const y = rect.top + (Math.floor(index / cols) + 0.5) * rect.height / rows;
  const target = document.elementFromPoint(x, y);
  if (!target) throw new Error('no element at captcha grid position');
  target.dispatchEvent(new MouseEvent('mousedown', {bubbles:true, clientX:x, clientY:y}));
  target.dispatchEvent(new MouseEvent('mouseup', {bubbles:true, clientX:x, clientY:y}));
  target.click();
}`, index)
		if err != nil {
			return fmt.Errorf("点击验证码网格 %d: %w", index, err)
		}
	}
	return nil
}

func isConsoleReady(page *rod.Page) bool {
	if adobeConsoleTargetOpen(page.Browser()) {
		return true
	}
	return adobeConsolePage(page, "")
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
