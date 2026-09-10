<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
    <img src="docs/logo.png" width="360" alt="LiteDeck">
  </picture>
</p>

<h1 align="center">LiteDeck</h1>

<p align="center">
  <b>원격 서버에는 아무것도 설치하지 않고, SSH 하나로 로컬 네이티브 GUI에서 다룹니다.</b>
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

> [!NOTE]
> **macOS · Windows · Ubuntu에서 실행을 확인했습니다.** 어느 환경에서 무엇까지 확인했는지는
> [지원 범위](docs/support.md)에 적어두었습니다.
> 삭제·프로세스 종료처럼 되돌릴 수 없는 작업은 시험용 서버에서 먼저 확인해보세요.

## 다섯 가지 원칙

1. **서버 무설치.** 에이전트·데몬·패키지를 설치하지 않습니다. 서버에 남는 것은 사용자가 시킨 일의 결과뿐입니다
2. **SSH만 사용.** 이미 열려 있는 SSH 포트 하나만 씁니다. 웹서버도, 중계 서버도 없습니다
3. **숨기지 않습니다.** GUI가 실행한 명령이 전부 Command Log에 그대로 뜹니다. sudo도 몰래 붙이지 않고 물어봅니다
4. **로그인 없음, 수집 없음, 오픈소스.** 계정을 요구하지 않고, 아무것도 수집하지 않으며, 전체 소스가 공개됩니다
5. **가볍게.** Electron을 쓰지 않습니다. 내려받는 파일 5–10MB, 설치 후 13–16MB, 실행까지 1초 이내

> 1번의 예외 하나: 편집기로 파일을 저장할 때 같은 디렉터리에 임시 파일을 만들고 `rename`으로
> 갈아끼웁니다. 저장이 중간에 끊겨도 원본이 반토막 나지 않게 하려는 것이고, 성공하면 임시 파일은
> 남지 않습니다. `rename`이 실패하면 임시 파일 경로를 화면에 알려주고 지우지 않습니다.
> 편집한 내용을 잃는 것보다 낫기 때문입니다.

## 2.1.0에서 달라진 것

**윈도우 서버가 리눅스 서버와 같은 탭을 받습니다.** 지금까지 윈도우에 붙으면 세션
탭과 보안 탭이 「이 서버는 답할 수 없다」고 말했습니다. 서버는 알고 있었고, 묻는
방법이 달랐을 뿐입니다.

- **터미널에서 셸을 고릅니다** — cmd · PowerShell · WSL. 개발자 기계에는 보통 셋 다
  있고, PowerShell 을 원했는데 cmd 가 열리는 것은 취향 문제가 아닙니다. 아는 명령의
  절반이 거기 없습니다. WSL 배포판은 레지스트리에서 읽습니다 — `wsl -l` 은 배포판이
  없는 기계에서 **도움말을 출력하고 0 으로 끝나서**, 줄마다 배포판으로 읽으면 메뉴에
  「WSL · --online, -o」가 생깁니다
- **세션 탭.** 윈도우 OpenSSH 는 리눅스가 만드는 `sshd: user@pts/0` 프로세스를 만들지
  않습니다. 세션은 실제 계정이 소유한 sshd 자식이고, 접속 주소는 소켓 표가 아니라
  OpenSSH 이벤트 로그에 있습니다 — 소켓 표는 22번 포트의 모든 연결을 리스너에게
  돌립니다. 끊기는 `taskkill /F /T` 로 트리째입니다. 프로세스 하나만 죽이면 그 아래
  셸이 살아남고 연결도 안 끊깁니다
- **보안 탭.** 방화벽 프로필 셋 중 지금 네트워크가 속한 하나, 포트를 여는 인바운드
  규칙(**뒤에 실제로 뭔가 듣고 있는 것을 위로** 올립니다), 계정 잠금 정책, Defender,
  그리고 누가 두드리고 있나. fail2ban 자리에는 계정 잠금이 오는데 **어떻게 약한지도
  적혀 있습니다** — 잠기는 것은 계정이지 주소가 아니라서, 계정 이름을 바꿔 가며
  두드리는 공격에는 걸리지 않습니다
- **읽는 창을 정직하게 말합니다.** 윈도우의 OpenSSH 로그는 1MB 순환입니다. 암호
  공격을 받는 실측 서버에서 2,343건이 **77분치**였습니다. 「최근 24시간」이라고
  적으면 온종일 이어진 공격이 방금 시작한 것처럼 보이므로, 로그에 남아 있는 가장
  오래된 시각을 대신 적습니다
- **어느 행도 채우지 못하는 열은 그리지 않습니다.** 윈도우에는 단말도 유휴 시간도
  없고, utmp 를 안 쓰는 컨테이너도 마찬가지입니다. 대시 여섯 개는 정직한 표가 아니라
  고장난 표로 읽힙니다


## Claude가 이 앱을 거쳐 서버를 다룹니다

Claude Code·Claude Desktop 같은 MCP 클라이언트가 **GUI가 앉던 자리에 앉습니다.** 같은 어댑터,
이미 인증된 같은 SSH 연결, 같은 Command Log를 씁니다. 조회 도구 12개와 변경 도구 6개를 주고,
**파일을 바꿀 때는 기본적으로 물어봅니다** — 승인창이 서버의 현재 내용 대비 diff를 보여주기
때문입니다. 호스트별로 **전부 물어보기**까지 올릴 수도 있습니다. 그 승인 정책은 앱이 쥐고 있어
클라이언트 쪽에서는 못 바꿉니다. MCP가 바꾼 파일은 되돌릴 수 있고, 서버에는 여전히 아무것도
설치하지 않습니다.

