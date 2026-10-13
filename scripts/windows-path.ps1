# Keep the registry's raw PATH (including %VARIABLE% entries) and registry type.
param(
    [Parameter(Mandatory = $true)][ValidateSet('Add', 'Remove')][string]$Action,
    [Parameter(Mandatory = $true)][string]$InstallDir
)
$ErrorActionPreference = 'Stop'
try {
    if ([string]::IsNullOrWhiteSpace($InstallDir) -or $InstallDir.Contains(';')) {
        throw 'The installation directory cannot be empty or contain a semicolon.'
    }
    Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class AudiobookEnvironment {
    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    public static extern IntPtr SendMessageTimeout(IntPtr hwnd, uint message,
        UIntPtr wParam, string lParam, uint flags, uint timeout, out UIntPtr result);
}
'@
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    try {
        $raw = $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $kind = if ($null -eq $raw) { [Microsoft.Win32.RegistryValueKind]::ExpandString } else { $key.GetValueKind('Path') }
        if ($kind -ne [Microsoft.Win32.RegistryValueKind]::String -and $kind -ne [Microsoft.Win32.RegistryValueKind]::ExpandString) {
            throw 'The user PATH has an unsupported registry type.'
        }
        $parts = if ([string]::IsNullOrEmpty($raw)) { @() } else { @($raw -split ';') }
        $parts = @($parts | Where-Object { $_ -ine $InstallDir })
        if ($Action -eq 'Add') { $parts += $InstallDir }
        $key.SetValue('Path', ($parts -join ';'), $kind)
    } finally { $key.Dispose() }
    [UIntPtr]$result = [UIntPtr]::Zero
    # Notify other applications; a hung listener must not block installation.
    [void][AudiobookEnvironment]::SendMessageTimeout([IntPtr]0xffff, 0x1a,
        [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
    exit 0
} catch {
    Write-Error $_ -ErrorAction Continue
    exit 1
}
