/*
===============================================================================================
[프로그램 명세서 및 작동 논리 (Program Specification & Logic)]

1. 프로그램 개요 (Overview)
   - 이 프로그램은 정적 웹사이트(로컬 폴더) 또는 원격 웹사이트(URL)의 리소스를 수집하여
     로컬 환경에서 오프라인으로 열람 가능하도록 미러링(Mirroring)하는 도구입니다.
   - 단순한 파일 복사가 아니라, HTML/CSS 내부의 링크를 분석하여 로컬 상대 경로로 자동 변환합니다.
   - 원격 모드에서는 Headless Browser(Chrome)를 사용하여 동적으로 렌더링된 페이지(SPA)도 캡처합니다.

2. 실행 모드 및 판단 로직 (Execution Modes)
   A. 원격 모드 (Remote Mode)
      - 입력값이 "http://" 또는 "https://"로 시작할 경우 활성화.
      - Chromedp 라이브러리를 사용하여 실제 크롬 브라우저를 백그라운드에서 실행.
      - 1920x1080 해상도로 페이지를 로딩하고, 자바스크립트를 주입하여 화면을 끝까지 스크롤(Lazy Loading 유도).
      - 렌더링이 완료된 최종 DOM(HTML)을 추출하여 파싱 시작.
   B. 로컬 모드 (Local Mode)
      - 입력값이 일반 파일 경로일 경우 활성화.
      - os.ReadFile을 통해 HTML 파일을 직접 읽어 파싱 시작.
      - <script> 태그 내부의 텍스트를 분석하여 동적으로 연결된 .html 파일도 재귀적으로 추적.

3. 주요 처리 로직 (Core Processing Logic)
   - 사전 유효성 검사: 작업 시작 전, 대상 URL 접속 가능 여부 또는 로컬 파일 존재 여부를 먼저 확인.
     실패 시 출력 폴더를 생성하지 않고 즉시 종료.
   - HTML 파싱 & 경로 변환: "golang.org/x/net/html" 패키지로 DOM 순회.
     src, href 속성을 찾아 리소스를 다운로드한 후, "저장될 HTML 파일의 위치"와 "저장된 리소스의 위치" 간의
     상대 경로(filepath.Rel)를 계산하여 링크를 수정함.
   - 리소스 평탄화 (Flattening): 모든 리소스는 다음 두 폴더에 통합 저장됨.
     - /front_local/assets/ : 이미지, JS, CSS 등
     - /front_local/fonts/  : 폰트 파일 (.woff, .ttf, .eot 등)
   - CSS 재귀 처리: 다운로드된 파일이 CSS일 경우, 정규식으로 "url(...)" 패턴을 찾아
     내부의 이미지나 폰트 파일을 재귀적으로 다운로드하고 경로를 수정함.

4. 출력 디렉토리 구조 (Directory Structure)
   /front_local
       ├── index.html (경로가 변환된 메인 파일)
       ├── sub/about.html (하위 폴더 구조 유지)
       ├── assets/ (모든 정적 리소스)
       └── fonts/  (모든 폰트 리소스)

===============================================================================================
*/

package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"golang.org/x/net/html"
)

// ==========================================
// [전역 설정 및 상태 변수]
// ==========================================

var (
	RootDir   string // 작업의 기준이 되는 루트 경로 (로컬 폴더 경로 또는 웹 Base URL)
	StartFile string // 최초 진입점이 되는 파일명 (예: index.html)
	OutputDir string // 결과물이 저장될 최종 루트 폴더 (예: ./front_local)
	AssetDir  = "assets" // JS, CSS, 이미지 저장 하위 폴더명
	FontDir   = "fonts"  // 폰트 파일 저장 하위 폴더명
	IsRemote  bool       // 원격 URL 크롤링 모드 여부
)

// 통계 집계용 변수
var (
	totalFiles int
	totalBytes int64
)

// 중복 처리 방지를 위한 맵 (Key: 원본 경로/URL, Value: 로컬 저장 상대경로)
var processedFiles = make(map[string]string)

// 무한 루프 방지를 위한 방문한 HTML 기록
var visitedHTMLs = make(map[string]bool)

