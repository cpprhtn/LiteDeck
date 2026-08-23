

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
    <img src="docs/logo.png" width="360" alt="LiteDeck">
  </picture>
</p>

<h1 align="center">LiteDeck</h1>

<p align="center">
  <b>서버에는 아무것도 설치하지 않고, SSH 하나로 원격 서버를 로컬 네이티브 GUI에서 다룹니다.</b>
</p>

<p align="center">
  <a href="https://github.com/cpprhtn/LiteDeck/actions/workflows/ci.yml"><img src="https://github.com/cpprhtn/LiteDeck/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/cpprhtn/LiteDeck/releases"><img src="https://img.shields.io/github/v/release/cpprhtn/LiteDeck?include_prereleases&label=release&color=orange" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey" alt="Platform">
</p>

<p align="center">
  <b>한국어</b> · <a href="README.en.md">English</a>
</p>

<p align="center">
  <a href="#설치">다운로드</a> ·
  <a href="#기능">기능</a> ·
  <a href="docs/security.md">보안</a> ·
  <a href="docs/mcp.md">MCP 연동</a> ·
  <a href="docs/support.md">지원 범위</a> ·
  <a href="CONTRIBUTING.md">기여하기</a>
</p>

<p align="center">
  <img src="docs/media/01-tour.gif" width="880"
       alt="LiteDeck: 파일·편집기·서비스·프로세스·컨테이너·네트워크·세션·터미널을 한 창에서">
</p>

<p align="center">
  <sub>접속 한 번으로 <b>파일 · 편집기 · 서비스 · 프로세스 · 컨테이너 · 네트워크 · 세션 · 모니터링 · 터미널</b>까지.<br>
  서버에는 아무것도 설치하지 않았습니다.</sub>
</p>

---

> [!NOTE]
> **macOS · Windows · Ubuntu에서 실행을 확인했습니다.** 어느 환경에서 무엇까지 확인했는지는
> [지원 범위](docs/support.md)에 적어두었습니다.
> 삭제·프로세스 종료처럼 되돌릴 수 없는 작업은 시험용 서버에서 먼저 확인해보세요.

## 화면을 보내지 않습니다

RDP·VNC·TeamViewer는 서버의 **화면 픽셀을 영상으로 스트리밍**합니다. LiteDeck은 그러지 않습니다.

```
[GUI 조작]              [LiteDeck]                  [서버 · 설치물 없음]
폴더 더블클릭    ───→   SFTP ReadDir          ───→   sftp-server 서브시스템
서비스 재시작    ───→   systemctl restart     ───→   systemd가 실행
컨테이너 삭제    ───→   docker rm -f <id>     ───→   dockerd가 실행
                 ←───  구조화된 텍스트/JSON   ←───
화면 그리기: 100% 로컬 PC
```

서버가 하는 일은 **원래 갖고 있던 명령을 실행하고 텍스트를 돌려주는 것뿐**입니다. 그래서:

- **서버 부하는 명령이 실행되는 순간 외에는 없습니다**
- **설치물도, 추가 포트도, 중계 서버도 없습니다.** 이미 열려 있는 SSH 포트 하나면 됩니다

## 5가지 원칙

1. **서버 무설치.** 에이전트·데몬·패키지를 설치하지 않습니다. 서버에 남는 것은 사용자가 시킨 일의 결과뿐입니다
2. **SSH만 사용.** 이미 열려 있는 SSH 포트 하나만 씁니다. 웹서버도, 중계 서버도 없습니다
3. **숨기지 않습니다.** GUI가 실행한 명령이 전부 Command Log에 그대로 뜹니다. sudo도 몰래 붙이지 않고 물어봅니다
4. **로그인 없음, 수집 없음, 오픈소스.** 계정을 요구하지 않고, 아무것도 수집하지 않으며, 전체 소스가 공개됩니다
5. **가볍게.** Electron을 쓰지 않습니다. 받는 파일 5~10MB, 설치 후 13~16MB, 실행까지 1초 이내

> 1번의 예외 하나: 편집기로 파일을 저장할 때 같은 디렉터리에 임시 파일을 만들고 `rename`으로
> 갈아끼웁니다. 저장이 중간에 끊겨도 원본이 반토막 나지 않게 하려는 것이고, 성공하면 임시 파일은
> 남지 않습니다. `rename`이 실패하면 임시 파일 경로를 화면에 알려주고 지우지 않습니다.
> 잃어버리는 것보다 낫기 때문입니다.

## 기능

