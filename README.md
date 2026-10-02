<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
    <img src="docs/logo.png" width="360" alt="LiteDeck">
  </picture>
</p>

<h1 align="center">LiteDeck</h1>

<p align="center">
  <b>SSH로 접속한 서버를 한 화면에서 관리하는 데스크톱 앱</b><br>
  서버에는 아무것도 설치하지 않습니다
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
  <a href="docs/features.md">기능</a> ·
  <a href="docs/security.md">보안</a> ·
  <a href="docs/mcp.md">MCP 연동</a> ·
  <a href="docs/support.md">지원 범위</a> ·
  <a href="CONTRIBUTING.md">기여하기</a>
</p>

<p align="center">
  <img src="docs/media/01-tour.gif" width="880"
       alt="LiteDeck: 파일·편집기·서비스·프로세스·컨테이너·네트워크·세션·모니터링·터미널을 한 창에서">
</p>

<p align="center">
  <sub>접속 한 번으로 <b>파일 · 편집기 · 서비스 · 프로세스 · 컨테이너 · 네트워크 · 세션 · 모니터링 · 터미널</b>까지.<br>
  서버에는 아무것도 설치하지 않았습니다.</sub>
</p>

---

## 무엇을 할 수 있나

SSH로 서버에 한 번 접속하면, 아래 작업을 같은 창에서 이어서 할 수 있습니다.

| | |
|---|---|
| **파일** | 탐색·업로드·다운로드, 폴더째 전송. 전송이 끊기면 이어받습니다 |
| **편집** | 문법 강조 편집기. 저장하기 전에 서버에 있는 내용과의 차이를 보여 줍니다 |
| **서비스 · 프로세스 · 컨테이너** | systemd 유닛과 Windows 서비스, 작업 관리자식 프로세스 표, Docker·Podman 컨테이너와 Compose 프로젝트 |
| **모니터링** | CPU·메모리·디스크·네트워크, NVIDIA GPU, 시간에 따른 추세와 시스템 이벤트 |
| **보안 · 세션** | 방화벽과 fail2ban 상태, 지금 접속해 있는 사람, 접속 실패 요약 |
| **터미널** | 내장 터미널. `vi nginx.conf`처럼 파일을 여는 명령은 앱의 편집기에서 엽니다 |
| **설정 동기화** <sub>v2.3.0</sub> | 호스트 목록과 승인 정책을 암호화된 파일 하나로 내보내 다른 PC로 옮깁니다 |

전체 목록과 자세한 설명은 [기능 문서](docs/features.md)에 있습니다.

## 왜 LiteDeck인가

1. **서버에 설치할 것이 없습니다.** 에이전트·데몬·패키지가 필요 없고, 이미 열려 있는 SSH 포트 하나로 동작합니다. 서버에는 사용자가 한 작업의 결과만 남습니다
2. **실행한 명령을 그대로 보여 줍니다.** 화면에서 한 작업은 전부 Command Log에 실제 명령으로 남습니다. 관리자 권한(sudo)이 필요하면 임의로 붙이지 않고 먼저 묻습니다
3. **가입할 계정이 없습니다.** 사용 정보를 수집하지 않고, 전체 소스가 공개돼 있습니다
4. **가볍습니다.** Electron을 쓰지 않습니다. 내려받는 파일 약 6MB, 설치 후 약 15MB이고, 1초 안에 실행됩니다 (v2.3.0 기준. macOS 판은 Intel·Apple Silicon 공용이라 12MB · 29MB)

## 설치와 첫 연결