// ==========================================
// [메인 실행 함수]
// ==========================================
func main() {
	// 1. 커맨드 라인 옵션 파싱
	outputParent := flag.String("o", ".", "결과물이 저장될 부모 폴더 경로")
	flag.Parse()

	// 2. 입력값 분석 및 설정 (로컬 폴더 vs 웹 URL)
	args := flag.Args()
	inputArg := "front" // 기본값
	if len(args) > 0 && args[0] != "." {
		inputArg = args[0]
	}

	// http:// 또는 https:// 로 시작하는지 확인하여 모드 결정
	if strings.HasPrefix(inputArg, "http://") || strings.HasPrefix(inputArg, "https://") {
		setupRemoteMode(inputArg)
	} else {
		setupLocalMode(inputArg)
	}

	// 3. [중요] 입력 경로 유효성 사전 검사 (출력 폴더 생성 전)
	if !validateInput() {
		os.Exit(1) // 유효하지 않으면 에러 메시지 출력 후 종료
	}

	// 4. 출력 폴더 설정 및 중복 확인
	OutputDir = filepath.Join(*outputParent, "front_local")
	if !checkAndPrepareOutput() {
		return // 사용자가 덮어쓰기를 거부하면 종료
	}

	// 5. 작업 시작 정보 출력
	printStartInfo()

	// 6. 메인 프로세스 시작 (재귀적 수집의 진입점)
	err := processHTMLFile(StartFile)

	// 7. 결과 통계 출력
	printResult(err)
}

// ==========================================
// [설정 및 유효성 검사 함수들]
// ==========================================

