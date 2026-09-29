; Backplane setup — installs for the current Windows account only, so no
; administrator password is needed. Built by packaging/build-windows.sh:
;
;   makensis -DVERSION=1.1.0 -DSTAGE=<stage dir> -DOUTFILE=<exe> installer.nsi
;
; STAGE holds Backplane.exe, Backplane.ico, Backplane.png, welcome.bmp and
; header.bmp. The uninstaller written here removes the program, its
; shortcuts, the hourly background check and the notification registration;
; the user's data is removed only if they tick the box.

Target amd64-unicode
ManifestDPIAware true
RequestExecutionLevel user
SetCompressor /SOLID lzma

!ifndef VERSION
  !define VERSION "1.1.0"
!endif
!ifndef STAGE
  !error "Pass -DSTAGE=<folder with the staged files>"
!endif
!ifndef OUTFILE
  !define OUTFILE "Install Backplane.exe"
!endif

!define APP        "Backplane"
!define PUBLISHER  "Safi Solutions"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Backplane"
!define AUMID      "SafiSolutions.Backplane"
!define TASK       "Backplane Monitor"
!define UNINSTALLER "Uninstall Backplane.exe"
; Microsoft Edge WebView2 Runtime (Evergreen) client GUID and bootstrapper.
!define WV2_GUID   "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!define WV2_URL    "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

Name "${APP}"
Caption "${APP} ${VERSION} Setup"
UninstallCaption "Uninstall ${APP}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\Backplane"
InstallDirRegKey HKCU "${UNINST_KEY}" "InstallLocation"
BrandingText "${PUBLISHER}"
ShowInstDetails nevershow
ShowUninstDetails nevershow

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "CompanyName" "${PUBLISHER}"
VIAddVersionKey "FileDescription" "${APP} Setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "© 2026 ${PUBLISHER}"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "WinVer.nsh"

; ---------- look ----------
!define MUI_ICON "${STAGE}\Backplane.ico"
!define MUI_UNICON "${STAGE}\Backplane.ico"
!define MUI_WELCOMEFINISHPAGE_BITMAP "${STAGE}\welcome.bmp"
!define MUI_UNWELCOMEFINISHPAGE_BITMAP "${STAGE}\welcome.bmp"
!define MUI_HEADERIMAGE
!define MUI_HEADERIMAGE_RIGHT
!define MUI_HEADERIMAGE_BITMAP "${STAGE}\header.bmp"
!define MUI_HEADERIMAGE_UNBITMAP "${STAGE}\header.bmp"
!define MUI_ABORTWARNING
!define MUI_UNABORTWARNING

; ---------- install pages ----------
!define MUI_WELCOMEPAGE_TITLE "Set up Backplane ${VERSION}"
!define MUI_WELCOMEPAGE_TEXT "Backplane sets up and watches your business backend (payments, database, email and hosting) from one place.$\r$\n$\r$\nIt installs for your Windows account only, so no administrator password is needed, and it never changes anything in your accounts until you approve a plan.$\r$\n$\r$\nClick Next to continue."
!insertmacro MUI_PAGE_WELCOME

!define MUI_DIRECTORYPAGE_TEXT_TOP "Backplane will be installed in this folder. The default is right for almost everyone."
!insertmacro MUI_PAGE_DIRECTORY

!insertmacro MUI_PAGE_INSTFILES

!define MUI_FINISHPAGE_TITLE "Backplane is ready"
!define MUI_FINISHPAGE_TEXT "New here? Start on the Bench. It builds a practice backend with pretend accounts, so you can watch every step work (and repair things you break on purpose) before connecting your real accounts."
!define MUI_FINISHPAGE_RUN "$INSTDIR\Backplane.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Open Backplane now"
!define MUI_FINISHPAGE_SHOWREADME ""
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Add a desktop shortcut"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION DesktopShortcut
!define MUI_FINISHPAGE_LINK "safisolutions.org"
!define MUI_FINISHPAGE_LINK_LOCATION "https://www.safisolutions.org"
!insertmacro MUI_PAGE_FINISH

; ---------- uninstall pages ----------
!define MUI_PAGE_HEADER_TEXT "Uninstall Backplane"
!define MUI_PAGE_HEADER_SUBTEXT "Remove Backplane from this PC."
!define MUI_UNCONFIRMPAGE_TEXT_TOP "Backplane will be removed from this PC. What it built in your Cloudflare, Supabase, Stripe, Resend or GitHub accounts keeps running; to remove that too, use Settings > Tear down in Backplane first."
!insertmacro MUI_UNPAGE_CONFIRM

!define MUI_PAGE_HEADER_TEXT "Your Backplane data"
!define MUI_PAGE_HEADER_SUBTEXT "Choose whether to keep it for a later reinstall."
!define MUI_COMPONENTSPAGE_TEXT_TOP "Leave the second box unticked to keep your backends, history and saved account connections for a reinstall. Exports in Downloads\Backplane exports are never removed."
!define MUI_COMPONENTSPAGE_NODESC
!define MUI_PAGE_CUSTOMFUNCTION_SHOW un.HideSpaceText
!insertmacro MUI_UNPAGE_COMPONENTS

