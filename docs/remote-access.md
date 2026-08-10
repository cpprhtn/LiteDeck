# 집 PC를 밖에서 다루기 (Tailscale)

> 영문판: [remote-access.en.md](remote-access.en.md)

LiteDeck은 SSH가 닿는 곳이면 어디든 붙습니다. 문제는 **SSH가 닿게 만드는 일**입니다.
집 PC나 홈랩은 보통 공유기 뒤에 있어서, 밖에서 접근하려면 포트포워딩을 열거나 DDNS를
붙이거나 VPN을 세우게 됩니다. 22번 포트를 인터넷에 그대로 여는 것은 권하지 않습니다.

Tailscale을 쓰면 그 셋 다 안 해도 됩니다. 두 도구의 역할이 깔끔하게 나뉩니다.

| | 없애주는 것 |
|---|---|
| **Tailscale** | 네트워크 설정 (포트포워딩, DDNS, 방화벽 구멍) |
| **LiteDeck** | 서버 설치물 (에이전트, 데몬, 패키지) |

합치면 **포트포워딩도 서버 설치물도 없이** 카페에서 집 PC의 파일을 열고, 서비스를
재시작하고, 코드를 고쳐 저장할 수 있습니다.

---

## 먼저 알아둘 것: Tailscale은 계정이 필요합니다

LiteDeck의 원칙 4는 "로그인 없음, 수집 없음"인데 **Tailscale은 계정이 필요하고,
조정 서버(coordination server)가 그들 인프라입니다.** 트래픽 자체는 기기 간
WireGuard 암호화로 직접 흐르지만, 어떤 기기가 있는지를 중개하는 곳은 그들 쪽입니다.

그 부분까지 직접 갖고 싶다면 아래 "계정 없이 가는 길"을 보세요. LiteDeck은 어느 쪽이든
차이를 모릅니다. LiteDeck에게는 그냥 붙을 수 있는 IP 하나일 뿐입니다.

---

## 1. 집 PC에 SSH 켜기

**Linux.** 이미 SSH로 접속하고 계시면 할 일이 없습니다.

**Windows.** 관리자 PowerShell에서 세 줄입니다.

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Start-Service sshd
Set-Service -Name sshd -StartupType Automatic
```

**macOS.** 시스템 설정 → 일반 → 공유 → 원격 로그인을 켭니다.

## 2. 양쪽에 Tailscale 설치

[tailscale.com/download](https://tailscale.com/download)에서 받아 집 PC와 노트북 양쪽에
설치하고, **같은 계정으로** 로그인합니다. 그러면 두 기기가 같은 tailnet에 들어갑니다.

공유기는 건드리지 않습니다. 포트를 열지도, DDNS를 붙이지도 않습니다.

## 3. 집 PC의 tailnet IP 확인

집 PC에서:

```bash
tailscale ip -4          # 100.x.y.z 형태로 나옵니다
```

MagicDNS를 켜두었다면 IP 대신 `mypc` 같은 이름을 그대로 써도 됩니다.

## 4. LiteDeck에 호스트 추가

**+ 추가**를 누르고 그 주소를 넣습니다.

| 항목 | 값 |
|---|---|
| 호스트 | `100.x.y.z` 또는 MagicDNS 이름 |
| 포트 | `22` |
| 사용자 | 집 PC의 계정 이름 |
| 인증 | ssh-agent를 맨 앞에 두는 것을 권합니다 |

접속을 누르면 처음 한 번 호스트 키 지문을 확인합니다. **이 지문은 집 PC에서 직접
확인하세요** — Tailscale이 연결 경로를 만들어줄 뿐, 상대가 맞는지 검증하는 것은
여전히 SSH의 일입니다.

```bash
# 집 PC에서 실행해 LiteDeck이 보여주는 지문과 대조합니다
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

여기까지가 전부입니다. 이제 노트북 어디서든 집 PC가 사이드바에 뜹니다.

---

## 계정 없이 가는 길

Tailscale의 조정 서버까지 직접 갖고 싶다면 두 가지 선택지가 있습니다.

**[Headscale](https://github.com/juanfont/headscale)** — Tailscale 조정 서버의
셀프호스팅 오픈소스 구현입니다(BSD-3). 클라이언트는 공식 Tailscale 앱을 그대로 쓰고,
로그인만 자기 서버로 향하게 합니다. 계정도, 외부 인프라도 없어집니다. 대신 조정 서버를
어딘가에 띄워 두어야 하는데, 그게 인터넷에 닿는 곳이어야 해서 VPS 한 대가 필요합니다.

**[WireGuard](https://www.wireguard.com/) 직접 구성** — 중개자 자체를 없애는 방법입니다.
키를 손으로 교환하고 엔드포인트를 직접 적습니다. 기기가 두세 대면 충분히 할 만하고,
NAT 뒤에 있는 쪽이 한쪽뿐이라면 특히 간단합니다. 기기가 늘어날수록 관리 비용이
빠르게 오릅니다.

**Tailscale·Headscale·WireGuard 중 무엇을 쓰든 LiteDeck 입장에서는 똑같습니다.**
IP 하나가 생기고, 거기에 SSH가 열려 있으면 됩니다.

---

## 알아둘 만한 것

**속도.** Tailscale은 가능하면 기기 간 직접 연결(WireGuard)을 만듭니다. 양쪽 NAT가
까다로우면 DERP 릴레이를 경유하는데, 그때는 지연이 늘어납니다. `tailscale status`가
어느 쪽인지 알려줍니다. LiteDeck은 화면을 스트리밍하지 않으므로 릴레이를 타도
쓸 만하지만, 파일 전송 속도는 눈에 띄게 차이납니다.

**집 PC가 잠들면 접속되지 않습니다.** 절전을 끄거나 Wake-on-LAN을 설정하세요.
이건 LiteDeck이나 Tailscale이 해결해 주는 문제가 아닙니다.

**Tailscale ACL.** tailnet에 기기가 여럿이면 ACL로 22번 포트에 닿을 수 있는 기기를
제한할 수 있습니다. 기본값은 전부 허용입니다.

**이 문서의 조합은 저자가 실제로 확인하지 않았습니다.** LiteDeck은 SSH가 닿는 IP면
동작하도록 만들어져 있어서 안 될 이유가 없지만, [지원 범위](../README.md#지원-범위)의
기준대로 **검증되지 않은 것은 검증되지 않았다고** 적어 둡니다. 해보셨다면 결과를
[이슈](https://github.com/cpprhtn/LiteDeck/issues)로 알려주세요.
