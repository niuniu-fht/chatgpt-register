# 项目默认运行规则

## CloakBrowser 生命周期

- Adobe 注册默认使用 CloakBrowser；免费许可证按单并发席位运行。
- 停止或重启服务前，先停止生产任务，再正常停止服务进程，等待浏览器子进程退出。
- 禁止直接强制结束服务进程后立即启动新实例；这种操作可能留下孤立的 CloakBrowser/Chromium 进程并占用并发席位。
- 重启后必须检查：9010 只有一个监听进程、没有残留 `~/.cloakbrowser` 浏览器进程、CloakBrowser 会话席位已释放。
- 若发现父进程已退出但 CloakBrowser 子进程仍存在，只清理 CloakBrowser 启动的进程树，不处理用户普通 Chrome。

## Windows 操作命令

```powershell
cd D:\register\chatgpt-register
# 先在界面停止生产任务，等待任务状态变为已停止
Stop-Process -Name chatgpt-register -ErrorAction SilentlyContinue
Start-Sleep -Seconds 3
Get-CimInstance Win32_Process | Where-Object {
  $_.ExecutablePath -like "$env:USERPROFILE\\.cloakbrowser\\*"
} | ForEach-Object {
  Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue
}
$env:ADDR="127.0.0.1:9010"
Start-Process .\chatgpt-register.exe -WorkingDirectory (Get-Location)
```

启动后确认：

```powershell
netstat -ano | Select-String ':9010'
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:9010/ -TimeoutSec 5
```

远程服务器使用 `systemctl stop chatgpt-register`，确认浏览器进程退出后再执行 `systemctl start chatgpt-register`。
