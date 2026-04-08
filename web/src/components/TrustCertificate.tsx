import { useState, useEffect } from 'react'

type Platform = 'ios' | 'macos' | 'android' | 'windows' | 'linux' | 'unknown'

function detectPlatform(): Platform {
  const ua = navigator.userAgent
  if (/iPad|iPhone|iPod/.test(ua)) return 'ios'
  if (/Macintosh|Mac OS X/.test(ua)) return 'macos'
  if (/Android/.test(ua)) return 'android'
  if (/Windows/.test(ua)) return 'windows'
  if (/Linux/.test(ua)) return 'linux'
  return 'unknown'
}

const platformLabels: Record<Platform, string> = {
  ios: 'iOS',
  macos: 'macOS',
  android: 'Android',
  windows: 'Windows',
  linux: 'Linux',
  unknown: 'Other',
}

function PlatformInstructions({ platform }: { platform: Platform }) {
  switch (platform) {
    case 'ios':
      return (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Tap the button below to download the configuration profile, then follow these steps:
          </p>
          <a
            href="/api/tls/ca.mobileconfig"
            className="block w-full px-3 py-2 bg-accent text-accent-foreground rounded font-medium text-center hover:opacity-90 transition-opacity"
          >
            Install Profile
          </a>
          <ol className="text-sm text-muted-foreground space-y-2 list-decimal list-inside">
            <li>Tap "Allow" when prompted to download the profile</li>
            <li>Open <strong>Settings</strong> &rarr; <strong>General</strong> &rarr; <strong>VPN & Device Management</strong></li>
            <li>Tap the <strong>guppi CA Trust</strong> profile and tap <strong>Install</strong></li>
            <li>Go to <strong>Settings</strong> &rarr; <strong>General</strong> &rarr; <strong>About</strong> &rarr; <strong>Certificate Trust Settings</strong></li>
            <li>Enable full trust for the <strong>guppi CA</strong> certificate</li>
          </ol>
        </div>
      )
    case 'macos':
      return (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Download the certificate and add it to your Keychain:
          </p>
          <div className="flex gap-2">
            <a
              href="/api/tls/ca.crt"
              className="flex-1 px-3 py-2 bg-accent text-accent-foreground rounded font-medium text-center hover:opacity-90 transition-opacity"
            >
              Download CA Certificate
            </a>
            <a
              href="/api/tls/ca.mobileconfig"
              className="flex-1 px-3 py-2 bg-card border border-border text-foreground rounded font-medium text-center hover:opacity-90 transition-opacity"
            >
              Install Profile
            </a>
          </div>
          <div className="text-sm text-muted-foreground">
            <p className="font-medium text-foreground mb-1">Option A: Configuration Profile (easiest)</p>
            <ol className="space-y-1 list-decimal list-inside mb-3">
              <li>Click "Install Profile" above</li>
              <li>Open <strong>System Settings</strong> &rarr; <strong>Privacy & Security</strong> &rarr; <strong>Profiles</strong></li>
              <li>Click the <strong>guppi CA Trust</strong> profile and click <strong>Install</strong></li>
            </ol>
            <p className="font-medium text-foreground mb-1">Option B: Manual</p>
            <ol className="space-y-1 list-decimal list-inside">
              <li>Click "Download CA Certificate" above</li>
              <li>Double-click the downloaded <code className="text-xs bg-card px-1 rounded">guppi-ca.crt</code> file</li>
              <li>Keychain Access will open — add it to the <strong>login</strong> keychain</li>
              <li>Double-click the certificate, expand <strong>Trust</strong>, set to <strong>Always Trust</strong></li>
            </ol>
          </div>
        </div>
      )
    case 'android':
      return (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Download the certificate and install it:
          </p>
          <a
            href="/api/tls/ca.crt"
            className="block w-full px-3 py-2 bg-accent text-accent-foreground rounded font-medium text-center hover:opacity-90 transition-opacity"
          >
            Download CA Certificate
          </a>
          <ol className="text-sm text-muted-foreground space-y-2 list-decimal list-inside">
            <li>Tap the button above to download the certificate</li>
            <li>Open <strong>Settings</strong> &rarr; <strong>Security</strong> &rarr; <strong>Encryption & credentials</strong></li>
            <li>Tap <strong>Install a certificate</strong> &rarr; <strong>CA certificate</strong></li>
            <li>Select the downloaded <code className="text-xs bg-card px-1 rounded">guppi-ca.crt</code> file</li>
          </ol>
          <p className="text-xs text-muted-foreground">
            Note: Menu paths may vary by device manufacturer.
          </p>
        </div>
      )
    case 'windows':
      return (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Download the certificate and install it to the Windows certificate store:
          </p>
          <a
            href="/api/tls/ca.crt"
            className="block w-full px-3 py-2 bg-accent text-accent-foreground rounded font-medium text-center hover:opacity-90 transition-opacity"
          >
            Download CA Certificate
          </a>
          <ol className="text-sm text-muted-foreground space-y-2 list-decimal list-inside">
            <li>Double-click the downloaded <code className="text-xs bg-card px-1 rounded">guppi-ca.crt</code> file</li>
            <li>Click <strong>Install Certificate</strong></li>
            <li>Select <strong>Current User</strong> and click Next</li>
            <li>Choose <strong>Place all certificates in the following store</strong></li>
            <li>Click Browse and select <strong>Trusted Root Certification Authorities</strong></li>
            <li>Click Next, then Finish</li>
          </ol>
        </div>
      )
    case 'linux':
      return (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Download the certificate and add it to your system trust store:
          </p>
          <a
            href="/api/tls/ca.crt"
            className="block w-full px-3 py-2 bg-accent text-accent-foreground rounded font-medium text-center hover:opacity-90 transition-opacity"
          >
            Download CA Certificate
          </a>
          <div className="text-sm text-muted-foreground">
            <p className="font-medium text-foreground mb-1">Debian / Ubuntu:</p>
            <pre className="bg-card border border-border rounded p-2 text-xs overflow-x-auto mb-3">
{`sudo cp guppi-ca.crt /usr/local/share/ca-certificates/
sudo update-ca-certificates`}
            </pre>
            <p className="font-medium text-foreground mb-1">Fedora / RHEL:</p>
            <pre className="bg-card border border-border rounded p-2 text-xs overflow-x-auto mb-3">
{`sudo cp guppi-ca.crt /etc/pki/ca-trust/source/anchors/
sudo update-ca-trust`}
            </pre>
            <p className="font-medium text-foreground mb-1">Arch:</p>
            <pre className="bg-card border border-border rounded p-2 text-xs overflow-x-auto">
{`sudo trust anchor guppi-ca.crt`}
            </pre>
          </div>
          <p className="text-xs text-muted-foreground">
            Note: Browsers like Chrome and Firefox may need to be restarted after installing.
            Firefox uses its own certificate store — import via Preferences &rarr; Certificates.
          </p>
        </div>
      )
    default:
      return (
        <div className="space-y-3">
          <a
            href="/api/tls/ca.crt"
            className="block w-full px-3 py-2 bg-accent text-accent-foreground rounded font-medium text-center hover:opacity-90 transition-opacity"
          >
            Download CA Certificate
          </a>
          <p className="text-sm text-muted-foreground">
            Download the certificate and add it to your operating system or browser's trusted certificate store.
          </p>
        </div>
      )
  }
}

