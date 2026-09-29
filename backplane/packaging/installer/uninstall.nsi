; "Uninstall Backplane.exe" in the release zip. It finds the installed copy
; and opens its uninstaller, so removing Backplane works from the same folder
; the installer came in. If the program folder was deleted by hand, it offers
; to clean up the leftover shortcuts, background check and registration.
;
;   makensis -DVERSION=1.1.0 -DSTAGE=<stage dir> -DOUTFILE=<exe> uninstall.nsi

Target amd64-unicode
ManifestDPIAware true
RequestExecutionLevel user
SetCompressor /SOLID lzma
SilentInstall silent

!ifndef VERSION
  !define VERSION "1.1.0"
!endif
!ifndef STAGE
  !error "Pass -DSTAGE=<folder with the staged files>"
!endif
!ifndef OUTFILE
  !define OUTFILE "Uninstall Backplane.exe"
!endif

!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Backplane"
!define AUMID      "SafiSolutions.Backplane"
!define TASK       "Backplane Monitor"

Name "Uninstall Backplane"
OutFile "${OUTFILE}"
Icon "${STAGE}\Backplane.ico"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "Backplane"
VIAddVersionKey "CompanyName" "Safi Solutions"
VIAddVersionKey "FileDescription" "Uninstall Backplane"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "© 2026 Safi Solutions"

!include "LogicLib.nsh"

Function .onInit
  ReadRegStr $0 HKCU "${UNINST_KEY}" "InstallLocation"
  ReadRegStr $1 HKCU "${UNINST_KEY}" "UninstallString"
  ${If} $1 == ""
    MessageBox MB_ICONINFORMATION|MB_OK "Backplane isn't installed for this Windows account, so there's nothing to remove."
    Quit
  ${EndIf}
  ${If} ${FileExists} "$0\Uninstall Backplane.exe"
    Exec '"$0\Uninstall Backplane.exe"'
    Quit
  ${EndIf}
  ${If} ${Cmd} `MessageBox MB_ICONQUESTION|MB_YESNO "Backplane's program folder is gone, so its uninstaller can't run.$\r$\n$\r$\nRemove the leftover shortcuts, background check and Apps list entry? Your Backplane data is kept." IDYES`
    nsExec::Exec 'taskkill /F /IM Backplane.exe'
    Pop $2
    nsExec::Exec 'schtasks /Delete /TN "${TASK}" /F'
    Pop $2
    DeleteRegKey HKCU "Software\Classes\AppUserModelId\${AUMID}"
    Delete "$SMPROGRAMS\Backplane.lnk"
    Delete "$DESKTOP\Backplane.lnk"
    DeleteRegKey HKCU "${UNINST_KEY}"
    MessageBox MB_ICONINFORMATION|MB_OK "Done. Backplane has been removed from this PC."
  ${EndIf}
  Quit
FunctionEnd

Section
SectionEnd
