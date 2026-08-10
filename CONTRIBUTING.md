# 기여 가이드

한국어로 관리합니다. 영어 문서는 [README.en.md](README.en.md)에 있습니다.

## 시작하기

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
git clone https://github.com/cpprhtn/LiteDeck.git
cd LiteDeck
wails dev
```

Wails v2.13이 **Go 1.25+** 를 요구합니다. 설치된 Go가 1.21 이상이면 `go build`가
필요한 툴체인을 스스로 받아오므로(`GOTOOLCHAIN=auto`) 대개 신경 쓸 것이 없습니다.

Linux에서는 `build-essential`과 webkit 개발 헤더가 필요합니다 — 웹뷰가 cgo로
링크되기 때문이고, C 컴파일러가 없으면 **cgo가 조용히 꺼진 채 빌드가 성공하며
웹뷰 없는 바이너리가 나옵니다.**

```bash
go test ./... -short    # 유닛만 (Docker 불필요, 몇 초)
go test ./... -race     # 통합 포함 (Docker 필요, 1분 남짓)
```

## 가장 환영하는 기여: 새 어댑터

**새 서버 OS 지원 = 어댑터 하나 구현**이 되도록 설계했습니다. Windows 지원도 그렇게 붙었습니다.

지금 있는 것:

- **systemd 기반 Linux** — `internal/adapter/linuxsystemd/`
- **Windows** — `internal/adapter/windowspowershell/` (전송 계층) + `internal/adapter/windows_*.go` (파서)

비어 있는 것:

- **Alpine / OpenRC** — `rc-service`, `rc-status`
- **macOS 서버** — `launchctl`
- **FreeBSD** — `service`

기존 어댑터 둘 중 가까운 쪽을 본뜨면 됩니다. 필요한 것은 명령어 매핑, 출력 파서, 그리고 **그 OS에서 실제로 캡처한 골든 파일**입니다. Windows 쪽은 `testdata/windows/capture.sh` 가 캡처와 익명화를 함께 해주니 참고하세요.

## 규칙 — 협상 불가

취향 문제가 아니라 전부 한 번씩 실제로 겪은 것들입니다. 어긴 PR은 리뷰에서 반드시 걸립니다.

### 1. 명령은 argv로만

```go
conn.Exec(ctx, "systemctl", "restart", "--", unit)   // ✅
conn.Exec(ctx, "sh", "-c", "systemctl restart "+unit) // ❌ 인젝션
```

모든 인자는 `internal/shellquote`를 지납니다. 파일명·유닛명·PID가 명령이 될 경로가 없어야 합니다.

**`--`를 빠뜨리지 마세요.** 없으면 `-`로 시작하는 이름이 옵션으로 읽히고, `kill -TERM -1`은 접근 가능한 **모든 프로세스**에 시그널을 보냅니다.

예외는 하나뿐입니다: 보간되는 것이 전혀 없는 **컴파일 타임 상수 스크립트**(`adapter.MetricsScript`). 사용자 입력이 한 글자라도 섞이면 예외가 아닙니다.

### 2. 파괴적 액션의 가드는 Go에 둡니다

다이얼로그는 제안입니다. 프론트엔드 버그·오클릭·바인딩 직접 호출이 전부 통과합니다. "`/etc` 재귀 삭제는 경로를 타이핑해야 한다"는 **Go가 거부해야만 규칙**입니다(`internal/app/paths.go`).

### 3. 슬라이스를 nil로 반환하지 마세요

Go의 nil 슬라이스는 JSON `null`이 되고, 프론트에서 `.length`가 터지면 **React 트리 전체가 언마운트되어 창이 빕니다.**

```go
out := []Thing{}   // ✅
var out []Thing    // ❌ 비어 있으면 null
```

`internal/adapter/nil_probe_test.go`가 이걸 강제합니다.

### 4. 골든 파일은 실제 서버 출력이어야 합니다

상상해서 만든 픽스처는 실제로 존재하는 케이스를 놓칩니다. 좀비 프로세스의 `comm`에 공백이 있다는 것, systemd 245가 `--output=json`을 조용히 무시한다는 것 — 둘 다 진짜 출력을 받고 나서야 발견했습니다.

```bash
docker exec <fixture> ps -eo pid,ppid,... > testdata/golden/<distro>/ps.txt
```

`provenance.txt`에 OS·버전을 함께 남겨주세요.

### 5. 조용한 실패를 만들지 마세요

이 프로젝트에서 잡은 버그의 절반이 이 형태였습니다:

- 화면이 비었는데 이유를 알 수 없음
- 클릭했는데 아무 일도 안 일어남 (탭이 `disabled`였음)
- 정상적인 프로브 실패가 빨간 오류로 집계됨
- 신호가 배경 소음에 묻힘

**모든 분기는 무언가를 반환해야 합니다.** "지원하지 않음"도 화면에 이유를 적으세요.

### 6. 비밀은 argv에 넣지 않습니다

원격 프로세스 테이블은 그 서버의 모든 사용자에게 보입니다. `stdin`으로 보내세요. 그러면 Command Log에 명령줄을 그대로 보여줘도 안전합니다.

`hosts.json`에 비밀을 저장하는 필드를 추가하지 마세요 — 화이트리스트 테스트가 막습니다.

## 스타일

- Go: `gofmt`, `go vet` 통과. 주석은 *무엇*이 아니라 **왜**를 적습니다
- TypeScript: `npm run build`가 `tsc`를 돌립니다. 타입 에러는 빌드 실패입니다
- CSS: 색·간격은 `tokens.css`의 변수만 씁니다. hex 값 하드코딩 금지
- 단축키: `platform.ts`를 거칩니다. 컴포넌트에 `"F2"` 같은 문자열 금지

## PR

- **DCO**: 커밋에 `Signed-off-by:`를 넣어주세요 (`git commit -s`). CLA는 없습니다
- 동작을 바꾸면 테스트를 함께 주세요. 통합 테스트가 있으면 더 좋습니다
- 설계 결정을 바꿨다면 PR 본문에 **발견 경위 / 원인 / 대응 / 검증** 순서로 적어주세요. "고쳤습니다"만 있으면 반년 뒤에 같은 실수를 되돌리게 됩니다

## 버그 리포트

`cmd/litedeck-probe`의 출력을 붙여주시면 가장 빠릅니다:

```bash
go run ./cmd/litedeck-probe -addr your-server -user you
```

배포판, systemd 버전, 컨테이너 런타임 유무, 동시성 결과가 한 번에 나옵니다. 비밀번호는 출력되지 않습니다.

## 라이선스

기여하시면 [Apache-2.0](LICENSE)으로 배포되는 데 동의하는 것으로 봅니다.