func setupRemoteMode(inputURL string) {
	IsRemote = true
	u, err := url.Parse(inputURL)
	if err != nil {
		panic("잘못된 URL입니다: " + err.Error())
	}

	// URL이 파일인지 폴더인지 판단
	ext := filepath.Ext(u.Path)
	isExplicitFile := ext != "" || (!strings.HasSuffix(u.Path, "/") && u.Path != "" && u.Path != "/")

	if isExplicitFile {
		StartFile = path.Base(u.Path)
		u.Path = path.Dir(u.Path)
	} else {
		StartFile = "index.html"
	}

	// Base URL 정규화
	if u.Path == "." || u.Path == "" {
		u.Path = "/"
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	RootDir = u.String()
}

func setupLocalMode(inputPath string) {
	IsRemote = false
	RootDir = inputPath
	StartFile = "index.html"
}

// validateInput: 입력된 경로/URL이 실제로 접근 가능한지 확인
func validateInput() bool {
	if IsRemote {
		checkURL := RootDir
		if StartFile != "index.html" {
			u, _ := url.Parse(RootDir)
			rel, _ := url.Parse(StartFile)
			checkURL = u.ResolveReference(rel).String()
		}

		client := &http.Client{Timeout: 5 * time.Second}
		req, _ := http.NewRequest("GET", checkURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("❌ 오류: 원격 서버에 접속할 수 없습니다.\n   주소: %s\n   에러: %v\n", checkURL, err)
			return false
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			fmt.Printf("❌ 오류: 원격 리소스를 찾을 수 없습니다 (HTTP %d).\n   주소: %s\n", resp.StatusCode, checkURL)
			return false
		}
	} else {
		checkPath := filepath.Join(RootDir, StartFile)
		if _, err := os.Stat(checkPath); os.IsNotExist(err) {
			fmt.Printf("❌ 오류: 입력 경로에서 시작 파일을 찾을 수 없습니다.\n   경로: %s\n", checkPath)
			return false
		}
	}
	return true
}

// checkAndPrepareOutput: 출력 폴더 존재 여부 확인 및 하위 디렉토리 생성
func checkAndPrepareOutput() bool {
	if info, err := os.Stat(OutputDir); err == nil && info.IsDir() {
		fmt.Printf("⚠️  경고: '%s' 폴더가 이미 존재합니다.\n", OutputDir)
		fmt.Print("   삭제 후 다시 생성하시겠습니까? (Y/n): ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))

		if input == "y" || input == "" {
			fmt.Println("♻️  기존 폴더 삭제 중...")
			os.RemoveAll(OutputDir)
		} else {
			fmt.Println("❌ 작업을 취소합니다.")
			return false
		}
	}

	// 디렉토리 생성
	dirs := []string{
		filepath.Join(OutputDir, AssetDir),
		filepath.Join(OutputDir, FontDir),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			panic(err)
		}
	}
	return true
}

func printStartInfo() {
	fmt.Println("==================================================")
	mode := "로컬 파일 분석"
	if IsRemote {
		mode = "웹 렌더링 (Headless Browser)"
	}
	fmt.Printf("🚀 작업 시작 (%s)\n   🔗 소스: %s (시작: %s)\n   📂 출력: %s\n", mode, RootDir, StartFile, OutputDir)
	fmt.Println("==================================================")
}

func printResult(err error) {
	fmt.Println("==================================================")
	if err != nil {
		fmt.Printf("❌ 오류 발생: %v\n", err)
	} else {
		fmt.Printf("✅ 작업 완료!\n")
		fmt.Printf("Total file %d saved bytes %s\n", totalFiles, formatComma(totalBytes))
	}
}

// ==========================================
// [핵심 로직 처리 함수들]
// ==========================================

// processHTMLFile: HTML 파일을 읽어 분석하고, 의존 리소스를 다운로드한 뒤 저장함
// - htmlRelPath: RootDir 기준의 상대 경로
func processHTMLFile(htmlRelPath string) error {
	normalizedPath := filepath.ToSlash(htmlRelPath)
	if visitedHTMLs[normalizedPath] {
		return nil
	}
	visitedHTMLs[normalizedPath] = true

	// 저장될 파일의 절대 경로
	outputFile := filepath.Join(OutputDir, htmlRelPath)
	// 저장될 파일이 위치한 로컬 디렉토리 (이 경로를 기준으로 상대 경로를 다시 계산함)
	localHtmlDir := filepath.Dir(outputFile)

	var currentContext string
	var content []byte
	var err error

	if IsRemote {
		// [원격 모드] URL 계산 및 Headless 브라우저 사용
		u, err := url.Parse(RootDir)
		if err != nil {
			return err
		}
		rel, err := url.Parse(normalizedPath)
		if err != nil {
			return err
		}
		targetURL := u.ResolveReference(rel).String()

		// 리소스 참조를 위한 Context URL 설정
		currentContext = targetURL
		if path.Ext(targetURL) != "" {
			currentContext = path.Dir(targetURL)
			if !strings.HasSuffix(currentContext, "/") {
				currentContext += "/"
			}
		}

		fmt.Printf(" 🖥️  브라우저 렌더링 중... (%s)\n", targetURL)
		content, err = fetchRenderedHTML(targetURL)
		if err != nil {
			return fmt.Errorf("Chrome 렌더링 실패: %w", err)
		}

	} else {
		// [로컬 모드] 단순 파일 읽기
		inputFile := filepath.Join(RootDir, htmlRelPath)
		currentContext = filepath.Dir(htmlRelPath) // 로컬 폴더 경로가 Context
		content, err = os.ReadFile(inputFile)
	}

	if err != nil {
		return fmt.Errorf("HTML 읽기 실패: %w", err)
	}

	// HTML 파싱
	doc, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return err
	}

	// 트리 구조 출력
	displayPath := filepath.ToSlash(filepath.Join("front_local", htmlRelPath))
	fmt.Printf(" 📄 %s\n", displayPath)

	// DOM 순회 및 속성 처리
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "script" {
				handleAttribute(n, "src", currentContext, localHtmlDir)
				// 로컬 모드일 때만 스크립트 텍스트 파싱 (원격은 이미 렌더링됨)
				if !IsRemote {
					scanScriptContent(n, currentContext)
				}
			}
			if n.Data == "link" {
				handleAttribute(n, "href", currentContext, localHtmlDir)
			}
			if n.Data == "img" {
				handleAttribute(n, "src", currentContext, localHtmlDir)
				// Lazy Loading 대응
				handleAttribute(n, "data-src", currentContext, localHtmlDir)
			}
			if n.Data == "iframe" {
				handleIframe(n, currentContext)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	// 결과 파일 저장
	if err := os.MkdirAll(filepath.Dir(outputFile), 0755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return err
	}

	err = os.WriteFile(outputFile, buf.Bytes(), 0644)
	if err == nil {
		updateStats(int64(buf.Len()))
	}
	return err
}

// fetchRenderedHTML: [원격 전용] Chromedp로 페이지를 열고 스크롤하여 최종 렌더링된 HTML을 반환
// - Timeout: 10초, Scroll Wait: 1초
func fetchRenderedHTML(urlStr string) ([]byte, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36"),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 전체 타임아웃 10초
	ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var res string

	// 스크롤 스크립트 (간격 100ms, 완료 대기 1s)
	scrollScript := `
		const scrollStep = 300;
		const delay = 100; 
		const scrollHeight = document.body.scrollHeight;
		let currentScroll = 0;
		
		const timer = setInterval(() => {
			window.scrollBy(0, scrollStep);
			currentScroll += scrollStep;
			if (currentScroll >= document.body.scrollHeight) {
				clearInterval(timer);
			}
		}, delay);
		
		new Promise(resolve => setTimeout(resolve, 1000));
	`

	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1920, 1080),
		chromedp.Navigate(urlStr),
		chromedp.Sleep(1*time.Second),        // 초기 로딩 대기
		chromedp.Evaluate(scrollScript, nil), // 스크롤 실행
		chromedp.Sleep(1*time.Second),        // 데이터 로딩 대기
		chromedp.OuterHTML("html", &res),
	)

	if err != nil {
		return nil, err
	}
	return []byte(res), nil
}

