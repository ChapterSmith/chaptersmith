; package-windows.nsi — per-user installer for the GUI or the CLI.
; makensis -DKIND=gui|cli -DVERSION=1.2.0 -DSTAGE=<dir> -DOUTFILE=<exe> -DICON=<ico> package-windows.nsi
!ifndef KIND
  !error "Pass -DKIND=gui|cli"
!endif
!ifndef VERSION
  !error "Pass -DVERSION=<x.y.z>"
!endif
!ifndef STAGE
  !error "Pass -DSTAGE=<staging dir>"
!endif
!ifndef OUTFILE
  !error "Pass -DOUTFILE=<setup.exe path>"
!endif
!ifndef ICON
  !error "Pass -DICON=<.ico path>"
!endif

Unicode true
SetCompressor /SOLID lzma
RequestExecutionLevel user
!include "MUI2.nsh"
!include "LogicLib.nsh"

!if "${KIND}" == "gui"
  !define PRODUCT "ChapterSmith"
  !define DIRNAME "ChapterSmith"
  !define EXE "chaptersmith-gui.exe"
!else
  !define PRODUCT "ChapterSmith CLI"
  !define DIRNAME "ChapterSmith-CLI"
  !define EXE "chaptersmith.exe"
!endif
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${DIRNAME}"
!define WV2KEY "Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

Name "${PRODUCT}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\${DIRNAME}"
!define MUI_ICON "${ICON}"
!define MUI_UNICON "${ICON}"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "Install"
  SetOutPath "$INSTDIR"
  ; Drop libraries from a previous version before copying the new ones.
  Delete "$INSTDIR\*.dll"
  File /r "${STAGE}\*.*"
!if "${KIND}" == "cli"
  File "windows-path.ps1"
!endif
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${PRODUCT}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "Profipoint"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
!if "${KIND}" == "gui"
  CreateShortcut "$SMPROGRAMS\${PRODUCT}.lnk" "$INSTDIR\${EXE}"
  ; Detect through the 32-bit machine view, plus the per-user registration.
  ; Microsoft: https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution
  Call DetectWebView2
  ${If} $0 == ""
    ClearErrors
    ExecWait '"$INSTDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install' $1
    ${If} ${Errors}
      SetErrorLevel 1
      Abort "Could not start the WebView2 runtime installer."
    ${EndIf}
    ${If} $1 == 3010
      ; ERROR_SUCCESS_REBOOT_REQUIRED: retain the restart requirement.
      SetRebootFlag true
    ${ElseIf} $1 != 0
      DetailPrint "WebView2 runtime installer failed with exit code $1."
      SetErrorLevel 1
      Abort "WebView2 runtime installation failed. Check your network and retry."
    ${EndIf}
    ; Some bootstrappers return before registration is visible. Bound the wait.
    StrCpy $2 30
    ${Do}
      Call DetectWebView2
      ${If} $0 != ""
        ${ExitDo}
      ${EndIf}
      Sleep 1000
      IntOp $2 $2 - 1
    ${LoopUntil} $2 == 0
    ${If} $0 == ""
      SetErrorLevel 1
      Abort "WebView2 runtime was not registered. Restart Windows if requested, then retry."
    ${EndIf}
  ${EndIf}
!else
  ; -File passes the directory as data, including apostrophes. Never expand PATH.
  nsExec::ExecToLog '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\windows-path.ps1" Add "$INSTDIR"'
  Pop $0
  ${If} $0 != 0
    SetErrorLevel 1
    Abort "Could not update the user PATH."
  ${EndIf}
!endif
SectionEnd

Section "Uninstall"
!if "${KIND}" == "gui"
  Delete "$SMPROGRAMS\${PRODUCT}.lnk"
!else
  nsExec::ExecToLog '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\windows-path.ps1" Remove "$INSTDIR"'
  Pop $0
  ${If} $0 != 0
    SetErrorLevel 1
    Abort "Could not remove the installation directory from the user PATH."
  ${EndIf}
!endif
  RMDir /r "$INSTDIR"
  DeleteRegKey HKCU "${UNINSTKEY}"
SectionEnd

!if "${KIND}" == "gui"
Function DetectWebView2
  ; Explicit view avoids manually prefixing WOW6432Node from a 32-bit installer.
  SetRegView 32
  ReadRegStr $0 HKLM "SOFTWARE\${WV2KEY}" "pv"
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKCU "Software\${WV2KEY}" "pv"
  ${EndIf}
  ${If} $0 == "0.0.0.0"
    StrCpy $0 ""
  ${EndIf}
FunctionEnd
!endif
