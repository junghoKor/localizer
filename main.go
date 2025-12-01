/*
===============================================================================================
[프로그램 명세서 및 작동 논리 (Program Architecture & Logic Specification)]
Designed for AI Context Understanding

1. 프로그램 개요 (Overview)
   - 이 프로그램은 정적 웹사이트(로컬 파일 시스템) 또는 원격 웹사이트(URL)의 모든 리소스를
     재귀적으로 수집하여 로컬 환경에서 오프라인으로 열람 가능하도록 미러링(Mirroring)하는 도구입니다.
   - 단순한 파일 다운로드가 아닌, HTML/CSS 내부의 참조 링크를 분석하여 로컬 상대 경로로 자동 변환합니다.

2. 실행 모드 (Execution Modes)
   A. 원격 모드 (Remote Mode)
      - 조건: 입력값이 "http://" 또는 "https://"로 시작할 때.
      - 동작: Headless Browser (Chromedp)를 사용하여 페이지를 엽니다.
      - 특징:
        1. 메인 스레드와 별개로 고루틴(Goroutine)이 브라우저 렌더링을 즉시 시작 (Pre-fetching).
        2. 1920x1080 해상도로 렌더링하며, 초기 로딩 5초 대기.
        3. 렌더링된 최종 DOM(OuterHTML)을 추출하여 파싱.
   B. 로컬 모드 (Local Mode)
      - 조건: 입력값이 일반 파일/폴더 경로일 때.
      - 동작: os.ReadFile을 통해 파일을 직접 읽습니다.
      - 특징: <script> 태그 내부의 텍스트까지 정규식으로 분석하여 동적으로 연결된 .html 파일도 추적.

3. 실행 옵션 (-o Flag)
   - "-o [경로]": 지정된 경로에 결과물을 저장합니다. (예: -o my_site)
   - "-o ." 또는 옵션 미지정: 기본값 "front_local" 폴더에 저장합니다.
   - 안전장치: 출력 폴더가 이미 존재할 경우, 사용자에게 삭제 여부(Y/n)를 확인합니다.

4. 타임아웃 및 리소스 관리 (Safety & Constraints)
   - Global Timeout: 전체 작업은 60초(1분)로 제한됩니다. 초과 시 작업 취소 및 경고 출력.
   - HTTP Client: 개별 리소스 다운로드는 30초 타임아웃이 적용됩니다.
   - Caching: 이미 다운로드된 리소스(Disk Cache)는 중복 요청하지 않고 건너뜁니다.

5. 데이터 처리 파이프라인 (Processing Pipeline)
   Step 1. 입력값 분석 (URL vs Local) 및 모드 설정.
   Step 2. 사전 유효성 검사 (URL 접속 가능 여부 / 파일 존재 여부).
   Step 3. 출력 디렉토리 준비 (/assets, /fonts 생성).
   Step 4. HTML 파싱 (Golang net/html 패키지 사용).
   Step 5. DOM 순회 -> 리소스 발견 -> 다운로드 -> 경로 재계산(filepath.Rel) -> 속성값 수정.
   Step 6. CSS 파일인 경우, 내부의 url(...) 패턴을 찾아 재귀적으로 리소스 다운로드.
   Step 7. 최종 파일 저장 및 통계 출력.

===============================================================================================
*/

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
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
	OutputDir string // 결과물이 저장될 최종 루트 폴더
	AssetDir  = "assets" // JS, CSS, 이미지 저장 하위 폴더명
	FontDir   = "fonts"  // 폰트 파일 저장 하위 폴더명
	IsRemote  bool       // 원격 URL 크롤링 모드 여부
)

// 30초 타임아웃이 설정된 HTTP 클라이언트 (개별 리소스 요청용)
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// 고루틴 결과를 전달받기 위한 구조체
type RenderResult struct {
	Data []byte
	Err  error
}

// 메인 페이지 렌더링 결과를 전달받는 채널
var rootRenderChan chan RenderResult

// 통계 집계용 변수
var (
	totalFiles int
	totalBytes int64
)

// 중복 처리 방지 및 방문 기록 맵
var processedFiles = make(map[string]string)
var visitedHTMLs = make(map[string]bool)

