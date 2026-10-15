; AION 2 Pet Tracker installer (NSIS 3)
; Build: makensis -DVERSION=0.2.0 -DSRCDIR=../dist installer/pettracker.nsi

Unicode true
SetCompressor /SOLID lzma

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef SRCDIR
  !define SRCDIR "..\dist"
!endif
!ifndef OUTFILE
  !define OUTFILE "${SRCDIR}\PetTracker-Setup-${VERSION}.exe"
!endif

!define APPNAME   "Pet Tracker"
!define APPEXE    "PetTracker.exe"
!define COMPANY   "caiooncoding"
!define REPOURL   "https://github.com/caiooncoding/aion-pet-tracker"
!define NPCAPURL  "https://npcap.com/#download"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\AionPetTracker"

Name "${APPNAME}"
OutFile "${OUTFILE}"
Icon "icon.ico"
UninstallIcon "icon.ico"
InstallDir "$LOCALAPPDATA\Programs\PetTracker"
InstallDirRegKey HKCU "Software\AionPetTracker" "InstallDir"
RequestExecutionLevel user
BrandingText "${APPNAME} ${VERSION}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "FileDescription" "${APPNAME} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "CompanyName" "${COMPANY}"
VIAddVersionKey "LegalCopyright" "MIT License"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

; Update mode: when Pet Tracker is already installed, the installer skips the
; license and folder pages, installs over the old version in the same folder and
; keeps everything in %APPDATA%\PetTracker (pets, settings, window position).
; No uninstall needed between versions.
Var IsUpdate
Var OldVersion
Var WasRunning
Var WelcomeTitle
Var WelcomeText
Var FinishTitle
Var FinishText

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APPEXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APPNAME} now"
!define MUI_FINISHPAGE_SHOWREADME ""
!define MUI_FINISHPAGE_SHOWREADME_NOTCHECKED
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Create a desktop shortcut"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION DesktopShortcut
!define MUI_FINISHPAGE_TITLE "$FinishTitle"
!define MUI_FINISHPAGE_TEXT "$FinishText"
!define MUI_WELCOMEPAGE_TITLE "$WelcomeTitle"
!define MUI_WELCOMEPAGE_TEXT "$WelcomeText"

!define MUI_PAGE_CUSTOMFUNCTION_SHOW WelcomeShow
!insertmacro MUI_PAGE_WELCOME
!define MUI_PAGE_CUSTOMFUNCTION_PRE SkipOnUpdate
!insertmacro MUI_PAGE_LICENSE "..\LICENSE"
!define MUI_PAGE_CUSTOMFUNCTION_PRE SkipOnUpdate
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_PAGE_CUSTOMFUNCTION_SHOW FinishShow
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Function .onInit
  StrCpy $IsUpdate 0
  StrCpy $WasRunning 0
  ReadRegStr $0 HKCU "Software\AionPetTracker" "InstallDir"
  ${If} $0 != ""
  ${AndIf} ${FileExists} "$0\${APPEXE}"
    StrCpy $IsUpdate 1
    StrCpy $INSTDIR $0
    ReadRegStr $OldVersion HKCU "${UNINSTKEY}" "DisplayVersion"
  ${EndIf}

  ${If} $IsUpdate == 1
    ${If} $OldVersion == "${VERSION}"
      StrCpy $WelcomeTitle "Reinstall ${APPNAME} ${VERSION}"
      StrCpy $WelcomeText "${APPNAME} ${VERSION} is already installed. This puts a fresh copy over it.$\r$\n$\r$\nYour pets, settings and window position are kept.$\r$\n$\r$\nIf ${APPNAME} is running it will be closed and started again."
    ${Else}
      ${If} $OldVersion == ""
        StrCpy $OldVersion "your current version"
      ${EndIf}
      StrCpy $WelcomeTitle "Update ${APPNAME} to ${VERSION}"
      StrCpy $WelcomeText "This updates ${APPNAME} from $OldVersion to ${VERSION}. No need to uninstall first.$\r$\n$\r$\nYour pets, settings and window position are kept.$\r$\n$\r$\nIf ${APPNAME} is running it will be closed and started again."
    ${EndIf}
    StrCpy $FinishTitle "${APPNAME} is up to date"
    StrCpy $FinishText "${APPNAME} ${VERSION} is installed."
  ${Else}
    StrCpy $WelcomeTitle "Welcome to ${APPNAME} ${VERSION}"
    StrCpy $WelcomeText "An overlay for AION 2 that tracks your pet souls while you farm.$\r$\n$\r$\n${APPNAME} needs Npcap to read the game's network traffic. If it's missing, the installer will point you to the download.$\r$\n$\r$\nClick Next to continue."
    StrCpy $FinishTitle "${APPNAME} is installed"
    StrCpy $FinishText "Start it before you log into your character, so it catches your pet list."
  ${EndIf}
FunctionEnd

Function SkipOnUpdate
  ${If} $IsUpdate == 1
    Abort
  ${EndIf}
FunctionEnd

Function WelcomeShow
  ${If} $IsUpdate == 1
    GetDlgItem $0 $HWNDPARENT 1
    ${If} $OldVersion == "${VERSION}"
      SendMessage $0 ${WM_SETTEXT} 0 "STR:Reinstall"
    ${Else}
      SendMessage $0 ${WM_SETTEXT} 0 "STR:Update"
    ${EndIf}
  ${EndIf}
FunctionEnd