// scanScriptContent: [로컬 전용] 스크립트 내부의 HTML 파일 참조 문자열을 찾아 재귀 분석
func scanScriptContent(n *html.Node, currentBaseDir string) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			re := regexp.MustCompile(`['"]([^'"]+\.html)['"]`)
			matches := re.FindAllStringSubmatch(c.Data, -1)
			for _, match := range matches {
				if len(match) < 2 {
					continue
				}
				detectedFile := match[1]
				if strings.HasPrefix(detectedFile, "http") || strings.HasPrefix(detectedFile, "//") {
					continue
				}
				localSrcCheck := filepath.Join(RootDir, currentBaseDir, detectedFile)
				if _, err := os.Stat(localSrcCheck); err == nil {
					processHTMLFile(filepath.Join(currentBaseDir, detectedFile))
				}
			}
		}
	}
}

// handleIframe: Iframe 태그를 발견하면 해당 소스를 재귀적으로 처리
func handleIframe(n *html.Node, currentBaseDir string) {
	for i, a := range n.Attr {
		if a.Key == "src" {
			if a.Val == "" || strings.HasPrefix(a.Val, "http") || strings.HasPrefix(a.Val, "//") {
				continue
			}
			if err := processHTMLFile(a.Val); err == nil {
				n.Attr[i].Val = filepath.ToSlash(a.Val)
			}
		}
	}
}

// handleAttribute: HTML 태그의 리소스 링크(src, href)를 찾아 다운로드 및 경로 수정
func handleAttribute(n *html.Node, attrName string, currentContext string, localHtmlDir string) {
	for i, a := range n.Attr {
		if a.Key == attrName {
			val := strings.TrimSpace(a.Val)
			if val == "" || strings.HasPrefix(val, "data:") || strings.HasPrefix(val, "#") {
				continue
			}

			resourceRelPath, err := downloadResource(val, currentContext)
			if err == nil {
				// HTML 파일 위치에서 리소스 위치까지의 상대 경로 계산
				absResourcePath := filepath.Join(OutputDir, resourceRelPath)
				relPath, err := filepath.Rel(localHtmlDir, absResourcePath)
				if err == nil {
					n.Attr[i].Val = filepath.ToSlash(relPath)
				}
			}
		}
	}
}

