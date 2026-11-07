"""Install the exact certificate/profile supplied by Deploy; never create an identity."""
import base64
import hashlib
import json
import os
from pathlib import Path
import plistlib
import secrets
import subprocess
import uuid


def export_keychain_archive(key, certificate, archive):
    # Apple's security importer requires a nonempty password and the PKCS#12
    # algorithms it supports, rather than OpenSSL 3's AES/SHA256 defaults.
    password = archive.with_suffix('.password')
    password.write_text(secrets.token_urlsafe(32))
    password.chmod(0o600)
    try:
        subprocess.run(['openssl', 'pkcs12', '-export', '-inkey', str(key), '-in', str(certificate),
                        '-out', str(archive), '-passout', 'file:' + str(password),
                        '-keypbe', 'PBE-SHA1-3DES', '-certpbe', 'PBE-SHA1-3DES', '-macalg', 'sha1'], check=True)
        archive.chmod(0o600)
    except BaseException:
        password.unlink(missing_ok=True)
        archive.unlink(missing_ok=True)
        raise
    return password


def install_provisioning_profile(profile, contents, platform, home=None):
    identifier = str(uuid.UUID(contents.get('UUID', '')))
    home = Path.home() if home is None else home
    extension = '.provisionprofile' if platform == 'macos' else '.mobileprovision'
    created = []
    marker = profile.parent / 'installed-profile-files.json'
    for location in (home / 'Library/Developer/Xcode/UserData/Provisioning Profiles',
                     home / 'Library/MobileDevice/Provisioning Profiles'):
        location.mkdir(parents=True, mode=0o700, exist_ok=True)
        destination = location / (identifier + extension)
        try:
            with destination.open('xb') as output:
                output.write(profile.read_bytes())
            destination.chmod(0o600)
        except FileExistsError:
            if destination.read_bytes() != profile.read_bytes():
                raise ValueError('installed provisioning profile UUID has different contents')
            continue
        created.append(str(destination))
        marker.write_text(json.dumps(created))
        marker.chmod(0o600)
    return created


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
    password = export_keychain_archive(key, certificate, p12)
    try:
        subprocess.run(['keychain', 'add-certificates', '--certificate', str(p12),
                        '--certificate-password', '@file:' + str(password)], check=True)
        install_provisioning_profile(profile, contents, os.environ.get('APTEVA_TARGET_KIND', 'ios'))
    finally:
        password.unlink(missing_ok=True)



if __name__ == '__main__':
    main()