Function FinishShow
  ; the desktop shortcut was offered on the first install; don't ask again
  ${If} $IsUpdate == 1
    ShowWindow $mui.FinishPage.ShowReadme ${SW_HIDE}
  ${EndIf}
FunctionEnd

; Close a running copy so its files can be replaced. Asks it to close first (it
; saves its state), then makes sure: the app and its overlay window (an Edge/Chrome
; process using Pet Tracker's own profile folder) are both stopped.
!macro CLOSE_RUNNING
  nsExec::ExecToStack `powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "$$r=0; if (Get-Process PetTracker -ErrorAction SilentlyContinue) { $$r=1; try { Invoke-WebRequest -UseBasicParsing -Method Post -TimeoutSec 3 'http://127.0.0.1:7319/api/window?do=close' | Out-Null } catch {}; for ($$i=0; $$i -lt 20 -and (Get-Process PetTracker -ErrorAction SilentlyContinue); $$i++) { Start-Sleep -Milliseconds 250 } }; Get-Process PetTracker -ErrorAction SilentlyContinue | Stop-Process -Force; Get-CimInstance Win32_Process -Filter \"Name='msedge.exe' or Name='chrome.exe'\" | Where-Object { $$_.CommandLine -like '*PetTracker\window*' } | ForEach-Object { Stop-Process -Id $$_.ProcessId -Force -ErrorAction SilentlyContinue }; Write-Output $$r"`
  Pop $0 ; exit code
  Pop $1 ; output
  StrCpy $1 $1 1
  ${If} $1 == "1"
    StrCpy $WasRunning 1
  ${EndIf}
  ; fallback in case PowerShell isn't available
  nsExec::Exec 'taskkill /F /IM ${APPEXE}'
  Sleep 500
!macroend

Function CloseRunning
  !insertmacro CLOSE_RUNNING
FunctionEnd

Function un.CloseRunning
  !insertmacro CLOSE_RUNNING
FunctionEnd

Function .onInstSuccess
  ; silent update (from the app's Update button): start it again
  ${If} ${Silent}
  ${AndIf} $WasRunning == 1
    Exec '"$INSTDIR\${APPEXE}"'
  ${EndIf}
FunctionEnd

Function DesktopShortcut
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\${APPEXE}" "" "$INSTDIR\${APPEXE}" 0
FunctionEnd

; Npcap is what reads the game's network traffic. Its free license doesn't allow
; bundling it, so the installer checks for it and points to the official download.
Function CheckNpcap
  IfFileExists "$SYSDIR\Npcap\wpcap.dll" found
  ReadRegStr $0 HKLM "SOFTWARE\Npcap" ""
  StrCmp $0 "" 0 found
  SetRegView 64
  ReadRegStr $0 HKLM "SOFTWARE\Npcap" ""
  SetRegView lastused
  StrCmp $0 "" 0 found
    MessageBox MB_YESNO|MB_ICONEXCLAMATION "${APPNAME} needs Npcap to read the game's network traffic, and it isn't installed.$\r$\n$\r$\nOpen the Npcap download page now?$\r$\n(Install it with the default options, then start ${APPNAME}.)" IDNO done
    ExecShell "open" "${NPCAPURL}"
    Goto done
  found:
  done:
FunctionEnd

Section "Install"
  ${If} $IsUpdate == 1
    DetailPrint "Updating ${APPNAME} $OldVersion -> ${VERSION} in $INSTDIR"
  ${EndIf}
  Call CloseRunning

  SetOutPath "$INSTDIR"
  File "${SRCDIR}\${APPEXE}"
  File "/oname=LICENSE.txt" "..\LICENSE"
  File "/oname=README.md" "..\README.md"

  ; zone maps for the Map tab (replaced on every install)
  RMDir /r "$INSTDIR\maps"
  SetOutPath "$INSTDIR\maps"
  File /r "..\assets\maps\*.*"
  SetOutPath "$INSTDIR"

  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; keep an existing desktop shortcut pointing at the new exe
  ${If} ${FileExists} "$DESKTOP\${APPNAME}.lnk"
    Call DesktopShortcut
  ${EndIf}

  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\${APPEXE}" "" "$INSTDIR\${APPEXE}" 0
  CreateShortcut "$SMPROGRAMS\${APPNAME}\Uninstall ${APPNAME}.lnk" "$INSTDIR\Uninstall.exe"

  WriteRegStr HKCU "Software\AionPetTracker" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "${COMPANY}"
  WriteRegStr HKCU "${UNINSTKEY}" "URLInfoAbout" "${REPOURL}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\${APPEXE}"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKCU "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNINSTKEY}" "EstimatedSize" "$0"

  ${IfNot} ${Silent}
    Call CheckNpcap
  ${EndIf}
SectionEnd

Section "Uninstall"
  Call un.CloseRunning
  Delete "$INSTDIR\${APPEXE}"
  Delete "$INSTDIR\LICENSE.txt"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir /r "$INSTDIR\maps"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\Uninstall ${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"
  Delete "$DESKTOP\${APPNAME}.lnk"

  DeleteRegKey HKCU "${UNINSTKEY}"
  DeleteRegKey HKCU "Software\AionPetTracker"

  MessageBox MB_YESNO|MB_ICONQUESTION "Also delete your saved pet progress and settings?$\r$\n($APPDATA\PetTracker)" /SD IDNO IDNO keep
    RMDir /r "$APPDATA\PetTracker"
  keep:
SectionEnd
