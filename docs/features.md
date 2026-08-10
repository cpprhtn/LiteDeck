# 기능 자세히

[← README](../README.md)

## Command Log: GUI로 CLI를 배웁니다

<p align="center">
  <img src="media/03-command-log.gif" width="820" alt="Command Log">
</p>

<p align="center"><sub>재시작이 권한 부족으로 거부되자 <b>물어봅니다</b>. 실행된 명령은 그대로 보이고, 비밀번호는 stdin 으로 가서 보이지 않습니다.</sub></p>


운영 서버를 만지는 GUI는 결국 "믿어달라"고 요구하는 셈입니다. LiteDeck은 **방금 무엇을 실행했는지 정확히 보여주는 것**으로 그 신뢰를 얻습니다.

```
$ systemctl list-units --type=service --all --output=json               120ms
$ journalctl -u myapp.service -n 200 -f --no-pager -q
$ sudo -S -p '' -- systemctl restart -- myapp.service                   310ms
$ powershell -EncodedCommand ⟨utf8 prelude⟩ Restart-Service -Name 'Spooler' -Force
```

비밀번호는 표준 입력으로 전달되므로 **명령줄을 그대로 보여줘도 안전합니다.** 이 기록은 로컬에만 남고 외부로 나가지 않습니다.

## 편집기: 양쪽 어디에도 설치가 없습니다

<p align="center">
  <img src="media/02-terminal-jump.gif" width="820" alt="터미널에서 code . 과 vi">
</p>

<p align="center"><sub>터미널에 <code>code .</code> 을 치면 <b>그 줄이 서버로 가기 전에</b> 앱이 가로채 파일 탭으로 옵니다. <code>vi</code> 로 새 파일을 열면 편집기 탭이 열립니다. 서버에는 VS Code 도 vi 도 필요 없습니다.</sub></p>


원격 파일을 고치려면 보통 둘 중 하나를 깝니다. 서버에 `vscode-server`(수백 MB)를 올리거나,
서버의 `vi`·`nano`를 터미널로 쓰거나. LiteDeck은 **둘 다 안 합니다.**

- **서버 쪽.** 아무것도 필요 없습니다. 파일은 SSH가 이미 제공하는 SFTP로 읽고 씁니다.
  서버에 편집기가 없어도, 있어도 상관없습니다
- **클라이언트 쪽.** VS Code를 설치할 필요가 없습니다. 편집기가 앱 안에 들어 있습니다
  (CodeMirror, 문법 24종). 첫 파일을 열 때 로드되므로 디렉터리만 보다 끄면 비용이 없습니다

터미널에서 `code .` 이나 `vi foo.conf` 를 치면 **앱이 그 줄을 서버로 보내기 전에 가로채**
파일 탭으로 이동합니다. 서버는 이 기능의 존재를 모릅니다. 그래서 서버에 VS Code도 vi도
없어도 되고, 반대로 **서버 쪽에서 무언가 열리는 일도 없습니다.**

<p align="center">
  <img src="media/05-editor.gif" width="820" alt="파일을 열어 고치고, 저장 전에 diff 를 본다">
</p>

<p align="center"><sub>트리에서 파일을 열면 문법 강조가 붙은 편집기가 오른쪽에 열립니다. 저장을 누르면 <b>서버에 지금 들어 있는 내용과의 diff</b> 가 먼저 뜹니다.</sub></p>

확인하고 넘어가면 임시 파일에 쓴 뒤 `rename`으로 갈아끼웁니다. 저장이 중간에 끊겨도
원본이 반토막 나지 않습니다.

> 대비: VS Code Remote-SSH는 서버에 서버를 설치합니다. 저사양 VPS에서 `vscode-server`가
> 메모리를 잡아먹는 것은 잘 알려진 문제입니다. 본격 원격 IDE가 필요하면 그쪽이 정답이고,
> **설정 파일 하나 고치자고 수백 MB를 올리기 싫을 때** 이쪽이 답입니다.