| | |
|---|---|
| **파일** | 트리 탐색·**이름으로 거르기**·업로드·다운로드(진행률·취소·**끊기면 이어받기**)·폴더째 전송·이름 변경·삭제·권한(체크박스 + `chmod 755` 직접 입력). 편집기가 못 여는 파일은 **읽기 전용으로** — 이미지는 그리고, 나머지는 앞부분을 hex로 |
| **코드 편집** | 트리 옆 분할 뷰 · 파일 탭 · 문법 강조 24종 · 찾기/바꾸기 · **저장 전 diff** · **원자적 저장**(임시 파일 + rename). **서버에도 클라이언트에도 편집기를 깔지 않습니다** |
| **서비스** | systemd 유닛 / Windows 서비스. 목록·필터·시작/중지/재시작·자동 시작 설정·**실시간 로그 보기**(Linux) |
| **프로세스** | 작업 관리자식 테이블. 정렬·검색·트리 보기·종료(TERM→KILL 2단계)·우선순위 변경 |
| **컨테이너** | Docker·Podman 카드. 시작/중지/재시작/삭제·**실시간 로그 보기**·이미지/볼륨 정리. Compose로 띄운 것은 **프로젝트별로 묶여** 전체 시작·중지·재시작 |
| **네트워크** | 인터페이스와 열린 포트. **외부에 노출된 포트를 구분해서 표시**. sshd 설정 점검 |
| **세션** | 이 서버에 누가 붙어 있는지. 접속별로 끊기 |
| **스케줄** | systemd 타이머. 다음·마지막 실행 시각 |
| **터미널** | xterm.js PTY, 탭 여러 개. `code .` · `vi foo.conf` 를 **앱이 가로채** 파일 탭에서 엽니다. 서버로 보내지 않으므로 VS Code나 vi가 없어도 됩니다 |
| **모니터링** | 요약 바는 한눈에, 모니터링 탭은 자세히. **CPU 를 사용자·커널·IO 대기·뺏김으로 갈라** 보여줍니다 — 90%가 전부 IO 대기면 디스크가, 전부 뺏김이면 하이퍼바이저가 문제입니다. **코어별 막대**, 메모리의 캐시·버퍼 분리, 파일시스템 전부, NVIDIA 카드가 있으면 **GPU 사용률·팬·온도·VRAM** (nvidia-smi) |
| **추세** | 보고 있는 동안의 CPU·메모리·GPU 를 시간축으로. **앱이 안 보던 구간은 선을 끊어** 표시합니다 — 없는 기록을 이어 그리지 않습니다 |
| **이벤트** | 무슨 일이 언제 있었나. **OOM killer**, 유닛 실패, 코어 덤프, 재시작 예약, 재부팅 경계. systemd 저널을 읽되 문구가 아니라 `MESSAGE_ID` 로 갈라내므로 서버 언어와 무관합니다 |
| **Command Log** | GUI가 실행한 **모든 명령을 실시간으로 표시**. 클릭하면 복사됩니다 |
| **MCP** | Claude Code·Claude Desktop이 이 앱을 통해 서버를 조회하고 바꿉니다. 서버별 opt-in, 변경은 승인, **되돌리기 가능** |
| **접속** | 비밀번호·키·에이전트·2FA. `~/.ssh/config` 가져오기. **ProxyJump** 로 경유 서버 한 단계 |
| **언어** | 한국어·영어. 설정된 언어가 있으면 그 언어를, 없으면 OS 언어를 따르고, 사이드바 아래 `KO`/`EN` 로 바꿉니다 |

### 무엇을 실행했는지 숨기지 않습니다

<p align="center">
  <img src="docs/media/03-command-log.gif" width="820" alt="Command Log">
</p>

<p align="center"><sub>재시작이 권한 부족으로 거부되자 <b>물어봅니다</b>. 실행된 명령은 그대로 보이고, 비밀번호는 stdin으로 가서 보이지 않습니다.</sub></p>

운영 서버를 만지는 GUI는 결국 "믿어달라"고 요구하는 셈입니다. LiteDeck은 **방금 무엇을 실행했는지
정확히 보여주는 것**으로 그 신뢰를 얻습니다. 비밀번호는 표준 입력으로만 가므로 명령줄을 그대로
보여줘도 안전하고, 이 기록은 로컬에만 남습니다.

### 편집기가 양쪽 어디에도 설치되지 않습니다

서버에 `vscode-server`(수백 MB)를 올리지도, 서버의 `vi`를 터미널로 쓰지도 않습니다. 파일은 SSH가
이미 주는 SFTP로 읽고 쓰고, 편집기는 앱 안에 들어 있습니다. 터미널에 `code .` 이나 `vi foo.conf`
를 치면 **앱이 그 줄을 서버로 보내기 전에 가로채** 파일 탭으로 옵니다. 저장하면 서버의 현재
내용과의 diff가 먼저 뜹니다.

→ [기능 자세히](docs/features.md)

### Claude가 이 앱을 거쳐 서버를 다룹니다

