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

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APPEXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APPNAME} now"
!define MUI_FINISHPAGE_SHOWREADME ""
!define MUI_FINISHPAGE_SHOWREADME_NOTCHECKED
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Create a desktop shortcut"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION DesktopShortcut

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "..\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

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
  ; close a running copy so the exe can be replaced
  nsExec::Exec 'taskkill /F /IM ${APPEXE}'
  Sleep 500

  SetOutPath "$INSTDIR"
  File "${SRCDIR}\${APPEXE}"
  File "/oname=LICENSE.txt" "..\LICENSE"
  File "/oname=README.md" "..\README.md"

  WriteUninstaller "$INSTDIR\Uninstall.exe"

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

  Call CheckNpcap
SectionEnd

Section "Uninstall"
  nsExec::Exec 'taskkill /F /IM ${APPEXE}'
  Sleep 500
  Delete "$INSTDIR\${APPEXE}"
  Delete "$INSTDIR\LICENSE.txt"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\Uninstall.exe"
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
