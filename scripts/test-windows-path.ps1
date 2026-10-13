# Run on Windows: exercise raw expandable PATH preservation and idempotent updates.
$ErrorActionPreference = 'Stop'
$key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
$options = [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames
$before = $key.GetValue('Path', $null, $options)
$beforeKind = if ($null -ne $before) { $key.GetValueKind('Path') }
$helper = Join-Path $PSScriptRoot 'windows-path.ps1'
$directory = "C:\Audiobook Test's CLI"
$fixture = '%SystemRoot%\Sentinel;C:\Other;;'
try {
    $key.SetValue('Path', $fixture, [Microsoft.Win32.RegistryValueKind]::ExpandString)
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $helper Add $directory
    if ($LASTEXITCODE -ne 0) { throw 'PATH add failed' }
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $helper Add $directory
    if ($LASTEXITCODE -ne 0) { throw 'PATH second add failed' }
    $actual = $key.GetValue('Path', $null, $options)
    if ($actual -cne ($fixture + ';' + $directory)) { throw "PATH changed or duplicated: $actual" }
    if ($key.GetValueKind('Path') -ne [Microsoft.Win32.RegistryValueKind]::ExpandString) { throw 'PATH registry type changed' }
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $helper Remove $directory
    if ($LASTEXITCODE -ne 0) { throw 'PATH remove failed' }
    if ($key.GetValue('Path', $null, $options) -cne $fixture) { throw 'PATH removal changed existing entries' }
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $helper Add 'C:\Invalid;Entry'
    if ($LASTEXITCODE -eq 0) { throw 'Invalid PATH entry accepted' }
} finally {
    if ($null -eq $before) { $key.DeleteValue('Path', $false) }
    else { $key.SetValue('Path', $before, $beforeKind) }
    $key.Dispose()
}
Write-Host 'test-windows-path: OK'
