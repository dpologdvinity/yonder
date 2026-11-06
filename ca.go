// ca.go is yonder's own certificate authority for --https. Phones refuse camera, clipboard and
// service workers on plain http, and no public authority signs certificates for addresses such
// as 192.168.1.20, so yonder makes its own.
//
// The first --https run creates a root certificate and uses it once, to sign an intermediate
// certificate; the root key is then dropped and never written. The intermediate may only vouch
// for private addresses (critical name constraints), so even if its key were stolen it could
// not be used to impersonate a real website (only services at private addresses). Each run signs a new certificate for its own
// addresses with a key that lives only in memory. A phone that installs the root (ca.crt) gets
// no certificate warning; one that does not can still click through the warning.

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// caCertFile is the root certificate in the authority's folder (os.UserConfigDir()/yonder), the
// one devices install. PEM.
const caCertFile = "ca.crt"

// interCertFile is the intermediate certificate in the authority's folder. PEM.
const interCertFile = "intermediate.crt"

// interKeyFile is the intermediate's private key in the authority's folder. PKCS #8 PEM, readable
// by the owner only.
const interKeyFile = "intermediate.key"

// caPagePath is the path, inside the shared site, of the page that explains how to trust yonder.
// Like everything else it sits behind the access gate.
const caPagePath = "/.yonder/ca"

// caCertPath is the path, inside the shared site, of the root certificate download.
const caCertPath = "/.yonder/ca.crt"

// caLife is how long the root and the intermediate are valid.
const caLife = 10 * 365 * 24 * time.Hour

// leafLife is how long each certificate for a run is valid. A run that lasts longer gets a new
// one in time (serverConfig).
const leafLife = 30 * 24 * time.Hour

// leafRenewBefore is how long before its end a run's certificate is replaced.
const leafRenewBefore = 24 * time.Hour

// lockFile is created in the authority's folder while a run loads or creates the authority, so
// two runs started at once end up with the same one.
const lockFile = ".lock"

// lockWait is how long a run waits for another run's lock before giving up. Creating the
// authority takes milliseconds. A variable so tests can shorten it.
var lockWait = 10 * time.Second

// lockStale is the age after which a lock is taken to be left behind by a run that crashed.
const lockStale = time.Minute

// userConfigDir finds the folder for per-user settings. A variable so tests can point it at a
// temporary folder.
var userConfigDir = os.UserConfigDir

// privateRanges are the only addresses the intermediate may vouch for: the private IPv4 ranges
// of RFC 1918 and the unique local IPv6 range of RFC 4193.
var privateRanges = []*net.IPNet{
	{IP: net.IPv4(10, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(172, 16, 0, 0).To4(), Mask: net.CIDRMask(12, 32)},
	{IP: net.IPv4(192, 168, 0, 0).To4(), Mask: net.CIDRMask(16, 32)},
	{IP: net.ParseIP("fc00::"), Mask: net.CIDRMask(7, 128)},
}

// certAuthority is a loaded authority: the root certificate (its key is gone), the intermediate
// and its key, the folder it is kept in, and whether this run just made it, so the banner can
// say so.
type certAuthority struct {
	root, inter *x509.Certificate
	interKey    *ecdsa.PrivateKey
	dir         string
	fresh       bool
}

// randomSerial returns a random positive serial number of 128 bits.
func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

// newCA makes a root and an intermediate valid from an hour before now, signs the intermediate
// with the root, and drops the root key.
func newCA(now time.Time) (*certAuthority, error) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	rootSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	interSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	// The date and part of the serial tell two of yonder's roots apart in a phone's settings.
	name := fmt.Sprintf("yonder local CA %s %06X", now.Format("2006-01-02"), new(big.Int).Rsh(rootSerial, 104))
	rootTmpl := &x509.Certificate{
		SerialNumber:          rootSerial,
		Subject:               pkix.Name{Organization: []string{"yonder"}, CommonName: name},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLife),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, err
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, err
	}
	interTmpl := &x509.Certificate{
		SerialNumber:                interSerial,
		Subject:                     pkix.Name{Organization: []string{"yonder"}, CommonName: "yonder local intermediate"},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(caLife),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true, // a device that does not understand the limits must refuse the certificate
		PermittedDNSDomains:         []string{"invalid"},
		PermittedIPRanges:           privateRanges,
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, root, &interKey.PublicKey, rootKey)
	if err != nil {
		return nil, err
	}
	inter, err := x509.ParseCertificate(interDER)
	if err != nil {
		return nil, err
	}
	return &certAuthority{root: root, inter: inter, interKey: interKey}, nil
}