// downloadResource: 리소스를 다운로드하여 assets/fonts 폴더에 저장하고 상대 경로 반환
func downloadResource(urlOrPath string, context string) (string, error) {
	var targetURL string
	var isRemote bool

	// 다운로드할 절대 경로/URL 계산
	if strings.HasPrefix(context, "http") {
		baseURL, err := url.Parse(context)
		if err != nil {
			return "", err
		}
		relURL, err := url.Parse(urlOrPath)
		if err != nil {
			return "", err
		}
		targetURL = baseURL.ResolveReference(relURL).String()
		isRemote = true
	} else {
		if strings.HasPrefix(urlOrPath, "http") || strings.HasPrefix(urlOrPath, "//") {
			targetURL = urlOrPath
			if strings.HasPrefix(urlOrPath, "//") {
				targetURL = "https:" + urlOrPath
			}
			isRemote = true
		} else {
			targetURL = filepath.Join(RootDir, context, urlOrPath)
			isRemote = false
		}
	}

	// 중복 다운로드 체크
	if savedRelPath, ok := processedFiles[targetURL]; ok {
		return savedRelPath, nil
	}

	// 저장할 파일명 및 폴더 결정
	u, _ := url.Parse(targetURL)
	var fileName string
	if isRemote {
		fileName = path.Base(u.Path)
	} else {
		fileName = filepath.Base(targetURL)
	}

	if idx := strings.Index(fileName, "?"); idx != -1 {
		fileName = fileName[:idx]
	}
	if fileName == "." || fileName == "/" || fileName == "" {
		if isRemote && u.Host != "" {
			fileName = u.Host + ".js"
		} else {
			fileName = "resource.bin"
		}
	}

	targetSubDir := AssetDir
	if isFontFile(fileName) {
		targetSubDir = FontDir
	}

	saveRelPath := filepath.Join(targetSubDir, fileName)
	saveFullPath := filepath.Join(OutputDir, saveRelPath)

	// 데이터 다운로드 (HTTP Get 또는 File Read)
	var data []byte
	var err error

	if isRemote {
		resp, err := http.Get(targetURL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("status %d", resp.StatusCode)
		}
		data, err = io.ReadAll(resp.Body)
	} else {
		data, err = os.ReadFile(targetURL)
	}
	if err != nil {
		return "", err
	}

	// CSS 파일은 내부 재귀 파싱
	if strings.HasSuffix(strings.ToLower(fileName), ".css") {
		var newContext string
		if isRemote {
			newContext = targetURL
		} else {
			newContext = filepath.Dir(urlOrPath)
		}
		data = processCSSContent(data, newContext, targetSubDir)
	}

	// 파일 저장
	if err := os.WriteFile(saveFullPath, data, 0644); err != nil {
		return "", err
	}

	updateStats(int64(len(data)))
	displayPath := "/" + filepath.ToSlash(saveRelPath)
	fmt.Printf("           └── %s\n", displayPath)

	processedFiles[targetURL] = saveRelPath
	return saveRelPath, nil
}

// processCSSContent: CSS 내부의 url(...) 참조를 찾아 다운로드 및 경로 수정
func processCSSContent(cssData []byte, contextURL string, cssSavedDir string) []byte {
	cssStr := string(cssData)
	re := regexp.MustCompile(`url\(['"]?(.*?)['"]?\)`)

	newCSS := re.ReplaceAllStringFunc(cssStr, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		link := strings.TrimSpace(parts[1])
		if strings.HasPrefix(link, "data:") || strings.HasPrefix(link, "#") {
			return match
		}

		resourcePath, err := downloadResource(link, contextURL)
		if err != nil {
			return match
		}

		absCssDir := filepath.Join(OutputDir, cssSavedDir)
		absResourcePath := filepath.Join(OutputDir, resourcePath)
		relPath, err := filepath.Rel(absCssDir, absResourcePath)
		if err != nil {
			return match
		}

		return fmt.Sprintf("url('%s')", filepath.ToSlash(relPath))
	})
	return []byte(newCSS)
}

func isFontFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".woff", ".woff2", ".ttf", ".otf", ".eot":
		return true
	}
	return false
}

func updateStats(size int64) {
	totalFiles++
	totalBytes += size
}

func formatComma(n int64) string {
	in := fmt.Sprintf("%d", n)
	numOfDigits := len(in)
	if n < 0 {
		numOfDigits--
	}
	numOfCommas := (numOfDigits - 1) / 3

	out := make([]byte, len(in)+numOfCommas)
	if n < 0 {
		in, out[0] = in[1:], '-'
	}

	for i, j, k := len(in)-1, len(out)-1, 0; ; i, j = i-1, j-1 {
		out[j] = in[i]
		if i == 0 {
			return string(out)
		}
		if k++; k == 3 {
			j, k = j-1, 0
			out[j] = ','
		}
	}
}