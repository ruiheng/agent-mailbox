param(
    [Parameter(Position = 0)]
    [string]$Target = "help",

    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$RemainingArgs
)

$ErrorActionPreference = "Stop"

$go = if ($env:GO) { $env:GO } else { "go" }
$cmdPath = if ($env:CMD_PATH) { $env:CMD_PATH } else { "./cmd/mailbox" }
$binDir = if ($env:BIN_DIR) { $env:BIN_DIR } else { "bin" }
$binaryName = if ($env:BINARY_NAME) { $env:BINARY_NAME } else { "agent-mailbox.exe" }
$prefix = if ($env:PREFIX) { $env:PREFIX } else { Join-Path $env:USERPROFILE ".local" }
$destDir = if ($env:DESTDIR) { $env:DESTDIR } else { "" }
$installDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $prefix "bin" }
$buildOutput = Join-Path $binDir $binaryName
$runArgs = if ($env:ARGS) { $null } else { $RemainingArgs }

function Ensure-Directory {
    param([string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Path $Path | Out-Null
    }
}

function Invoke-Go {
    param([string[]]$Arguments)

    & $go @Arguments
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
}

function Assert-CgoRequired {
    $cgoEnabled = if ($env:CGO_ENABLED) {
        $env:CGO_ENABLED
    } else {
        (& $go env CGO_ENABLED).Trim()
    }

    if ($cgoEnabled -eq "0") {
        throw @'
This project requires CGO because it uses github.com/mattn/go-sqlite3.

Current environment:
  CGO_ENABLED=0

On Windows, install a C toolchain first, then rerun with CGO enabled.
Example:
  $env:CGO_ENABLED = 1
  $env:CC = "C:/msys64/ucrt64/bin/gcc.exe"
  $env:CXX = "C:/msys64/ucrt64/bin/g++.exe"
  ./make.ps1 build
'@
    }
}

function Resolve-CompilerPath {
    param([string]$Compiler)

    if ([string]::IsNullOrWhiteSpace($Compiler)) {
        return $Compiler
    }

    if ([System.IO.Path]::IsPathRooted($Compiler) -and (Test-Path -LiteralPath $Compiler)) {
        return (Resolve-Path -LiteralPath $Compiler).Path
    }

    $resolvedCommand = Get-Command $Compiler -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $resolvedCommand -and -not [string]::IsNullOrWhiteSpace($resolvedCommand.Source)) {
        return $resolvedCommand.Source
    }

    $compilerLeaf = [System.IO.Path]::GetFileName($Compiler)
    if ([string]::IsNullOrWhiteSpace([System.IO.Path]::GetExtension($compilerLeaf))) {
        $compilerLeaf = "$compilerLeaf.exe"
    }

    $msys2Candidates = @(
        (Join-Path "C:\msys64\ucrt64\bin" $compilerLeaf),
        (Join-Path "C:\msys64\mingw64\bin" $compilerLeaf)
    )

    foreach ($candidate in $msys2Candidates) {
        if (Test-Path -LiteralPath $candidate) {
            return $candidate
        }
    }

    return $Compiler
}

function Add-DirectoryToPath {
    param([string]$Directory)

    if ([string]::IsNullOrWhiteSpace($Directory) -or -not (Test-Path -LiteralPath $Directory)) {
        return
    }

    $pathParts = $env:Path -split ';'
    if ($pathParts -notcontains $Directory) {
        $env:Path = "$Directory;$env:Path"
    }
}

function Initialize-CgoToolchain {
    $configuredCC = if ($env:CC) {
        $env:CC
    } else {
        (& $go env CC).Trim()
    }
    $configuredCXX = if ($env:CXX) {
        $env:CXX
    } else {
        (& $go env CXX).Trim()
    }

    if (-not [string]::IsNullOrWhiteSpace($configuredCC)) {
        $resolvedCC = Resolve-CompilerPath -Compiler $configuredCC
        $env:CC = $resolvedCC
        Add-DirectoryToPath -Directory (Split-Path -Path $resolvedCC -Parent)
    }

    if (-not [string]::IsNullOrWhiteSpace($configuredCXX)) {
        $resolvedCXX = Resolve-CompilerPath -Compiler $configuredCXX
        $env:CXX = $resolvedCXX
        Add-DirectoryToPath -Directory (Split-Path -Path $resolvedCXX -Parent)
    }
}

