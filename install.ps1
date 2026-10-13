# Joins this Windows PC to a Handloom hub, with one pasted line. Handloom's link runs on Linux, and
# Windows has Linux built in (WSL2), so this script gets WSL ready, runs the normal Linux installer
# INSIDE it (install.sh, which checks its download against the release's checksums and Sigstore
# signature), and then makes the link start by itself every time you sign in to Windows.
#
#   & ([scriptblock]::Create((irm https://raw.githubusercontent.com/afiffattouh/handloom/main/install.ps1))) -Hub https://your-hub -Token hvj_...
#
# The web page Machines, Add a machine, shows that line ready to paste, for your hub.
# Works in Windows PowerShell 5.1 and PowerShell 7. You do not need to be an administrator, except for
# the one-time `wsl --install` on a PC that has no WSL yet (Windows asks you, and then needs a restart).
#
# Parameters:
#   -Hub       the hub's address, for example https://handloom.example.com
#   -Token     the one-time join token from the web page (starts with hvj_)
#   -Distro    the Linux to use inside WSL (default: your Ubuntu; one is installed if you have none)
#   -SkipTask  do not create the sign-in task (the link then stops when WSL stops)
#   -Help      show this text
#
# It never downloads and runs anything without the checks above, and it does not run text it did not
# write: the only thing run inside Linux is the install.sh line below.
# To undo it: Unregister-ScheduledTask -TaskName 'Handloom link' -Confirm:$false, then in Linux: handloom uninstall --purge
[CmdletBinding()]
param(
    [string]$Hub,
    [string]$Token,
    [string]$Distro,
    [switch]$SkipTask,
    [switch]$Help
)

$InstallShUrl = 'https://raw.githubusercontent.com/afiffattouh/handloom/main/install.sh'
$TaskName = 'Handloom link'

function Say([string]$text) { Write-Host $text }
function Step([string]$text) { Write-Host ''; Write-Host ('== ' + $text) -ForegroundColor Cyan }
function Fail([string]$text) { throw $text }

# Reads what wsl.exe prints: it writes UTF-16, which PowerShell would show with gaps between letters.
function Invoke-Wsl {
    param([string[]]$WslArgs)
    $old = [Console]::OutputEncoding
    try {
        [Console]::OutputEncoding = [System.Text.Encoding]::Unicode
        $lines = & wsl.exe @WslArgs 2>&1
        $code = $LASTEXITCODE
    } finally {
        [Console]::OutputEncoding = $old
    }
    $text = @($lines | ForEach-Object { ([string]$_) -replace "`0", '' -replace "`r", '' } | Where-Object { $_ -ne '' })
    return [pscustomobject]@{ Code = $code; Lines = $text }
}

function Test-WindowsVersion {
    $build = [int](Get-CimInstance Win32_OperatingSystem).BuildNumber
    if ($build -lt 19041) {
        Fail ("This version of Windows (build $build) is too old for WSL2. You need Windows 10 version 2004 (build 19041) or newer, or Windows 11. Update Windows, then run this line again.")
    }
    Say ("Windows build $build: good.")
}

function Test-WslPresent {
    if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) { return $false }
    $r = Invoke-Wsl -WslArgs @('--status')
    return ($r.Code -eq 0)
}

# Linux distributions installed in WSL, without the ones Docker Desktop keeps for itself.
function Get-Distros {
    $r = Invoke-Wsl -WslArgs @('--list', '--quiet')
    if ($r.Code -ne 0) { return @() }
    return @($r.Lines | Where-Object { $_ -notlike 'docker-desktop*' })
}

function Get-YesNo([string]$question) {
    $answer = Read-Host ($question + ' [y/N]')
    return ($answer -match '^(y|yes)$')
}