// leaf makes this run's certificate for ips, with a new key that is never written: valid from an
// hour before now (phone clocks drift) for leafLife. It returns the certificate together with
// the intermediate, so a device that trusts only the root can check it.
func (ca *certAuthority) leaf(ips []net.IP, now time.Time) (tls.Certificate, error) {
	if len(ips) == 0 {
		return tls.Certificate{}, errors.New("no address to make a certificate for")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"yonder"}}, // no host name in the common name
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(leafLife),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.inter, &key.PublicKey, ca.interKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.inter.Raw}, PrivateKey: key, Leaf: parsed}, nil
}

// serverConfig returns the TLS settings for serving ips: a certificate for this run, replaced
// leafRenewBefore before it ends (a run without --for can outlast it), TLS 1.2 or newer, and
// HTTP/1.1 only. HTTP/2 would carry WebSockets differently, and yonder passes them to the app by
// taking over an HTTP/1.1 connection, which it also tracks to close on exit. A renewal that
// fails is reported once on errOut; the server's own log drops handshake errors.
func (ca *certAuthority) serverConfig(ips []net.IP, now func() time.Time, errOut io.Writer) (*tls.Config, error) {
	first, err := ca.leaf(ips, now())
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	current := &first // replaced, never changed in place: a handshake may still be using it
	reported := false
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			mu.Lock()
			defer mu.Unlock()
			if t := now(); t.Add(leafRenewBefore).After(current.Leaf.NotAfter) {
				next, err := ca.leaf(ips, t)
				if err != nil {
					if !reported {
						reported = true
						say(errOut, "yonder: could not renew the https certificate, so devices cannot connect: %v\n", err)
					}
					return nil, err
				}
				current = &next
			}
			return current, nil
		},
	}, nil
}

// caDir returns the authority's folder, os.UserConfigDir()/yonder.
func caDir() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot find the folder for yonder's certificate: %w", err)
	}
	return filepath.Join(dir, "yonder"), nil
}

// loadOrCreateCA loads the authority in dir. When there is none yet, or it would expire before a
// new certificate does, it makes a new one, saves it, and marks it fresh. Damaged or mismatched
// files are an error, not replaced, so the owner learns that the root on their devices is
// no longer the one in use. It holds the folder's lock throughout, so runs started at the same
// moment all use the authority that ends up on disk.
func loadOrCreateCA(dir string, now time.Time) (*certAuthority, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the folder for yonder's certificate: %w", err)
	}
	unlock, err := lockFolder(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ca, err := loadCA(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// None yet: make one.
	case err != nil:
		return nil, fmt.Errorf("%w (yonder --remove-ca deletes the files, and the next --https run makes new ones)", err)
	case now.Add(leafLife).After(ca.inter.NotAfter) || now.Add(leafLife).After(ca.root.NotAfter):
		// Too close to the end: replace it.
	default:
		return ca, nil
	}
	if ca, err = newCA(now); err != nil {
		return nil, err
	}
	if err := saveCA(dir, ca); err != nil {
		return nil, fmt.Errorf("saving yonder's certificate in %s: %w", dir, err)
	}
	ca.dir, ca.fresh = dir, true
	return ca, nil
}

