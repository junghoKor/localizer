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
