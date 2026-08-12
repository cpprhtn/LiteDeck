# 설치와 서버 준비

[← README](../README.md)

## 첫 실행: 경고 넘기기

**macOS 15 (Sequoia) 이상.** `Apple could not verify "litedeck" is free of malware…` 가 뜹니다. 이 창에는 "휴지통으로 이동"과 "취소"밖에 없습니다.

**시스템 설정 → 개인정보 보호 및 보안** 을 열고 아래로 내려가면 `"litedeck"이(가) 차단되었습니다` 옆에 **"그래도 열기"** 가 있습니다. 한 번 누르면 이후로는 그냥 열립니다. 터미널이 빠르면:

```bash
xattr -d com.apple.quarantine /Applications/litedeck.app
```

> 흔히 안내되는 **우클릭 → 열기** 는 macOS 15부터 Apple이 없앴습니다. 14 이하에서만 통합니다.

**Windows.** SmartScreen이 `Windows에서 PC를 보호했습니다` 를 띄웁니다. **추가 정보 → 실행** 을 누르면 됩니다. WebView2가 없으면 설치 안내가 나옵니다.

**Defender가 파일을 지울 수 있습니다.** 서명되지 않은 새 실행 파일에는 평판 정보가 없어서, 다운로드 직후 경고 없이 격리되기도 합니다. 바이러스가 아니라 **서명이 없기 때문**이며, 서명·공증에는 비용이 들어 초기 릴리스는 미서명으로 배포합니다.

지워졌다면 **Windows 보안 → 바이러스 및 위협 방지 → 보호 기록** 에서 해당 항목의 **작업 → 허용** 으로 복원할 수 있습니다. 미리 막으려면 같은 화면의 **설정 관리 → 제외 항목 추가** 에 압축 푼 폴더를 등록하세요.

**Linux.** 별도 절차 없이 압축을 풀고 실행 권한만 주면 됩니다. 다만 릴리스 바이너리는
**`libwebkit2gtk-4.1`** 에 링크돼 있어 **Ubuntu 24.04 이상**이 필요합니다 — 빌드가 도는 CI
러너가 24.04 라서입니다. 22.04처럼 `libwebkit2gtk-4.0` 만 있는 배포판에서는 실행되지 않으니
[직접 빌드](building.md)하세요.

```bash
tar xzf litedeck-linux-amd64.tar.gz && chmod +x litedeck && ./litedeck
```

## 서버 준비

**Linux.** 이미 SSH로 접속하고 계시면 준비는 끝났습니다. 추가 설정이 없습니다.

**Windows.** OpenSSH 서버만 켜면 됩니다. 관리자 PowerShell에서:

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Start-Service sshd
Set-Service -Name sshd -StartupType Automatic
```

기본 셸이 cmd.exe든 PowerShell이든 상관없습니다. LiteDeck은 명령을 `-EncodedCommand` 로 보내므로 셸의 따옴표 규칙에 영향을 받지 않습니다.

**집에 있는 PC를 외부에서 다루려면** 포트포워딩 대신 Tailscale 같은 메시 VPN을 쓰는 편이 낫습니다.
설정 순서와, 계정 없이 가는 방법(Headscale·WireGuard)은 [docs/remote-access.md](remote-access.md)에 적어두었습니다.