**명령 실행도 줄 수 있습니다** <sub>v1.7.0</sub> — GUI 로 안 되는 일을 시키려면 결국 필요합니다.
파일 삭제와 마찬가지로 **서버별로 따로 켜고, 기본은 꺼짐**입니다. 켠 뒤에는 다른 변경 도구와
같은 승인 정책을 따릅니다 — 자율주행을 시킬 생각이면 명령도 같이 가야지, 명령만 따로 떼어
매번 묻게 하면 완화 모드가 의미를 잃습니다.

```bash
claude mcp add --transport http litedeck http://127.0.0.1:<포트>/mcp \
  --header "Authorization: Bearer <토큰>"
```

→ [MCP 연동](docs/mcp.md)

## 설치

[릴리스 페이지](https://github.com/cpprhtn/LiteDeck/releases)에서 받으세요.

| 파일 | 대상 |
|---|---|
| `litedeck-desktop-macos.zip` | macOS (Intel·Apple Silicon 공용) |
| `litedeck-desktop-windows-amd64.zip` | Windows 10/11 (amd64). 압축을 풀면 `litedeck.exe` 하나, 설치 없이 바로 실행 |
| `litedeck-desktop-linux-amd64.tar.gz` | Linux (amd64). **Ubuntu 24.04 이상** — `libwebkit2gtk-4.1` 이 필요합니다. 22.04라면 [직접 빌드](docs/building.md) |

> [!WARNING]
> **현재 릴리스는 코드 서명이 되어 있지 않습니다.** 서명·공증에는 비용이 들어 초기에는 미서명으로 배포하며,
> 함께 올리는 SHA256 체크섬은 **서명을 대신하지 못합니다.** 신뢰가 필요하면
> [직접 빌드](docs/building.md)하세요.
>
> 그래서 첫 실행 시 macOS Gatekeeper와 Windows SmartScreen 경고가 뜹니다. 넘어가는 방법은
> [설치와 서버 준비](docs/install.md)에 적어두었습니다.

**브라우저로 쓰기 — 서버 모드.** 데스크톱 앱 대신 서버 한 대에 올려두고 브라우저로 접근하는
헤드리스 빌드도 있습니다 — `litedeck-server-linux-amd64.tar.gz` · `litedeck-server-linux-arm64.tar.gz`.
그라파나처럼 URL로 접속해서 씁니다. 실행·노출·로그인은 [서버 모드](docs/server-mode.md)에 적어두었습니다.

**서버 쪽 준비.** Linux 서버라면 이미 SSH로 접속하고 계실 테니 준비는 끝났습니다. Windows는 OpenSSH 서버만 켜면 됩니다.
집에 있는 PC를 외부에서 다루려면 포트포워딩 대신 메시 VPN을 권합니다 —
[서버 준비](docs/install.md#서버-준비) · [Tailscale로 외부에서 쓰기](docs/remote-access.md)

## 이 도구가 맞는 경우

- **서버 두세 대에서 대여섯 대**를 직접 돌봅니다. 로그를 보고, 서비스를 재시작하고, 설정 파일을 고칩니다
- 그 서버에 **뭔가를 더 깔고 싶지 않습니다.** SSH는 이미 열려 있고, 그것으로 끝내고 싶습니다
- GUI는 원하지만 **무엇이 실행됐는지는 보고 싶습니다**
- 터미널을 버릴 생각은 없습니다. 자주 하는 일만 클릭으로 하고 싶을 뿐입니다

서버 수십 대를 한꺼번에 다뤄야 하거나, 선언적 상태 관리나 본격적인 원격 개발 환경이 필요하시다면 다른 도구가 낫습니다.
어떤 경우에 그런지 [안 맞는 경우](docs/support.md#안-맞는-경우)에 적어두었습니다.

## 더 읽어보기

| | |
|---|---|
| [보안](docs/security.md) | 호스트 키 검증·인증·자격증명 저장·sudo·MCP 엔드포인트. **못 하는 것을 먼저** 적었습니다 |
| [지원 범위](docs/support.md) | 무엇을 어디서 검증했고 무엇이 미검증인지. 안 맞는 경우와 하지 않는 것 |
| [MCP 연동](docs/mcp.md) | 도구 18개, 승인 정책, 되돌리기, 안전장치 |
| [기능 자세히](docs/features.md) | **전체 기능 목록**, 그리고 Command Log·편집기·전송·Compose·sshd 점검·ProxyJump |
| [설치와 서버 준비](docs/install.md) | 첫 실행 경고 넘기기, Windows OpenSSH 켜기 |
| [Tailscale로 외부에서 쓰기](docs/remote-access.md) | 포트포워딩 없이 집 PC에 붙기. Tailscale SSH·MCP·서브넷 라우터, 그리고 계정 없이 가는 길 |
| [소스에서 빌드](docs/building.md) | 빌드와 테스트 |

## 기여

[CONTRIBUTING.md](CONTRIBUTING.md)를 참고하세요. **새 서버 OS를 지원하는 일이 어댑터 하나 구현으로 끝나도록** 설계돼 있습니다. Windows 지원도 그렇게 붙었습니다. OpenRC(Alpine)·launchd(macOS)·FreeBSD가 비어 있습니다.

취약점을 발견하셨다면 공개 이슈 대신 **cpprhtn@naver.com**으로 보내주세요.

## 라이선스

[Apache-2.0](LICENSE)
