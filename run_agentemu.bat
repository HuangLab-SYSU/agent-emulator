@echo off

rem Start an AgentEmulator experiment: compile the agent trace into a
rem transaction plan and automatically run it on a freshly launched
rem BlockEmulator-X cluster. Results land in .\exp\agentemu-results\.
rem
rem Usage:
rem   run_agentemu.bat                    (use agentEmuConfig.yaml)
rem   run_agentemu.bat my-config.yaml     (use another config)

setlocal

cd /d "%~dp0"

set "CONFIG=%~1"
if "%CONFIG%"=="" set "CONFIG=agentEmuConfig.yaml"

if not exist "%CONFIG%" (
  echo config file not found: %CONFIG% 1>&2
  exit /b 1
)

rem Compile first so a broken build does not wipe the previous results.
go build ./...
if errorlevel 1 (
  echo go build failed, aborting 1>&2
  exit /b 1
)

rem Clean previous outputs: measurement files are created exclusively, and a
rem stale agent_registry.json would suppress re-registration of active agents.
if exist ".\exp" rmdir /s /q ".\exp"

rem Run the whole pipeline: trace -> plan -> auto-launched cluster -> results.
rem Cluster logs are mirrored to this console and kept under
rem exp\agentemu-results\round_001\chain\logs\.
go run cmd/agentemu/main.go -config "%CONFIG%"
if errorlevel 1 exit /b 1

echo.
echo agentemu finished; results:
echo   plan ^& action map : exp\agentemu-results\round_001\
echo   agent csvs        : exp\agentemu-results\round_001\agents\
echo   agent registry    : exp\agentemu-results\agent_registry.json
echo   chain measurements: exp\agentemu-results\round_001\chain\results\
echo   round summary     : exp\agentemu-results\rounds_summary.json

rem Post-experiment figures: plot agent balances from the latest round, build
rem an HTML gallery and open it in the default browser. A failed experiment
rem aborts earlier, so this only runs on success (parity with run_agentemu.sh).
set "FIGS_CODE=figs\python_code"
set "FIGS_OUT=figs\figs_results"
set "PY=python"
where python >nul 2>nul || set "PY=py -3"

set "LATEST_ROUND="
for /d %%D in ("exp\agentemu-results\round_*") do set "LATEST_ROUND=%%~D\"

if not "%LATEST_ROUND%"=="" if exist "%LATEST_ROUND%agents\agent-*.csv" (
  if not exist "%FIGS_OUT%" mkdir "%FIGS_OUT%"
  rem Drop figures from the previous run so the gallery never mixes runs.
  del /q "%FIGS_OUT%\*.png" "%FIGS_OUT%\index.html" 2>nul
  %PY% "%FIGS_CODE%\plot_agent_balance.py" --data-dir "%LATEST_ROUND%agents" --fig-dir "%FIGS_OUT%"
  if errorlevel 1 exit /b 1
  %PY% "%FIGS_CODE%\build_fig_html.py" --fig-dir "%FIGS_OUT%" --data-dir "%LATEST_ROUND%agents"
  if errorlevel 1 exit /b 1
  echo   figures ^& gallery : %FIGS_OUT%\index.html
  start "" "%FIGS_OUT%\index.html"
) else (
  echo warn: no agent CSVs under exp\agentemu-results; skip plotting 1>&2
)