Claude Code·Claude Desktop 같은 MCP 클라이언트가 **GUI가 앉던 자리에 앉습니다.** 같은 어댑터,
이미 인증된 같은 SSH 연결, 같은 Command Log를 씁니다. 조회 12개와 변경 5개를 주고, **변경은
기본적으로 매번 물어봅니다.** 그 결재 정책은 앱이 소유하며 클라이언트 쪽에서는 끌 수 없습니다.
MCP가 바꾼 파일은 되돌릴 수 있고, 서버에는 여전히 아무것도 설치하지 않습니다.

```bash
claude mcp add --transport http litedeck http://127.0.0.1:<포트>/mcp \
  --header "Authorization: Bearer <토큰>"
```

→ [MCP 연동](docs/mcp.md)

## 설치

[릴리스 페이지](https://github.com/cpprhtn/LiteDeck/releases)에서 받으세요.

| 파일 | 대상 |
|---|---|
| `litedeck-macos.zip` | macOS (Intel·Apple Silicon 공용) |
| `litedeck-windows-amd64.zip` | Windows 10/11 (amd64). 압축을 풀면 `litedeck.exe` 하나, 설치 없이 바로 실행 |
| `litedeck-linux-amd64.tar.gz` | Linux (amd64). **Ubuntu 24.04 이상** — `libwebkit2gtk-4.1` 이 필요합니다. 22.04 라면 [직접 빌드](docs/building.md) |

> [!WARNING]
> **현재 릴리스는 코드 서명이 되어 있지 않습니다.** 서명·공증에는 비용이 들어 초기에는 미서명으로 배포하며,
> 함께 올리는 SHA256 체크섬은 **서명을 대신하지 못합니다.** 신뢰가 필요하면
> [직접 빌드](docs/building.md)하세요.
>
> 그래서 첫 실행에 macOS Gatekeeper와 Windows SmartScreen 경고가 뜹니다. 넘어가는 방법은
> [설치와 서버 준비](docs/install.md)에 적어두었습니다.

**서버 쪽 준비.** Linux은 이미 SSH로 접속하고 계시면 끝입니다. Windows는 OpenSSH 서버만 켜면 됩니다.
집에 있는 PC를 외부에서 다루려면 포트포워딩 대신 메시 VPN을 권합니다 —
[서버 준비](docs/install.md#서버-준비) · [Tailscale로 외부에서 쓰기](docs/remote-access.md)

## 이 도구가 맞는 경우

- **서버 두세 대에서 대여섯 대**를 직접 돌본다. 로그를 보고, 서비스를 재시작하고, 설정 파일을 고친다
- 그 서버에 **뭔가를 더 깔고 싶지 않다.** SSH는 이미 열려 있고, 그것으로 끝내고 싶다
- GUI는 원하지만 **무엇이 실행됐는지는 보고 싶다**
- 터미널을 버릴 생각은 없다. 자주 하는 일만 클릭으로 하고 싶을 뿐이다

서버 수십 대를 한꺼번에, 선언적으로, 또는 본격적인 원격 개발이 필요하시다면 다른 도구가 낫습니다.
어떤 경우에 그런지 [안 맞는 경우](docs/support.md#안-맞는-경우)에 적어두었습니다.

## 더 읽어보기

| | |
|---|---|
| [보안](docs/security.md) | 호스트 키 검증·인증·자격증명 저장·sudo·MCP 엔드포인트. **못 하는 것을 먼저** 적었습니다 |
| [지원 범위](docs/support.md) | 무엇을 어디서 검증했고 무엇이 미검증인지. 안 맞는 경우와 하지 않는 것 |
| [MCP 연동](docs/mcp.md) | 도구 17개, 결재 정책, 되돌리기, 안전장치 |
| [기능 자세히](docs/features.md) | Command Log와 편집기 |
| [설치와 서버 준비](docs/install.md) | 첫 실행 경고 넘기기, Windows OpenSSH 켜기 |
| [Tailscale로 외부에서 쓰기](docs/remote-access.md) | 포트포워딩 없이 집 PC에 붙기. Tailscale SSH·MCP·서브넷 라우터, 그리고 계정 없이 가는 길 |
| [소스에서 빌드](docs/building.md) | 빌드와 테스트 |

## 기여

[CONTRIBUTING.md](CONTRIBUTING.md)를 참고하세요. **새 서버 OS 지원 = 어댑터 하나 구현**이 되도록 설계돼 있습니다. Windows 지원도 그렇게 붙었습니다. OpenRC(Alpine)·launchd(macOS)·FreeBSD가 비어 있습니다.

취약점을 발견하셨다면 공개 이슈 대신 **cpprhtn@naver.com** 으로 보내주세요.

## 라이선스

[Apache-2.0](LICENSE)
