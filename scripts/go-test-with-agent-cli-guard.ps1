param([Parameter(Position = 0, ValueFromRemainingArguments = $true)][string[]]$CommandArgs)

$ErrorActionPreference = 'Stop'
if (-not $CommandArgs -or $CommandArgs.Count -eq 0) {
    throw 'Usage: go-test-with-agent-cli-guard.ps1 go -C server test <packages> [flags]'
}
$guardRoot = Join-Path ([IO.Path]::GetTempPath()) ('multica-agent-cli-guard-' + [guid]::NewGuid().ToString('N'))
$guardRoot = [IO.Path]::GetFullPath($guardRoot)
$guardBin = Join-Path $guardRoot 'bin'
$marker = Join-Path $guardRoot 'invocations.log'
$previousPath = $env:PATH
$previousMarker = $env:MULTICA_AGENT_CLI_GUARD_MARKER
$previousCache = $env:GOCACHE
$commandExit = 1
try {
    [void](New-Item -ItemType Directory -Path $guardBin -Force)
    $source = Join-Path $guardRoot 'sentinel.go'
    [IO.File]::WriteAllText($source, @'
package main
import ("os"; "path/filepath")
func main() {
    if f, err := os.OpenFile(os.Getenv("MULTICA_AGENT_CLI_GUARD_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
        _, _ = f.WriteString(filepath.Base(os.Args[0])+" [arguments redacted]\n")
        _ = f.Close()
    }
    os.Exit(126)
}
'@, [Text.UTF8Encoding]::new($false))
    $sentinel = Join-Path $guardRoot 'sentinel.exe'
    & go build -o $sentinel $source
    if ($LASTEXITCODE -ne 0) { throw 'Failed to build the agent CLI guard' }
    foreach ($name in Get-Content -LiteralPath (Join-Path $PSScriptRoot 'agent-cli-command-names.txt')) {
        if (-not $name -or $name.StartsWith('#')) { continue }
        if ($name -notmatch '^[A-Za-z0-9._-]+$') { throw 'Invalid agent CLI command name' }
        Copy-Item -LiteralPath $sentinel -Destination (Join-Path $guardBin ($name + '.exe'))
        $batch = "@echo off`r`n>> `"%MULTICA_AGENT_CLI_GUARD_MARKER%`" echo $name [arguments redacted]`r`nexit /b 126`r`n"
        foreach ($extension in @('.cmd', '.bat')) {
            [IO.File]::WriteAllText((Join-Path $guardBin ($name + $extension)), $batch, [Text.UTF8Encoding]::new($false))
        }
    }
    # Keep compiler caches reusable while tests isolate their profile directories.
    if (-not $env:GOCACHE) { $env:GOCACHE = (& go env GOCACHE).Trim() }
    $env:PATH = $guardBin + [IO.Path]::PathSeparator + $previousPath
    $env:MULTICA_AGENT_CLI_GUARD_MARKER = $marker
    $executable, $arguments = $CommandArgs[0], @($CommandArgs | Select-Object -Skip 1)
    $application = Get-Command -Name $executable -CommandType Application -ErrorAction Stop | Select-Object -First 1
    & $application.Source @arguments
    $commandExit = $LASTEXITCODE
    if ((Test-Path -LiteralPath $marker) -and (Get-Item -LiteralPath $marker).Length -gt 0) {
        foreach ($invocation in Get-Content -LiteralPath $marker) {
            Write-Host "Unexpected agent CLI invocation: $invocation"
        }
        $commandExit = 1
    }
} finally {
    $env:PATH = $previousPath
    $env:MULTICA_AGENT_CLI_GUARD_MARKER = $previousMarker
    $env:GOCACHE = $previousCache
    $temporaryRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $guardRoot.StartsWith($temporaryRoot, [StringComparison]::OrdinalIgnoreCase) -or
        -not ([IO.Path]::GetFileName($guardRoot)).StartsWith('multica-agent-cli-guard-')) {
        throw 'Refusing to clean an unexpected guard directory'
    }
    if (Test-Path -LiteralPath $guardRoot) { Remove-Item -LiteralPath $guardRoot -Recurse -Force }
}
exit $commandExit