function Install-Handloom {
    if ($Help -or -not $Hub -or -not $Token) {
        Say 'Joins this Windows PC to a Handloom hub.'
        Say ''
        Say 'Paste the line the hub shows (web page: Machines, Add a machine), or run:'
        Say ('  & ([scriptblock]::Create((irm ' + $InstallShUrl.Replace('install.sh', 'install.ps1') + '))) -Hub <hub address> -Token <token>')
        Say ''
        Say 'It sets up Linux inside Windows (WSL2) if needed, installs and joins there, and starts the link at every sign-in.'
        return
    }

    # Only plain addresses and tokens are accepted, because they are put into a Linux command line.
    if ($Hub -notmatch '^https?://[A-Za-z0-9._:\-]+(/[A-Za-z0-9._~\-/]*)?$') { Fail 'The hub address does not look right. It should be like https://handloom.example.com' }
    if ($Token -notmatch '^hvj_[A-Za-z0-9_\-]+$') { Fail 'The token does not look right. It should start with hvj_ (a join token from the web page Machines, Add a machine).' }
    if ($Distro -and $Distro -notmatch '^[A-Za-z0-9._\-]+$') { Fail 'The -Distro name may only have letters, digits, dots, dashes and underscores.' }
    $Hub = $Hub.TrimEnd('/')

    Step 'Checking Windows'
    Test-WindowsVersion

    Step 'Checking WSL (Linux inside Windows)'
    if (-not (Test-WslPresent)) {
        Say 'WSL is not set up on this PC yet. Handloom runs its agents in Linux, and WSL is how Windows provides it.'
        Say 'Setting it up is a one-time step: it needs administrator permission (Windows will ask you) and then a restart.'
        if (-not (Get-YesNo 'Run "wsl --install" now?')) {
            Say 'OK, nothing was changed. When you are ready, run PowerShell as administrator, type: wsl --install   restart, then run this same line again.'
            return
        }
        try {
            Start-Process -FilePath wsl.exe -ArgumentList '--install' -Verb RunAs -Wait
        } catch {
            Fail 'Windows did not let me start "wsl --install" (the permission question was declined?). Run PowerShell as administrator and type: wsl --install'
        }
        Say ''
        Say 'WSL is being installed. Restart the PC when Windows asks (or now), then run this SAME line again: it carries on from here.'
        Say 'The join token only works for a short time. If it has expired, make a new one in the web page (Machines, Add a machine).'
        return
    }
    Say 'WSL is there.'

    $distros = Get-Distros
    if ($Distro) {
        if ($distros -notcontains $Distro) { Fail ("There is no WSL distribution called $Distro. You have: " + ($distros -join ', ')) }
    } else {
        $Distro = ($distros | Where-Object { $_ -like 'Ubuntu*' } | Select-Object -First 1)
        if (-not $Distro) { $Distro = ($distros | Select-Object -First 1) }
    }
    if (-not $Distro) {
        Say 'WSL has no Linux in it yet. I will install Ubuntu (free, from Microsoft). It opens a window that asks you to choose a Linux user name and password.'
        if (-not (Get-YesNo 'Install Ubuntu now?')) { Say 'OK, nothing was changed.'; return }
        & wsl.exe --install -d Ubuntu
        Say ''
        Say 'When the Ubuntu window has asked for your user name and password and shows a prompt, close it and run this SAME line again.'
        Say 'If the join token has expired by then, make a new one in the web page (Machines, Add a machine).'
        return
    }
    Say ("Using the Linux called $Distro.")

    Step "Installing Handloom inside $Distro and joining the hub"
    Say 'This runs the normal Linux installer there. It checks its download against the release checksums, and it asks before it installs git or tmux.'
    $inner = "if command -v curl >/dev/null 2>&1; then curl -fsSL $InstallShUrl; else wget -qO- $InstallShUrl; fi | sh -s -- join '$Hub' '$Token'"
    & wsl.exe -d $Distro --exec sh -c $inner
    $joinCode = $LASTEXITCODE

    # where did the program go?
    $find = 'for p in $HOME/.local/bin/handloom /usr/local/bin/handloom /usr/bin/handloom; do if [ -x $p ]; then echo $p; exit 0; fi; done; exit 1'
    $r = Invoke-Wsl -WslArgs @('-d', $Distro, '--exec', 'sh', '-c', $find)
    $bin = $r.Lines | Select-Object -First 1
    if ($r.Code -ne 0 -or -not $bin -or $bin -notmatch '^/[A-Za-z0-9._/\-]+$') {
        Fail ("Handloom did not end up installed inside $Distro, so I stopped. Read what the installer said above. If the token had expired, make a new one in the web page and run the line again.")
    }
    Say ("Handloom is installed in Linux at $bin.")

    if (-not $SkipTask) {
        Step 'Making the link start by itself when you sign in to Windows'
        # `link start --wait` starts the link in Linux if it is not running and stays until it stops, which also
        # keeps WSL awake: WSL shuts itself down when nothing is using it.
        $inside = "& wsl.exe -d '$Distro' --exec '$bin' link start --wait"
        $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument ('-NoProfile -WindowStyle Hidden -Command "' + $inside + '"')
        $trigger = New-ScheduledTaskTrigger -AtLogOn -User ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name)
        $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -Hidden
        try {
            Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Description 'Keeps the Handloom link running (in WSL) while you are signed in.' -Force | Out-Null
            Start-ScheduledTask -TaskName $TaskName
            Say "Done: the task '$TaskName' starts the link at every sign-in, and it is running now."
        } catch {
            Say ("I could not create the sign-in task (" + $_.Exception.Message + "). The link is running now, but it will not start by itself after a restart. Run this line again, or start it yourself: wsl -d $Distro --exec $bin link start --wait")
        }
    }

    Step 'Is the link up?'
    $up = $false
    for ($i = 0; $i -lt 10 -and -not $up; $i++) {
        $s = Invoke-Wsl -WslArgs @('-d', $Distro, '--exec', $bin, 'link', 'status')
        $up = ($s.Code -eq 0)
        if (-not $up) { Start-Sleep -Seconds 2 }
    }
    if ($up) { Say 'Yes: the link is running. The web page should show this machine as ready in a moment.' }
    else { Say ("Not yet. Look at the installer's messages above, then in Linux run: handloom doctor") }

    Step 'What is next'
    Say 'Handloom runs your agents in Linux, so the agent programs (Claude Code, Codex, ...) must be installed in the Linux side, not in Windows.'
    Say ("Open Linux with:  wsl -d $Distro")
    Say 'There, run:  handloom doctor'
    Say 'It says what is missing, one step at a time, with the exact line to type (for Claude Code that is the installer line it prints, then signing in).'
    Say ("To remove this later:  Unregister-ScheduledTask -TaskName '$TaskName' -Confirm:`$false   and in Linux:  handloom uninstall --purge")
    if ($joinCode -ne 0) { Say 'Note: the installer finished with things still to do (above). Do them, then run `handloom doctor` in Linux.' }
}

try {
    Install-Handloom
} catch {
    Write-Host ''
    Write-Host ('Stopped: ' + $_.Exception.Message) -ForegroundColor Red
}
