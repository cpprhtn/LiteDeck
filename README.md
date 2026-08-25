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

## 5가지 원칙

1. **서버 무설치.** 에이전트·데몬·패키지를 설치하지 않습니다. 서버에 남는 것은 사용자가 시킨 일의 결과뿐입니다
2. **SSH만 사용.** 이미 열려 있는 SSH 포트 하나만 씁니다. 웹서버도, 중계 서버도 없습니다
3. **숨기지 않습니다.** GUI가 실행한 명령이 전부 Command Log에 그대로 뜹니다. sudo도 몰래 붙이지 않고 물어봅니다
4. **로그인 없음, 수집 없음, 오픈소스.** 계정을 요구하지 않고, 아무것도 수집하지 않으며, 전체 소스가 공개됩니다
5. **가볍게.** Electron을 쓰지 않습니다. 받는 파일 5–10MB, 설치 후 13–16MB, 실행까지 1초 이내

> 1번의 예외 하나: 편집기로 파일을 저장할 때 같은 디렉터리에 임시 파일을 만들고 `rename`으로
> 갈아끼웁니다. 저장이 중간에 끊겨도 원본이 반토막 나지 않게 하려는 것이고, 성공하면 임시 파일은
> 남지 않습니다. `rename`이 실패하면 임시 파일 경로를 화면에 알려주고 지우지 않습니다.
> 잃어버리는 것보다 낫기 때문입니다.

## 모니터링 탭 <sub>v1.5.0</sub>

<p align="center">
  <img src="docs/media/06-monitoring.png" width="880" alt="모니터링 탭: CPU 분해, 코어별, 메모리, GPU, 네트워크, 디스크 I/O, 시스템 정보, 파일시스템">
</p>

요약 바가 **CPU 40%** 라고 말하면, 이 탭은 **그 40%가 무엇인지** 말합니다.

- **CPU 를 사용자·커널·IO 대기·뺏김으로 가릅니다.** 90%가 전부 IO 대기면 CPU 가 모자란 게 아니라 디스크를 기다리는 것이고, 전부 뺏김이면 바쁘지도 않은데 하이퍼바이저가 시간을 남에게 넘기는 것입니다. 갈라놓기 전에는 셋 다 「바쁨」입니다
- **코어별 다이.** 32코어가 "40%"면 전부 반쯤 바쁜 것이거나 **하나가 박히고 나머지가 노는 것**인데, 뒤쪽이 단일 스레드 병목의 모양입니다
- **inode.** 용량은 남았는데 파일을 못 만드는 상태고, 그때도 모든 도구가 `no space left on device` 라고 합니다 — 바이트가 떨어졌을 때와 같은 문장입니다
- **네트워크 오류·버림**, **디스크 I/O**, **PSI**(사용률이 아니라 「얼마나 기다렸는가」), 실행 대기·IO 블록, 열린 FD
- **NVIDIA 카드**가 있으면 사용률·팬·온도·VRAM

보고 있는 동안의 추세를 시간축으로 그리되, **앱이 안 보던 구간은 선을 끊습니다.** 없는 기록을 이어 그리지 않습니다.

전부 `/proc` 과 `df` 를 읽습니다. **서버에 설치하는 것은 여전히 없습니다.**

> 화면은 데모용 컨테이너입니다([`testdata/demo`](testdata/demo)). GPU 는 그 안의 대역이고, 나머지 숫자는 실제로 그 컨테이너에서 나온 값입니다.

## Claude가 이 앱을 거쳐 서버를 다룹니다

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
| `litedeck-desktop-macos.zip` | macOS (Intel·Apple Silicon 공용) |
| `litedeck-desktop-windows-amd64.zip` | Windows 10/11 (amd64). 압축을 풀면 `litedeck.exe` 하나, 설치 없이 바로 실행 |
| `litedeck-desktop-linux-amd64.tar.gz` | Linux (amd64). **Ubuntu 24.04 이상** — `libwebkit2gtk-4.1` 이 필요합니다. 22.04 라면 [직접 빌드](docs/building.md) |

> [!WARNING]
> **현재 릴리스는 코드 서명이 되어 있지 않습니다.** 서명·공증에는 비용이 들어 초기에는 미서명으로 배포하며,
> 함께 올리는 SHA256 체크섬은 **서명을 대신하지 못합니다.** 신뢰가 필요하면
> [직접 빌드](docs/building.md)하세요.
>
> 그래서 첫 실행에 macOS Gatekeeper와 Windows SmartScreen 경고가 뜹니다. 넘어가는 방법은
> [설치와 서버 준비](docs/install.md)에 적어두었습니다.

**서버 쪽 준비.** Linux는 이미 SSH로 접속하고 계시면 끝입니다. Windows는 OpenSSH 서버만 켜면 됩니다.
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
| [기능 자세히](docs/features.md) | **전체 기능 목록**, 그리고 Command Log·편집기·전송·Compose·sshd 점검·ProxyJump |
| [설치와 서버 준비](docs/install.md) | 첫 실행 경고 넘기기, Windows OpenSSH 켜기 |
| [Tailscale로 외부에서 쓰기](docs/remote-access.md) | 포트포워딩 없이 집 PC에 붙기. Tailscale SSH·MCP·서브넷 라우터, 그리고 계정 없이 가는 길 |
| [소스에서 빌드](docs/building.md) | 빌드와 테스트 |

## 기여

[CONTRIBUTING.md](CONTRIBUTING.md)를 참고하세요. **새 서버 OS 지원 = 어댑터 하나 구현**이 되도록 설계돼 있습니다. Windows 지원도 그렇게 붙었습니다. OpenRC(Alpine)·launchd(macOS)·FreeBSD가 비어 있습니다.

취약점을 발견하셨다면 공개 이슈 대신 **cpprhtn@naver.com** 으로 보내주세요.

## 라이선스

[Apache-2.0](LICENSE)
