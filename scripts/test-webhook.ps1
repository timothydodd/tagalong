<#
.SYNOPSIS
    Fire a test webhook at a running tagalong instance (Docker Hub or GitHub/GHCR).

.DESCRIPTION
    Builds a realistic webhook payload, computes the GitHub HMAC signature when a
    secret is given (using .NET — no openssl needed), and POSTs it. Webhooks are
    not behind the portal login, so no credentials are required.

    The response tells you what happened:
      202 deploying   - matched an app and enqueued a deploy
      200 skipped     - matched an app but the tag didn't qualify (pattern/live match)
      200 no app for  - (github) signature ok, but no app has that image_repo
      200 ignored     - (github) benign event (ping / non-container / digest-only)
      400 / 401 / 404 - repo mismatch, bad signature, or unknown token

.PARAMETER Type
    'dockerhub' or 'github' (default 'github').

.PARAMETER BaseUrl
    tagalong base URL. Default http://localhost:8080. Use your public URL (or the
    TAGALONG_HOOKS_LISTEN port) to test remotely.

.PARAMETER Token
    Docker Hub only: the app's webhook_token (from the app's Webhooks card).

.PARAMETER Secret
    GitHub only: the webhook secret from Settings. Leave empty to send unsigned
    (only works if no secret is configured in tagalong).

.PARAMETER Repo
    The image repo the payload advertises.
      dockerhub -> "owner/name"                 e.g. timdoddcool/robo-dash
      github    -> "registry/owner/path"        e.g. ghcr.io/you/api
    Must match a configured app's image_repo for a deploy to happen.

.PARAMETER Tag
    The pushed tag (default 'latest').

.PARAMETER Ping
    GitHub only: send a ping event instead of a package push (expects 200 ignored).

.EXAMPLE
    ./scripts/test-webhook.ps1 -Type dockerhub -Token abc123 -Repo timdoddcool/robo-dash -Tag 4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9

.EXAMPLE
    ./scripts/test-webhook.ps1 -Type github -Secret my-gh-hook-secret -Repo ghcr.io/you/api -Tag v1.2.3

.EXAMPLE
    ./scripts/test-webhook.ps1 -Type github -Secret my-gh-hook-secret -Ping
#>
[CmdletBinding()]
param(
    [ValidateSet('dockerhub', 'github')]
    [string]$Type = 'github',
    [string]$BaseUrl = 'http://localhost:8080',
    [string]$Token = '',
    [string]$Secret = '',
    [string]$Repo = '',
    [string]$Tag = 'latest',
    [switch]$Ping
)

$ErrorActionPreference = 'Stop'

function Get-HmacSha256Hex([string]$secret, [byte[]]$bytes) {
    $hmac = [System.Security.Cryptography.HMACSHA256]::new([System.Text.Encoding]::UTF8.GetBytes($secret))
    try {
        return (($hmac.ComputeHash($bytes) | ForEach-Object { $_.ToString('x2') }) -join '')
    }
    finally {
        $hmac.Dispose()
    }
}

# --- Build URL, body, and headers per source -------------------------------

$BaseUrl = $BaseUrl.TrimEnd('/')
$headers = @{}

if ($Type -eq 'dockerhub') {
    if (-not $Token) { throw "Docker Hub needs -Token (the app's webhook_token)." }
    if (-not $Repo) { throw "Docker Hub needs -Repo, e.g. -Repo timdoddcool/robo-dash." }

    $url = "$BaseUrl/hooks/dockerhub/$Token"
    $body = @{
        push_data  = @{ tag = $Tag }
        repository = @{ repo_name = $Repo }
    } | ConvertTo-Json -Compress -Depth 5
}
else {
    $url = "$BaseUrl/hooks/github"
    if ($Ping) {
        $body = '{"zen":"Keep it simple.","hook_id":1}'
    }
    else {
        if (-not $Repo) { throw "GitHub needs -Repo, e.g. -Repo ghcr.io/you/api." }
        $body = @{
            action           = 'published'
            registry_package = @{
                package_type    = 'container'
                package_version = @{
                    package_url        = "${Repo}:$Tag"
                    container_metadata = @{ tag = @{ name = $Tag } }
                }
            }
        } | ConvertTo-Json -Compress -Depth 8
    }

    # Sign the EXACT bytes we send, so the HMAC matches on the server side.
    $bodyBytes = [System.Text.Encoding]::UTF8.GetBytes($body)
    if ($Secret) {
        $sig = Get-HmacSha256Hex $Secret $bodyBytes
        $headers['X-Hub-Signature-256'] = "sha256=$sig"
    }
}

$bodyBytes = [System.Text.Encoding]::UTF8.GetBytes($body)

# --- Show what we're sending -----------------------------------------------

Write-Host "POST $url" -ForegroundColor Cyan
foreach ($k in $headers.Keys) { Write-Host "  $k`: $($headers[$k])" -ForegroundColor DarkGray }
Write-Host "  body: $body" -ForegroundColor DarkGray
Write-Host ""

# --- Send (works on Windows PowerShell 5.1 and PowerShell 7+) ---------------

$req = @{
    Uri             = $url
    Method          = 'Post'
    Body            = $bodyBytes
    ContentType     = 'application/json'
    Headers         = $headers
    UseBasicParsing = $true
}

$code = 0
$content = ''
if ($PSVersionTable.PSVersion.Major -ge 6) {
    $resp = Invoke-WebRequest @req -SkipHttpErrorCheck
    $code = [int]$resp.StatusCode
    $content = $resp.Content
}
else {
    try {
        $resp = Invoke-WebRequest @req
        $code = [int]$resp.StatusCode
        $content = $resp.Content
    }
    catch {
        $r = $_.Exception.Response
        if (-not $r) { throw }
        $code = [int]$r.StatusCode
        $reader = New-Object System.IO.StreamReader($r.GetResponseStream())
        $content = $reader.ReadToEnd()
        $reader.Close()
    }
}

# --- Report -----------------------------------------------------------------

$color = if ($code -lt 300) { 'Green' } elseif ($code -lt 500) { 'Yellow' } else { 'Red' }
Write-Host "HTTP $code" -ForegroundColor $color
if ($content) { Write-Host $content }