// lockFolder takes the lock in dir, waiting up to lockWait for another run to release it, and
// returns the function that releases it. A lock older than lockStale is left over from a crash
// and is taken over.
func lockFolder(dir string) (unlock func(), err error) {
	path := filepath.Join(dir, lockFile)
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close() // empty; only its existence matters
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStale {
			_ = os.Remove(path) // another waiting run may have removed it first
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another yonder is setting up the certificate; if none is running, delete %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// loadCA reads the three files in dir and checks that they belong together. It returns an error
// that wraps fs.ErrNotExist only when none of the files exists.
func loadCA(dir string) (*certAuthority, error) {
	var data [3][]byte
	missing := 0
	for i, name := range []string{caCertFile, interCertFile, interKeyFile} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			missing++
			continue
		}
		if err != nil {
			return nil, err
		}
		data[i] = b
	}
	if missing == 3 {
		return nil, fs.ErrNotExist
	}
	if missing > 0 {
		return nil, fmt.Errorf("some of yonder's certificate files in %s are missing", dir)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, interKeyFile))
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("other users of this computer can read %s", filepath.Join(dir, interKeyFile))
		}
	}
	broken := fmt.Errorf("yonder's certificate files in %s are damaged or do not belong together", dir)
	root, err := parsePEMCert(data[0])
	if err != nil {
		return nil, broken
	}
	inter, err := parsePEMCert(data[1])
	if err != nil {
		return nil, broken
	}
	block, _ := pem.Decode(data[2])
	if block == nil {
		return nil, broken
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, broken
	}
	interKey, ok := key.(*ecdsa.PrivateKey)
	if !ok || !interKey.PublicKey.Equal(inter.PublicKey) || inter.CheckSignatureFrom(root) != nil {
		return nil, broken
	}
	return &certAuthority{root: root, inter: inter, interKey: interKey, dir: dir}, nil
}

// parsePEMCert parses the first PEM block in data as a certificate.
func parsePEMCert(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// saveCA writes ca to dir, which must exist. Each file is written to a temporary name and then
// renamed, so a crash never leaves half a file.
func saveCA(dir string, ca *certAuthority) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(ca.interKey)
	if err != nil {
		return err
	}
	files := []struct {
		name, typ string
		der       []byte
		mode      os.FileMode
	}{
		{interKeyFile, "PRIVATE KEY", keyDER, 0o600},
		{interCertFile, "CERTIFICATE", ca.inter.Raw, 0o644},
		{caCertFile, "CERTIFICATE", ca.root.Raw, 0o644},
	}
	for _, f := range files {
		if err := writeFileAtomic(dir, f.name, pem.EncodeToMemory(&pem.Block{Type: f.typ, Bytes: f.der}), f.mode); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes data to dir/name with mode perm through a temporary file. The temporary
// file is created readable by the owner only, so a key is never readable by others, even for a
// moment.
func writeFileAtomic(dir, name string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // fails harmlessly once the rename has happened
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// removeCA deletes the authority's files in dir, and dir itself if nothing else is left in it.
// It reports whether there was anything to delete.
func removeCA(dir string) (bool, error) {
	removed := false
	for _, name := range []string{caCertFile, interCertFile, interKeyFile} {
		err := os.Remove(filepath.Join(dir, name))
		if err == nil {
			removed = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
	}
	_ = os.Remove(filepath.Join(dir, lockFile)) // only there if a run crashed; ignoring a missing one
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil || len(entries) == 0 {
			return removed, err
		}
		// Other files are in the folder; they are not yonder's to delete.
	}
	return removed, nil
}

// fingerprint returns the SHA-256 of der as colon-separated pairs of capital hex digits, the way
// phones and browsers show a certificate's fingerprint.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(pairs, ":")
}

// withCAPages answers the two certificate paths in front of next: caPagePath, a page with the
// steps to trust yonder, and caCertPath, the root certificate itself (DER, with the type that
// makes iPhones offer to install it, or with "?save" as a plain file, which Android needs). The
// page does not show the fingerprint: it arrives over a connection the device cannot check yet,
// so an attacker's copy could show the real one. Every other path goes to next, and so does every
// request through the --public tunnel, because the certificate is only for this network.
func withCAPages(next http.Handler, ca *certAuthority) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fromTunnel(r) || (r.URL.Path != caPagePath && r.URL.Path != caCertPath) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			refuse(w, http.StatusMethodNotAllowed, "yonder: this page can only be read\n")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == caCertPath {
			if r.URL.Query().Has("save") {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", `attachment; filename="yonder-ca.crt"`)
			} else {
				w.Header().Set("Content-Type", "application/x-x509-ca-cert")
			}
			_, _ = w.Write(ca.root.Raw) // a client that went away cannot be helped
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		say(w, caPageHTML, html.EscapeString(ca.root.Subject.CommonName), caCertPath, caCertPath+"?save")
	})
}

