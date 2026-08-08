# LiteDeck

**서버에는 아무것도 설치하지 않고, SSH 하나만으로 원격 서버의 파일·서비스·프로세스·컨테이너를 로컬 네이티브 GUI로 다루는 무료 오픈소스 데스크톱 클라이언트.**

[![CI](https://github.com/cpprhtn/LiteDeck/actions/workflows/ci.yml/badge.svg)](https://github.com/cpprhtn/LiteDeck/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Version](https://img.shields.io/badge/version-0.1.0--beta-orange)](https://github.com/cpprhtn/LiteDeck/releases)

> **v0.1.0-beta** — macOS 클라이언트와 컨테이너 서버 픽스처에서 검증했습니다. **실제 하드웨어 서버와 Windows 클라이언트는 아직 검증하지 않았습니다.** 무엇이 확인됐고 무엇이 안 됐는지는 [지원 범위](#지원-범위)에 그대로 적어두었습니다. 프로덕션 서버에 쓰실 거라면 파괴적 액션(삭제·프로세스 종료)은 먼저 시험용 서버에서 확인해보세요.

---

## 화면을 보내지 않습니다

RDP·VNC·TeamViewer는 서버의 **화면 픽셀을 영상으로 스트리밍**합니다. LiteDeck은 그러지 않습니다.

```
[GUI 액션]              [LiteDeck]                  [서버 — 설치물 없음]
폴더 더블클릭    ───→   SFTP ReadDir          ───→   sftp-server 서브시스템
서비스 재시작    ───→   systemctl restart     ───→   systemd가 실행
컨테이너 삭제    ───→   docker rm -f <id>     ───→   dockerd가 실행
                 ←───  구조화된 텍스트/JSON   ←───
렌더링: 100% 로컬 PC
```

서버가 하는 일은 **원래 갖고 있던 명령을 실행하고 텍스트를 돌려주는 것뿐**입니다. 그래서:

- **마우스 반응은 로컬 네이티브 속도** — 네트워크 지연은 데이터 갱신에만 영향
- **서버 리소스 사용은 명령 실행 순간 외 0에 수렴**
- **설치물·추가 포트·중계 서버 없음** — 22번 포트 하나

## 5대 원칙

1. **Zero Server Install** — 서버에 에이전트·데몬·패키지를 설치하지 않고, 파일을 남기지 않습니다
2. **SSH-Only** — SSH 포트 하나만 씁니다. 추가 포트도, 웹서버도, 중계 서버도 없습니다
3. **Beyond Files** — 파일뿐 아니라 프로세스·서비스·컨테이너·상태까지 GUI로 다룹니다
4. **No Login, No Telemetry, FOSS** — 계정을 요구하지 않고, 아무것도 수집하지 않으며, 전체 소스가 공개됩니다
5. **Lightweight** — Electron을 쓰지 않습니다. 바이너리 10MB 미만, 콜드 스타트 1초 이내

## 기능

| | |
|---|---|
| **파일** | 탐색·업로드·다운로드(진행률·취소)·이름변경·삭제·권한(체크박스 + `chmod 755` 직접 입력)·텍스트 편집기 |
| **서비스** | systemd 유닛 목록·필터(failed만)·시작/중지/재시작/enable·**실시간 저널 팔로우** |
| **프로세스** | 작업관리자식 테이블·정렬·검색·트리 보기·종료(TERM→KILL 2단계)·우선순위 변경 |
| **컨테이너** | Docker/Podman 카드·시작/중지/재시작/삭제·**실시간 로그 팔로우**·이미지/볼륨(크기순·미사용 정리) |
| **네트워크** | 인터페이스·열린 포트(**외부 노출 여부 구분**) |
| **스케줄** | systemd 타이머 조회 — 다음/마지막 실행 |
| **터미널** | xterm.js PTY 다중 탭 |
| **모니터링** | CPU·메모리·디스크·로드 요약 바 + 스파크라인 |
| **Command Log** | GUI가 실행한 **모든 명령을 실시간 표시**. 클릭하면 복사됩니다 |

### Command Log — GUI로 CLI를 배웁니다

프로덕션 서버를 만지는 GUI는 믿어달라고 요구하는 셈입니다. LiteDeck은 **방금 무엇을 실행했는지 정확히 보여주는 것**으로 그 신뢰를 얻습니다.

```
$ systemctl list-units --type=service --all --output=json    120ms
$ journalctl -u myapp.service -n 200 -f --no-pager -q
$ sudo -S -p '' -- systemctl restart -- myapp.service        310ms
```

비밀번호는 `stdin`으로 가므로 **명령줄은 그대로 보여도 안전합니다.** 로그는 로컬 전용이고 외부로 전송되지 않습니다.

## 다른 도구와의 차이

| | LiteDeck | Termius | Cockpit | SSHFS/WinSCP |
|---|---|---|---|---|
| 서버 설치 | **불필요** | 불필요 | **필요** | 불필요 |
| 추가 포트 개방 | **없음** | 없음 | **9090** | 없음 |
| 파일 | ✅ | ✅ | ✅ | ✅ |
| 서비스·프로세스·컨테이너 | ✅ | ❌ | ✅ | ❌ |
| 계정 강제 | **없음** | 있음 | 없음 | 없음 |
| 텔레메트리 | **없음** | 있음 | 없음 | 없음 |
| 가격 | **무료·FOSS** | 핵심 기능 유료 | 무료 | 무료 |

## 설치

[릴리스 페이지](https://github.com/cpprhtn/LiteDeck/releases)에서 받으세요.

> **현재 릴리스는 서명되어 있지 않습니다.** macOS는 Gatekeeper가, Windows는 SmartScreen이 경고합니다. 서명·공증에는 비용이 들어 초기에는 미서명으로 배포하며, 함께 배포하는 SHA256 체크섬은 **서명의 대체물이 아닙니다.** 신뢰가 필요하면 아래처럼 직접 빌드하세요.

### 첫 실행 — 경고를 넘기는 법

**macOS 15 (Sequoia) 이상** — `Apple could not verify "litedeck" is free of malware…` 가 뜹니다. 이 대화상자에는 "휴지통으로 이동"과 "취소"밖에 없습니다.

**시스템 설정 → 개인정보 보호 및 보안** 을 열고 아래 보안 항목까지 내려가면 `"litedeck"이(가) 차단되었습니다` 옆에 **"그래도 열기"** 가 있습니다. 누르고 인증하면 이후로는 그냥 열립니다.

터미널이 빠르면:

```bash
xattr -d com.apple.quarantine /Applications/litedeck.app
```

> 예전에 흔히 안내되던 **우클릭 → 열기** 는 macOS 15부터 Apple이 없앴습니다. 14 이하에서는 여전히 그 방법이 됩니다.

**Windows** — SmartScreen이 `Windows에서 PC를 보호했습니다` 를 띄웁니다. **추가 정보 → 실행** 을 누르면 됩니다.

**Linux** — 별도 절차가 없습니다. 압축을 풀고 실행 권한을 주면 됩니다.

```bash
tar xzf litedeck-linux-amd64.tar.gz && chmod +x litedeck && ./litedeck
```

## 소스에서 빌드

```bash
# 필요: Go 1.25+, Node 20+, Wails v2
#
# Go가 더 낮아도 됩니다 — Go 1.21+ 라면 `go build`가 필요한 툴체인을 스스로
# 받아옵니다(GOTOOLCHAIN=auto, 기본값). Ubuntu 22.04의 기본 Go 1.18만 아니면
# 별도 조치가 필요 없습니다.
go install github.com/wailsapp/wails/v2/cmd/wails@latest

git clone https://github.com/cpprhtn/LiteDeck.git
cd LiteDeck
wails build          # build/bin/ 에 생성됩니다
wails dev            # 핫 리로드 개발 모드
```

Linux는 웹뷰 개발 헤더와 C 컴파일러가 필요합니다(웹뷰는 cgo로 링크됩니다):

```bash
sudo apt install build-essential pkg-config libgtk-3-dev

# 배포판에 따라 webkit 패키지 이름이 다릅니다
sudo apt install libwebkit2gtk-4.1-dev   # 24.04 이상 — wails build -tags webkit2_41
sudo apt install libwebkit2gtk-4.0-dev   # 22.04     — 태그 불필요
```

두 변종 모두 링크되는 것을 컨테이너에서 확인했습니다 — 22.04는
`libwebkit2gtk-4.0.so.37`, 24.04는 `libwebkit2gtk-4.1.so.0`에 연결됩니다.

## 지원 범위

v0.1.0-beta는 **검증된 것과 검증되지 않은 것을 구분해서** 적습니다. 아래 "미검증"은 코드가 없다는 뜻이 아니라, 저자가 실제로 돌려보지 않았다는 뜻입니다.

### 검증됨

| | 어디서 | 무엇을 |
|---|---|---|
| **클라이언트** | macOS 26.5.2 · arm64 | 개발·실행·전 기능. 릴리스 universal 바이너리 실행도 확인 |
| **서버** | Ubuntu 22.04.5 / systemd 249 | 전체 플로우 — 서비스(JSON 경로)·파일·프로세스·타이머·저널 팔로우·권한 상승 |
| **서버** | Ubuntu 20.04.6 / systemd 245 | 서비스 **표 폴백 경로** (JSON 미지원 버전) |
| **서버** | Alpine 3.20 + OpenSSH | 전송 계층 — SFTP·재연결·인젝션 방어·동시 세션 |
| **컨테이너** | docker 28 (dind) | 컨테이너·이미지·볼륨 |

**서버 검증은 전부 컨테이너 픽스처입니다** (`testdata/`). 실제 하드웨어·클라우드 서버에서는 아직 돌려보지 않았습니다.

Linux 클라이언트는 **빌드·링크만** 확인했습니다 — Ubuntu 22.04는 `libwebkit2gtk-4.0.so.37`, 24.04는 `libwebkit2gtk-4.1.so.0`에 연결되는 것까지입니다. 실행은 확인하지 않았습니다.

### 미검증

| | 상태 |
|---|---|
| **Windows 클라이언트** | 코드·CI 빌드는 있으나 실기 확인 안 됨 |
| **Linux 클라이언트 실행** | 링크만 확인, 창을 띄워보지 않음 |
| **실제 하드웨어 서버** | 컨테이너 픽스처만 사용 |
| **Debian, RHEL/Rocky 8** | systemd 245 표 폴백 경로를 공유하므로 동작이 예상되나 확인 안 됨 |
| **Podman** | `docker` 호환 CLI라 파서를 공유하지만 실행 안 해봄 |
| **ProxyJump / 다단 SSH** | 미구현 |
| **원격 → 로컬 드래그** | Wails v2에 드래그-아웃 API가 없어 구현 불가. 다운로드 버튼으로 대체 |

여기 없는 조합에서 돌려보셨다면 [이슈](https://github.com/cpprhtn/LiteDeck/issues)로 알려주세요. `go run ./cmd/litedeck-probe -addr <서버> -user <계정>` 출력이 있으면 가장 빠릅니다.

systemd 246 미만(Ubuntu 20.04, RHEL 8)에서는 JSON 출력이 없어 **표 파싱으로 자동 폴백**합니다. 서버 버전을 감지해 형식을 고르므로 사용자가 신경 쓸 것은 없습니다.

## 하지 않는 것

- **화면 스트리밍** — 구조화된 상태를 내놓지 못하는 대상(GUI 앱·디자이너·디버거)은 RDP가 정답이고, LiteDeck은 그 영역을 대체하려 하지 않습니다
- **구성 관리** — 선언적 상태 관리는 Ansible·Terraform의 영역입니다. LiteDeck은 명령적 도구입니다
- **서버 상주 감시** — inotify 같은 실시간 감시는 에이전트가 필요하므로 원칙 1에 어긋납니다
- **플릿 관리** — 서버 2~5대를 다루는 도구입니다. 수십 대를 집합으로 다루는 건 다른 제품입니다

## 개발

```bash
go test ./... -short          # 유닛 테스트 (Docker 불필요)
go test ./... -race           # 통합 테스트 포함 (Docker 필요)
```

통합 테스트는 실제 서버를 띄웁니다 — `testdata/`에 sshd·systemd·Docker-in-Docker 픽스처가 있습니다. Docker가 없으면 실패가 아니라 skip하므로, **`-race`가 몇 초 만에 끝났다면 통합 테스트가 안 돌았다는 뜻입니다** (Docker가 켜져 있으면 1분 남짓 걸립니다).

v0.1.0-beta를 만든 환경: Go 1.26.5 · Node 22.13.1 · Wails 2.13.0 · Docker 29.4.0 (macOS 26.5.2 arm64).

기여는 [CONTRIBUTING.md](CONTRIBUTING.md)를 참고하세요. **새 서버 OS 지원 = 어댑터 하나 구현**이 되도록 설계돼 있습니다.

## 라이선스

[Apache-2.0](LICENSE)
