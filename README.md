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

6. 출력 디렉토리 구조 (Directory Structure)
   /front_local
       ├── index.html (경로가 변환된 메인 파일)
       ├── sub/about.html (하위 폴더 구조 유지)
       ├── assets/ (모든 정적 리소스)
       └── fonts/  (모든 폰트 리소스)
   