// caPageHTML is the page at caPagePath. Its arguments are the root's name, the download path for
// iPhones and the download path that saves a plain file.
const caPageHTML = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Trust yonder on this device</title></head>
<body style="font:16px/1.5 system-ui,sans-serif;margin:2rem;max-width:40rem">
<h1>Trust yonder on this device</h1>
<p>This removes the certificate warning for sites shared with <code>yonder --https</code> from the computer that showed you this link. The certificate, <b>%s</b>, can only vouch for addresses on private networks (such as 192.168.x.x), never for a real website.</p>
<p><b>Check the fingerprint in your device's own certificate details</b>, as the steps below show, and compare it with the one in the terminal of the computer that shares. It must match exactly. This page cannot show it for you: someone on this network could have sent you a fake copy of this page.</p>
<h2>iPhone and iPad</h2>
<p><a href="%s" style="display:inline-block;padding:.6rem 1rem;background:#2563eb;color:#fff;border-radius:.4rem;text-decoration:none">Download the certificate</a></p>
<ol><li>Tap Download (in Safari), then Allow.</li><li>Open Settings and tap <i>Profile Downloaded</i> near the top. Tap <i>More Details</i>, then the certificate, and compare its SHA-256 fingerprint with the terminal. If it differs, tap Remove.</li><li>If it matches, tap Install.</li><li>Open Settings &gt; General &gt; About &gt; Certificate Trust Settings and turn on the yonder certificate.</li></ol>
<h2>Android</h2>
<p><a href="%s" style="display:inline-block;padding:.6rem 1rem;background:#2563eb;color:#fff;border-radius:.4rem;text-decoration:none">Save the certificate file</a></p>
<ol><li>Tap Save. The file <i>yonder-ca.crt</i> goes to Downloads.</li><li>Open Settings and search for <i>CA certificate</i> (usually Security &gt; More security settings &gt; Encryption &amp; credentials &gt; Install a certificate &gt; CA certificate). Choose <i>Install anyway</i> and pick the file.</li><li>Then open Encryption &amp; credentials &gt; Trusted credentials &gt; User, tap the yonder certificate, and compare its SHA-256 fingerprint with the terminal. If it differs, remove it.</li><li>Chrome trusts it at once; Firefox needs <i>Use third party CA certificates</i> turned on in its secret settings.</li></ol>
<h2>Computers</h2>
<p>Save the file with the Android button, compare its fingerprint (on a Mac, Keychain Access shows it; on Windows, the certificate's Details tab), then trust it for websites: on a Mac, set it to Always Trust in Keychain Access; on Windows, install it into <i>Trusted Root Certification Authorities</i>.</p>
<h2>To undo</h2>
<p>Remove the profile (iPhone: Settings &gt; General &gt; VPN &amp; Device Management) or the certificate (Android: Encryption &amp; credentials &gt; User credentials). On the computer, <code>yonder --remove-ca</code> deletes yonder's copy.</p>
</body></html>`