// ==========================================
// [메인 실행 함수]
// ==========================================
func main() {
	// 0. 프로그램 배너 출력
	fmt.Println("===================================================")
	fmt.Println("   JunghoKor's AI Web page local downloader v0.2")
	fmt.Println("===================================================")

	// 1. [전처리] 인자 재배열 (Flag Reordering)
	// Go flag 패키지는 [옵션] [인자] 순서를 강제하므로, 사용자가 섞어 써도 동작하도록 재배열
	var flagArgs []string
	var normalArgs []string
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if strings.HasPrefix(arg, "-") {
			// -otest 처럼 붙여쓴 경우 분리
			if strings.HasPrefix(arg, "-o") && len(arg) > 2 && arg[2] != '=' {
				flagArgs = append(flagArgs, "-o")
				flagArgs = append(flagArgs, arg[2:])
			} else {
				flagArgs = append(flagArgs, arg)
				// -o 뒤에 값이 바로 오면 같이 가져감
				if arg == "-o" && i+1 < len(os.Args) && !strings.HasPrefix(os.Args[i+1], "-") {
					flagArgs = append(flagArgs, os.Args[i+1])
					i++
				}
			}
		} else {
			normalArgs = append(normalArgs, arg)
		}
	}
	os.Args = append([]string{os.Args[0]}, append(flagArgs, normalArgs...)...)

	// 2. 옵션 파싱
	outputFlag := flag.String("o", "", "결과물이 저장될 폴더 경로")
	flag.Parse()

	// 3. 출력 폴더 결정 로직
	if *outputFlag == "" || *outputFlag == "." {
		OutputDir = "front_local"
	} else {
		OutputDir = *outputFlag
	}

	// 4. 입력값 분석 및 모드 결정
	args := flag.Args()
	inputArg := "front" // 기본값
	if len(args) > 0 {
		inputArg = args[0]
	}

	// 전체 작업에 대한 60초 타임아웃 컨텍스트 생성
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if strings.HasPrefix(inputArg, "http://") || strings.HasPrefix(inputArg, "https://") {
		setupRemoteMode(inputArg)
		// 원격 모드일 경우, 메인 페이지 렌더링을 백그라운드에서 즉시 시작
		startRemoteRendering(ctx)
	} else {
		setupLocalMode(inputArg)
	}

	// 5. 입력 경로 유효성 검사 (실제 접속/존재 확인)
	if !validateInput() {
		os.Exit(1)
	}

	// 6. 출력 폴더 생성 및 중복 확인 (사용자 동의)
	if !checkAndPrepareOutput() {
		return
	}

	// 7. 작업 시작
	printStartInfo()
	err := processHTMLFile(ctx, StartFile)

	// 8. 결과 통계 출력
	printResult(err)
}

// ==========================================
// [설정 및 유효성 검사 함수들]
// ==========================================

// setupRemoteMode: 원격 URL을 분석하여 RootDir(Base URL)과 StartFile을 설정합니다.
func setupRemoteMode(inputURL string) {
	IsRemote = true
	u, err := url.Parse(inputURL)
	if err != nil {
		panic("잘못된 URL입니다: " + err.Error())
	}
	ext := filepath.Ext(u.Path)
	// URL이 구체적인 파일을 가리키는지 확인
	isExplicitFile := ext != "" || (!strings.HasSuffix(u.Path, "/") && u.Path != "" && u.Path != "/")

	if isExplicitFile {
		StartFile = path.Base(u.Path)
		u.Path = path.Dir(u.Path)
	} else {
		StartFile = "index.html"
	}

	// Base URL 정규화
	if u.Path == "." || u.Path == "" { u.Path = "/" }
	if !strings.HasSuffix(u.Path, "/") { u.Path += "/" }
	RootDir = u.String()
}