!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; ---------- shared ----------
; Close a running Backplane (politely first) so its files can be replaced.
!macro STOP_BACKPLANE
  nsExec::Exec 'taskkill /IM Backplane.exe'
  Pop $0
  ${If} $0 == 0
    Sleep 2000
  ${EndIf}
  nsExec::Exec 'taskkill /F /IM Backplane.exe'
  Pop $0
!macroend

; Sets $0 to 1 when the WebView2 Runtime is installed (per machine or per user).
!macro HAS_WEBVIEW2
  StrCpy $0 0
  ReadRegStr $1 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WV2_GUID}" "pv"
  ${If} $1 == ""
    ReadRegStr $1 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\${WV2_GUID}" "pv"
  ${EndIf}
  ${If} $1 == ""
    ReadRegStr $1 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WV2_GUID}" "pv"
  ${EndIf}
  ${If} $1 != ""
  ${AndIf} $1 != "0.0.0.0"
    StrCpy $0 1
  ${EndIf}
!macroend

Function .onInit
  ; One setup at a time.
  System::Call 'kernel32::CreateMutex(p 0, i 0, t "SafiSolutions.Backplane.Setup") p .r1 ?e'
  Pop $0
  ${If} $0 == 183
    MessageBox MB_ICONINFORMATION|MB_OK "Backplane Setup is already running."
    Abort
  ${EndIf}
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_ICONSTOP|MB_OK "Backplane needs Windows 10 or Windows 11."
    Abort
  ${EndIf}
FunctionEnd

Section "Backplane" SecApp
  SectionIn RO
  SetOutPath "$INSTDIR"
  !insertmacro STOP_BACKPLANE

  File "${STAGE}\Backplane.exe"
  File "${STAGE}\Backplane.ico"
  File "${STAGE}\Backplane.png"
  WriteUninstaller "$INSTDIR\${UNINSTALLER}"

  CreateShortcut "$SMPROGRAMS\Backplane.lnk" "$INSTDIR\Backplane.exe" "" "$INSTDIR\Backplane.exe" 0 SW_SHOWNORMAL "" "Set up and monitor your business backend"
  ; Refresh the desktop shortcut if an earlier install added one.
  ${If} ${FileExists} "$DESKTOP\Backplane.lnk"
    CreateShortcut "$DESKTOP\Backplane.lnk" "$INSTDIR\Backplane.exe" "" "$INSTDIR\Backplane.exe" 0 SW_SHOWNORMAL "" "Set up and monitor your business backend"
  ${EndIf}

  ; Apps & features entry (per user).
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\Backplane.exe,0"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\${UNINSTALLER}"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\${UNINSTALLER}" /S'
  WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "https://www.safisolutions.org"
  WriteRegStr HKCU "${UNINST_KEY}" "HelpLink" "https://www.safisolutions.org/support.html"
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" "$0"

  ; Backplane draws its window with Microsoft Edge WebView2. Windows 11 and
  ; most Windows 10 PCs already have it; without it Backplane opens in the
  ; default browser instead, so this is an offer, not a requirement.
  !insertmacro HAS_WEBVIEW2
  ${If} $0 == 0
  ${AndIfNot} ${Silent}
    ${If} ${Cmd} `MessageBox MB_ICONINFORMATION|MB_YESNO "Backplane draws its window with Microsoft Edge WebView2, which isn't on this PC yet.$\r$\n$\r$\nDownload it from Microsoft now? (It's free and takes a minute. Until it's installed, Backplane opens in your web browser instead.)" IDYES`
      ExecShell "open" "${WV2_URL}"
    ${EndIf}
  ${EndIf}
SectionEnd

Function DesktopShortcut
  CreateShortcut "$DESKTOP\Backplane.lnk" "$INSTDIR\Backplane.exe" "" "$INSTDIR\Backplane.exe" 0 SW_SHOWNORMAL "" "Set up and monitor your business backend"
FunctionEnd

; ---------- uninstall ----------
Section "un.Backplane (the program)" SecUnApp
  SectionIn RO
  !insertmacro STOP_BACKPLANE

  ; Hourly background check and the notification registration.
  nsExec::Exec 'schtasks /Delete /TN "${TASK}" /F'
  Pop $0
  DeleteRegKey HKCU "Software\Classes\AppUserModelId\${AUMID}"

  Delete "$SMPROGRAMS\Backplane.lnk"
  Delete "$DESKTOP\Backplane.lnk"

  ; Only Backplane's own files: the folder may have been changed at install.
  Delete "$INSTDIR\Backplane.exe"
  Delete "$INSTDIR\Backplane.ico"
  Delete "$INSTDIR\Backplane.png"
  Delete "$INSTDIR\${UNINSTALLER}"
  RMDir "$INSTDIR"

  ; The window's browser cache (not user data).
  RMDir /r "$LOCALAPPDATA\Backplane\WebView2"
  RMDir "$LOCALAPPDATA\Backplane"

  DeleteRegKey HKCU "${UNINST_KEY}"
SectionEnd

Section /o "un.Also delete my Backplane data" SecUnData
  RMDir /r "$APPDATA\Backplane"
SectionEnd

; "Space required: 0.0 KB" means nothing when removing a program.
Function un.HideSpaceText
  ShowWindow $mui.ComponentsPage.SpaceRequired ${SW_HIDE}
FunctionEnd