function Resolve-InstallDestinationRoot {
    param(
        [string]$InstallDir,
        [string]$DestDir
    )

    if ([string]::IsNullOrWhiteSpace($DestDir)) {
        return $InstallDir
    }

    if (-not [System.IO.Path]::IsPathRooted($InstallDir)) {
        return Join-Path $DestDir $InstallDir
    }

    $qualifier = Split-Path -Path $InstallDir -Qualifier
    $relativeInstallDir = $InstallDir
    if (-not [string]::IsNullOrWhiteSpace($qualifier)) {
        $relativeInstallDir = $InstallDir.Substring($qualifier.Length)
    }

    $relativeInstallDir = $relativeInstallDir.TrimStart('\', '/')
    if ([string]::IsNullOrWhiteSpace($relativeInstallDir)) {
        return $DestDir
    }

    return Join-Path $DestDir $relativeInstallDir
}

function Split-ArgumentString {
    param([string]$ArgumentString)

    if ([string]::IsNullOrWhiteSpace($ArgumentString)) {
        return @()
    }

    if (-not ("CommandLineArgumentSplitter" -as [type])) {
        Add-Type -TypeDefinition @"
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

public static class CommandLineArgumentSplitter {
    [DllImport("shell32.dll", SetLastError = true)]
    private static extern IntPtr CommandLineToArgvW(
        [MarshalAs(UnmanagedType.LPWStr)] string lpCmdLine,
        out int pNumArgs);

    [DllImport("kernel32.dll")]
    private static extern IntPtr LocalFree(IntPtr hMem);

    public static string[] Split(string commandLine) {
        IntPtr argv = CommandLineToArgvW(commandLine, out int argc);
        if (argv == IntPtr.Zero) {
            throw new Win32Exception(Marshal.GetLastWin32Error());
        }

        try {
            string[] args = new string[argc];
            for (int i = 0; i < argc; i++) {
                IntPtr argPtr = Marshal.ReadIntPtr(argv, i * IntPtr.Size);
                args[i] = Marshal.PtrToStringUni(argPtr);
            }
            return args;
        } finally {
            LocalFree(argv);
        }
    }
}
"@
    }

    return [CommandLineArgumentSplitter]::Split($ArgumentString)
}

function Show-Help {
    @(
        "Available targets:"
        "  ./make.ps1 build              Build the agent-mailbox CLI into $buildOutput"
        "  ./make.ps1 test               Run the Go test suite"
        "  ./make.ps1 run -- <args>      Run the CLI with go run and pass args through"
        "  ./make.ps1 run-mcp            Run the built-in stdio MCP server with go run"
        "  ./make.ps1 install            Copy the built CLI into $installDir"
        "  ./make.ps1 clean              Remove local build output"
    ) | ForEach-Object { Write-Output $_ }
}

switch ($Target) {
    "help" {
        Show-Help
    }
    "build" {
        Assert-CgoRequired
        Initialize-CgoToolchain
        Ensure-Directory $binDir
        Invoke-Go @("build", "-o", $buildOutput, $cmdPath)
    }
    "test" {
        Assert-CgoRequired
        Initialize-CgoToolchain
        Invoke-Go @("test", "./...")
    }
    "run" {
        Assert-CgoRequired
        Initialize-CgoToolchain
        if ($env:ARGS) {
            $runArgs = Split-ArgumentString -ArgumentString $env:ARGS
        }
        $goArgs = @("run", $cmdPath) + $runArgs
        Invoke-Go $goArgs
    }
    "run-mcp" {
        Assert-CgoRequired
        Initialize-CgoToolchain
        Invoke-Go @("run", $cmdPath, "mcp")
    }
    "install" {
        & $PSCommandPath build
        if ($LASTEXITCODE -ne 0) {
            exit $LASTEXITCODE
        }

        $destinationRoot = Resolve-InstallDestinationRoot -InstallDir $installDir -DestDir $destDir
        Ensure-Directory $destinationRoot
        Copy-Item -LiteralPath $buildOutput -Destination (Join-Path $destinationRoot $binaryName) -Force
    }
    "clean" {
        if (Test-Path -LiteralPath $binDir) {
            Remove-Item -LiteralPath $binDir -Recurse -Force
        }
    }
    default {
        Write-Error "Unknown target '$Target'. Run ./make.ps1 help."
    }
}