// startRemoteRendering: 원격 URL 렌더링을 별도 고루틴에서 시작합니다. (프리페칭)
func startRemoteRendering(ctx context.Context) {
	rootRenderChan = make(chan RenderResult, 1)
	u, _ := url.Parse(RootDir)
	rel, _ := url.Parse(StartFile)
	targetURL := u.ResolveReference(rel).String()

	fmt.Println("-> 입력 URL 렌더링 시작")

	go func() {
		// fetchRenderedHTML 내부에서 30초 타임아웃 컨텍스트를 별도로 사용함
		data, err := fetchRenderedHTML(ctx, targetURL)
		
		// 메인 스레드가 이미 종료되었을 경우를 대비한 select
		select {
		case rootRenderChan <- RenderResult{Data: data, Err: err}:
		case <-ctx.Done():
		}
		close(rootRenderChan)
	}()
}

// setupLocalMode: 로컬 파일 경로를 기준으로 설정을 초기화합니다.
func setupLocalMode(inputPath string) {
	IsRemote = false
	RootDir = inputPath
	StartFile = "index.html"
}

// validateInput: 입력된 경로가 실제로 접근 가능한지 사전 검사합니다.
func validateInput() bool {
	if IsRemote {
		checkURL := RootDir
		if StartFile != "index.html" {
			u, _ := url.Parse(RootDir)
			rel, _ := url.Parse(StartFile)
			checkURL = u.ResolveReference(rel).String()
		}
		// 가벼운 HTTP Request로 연결 확인
		req, _ := http.NewRequest("GET", checkURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
		
		resp, err := httpClient.Do(req)
		if err != nil {
			fmt.Printf("❌ 오류: 원격 서버 접속 불가 (%s)\n", err)
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			fmt.Printf("❌ 오류: 원격 리소스 없음 (HTTP %d)\n", resp.StatusCode)
			return false
		}
	} else {
		checkPath := filepath.Join(RootDir, StartFile)
		if _, err := os.Stat(checkPath); os.IsNotExist(err) {
			fmt.Printf("❌ 오류: 입력 파일을 찾을 수 없음 (%s)\n", checkPath)
			return false
		}
	}
	return true
}

// checkAndPrepareOutput: 출력 폴더가 존재하면 삭제 여부를 묻고, 필요한 하위 폴더를 생성합니다.
func checkAndPrepareOutput() bool {
	if info, err := os.Stat(OutputDir); err == nil && info.IsDir() {
		absPath, _ := filepath.Abs(OutputDir)
		fmt.Printf("\n⚠️  경고: 출력 폴더가 이미 존재합니다.\n   경로: %s\n", absPath)
		fmt.Print("   기존 폴더를 삭제하고 다시 생성하시겠습니까? (Y/n): ")

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

	// assets, fonts 폴더 생성
	dirs := []string{
		filepath.Join(OutputDir, AssetDir),
		filepath.Join(OutputDir, FontDir),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			panic(fmt.Sprintf("폴더 생성 실패: %v", err))
		}
	}
	return true
}

func printStartInfo() {
	mode := "로컬 파일 분석"
	if IsRemote {
		mode = "웹 렌더링 (Headless Browser)"
	}
	absOut, _ := filepath.Abs(OutputDir)
	fmt.Printf("🚀 작업 시작 (%s)\n   🔗 소스: %s (시작: %s)\n   📂 출력: %s\n", mode, RootDir, StartFile, absOut)
	fmt.Println("==================================================")
}

func printResult(err error) {
	fmt.Println("==================================================")
	if err != nil {
		// Context 타임아웃 에러인지 확인
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
			fmt.Printf("*** Warning : Timeout (1분 초과)\n")
		} else {
			fmt.Printf("❌ 오류 발생: %v\n", err)
		}
	} else {
		fmt.Printf("✅ 작업 완료!\n")
	}
	fmt.Printf("Total %d files, saved %s bytes\n", totalFiles, formatComma(totalBytes))
}

// shouldIgnoreLink: 수집하지 말아야 할 스키마(data, mailto 등)를 필터링합니다.
func shouldIgnoreLink(link string) bool {
	link = strings.TrimSpace(strings.ToLower(link))
	if link == "" { return true }
	if strings.HasPrefix(link, "data:") ||
		strings.HasPrefix(link, "#") ||
		strings.HasPrefix(link, "about:") ||
		strings.HasPrefix(link, "javascript:") ||
		strings.HasPrefix(link, "mailto:") ||
		strings.HasPrefix(link, "tel:") ||
		strings.HasPrefix(link, "sms:") ||
		strings.HasPrefix(link, "chrome:") {
		return true
	}
	return false
}