export function TrustCertificate({ onBack }: { onBack: () => void }) {
  const [caAvailable, setCaAvailable] = useState<boolean | null>(null)
  const [detectedPlatform] = useState<Platform>(detectPlatform)
  const [selectedPlatform, setSelectedPlatform] = useState<Platform>(detectedPlatform)

  useEffect(() => {
    fetch('/api/tls/status')
      .then(r => r.json())
      .then(data => setCaAvailable(data.ca_available ?? false))
      .catch(() => setCaAvailable(false))
  }, [])

  if (caAvailable === null) {
    return <div className="flex items-center justify-center h-full w-full bg-background" />
  }

  if (!caAvailable) {
    return (
      <div className="flex items-center justify-center h-full w-full bg-background">
        <div className="w-full max-w-md p-8">
          <div className="text-center mb-6">
            <h1 className="text-2xl font-bold text-foreground tracking-tight">guppi</h1>
            <p className="text-sm text-muted-foreground mt-1">certificate trust</p>
          </div>
          <p className="text-sm text-muted-foreground text-center mb-4">
            No CA certificate is available. This server may be using an external certificate that is already trusted, or TLS is disabled.
          </p>
          <button onClick={onBack} className="w-full px-3 py-2 bg-card border border-border text-foreground rounded font-medium hover:opacity-90 transition-opacity">
            Back to login
          </button>
        </div>
      </div>
    )
  }

  const platforms: Platform[] = ['ios', 'macos', 'android', 'windows', 'linux']

  return (
    <div className="flex items-center justify-center min-h-full w-full bg-background py-8">
      <div className="w-full max-w-md px-8">
        <div className="text-center mb-6">
          <h1 className="text-2xl font-bold text-foreground tracking-tight">guppi</h1>
          <p className="text-sm text-muted-foreground mt-1">trust certificate</p>
        </div>

        <p className="text-sm text-muted-foreground mb-4">
          Guppi uses a self-signed CA certificate for HTTPS. Trust this certificate on your device to avoid browser warnings and enable PWA installation.
        </p>

        {/* Platform selector */}
        <div className="flex flex-wrap gap-1 mb-4">
          {platforms.map(p => (
            <button
              key={p}
              onClick={() => setSelectedPlatform(p)}
              className={`px-2 py-1 text-xs rounded transition-colors ${
                selectedPlatform === p
                  ? 'bg-accent text-accent-foreground'
                  : 'bg-card border border-border text-muted-foreground hover:text-foreground'
              }`}
            >
              {platformLabels[p]}
              {p === detectedPlatform && ' *'}
            </button>
          ))}
        </div>

        {/* Instructions for selected platform */}
        <div className="bg-card border border-border rounded-lg p-4 mb-4">
          <PlatformInstructions platform={selectedPlatform} />
        </div>

        <button
          onClick={onBack}
          className="w-full px-3 py-2 bg-card border border-border text-foreground rounded font-medium hover:opacity-90 transition-opacity"
        >
          Back to login
        </button>
      </div>
    </div>
  )
}
