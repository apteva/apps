"""Install the exact certificate/profile supplied by Deploy; never create an identity."""
import base64
import hashlib
import os
from pathlib import Path
import plistlib
import subprocess


def main():
    root = Path(os.environ['APTEVA_BUILD_DIR']) / 'apteva-signing'
    root.mkdir(mode=0o700, exist_ok=True)
    required = ('CERTIFICATE_PRIVATE_KEY', 'APTEVA_CERTIFICATE_PEM',
                'APTEVA_PROVISIONING_PROFILE_BASE64', 'APTEVA_APPLE_CERT_SHA256')
    for name in required:
        if not os.environ.get(name):
            raise ValueError('missing managed signing variable: ' + name)
    key = root / 'key.pem'
    certificate = root / 'certificate.pem'
    profile = root / 'profile.mobileprovision'
    p12 = root / 'certificate.p12'
    key.write_text(os.environ['CERTIFICATE_PRIVATE_KEY'])
    certificate.write_text(os.environ['APTEVA_CERTIFICATE_PEM'])
    profile.write_bytes(base64.b64decode(os.environ['APTEVA_PROVISIONING_PROFILE_BASE64'], validate=True))
    for path in (key, certificate, profile):
        path.chmod(0o600)
    der = subprocess.check_output(['openssl', 'x509', '-in', str(certificate), '-outform', 'DER'])
    expected = os.environ['APTEVA_APPLE_CERT_SHA256'].replace(':', '').lower()
    if hashlib.sha256(der).hexdigest() != expected:
        raise ValueError('managed Apple certificate fingerprint mismatch')
    contents = plistlib.loads(subprocess.check_output(['security', 'cms', '-D', '-i', str(profile)]))
    entitlements = contents.get('Entitlements', {})
    identifier = entitlements.get('application-identifier') or entitlements.get('com.apple.application-identifier', '')
    bundle = os.environ['APTEVA_BUNDLE_ID']
    if not identifier.endswith('.' + bundle) or der not in contents.get('DeveloperCertificates', []):
        raise ValueError('managed provisioning profile does not match the bundle and certificate')
    subprocess.run(['openssl', 'pkcs12', '-export', '-inkey', str(key), '-in', str(certificate),
                    '-out', str(p12), '-passout', 'pass:'], check=True)
    p12.chmod(0o600)
    subprocess.run(['keychain', 'add-certificates', '--certificate', str(p12)], check=True)


if __name__ == '__main__':
    main()