// ==========================================
// [핵심 로직 처리 함수들]
// ==========================================

// processHTMLFile: HTML 파일을 처리하는 핵심 함수. 재귀적으로 호출될 수 있습니다.
func processHTMLFile(ctx context.Context, htmlRelPath string) error {
	// 작업 취소 확인
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	normalizedPath := filepath.ToSlash(htmlRelPath)
	if visitedHTMLs[normalizedPath] {
		return nil
	}
	visitedHTMLs[normalizedPath] = true

	outputFile := filepath.Join(OutputDir, htmlRelPath)
	localHtmlDir := filepath.Dir(outputFile)

	var currentContext string
	var content []byte
	var err error

	if IsRemote {
		u, err := url.Parse(RootDir)
		if err != nil { return err }
		rel, err := url.Parse(normalizedPath)
		if err != nil { return err }
		targetURL := u.ResolveReference(rel).String()

		currentContext = targetURL
		if path.Ext(targetURL) != "" {
			currentContext = path.Dir(targetURL)
			if !strings.HasSuffix(currentContext, "/") { currentContext += "/" }
		}

		// 시작 파일인 경우, 미리 실행해둔 고루틴의 결과를 기다림
		if htmlRelPath == StartFile && rootRenderChan != nil {
			fmt.Println(" ⏳ 렌더링 결과 대기 중 (최대 15초)...")
			select {
			case result := <-rootRenderChan:
				content, err = result.Data, result.Err
				if err != nil { return fmt.Errorf("Background 렌더링 실패: %w", err) }
				fmt.Println(" ✨ 렌더링 데이터 수신 완료")
			case <-time.After(15 * time.Second):
				return fmt.Errorf("⏳ 렌더링 시간 초과 (15초)")
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			// iframe 등으로 재귀 호출된 경우 동기적으로 렌더링
			fmt.Printf(" 🖥️  브라우저 렌더링 중... (%s)\n", targetURL)
			content, err = fetchRenderedHTML(ctx, targetURL)
			if err != nil { return fmt.Errorf("Chrome 렌더링 실패: %w", err) }
		}
	} else {
		// 로컬 파일 읽기
		inputFile := filepath.Join(RootDir, htmlRelPath)
		currentContext = filepath.Dir(htmlRelPath)
		content, err = os.ReadFile(inputFile)
	}

	if err != nil { return fmt.Errorf("HTML 읽기 실패: %w", err) }

	doc, err := html.Parse(bytes.NewReader(content))
	if err != nil { return err }

	displayPath := filepath.ToSlash(filepath.Join(OutputDir, htmlRelPath))
	fmt.Printf(" 📄 %s\n", displayPath)

	// DOM 순회하며 리소스 수집
	var f func(*html.Node)
	f = func(n *html.Node) {
		// 루프 내에서도 타임아웃 체크
		select {
		case <-ctx.Done():
			return
		default:
		}

		if n.Type == html.ElementNode {
			if n.Data == "script" {
				handleAttribute(ctx, n, "src", currentContext, localHtmlDir)
				if !IsRemote { scanScriptContent(ctx, n, currentContext) }
			}
			if n.Data == "link" {
				handleAttribute(ctx, n, "href", currentContext, localHtmlDir)
			}
			if n.Data == "img" {
				handleAttribute(ctx, n, "src", currentContext, localHtmlDir)
				handleAttribute(ctx, n, "data-src", currentContext, localHtmlDir)
			}
			if n.Data == "iframe" {
				handleIframe(ctx, n, currentContext)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	if ctx.Err() != nil { return ctx.Err() }

	// 변환된 HTML 저장
	if err := os.MkdirAll(filepath.Dir(outputFile), 0755); err != nil { return err }
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil { return err }

	err = os.WriteFile(outputFile, buf.Bytes(), 0644)
	if err == nil { updateStats(int64(buf.Len())) }
	return err
}

// fetchRenderedHTML: Chromedp를 이용하여 웹페이지를 렌더링하고 HTML을 반환합니다.
func fetchRenderedHTML(ctx context.Context, urlStr string) ([]byte, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36"),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()
	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	
	// 페이지별 최대 30초 타임아웃
	taskCtx, cancel = context.WithTimeout(taskCtx, 30*time.Second)
	defer cancel()

	var res string

	err := chromedp.Run(taskCtx,
		chromedp.EmulateViewport(1920, 1080),
		chromedp.Navigate(urlStr),
		chromedp.Sleep(5*time.Second), // DOM 구성 대기
		chromedp.OuterHTML("html", &res),
	)

	if err != nil { return nil, err }
	return []byte(res), nil
}

// scanScriptContent: 로컬 스크립트 내의 HTML 파일 참조를 스캔합니다.
func scanScriptContent(ctx context.Context, n *html.Node, currentBaseDir string) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			re := regexp.MustCompile(`['"]([^'"]+\.html)['"]`)
			matches := re.FindAllStringSubmatch(c.Data, -1)
			for _, match := range matches {
				if ctx.Err() != nil { return }
				if len(match) < 2 { continue }
				detectedFile := match[1]
				if shouldIgnoreLink(detectedFile) { continue }
				if strings.HasPrefix(detectedFile, "http") || strings.HasPrefix(detectedFile, "//") { continue }
				localSrcCheck := filepath.Join(RootDir, currentBaseDir, detectedFile)
				if _, err := os.Stat(localSrcCheck); err == nil {
					processHTMLFile(ctx, filepath.Join(currentBaseDir, detectedFile))
				}
			}
		}
	}
}

// handleIframe: Iframe 태그를 처리합니다.
func handleIframe(ctx context.Context, n *html.Node, currentBaseDir string) {
	for i, a := range n.Attr {
		if a.Key == "src" {
			if shouldIgnoreLink(a.Val) { continue }
			if strings.HasPrefix(a.Val, "http") || strings.HasPrefix(a.Val, "//") { continue }
			if err := processHTMLFile(ctx, a.Val); err == nil {
				n.Attr[i].Val = filepath.ToSlash(a.Val)
			}
		}
	}
}

// handleAttribute: 일반 리소스 속성(src, href)을 처리합니다.
func handleAttribute(ctx context.Context, n *html.Node, attrName string, currentContext string, localHtmlDir string) {
	for i, a := range n.Attr {
		if a.Key == attrName {
			val := strings.TrimSpace(a.Val)
			if shouldIgnoreLink(val) { continue }

			resourceRelPath, err := downloadResource(ctx, val, currentContext)
			if err == nil {
				absResourcePath := filepath.Join(OutputDir, resourceRelPath)
				relPath, err := filepath.Rel(localHtmlDir, absResourcePath)
				if err == nil {
					n.Attr[i].Val = filepath.ToSlash(relPath)
				}
			}
		}
	}
}

// downloadResource: 리소스를 다운로드하고 저장합니다. (중복 확인 및 캐싱 포함)
func downloadResource(ctx context.Context, urlOrPath string, contextStr string) (string, error) {
	if ctx.Err() != nil { return "", ctx.Err() }

	var targetURL string
	var isRemote bool

	// 다운로드 대상 절대 경로 계산
	if strings.HasPrefix(contextStr, "http") {
		baseURL, err := url.Parse(contextStr)
		if err != nil { return "", err }
		relURL, err := url.Parse(urlOrPath)
		if err != nil { return "", err }
		targetURL = baseURL.ResolveReference(relURL).String()
		isRemote = true
	} else {
		if strings.HasPrefix(urlOrPath, "http") || strings.HasPrefix(urlOrPath, "//") {
			targetURL = urlOrPath
			if strings.HasPrefix(urlOrPath, "//") { targetURL = "https:" + urlOrPath }
			isRemote = true
		} else {
			targetURL = filepath.Join(RootDir, contextStr, urlOrPath)
			isRemote = false
		}
	}

	if savedRelPath, ok := processedFiles[targetURL]; ok { return savedRelPath, nil }

	u, _ := url.Parse(targetURL)
	var fileName string
	if isRemote { fileName = path.Base(u.Path) } else { fileName = filepath.Base(targetURL) }

	if idx := strings.Index(fileName, "?"); idx != -1 { fileName = fileName[:idx] }
	if fileName == "." || fileName == "/" || fileName == "" {
		if isRemote && u.Host != "" { fileName = u.Host + ".js" } else { fileName = "resource.bin" }
	}

	targetSubDir := AssetDir
	if isFontFile(fileName) { targetSubDir = FontDir }

	saveRelPath := filepath.Join(targetSubDir, fileName)
	saveFullPath := filepath.Join(OutputDir, saveRelPath)

	// [캐싱] 이미 존재하는 파일이면 다운로드 스킵
	if info, err := os.Stat(saveFullPath); err == nil && !info.IsDir() {
		processedFiles[targetURL] = saveRelPath
		displayPath := "/" + filepath.ToSlash(filepath.Join(filepath.Base(OutputDir), saveRelPath))
		fmt.Printf("           └── %s (Cached)\n", displayPath)

		// CSS라면 내부 파싱만 다시 수행
		if strings.HasSuffix(strings.ToLower(fileName), ".css") {
			content, _ := os.ReadFile(saveFullPath)
			var newContext string
			if isRemote { newContext = targetURL } else { newContext = filepath.Dir(urlOrPath) }
			processCSSContent(ctx, content, newContext, targetSubDir)
		}
		return saveRelPath, nil
	}

	var data []byte
	var err error

	if isRemote {
		req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
		if err != nil { return "", err }
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

		resp, err := httpClient.Do(req)
		if err != nil { return "", err }
		defer resp.Body.Close()
		if resp.StatusCode != 200 { return "", fmt.Errorf("status %d", resp.StatusCode) }
		data, err = io.ReadAll(resp.Body)
	} else {
		data, err = os.ReadFile(targetURL)
	}
	if err != nil { return "", err }

	// CSS 파일 내부 파싱 (재귀)
	if strings.HasSuffix(strings.ToLower(fileName), ".css") {
		var newContext string
		if isRemote { newContext = targetURL } else { newContext = filepath.Dir(urlOrPath) }
		data = processCSSContent(ctx, data, newContext, targetSubDir)
	}

	if err := os.WriteFile(saveFullPath, data, 0644); err != nil { return "", err }

	updateStats(int64(len(data)))
	displayPath := "/" + filepath.ToSlash(filepath.Join(filepath.Base(OutputDir), saveRelPath))
	fmt.Printf("           └── %s\n", displayPath)

	processedFiles[targetURL] = saveRelPath
	return saveRelPath, nil
}

// processCSSContent: CSS 파일 내부의 url()을 찾아 리소스를 다운로드합니다.
func processCSSContent(ctx context.Context, cssData []byte, contextURL string, cssSavedDir string) []byte {
	if ctx.Err() != nil { return cssData }

	cssStr := string(cssData)
	re := regexp.MustCompile(`url\(['"]?(.*?)['"]?\)`)
	newCSS := re.ReplaceAllStringFunc(cssStr, func(match string) string {
		if ctx.Err() != nil { return match }

		parts := re.FindStringSubmatch(match)
		if len(parts) < 2 { return match }
		link := strings.TrimSpace(parts[1])
		
		if shouldIgnoreLink(link) { return match }

		resourcePath, err := downloadResource(ctx, link, contextURL)
		if err != nil { return match }

		absCssDir := filepath.Join(OutputDir, cssSavedDir)
		absResourcePath := filepath.Join(OutputDir, resourcePath)
		relPath, err := filepath.Rel(absCssDir, absResourcePath)
		if err != nil { return match }
		return fmt.Sprintf("url('%s')", filepath.ToSlash(relPath))
	})
	return []byte(newCSS)
}

func isFontFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".woff", ".woff2", ".ttf", ".otf", ".eot": return true
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
	if n < 0 { numOfDigits-- }
	numOfCommas := (numOfDigits - 1) / 3
	out := make([]byte, len(in)+numOfCommas)
	if n < 0 { in, out[0] = in[1:], '-' }
	for i, j, k := len(in)-1, len(out)-1, 0; ; i, j = i-1, j-1 {
		out[j] = in[i]
		if i == 0 { return string(out) }
		if k++; k == 3 { j, k = j-1, 0; out[j] = ',' }
	}
}