[릴리스 페이지](https://github.com/cpprhtn/LiteDeck/releases)에서 받으세요.

| 파일 | 대상 |
|---|---|
| `litedeck-desktop-macos.zip` | macOS (Intel·Apple Silicon 공용) |
| `litedeck-desktop-windows-amd64.zip` | Windows 10/11 (amd64). 압축을 풀면 `litedeck.exe` 하나이고, 설치 없이 바로 실행합니다 |
| `litedeck-desktop-linux-amd64.tar.gz` | Linux (amd64). **Ubuntu 24.04 이상** — `libwebkit2gtk-4.1`이 필요합니다. 22.04라면 [직접 빌드](docs/building.md)하세요 |

> [!WARNING]
> **현재 릴리스는 코드 서명이 되어 있지 않습니다.** 서명·공증에는 비용이 들어 초기에는 미서명으로 배포하며,
> 함께 올리는 SHA256 체크섬은 **서명을 대신하지 못합니다.** 신뢰가 필요하면
> [직접 빌드](docs/building.md)하세요.
>
> 그래서 첫 실행 때 macOS Gatekeeper와 Windows SmartScreen 경고가 뜹니다. 넘어가는 방법은
> [설치와 서버 준비](docs/install.md)에 적어 두었습니다.

**첫 연결**

1. 앱을 열고 왼쪽 위의 **+ 추가**를 누릅니다. `~/.ssh/config`를 쓰고 있다면 **가져오기**로 한 번에 불러올 수 있습니다
2. 주소·사용자·인증 방식(SSH 에이전트 · 키 파일 · 비밀번호)을 입력합니다
3. 처음 접속하는 서버는 호스트 키 지문을 확인하라고 묻습니다. 확인하면 서버의 탭들이 열립니다

**서버 쪽 준비.** Linux 서버라면 이미 SSH로 접속하고 계실 테니 준비할 것이 없습니다. Windows는 OpenSSH 서버만 켜면 됩니다.
집에 있는 PC를 외부에서 다루려면 포트포워딩 대신 메시 VPN을 권합니다 —
[서버 준비](docs/install.md#서버-준비) · [Tailscale로 외부에서 쓰기](docs/remote-access.md)

**웹 브라우저로 쓰기 — 서버 모드.** 데스크톱 앱 대신, 서버 한 대에 LiteDeck을 올려 두고 웹 브라우저로
접속해 쓸 수도 있습니다 — `litedeck-server-linux-amd64.tar.gz` · `litedeck-server-linux-arm64.tar.gz`.
이 경우에는 LiteDeck이 웹 서버로 동작하고 로그인 화면이 있습니다. 실행·노출·로그인 설정은
[서버 모드](docs/server-mode.md)에 적어 두었습니다.

## MCP 연동 — AI 도구로 서버 다루기

Claude Code·Claude Desktop 같은 MCP 클라이언트가 LiteDeck을 통해 서버를 조회하고 작업할 수 있습니다.
화면과 **같은 SSH 연결**을 쓰고, AI가 실행한 명령도 **같은 Command Log**에 남습니다. 서버에는 여전히
아무것도 설치하지 않습니다.

- 조회 도구 12개, 변경 도구 6개
- **파일을 바꾸기 전에는 기본적으로 확인을 받습니다.** 확인 창에 서버의 현재 내용과의 차이가 표시됩니다.
  서버별로 「모든 변경을 확인」까지 올릴 수 있습니다
- 승인 정책은 LiteDeck에서 서버별로 정하며, AI 클라이언트 쪽에서는 바꿀 수 없습니다
- AI가 바꾼 파일은 되돌릴 수 있습니다
- 명령 실행과 파일 삭제는 서버별로 따로 켜야 하며, 기본은 꺼져 있습니다 <sub>v1.7.0</sub>.
  켜면 다른 변경 도구와 같은 승인 정책이 적용됩니다

```bash
claude mcp add --transport http litedeck http://127.0.0.1:<포트>/mcp \
  --header "Authorization: Bearer <토큰>"
```

→ [MCP 연동](docs/mcp.md)

## 지원 환경

> [!NOTE]
> **macOS · Windows · Ubuntu에서 실행을 확인했습니다.** 어느 환경에서 무엇까지 확인했는지는
> [지원 범위](docs/support.md)에 적어 두었습니다.
> 삭제·프로세스 종료처럼 되돌릴 수 없는 작업은 시험용 서버에서 먼저 해 보세요.

- **관리할 서버**: systemd를 쓰는 Linux(Ubuntu·Debian·라즈베리파이 등)와 Windows(OpenSSH 서버).
  Windows 서버는 세션·보안 탭까지 지원하며, 이벤트 탭은 아직 없습니다
- **앱을 실행할 PC**: macOS · Windows 10/11 · Linux(Ubuntu 24.04 이상)

**이런 환경에 맞습니다**

- 서버 두세 대에서 대여섯 대 정도를 직접 운영합니다. 로그를 보고, 서비스를 재시작하고, 설정 파일을 고칩니다
- 그 서버에 다른 프로그램을 더 설치하고 싶지 않습니다. 이미 열려 있는 SSH로 충분하길 바랍니다
- GUI를 쓰더라도, 무엇이 실행됐는지는 확인하고 싶습니다
- 터미널을 버릴 생각은 없고, 자주 하는 일만 클릭으로 하고 싶습니다

서버 수십 대를 한꺼번에 다루거나, 선언적 상태 관리나 본격적인 원격 개발 환경이 필요하다면 다른 도구가 낫습니다.
어떤 경우인지는 [LiteDeck이 적합하지 않은 환경](docs/support.md#litedeck이-적합하지-않은-환경)에 적어 두었습니다.

## 더 읽어보기

| | |
|---|---|
| [보안](docs/security.md) | 호스트 키 검증·인증·자격증명 저장·sudo·MCP 엔드포인트. **못 하는 것을 먼저** 적었습니다 |
| [지원 범위](docs/support.md) | 무엇을 어디서 검증했고 무엇이 미검증인지. 적합하지 않은 환경과 지원하지 않는 기능 |
| [MCP 연동](docs/mcp.md) | 도구 18개, 승인 정책, 되돌리기, 안전장치 |
| [기능 자세히](docs/features.md) | **전체 기능 목록**, 그리고 Command Log·편집기·전송·Compose·sshd 점검·ProxyJump |
| [설치와 서버 준비](docs/install.md) | 첫 실행 경고 넘기기, Windows OpenSSH 켜기 |
| [Tailscale로 외부에서 쓰기](docs/remote-access.md) | 포트포워딩 없이 집 PC에 접속하기. Tailscale SSH·MCP·서브넷 라우터, 그리고 계정 없이 가는 길 |
| [소스에서 빌드](docs/building.md) | 빌드와 테스트 |

## 기여

[CONTRIBUTING.md](CONTRIBUTING.md)를 참고하세요. **새 서버 OS를 지원하는 일이 어댑터 하나 구현으로 끝나도록** 설계돼 있습니다. Windows 지원도 그렇게 붙었습니다. OpenRC(Alpine)·launchd(macOS)·FreeBSD가 비어 있습니다.

취약점을 발견하셨다면 공개 이슈 대신 **cpprhtn@naver.com**으로 보내주세요.

## 라이선스

[Apache-2.0](LICENSE)
