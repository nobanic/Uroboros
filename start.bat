@echo off
rem Switch the console to UTF-8; otherwise non-ASCII text in this file would be garbled in a GBK terminal.
chcp 65001 >nul 2>&1
rem Uroboros supervisor start script (Windows)
rem
rem Usage:
rem   start.bat                  Run in the foreground (Ctrl-C to stop)
rem   start.bat -addr :9000      Extra arguments are passed through to uroboros as-is
rem
rem It does just one thing: launch uroboros.exe, and after the process exits decide from the exit code whether to relaunch it.
rem
rem   0      User stopped it normally  -> exit the loop
rem   75     Program requested restart -> relaunch immediately (one-click update or rollback clicked in the UI)
rem   other  Crash                     -> relaunch after a backoff (1->2->4… up to 60 seconds)
rem
rem Downloading, SHA256 verification, and swapping are not done here; uroboros does them all itself at startup
rem (the selfupdate package). The script stays dead simple; see the notes at the top of start.sh for details.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=uroboros.exe"
if not exist "%BIN%" (
	echo [uroboros] executable %BIN% not found 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [uroboros] exited normally
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem Update/rollback is ready: after relaunch, uroboros completes the swap at startup.
	echo [uroboros] restart requested (applying new version)…
	set /a delay=1
	goto loop
)

echo [uroboros] abnormal exit ^(code=!code!^), restarting in !delay!s 1>&2
rem timeout fails in a redirected console, so use ping as a fallback (delaying N seconds needs N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
