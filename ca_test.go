// ca_test.go tests yonder's certificate authority for --https: every field of the root, the
// intermediate and the per-run certificate, that the name constraints keep the authority to
// private addresses, how the files are kept on disk, and the pages that hand the certificate to
// a phone.

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// oidNameConstraints is the object identifier of the X.509 name constraints extension.
var oidNameConstraints = asn1.ObjectIdentifier{2, 5, 29, 30}

// testCA returns a fresh authority made at a fixed moment.
func testCA(t *testing.T) *certAuthority {
	t.Helper()
	ca, err := newCA(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// checkKeyAndSerial checks that a certificate has an ECDSA P-256 key and a serial number of at
// least 64 random bits (positive, so it encodes in 9 or more bytes with a leading zero at most).
func checkKeyAndSerial(t *testing.T, name string, c *x509.Certificate) {
	t.Helper()
	pub, ok := c.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Errorf("%s: key %T, want ECDSA P-256", name, c.PublicKey)
	}
	if c.SignatureAlgorithm != x509.ECDSAWithSHA256 {
		t.Errorf("%s: signature %v, want ECDSA with SHA-256", name, c.SignatureAlgorithm)
	}
	if c.SerialNumber.Sign() <= 0 || c.SerialNumber.BitLen() < 64 {
		t.Errorf("%s: serial %v, want a positive number of 64 bits or more", name, c.SerialNumber)
	}
}

// TestNewCARoot checks the root: a self-signed CA that may sign exactly one level below it.
func TestNewCARoot(t *testing.T) {
	ca := testCA(t)
	r := ca.root
	checkKeyAndSerial(t, "root", r)
	if !r.IsCA || !r.BasicConstraintsValid || r.MaxPathLen != 1 {
		t.Errorf("root: IsCA %v, MaxPathLen %d; want a CA with path length 1", r.IsCA, r.MaxPathLen)
	}
	if r.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		t.Errorf("root: key usage %v, want certificate and CRL signing only", r.KeyUsage)
	}
	if err := r.CheckSignatureFrom(r); err != nil {
		t.Errorf("root is not self-signed: %v", err)
	}
	if !strings.HasPrefix(r.Subject.CommonName, "yonder local CA ") {
		t.Errorf("root name %q, want it to start with %q so people recognize it in their settings", r.Subject.CommonName, "yonder local CA ")
	}
}

// TestNewCAIntermediate checks the intermediate: a CA signed by the root that cannot sign another
// CA, is only for web servers, and is limited by critical name constraints to private addresses
// and to the DNS domain "invalid", so it can never vouch for a real website.
func TestNewCAIntermediate(t *testing.T) {
	ca := testCA(t)
	in := ca.inter
	checkKeyAndSerial(t, "intermediate", in)
	if err := in.CheckSignatureFrom(ca.root); err != nil {
		t.Errorf("intermediate is not signed by the root: %v", err)
	}
	if !in.IsCA || !in.BasicConstraintsValid || in.MaxPathLen != 0 || !in.MaxPathLenZero {
		t.Errorf("intermediate: IsCA %v, MaxPathLen %d, zero %v; want a CA with path length 0", in.IsCA, in.MaxPathLen, in.MaxPathLenZero)
	}
	if in.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		t.Errorf("intermediate: key usage %v, want certificate and CRL signing only", in.KeyUsage)
	}
	if len(in.ExtKeyUsage) != 1 || in.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("intermediate: extended key usage %v, want server authentication only", in.ExtKeyUsage)
	}
	var ranges []string
	for _, n := range in.PermittedIPRanges {
		ranges = append(ranges, n.String())
	}
	if got, want := strings.Join(ranges, " "), "10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7"; got != want {
		t.Errorf("permitted IP ranges %q, want %q", got, want)
	}
	if strings.Join(in.PermittedDNSDomains, " ") != "invalid" || !in.PermittedDNSDomainsCritical {
		t.Errorf("permitted DNS domains %q (critical %v), want only %q, critical", in.PermittedDNSDomains, in.PermittedDNSDomainsCritical, "invalid")
	}
	if len(in.ExcludedIPRanges)+len(in.ExcludedDNSDomains)+len(in.PermittedEmailAddresses)+len(in.PermittedURIDomains) != 0 {
		t.Error("intermediate has name constraints beyond the IP ranges and the DNS domain")
	}
	critical := false
	for _, e := range in.Extensions {
		if e.Id.Equal(oidNameConstraints) {
			critical = e.Critical
		}
	}
	if !critical {
		t.Error("the name constraints extension is missing or not critical")
	}
	if in.NotAfter.After(ca.root.NotAfter) {
		t.Errorf("intermediate outlives the root: %v after %v", in.NotAfter, ca.root.NotAfter)
	}
}

// TestLeafFields checks the certificate a run serves: the exact addresses as IP entries and no
// host name, for web servers only, not a CA, valid 30 days from an hour ago, sent with the
// intermediate so the phone needs only the root.
func TestLeafFields(t *testing.T) {
	ca := testCA(t)
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	ips := []net.IP{net.ParseIP("192.168.1.20").To4(), net.ParseIP("10.0.0.7").To4()}
	cert, err := ca.leaf(ips, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.Certificate) != 2 || string(cert.Certificate[1]) != string(ca.inter.Raw) || cert.Leaf == nil || string(cert.Leaf.Raw) != string(cert.Certificate[0]) {
		t.Fatalf("chain has %d certificates; want the leaf followed by the intermediate, and the parsed leaf", len(cert.Certificate))
	}
	if _, ok := cert.PrivateKey.(*ecdsa.PrivateKey); !ok {
		t.Errorf("leaf key %T, want ECDSA", cert.PrivateKey)
	}
	l, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	checkKeyAndSerial(t, "leaf", l)
	if l.Subject.CommonName != "" || len(l.DNSNames) != 0 || len(l.EmailAddresses)+len(l.URIs) != 0 {
		t.Errorf("leaf names: CN %q, DNS %v; want neither", l.Subject.CommonName, l.DNSNames)
	}
	if len(l.IPAddresses) != 2 || !l.IPAddresses[0].Equal(ips[0]) || !l.IPAddresses[1].Equal(ips[1]) {
		t.Errorf("leaf IP addresses %v, want %v", l.IPAddresses, ips)
	}
	if l.IsCA || !l.BasicConstraintsValid {
		t.Error("leaf must say it is not a CA")
	}
	if l.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("leaf key usage %v, want digital signature only", l.KeyUsage)
	}
	if len(l.ExtKeyUsage) != 1 || l.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("leaf extended key usage %v, want server authentication only", l.ExtKeyUsage)
	}
	if !l.NotBefore.Equal(now.Add(-time.Hour)) || !l.NotAfter.Equal(now.Add(30*24*time.Hour)) {
		t.Errorf("leaf valid %v to %v; want an hour before %v to 30 days after", l.NotBefore, l.NotAfter, now)
	}
	if err := l.CheckSignatureFrom(ca.inter); err != nil {
		t.Errorf("leaf is not signed by the intermediate: %v", err)
	}
	other, err := ca.leaf(ips, now)
	if err != nil {
		t.Fatal(err)
	}
	if other.PrivateKey.(*ecdsa.PrivateKey).Equal(cert.PrivateKey) {
		t.Error("two runs got the same key; each run must make its own")
	}
}

// verifyLeaf checks a leaf certificate the way a phone that trusts only the root would.
func verifyLeaf(ca *certAuthority, leafDER []byte, ip string, now time.Time) error {
	l, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return err
	}
	roots, inters := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(ca.root)
	inters.AddCert(ca.inter)
	_, err = l.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, DNSName: ip, CurrentTime: now})
	return err
}

// TestNameConstraintsLimitTheAuthority checks what a device that trusts the root accepts: a
// certificate for a private address passes; one for a public address, or one that also names a
// real website, is refused, even though it is signed by the intermediate.
func TestNameConstraintsLimitTheAuthority(t *testing.T) {
	ca := testCA(t)
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for _, ip := range []string{"192.168.1.20", "10.1.2.3", "172.20.0.5", "fd00::5"} {
		cert, err := ca.leaf([]net.IP{net.ParseIP(ip)}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyLeaf(ca, cert.Certificate[0], ip, now); err != nil {
			t.Errorf("leaf for %s refused: %v", ip, err)
		}
	}
	for _, ip := range []string{"8.8.8.8", "100.64.0.1", "172.32.0.1", "2001:db8::1"} {
		cert, err := ca.leaf([]net.IP{net.ParseIP(ip)}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyLeaf(ca, cert.Certificate[0], ip, now); err == nil {
			t.Errorf("leaf for the public address %s was accepted", ip)
		}
	}
	// Signed by hand, the way a thief of the intermediate key would, with a real website's name.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		DNSNames: []string{"bank.example.com"}, IPAddresses: []net.IP{net.ParseIP("192.168.1.20")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.inter, &key.PublicKey, ca.interKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLeaf(ca, der, "bank.example.com", now); err == nil {
		t.Error("a certificate for bank.example.com was accepted")
	}
}

// TestLeafNeedsAnAddress checks that a leaf with no address is refused, because no device could
// ever accept it.
func TestLeafNeedsAnAddress(t *testing.T) {
	if _, err := testCA(t).leaf(nil, time.Now()); err == nil {
		t.Error("leaf(nil) succeeded, want an error")
	}
}

// TestLoadOrCreateCAKeepsTheAuthority checks the files: the first run creates the folder (only
// the owner may enter it) with the root certificate, the intermediate certificate and the
// intermediate key (only the owner may read it); the second run loads the same authority; and the
// root key is never written anywhere.
func TestLoadOrCreateCAKeepsTheAuthority(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "yonder")
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	first, err := loadOrCreateCA(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if !first.fresh {
		t.Error("the first run did not report a new authority")
	}
	second, err := loadOrCreateCA(dir, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.fresh || !second.root.Equal(first.root) || !second.inter.Equal(first.inter) || !second.interKey.Equal(first.interKey) {
		t.Error("the second run did not load the authority the first one saved")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	keys := 0
	for _, e := range entries {
		names = append(names, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for rest := data; ; {
			var b *pem.Block
			if b, rest = pem.Decode(rest); b == nil {
				break
			}
			if strings.Contains(b.Type, "PRIVATE KEY") {
				keys++
				k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
				if err != nil || !first.interKey.Equal(k) {
					t.Errorf("%s holds a private key that is not the intermediate's", e.Name())
				}
			}
		}
	}
	if got := strings.Join(names, " "); got != "ca.crt intermediate.crt intermediate.key" {
		t.Errorf("files %q, want ca.crt, intermediate.crt and intermediate.key only", got)
	}
	if keys != 1 {
		t.Errorf("%d private keys on disk, want 1 (the intermediate's; the root key is never kept)", keys)
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{"": 0o700, "intermediate.key": 0o600, "ca.crt": 0o644, "intermediate.crt": 0o644} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Errorf("%q has mode %v, want %v", filepath.Join(dir, name), info.Mode().Perm(), want)
			}
		}
	}
}

// TestLoadOrCreateCARenewsBeforeItExpires checks that an authority that would expire before a
// new 30-day certificate does is replaced, and the run says so.
func TestLoadOrCreateCARenewsBeforeItExpires(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	old, err := loadOrCreateCA(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	later := old.inter.NotAfter.Add(-29 * 24 * time.Hour)
	renewed, err := loadOrCreateCA(dir, later)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.fresh || renewed.root.Equal(old.root) {
		t.Error("an authority 29 days from expiry was kept; want a new one")
	}
	again, err := loadOrCreateCA(dir, later)
	if err != nil || again.fresh || !again.root.Equal(renewed.root) {
		t.Errorf("the renewed authority was not saved: err %v", err)
	}
}

// TestLoadOrCreateCARefusesBrokenFiles checks that damaged or mismatched files stop the run with
// a message that names the fix, instead of being silently replaced.
func TestLoadOrCreateCARefusesBrokenFiles(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	other, err := newCA(now)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		damage func(dir string)
		want   string
	}{
		"garbage certificate": {func(dir string) { must(t, os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("nope"), 0o644)) }, "damaged or"},
		"key from another authority": {func(dir string) {
			b, err := x509.MarshalPKCS8PrivateKey(other.interKey)
			must(t, err)
			must(t, os.WriteFile(filepath.Join(dir, "intermediate.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}), 0o600))
		}, "do not belong together"},
		"root from another authority": {func(dir string) {
			must(t, os.WriteFile(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.root.Raw}), 0o644))
		}, "do not belong together"},
		"a file deleted": {func(dir string) { must(t, os.Remove(filepath.Join(dir, "intermediate.crt"))) }, "are missing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := loadOrCreateCA(dir, now); err != nil {
				t.Fatal(err)
			}
			c.damage(dir)
			if _, err := loadOrCreateCA(dir, now); err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "--remove-ca") {
				t.Errorf("err = %v, want an error that says %q and suggests --remove-ca", err, c.want)
			}
		})
	}
}

// TestLoadOrCreateCARefusesAKeyOthersCanRead checks that yonder will not use an intermediate key
// that other users of the computer can read, as ssh does with its keys.
func TestLoadOrCreateCARefusesAKeyOthersCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use Unix file modes")
	}
	dir := t.TempDir()
	now := time.Now()
	if _, err := loadOrCreateCA(dir, now); err != nil {
		t.Fatal(err)
	}
	must(t, os.Chmod(filepath.Join(dir, "intermediate.key"), 0o644))
	if _, err := loadOrCreateCA(dir, now); err == nil || !strings.Contains(err.Error(), "other users") {
		t.Errorf("err = %v, want a refusal that says other users can read the key", err)
	}
}

// TestServerTLSConfigReportsAFailedRenewal checks that a certificate that cannot be renewed is
// reported once, where the owner sees it, instead of only failing every handshake in silence.
func TestServerTLSConfigReportsAFailedRenewal(t *testing.T) {
	ca := testCA(t)
	start := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	clock := start
	var errOut strings.Builder
	cfg, err := ca.serverConfig([]net.IP{net.ParseIP("192.168.1.20")}, func() time.Time { return clock }, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	ca.interKey = other // signing with a key that does not match the intermediate fails
	clock = start.Add(29*24*time.Hour + time.Minute)
	for range 3 {
		if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{}); err == nil {
			t.Fatal("renewal with the wrong key succeeded")
		}
	}
	if got := errOut.String(); strings.Count(got, "yonder:") != 1 || !strings.Contains(got, "renew") {
		t.Errorf("reported %q, want one line about renewing", got)
	}
}

// TestLoadOrCreateCAIsSafeToRunTwiceAtOnce checks that yonders started at the same moment, all
// creating the authority for the first time, end up with one authority: the one on disk.
func TestLoadOrCreateCAIsSafeToRunTwiceAtOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "yonder")
	now := time.Now()
	const n = 8
	results := make(chan *certAuthority, n)
	errs := make(chan error, n)
	for range n {
		go func() {
			ca, err := loadOrCreateCA(dir, now)
			results <- ca
			errs <- err
		}()
	}
	var got []*certAuthority
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		got = append(got, <-results)
	}
	onDisk, err := loadCA(dir)
	if err != nil {
		t.Fatalf("the files do not belong together after %d runs at once: %v", n, err)
	}
	fresh := 0
	for _, ca := range got {
		if !ca.root.Equal(onDisk.root) {
			t.Error("a run uses a root that is not the one on disk")
		}
		if ca.fresh {
			fresh++
		}
	}
	if fresh != 1 {
		t.Errorf("%d runs say they made the authority, want 1", fresh)
	}
}

// TestLoadOrCreateCALock checks the lock that keeps two runs from creating the authority at once:
// a lock left behind by a crash is taken over, and a live one makes the run wait and then give
// up with a message that names the lock.
func TestLoadOrCreateCALock(t *testing.T) {
	old := lockWait
	lockWait = 300 * time.Millisecond
	defer func() { lockWait = old }()

	dir := t.TempDir()
	lock := filepath.Join(dir, ".lock")
	must(t, os.WriteFile(lock, nil, 0o600))
	long := time.Now().Add(-time.Hour)
	must(t, os.Chtimes(lock, long, long))
	if _, err := loadOrCreateCA(dir, time.Now()); err != nil {
		t.Errorf("a stale lock stopped the run: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("the lock is still there after the run: %v", err)
	}

	dir = t.TempDir()
	lock = filepath.Join(dir, ".lock")
	must(t, os.WriteFile(lock, nil, 0o600))
	if _, err := loadOrCreateCA(dir, time.Now()); err == nil || !strings.Contains(err.Error(), lock) {
		t.Errorf("err = %v, want a timeout that names %s", err, lock)
	}
}

// TestRemoveCA checks that --remove-ca deletes yonder's files and folder, reports whether there
// was anything to remove, and leaves files it did not make.
func TestRemoveCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "yonder")
	if removed, err := removeCA(dir); err != nil || removed {
		t.Errorf("removeCA on nothing = %v, %v; want false, nil", removed, err)
	}
	if _, err := loadOrCreateCA(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(dir, ".lock"), nil, 0o600)) // left behind by a crash
	if removed, err := removeCA(dir); err != nil || !removed {
		t.Errorf("removeCA = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder is still there: %v", err)
	}
	if _, err := loadOrCreateCA(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	must(t, writeFile(dir, "notes.txt", "mine"))
	if removed, err := removeCA(dir); err != nil || !removed {
		t.Errorf("removeCA with a foreign file = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("a file yonder did not make was removed: %v", err)
	}
}

// TestFingerprint checks the format people compare on their phones: the SHA-256 of the
// certificate as colon-separated pairs of capital hex digits.
func TestFingerprint(t *testing.T) {
	got := fingerprint([]byte("abc"))
	want := "BA:78:16:BF:8F:01:CF:EA:41:41:40:DE:5D:AE:22:23:B0:03:61:A3:96:17:7A:9C:B4:10:FF:61:F2:00:15:AD"
	if got != want {
		t.Errorf("fingerprint = %s, want %s", got, want)
	}
}

// TestCAPages checks the pages that hand the root certificate to a phone: the download is the
// DER certificate with the type phones install from, a second download saves it as a plain file
// (Android installs CA certificates only from a file), the page has the steps but no fingerprint,
// because a page that arrives over an unchecked connection cannot vouch for one, and every other
// path goes on to what is shared.
func TestCAPages(t *testing.T) {
	ca := testCA(t)
	h := withCAPages(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { say(w, "app") }), ca)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.yonder/ca.crt", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-x509-ca-cert" || rec.Body.String() != string(ca.root.Raw) {
		t.Errorf("download: %d %q, %d bytes; want 200, application/x-x509-ca-cert and the DER root", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.yonder/ca.crt?save", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/octet-stream" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="yonder-ca.crt"` || rec.Body.String() != string(ca.root.Raw) {
		t.Errorf("saved download: %d %q %q; want 200, a plain file named yonder-ca.crt and the DER root", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.yonder/ca", nil))
	body := rec.Body.String()
	for _, want := range []string{`href="/.yonder/ca.crt"`, `href="/.yonder/ca.crt?save"`, "iPhone", "Android", "Certificate Trust Settings", "More Details", "terminal"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, fingerprint(ca.root.Raw)) || strings.Contains(body, fingerprint(ca.root.Raw)[:20]) {
		t.Error("the page shows the fingerprint; an attacker's copy of the page could show the real one too")
	}
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("page type %q", rec.Header().Get("Content-Type"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/.yonder/ca.crt", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to the download: %d, want 405", rec.Code)
	}

	for _, p := range []string{"/", "/.yonder/ca.crt/x", "/.yonder", "/x/.yonder/ca"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Body.String() != "app" {
			t.Errorf("%s did not reach the shared site", p)
		}
	}

	// Through the --public tunnel the pages do not exist: the certificate is for this network.
	tunneled := withTunnelEdge(h)
	for _, p := range []string{"/.yonder/ca", "/.yonder/ca.crt"} {
		rec = httptest.NewRecorder()
		tunneled.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Body.String() != "app" {
			t.Errorf("%s through the tunnel answered %q, want the shared site", p, rec.Body.String())
		}
	}
}

// TestServerTLSConfig checks the TLS settings: TLS 1.2 or newer, HTTP/1.1 only (WebSockets reach
// the app by taking over an HTTP/1.1 connection), and a certificate for the run's addresses that
// is replaced a day before it ends, because a run without --for can last longer than 30 days.
func TestServerTLSConfig(t *testing.T) {
	ca := testCA(t)
	start := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	clock := start
	cfg, err := ca.serverConfig([]net.IP{net.ParseIP("192.168.1.20")}, func() time.Time { return clock }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS12 || strings.Join(cfg.NextProtos, " ") != "http/1.1" {
		t.Errorf("min version %x, protocols %v; want TLS 1.2 and http/1.1 only", cfg.MinVersion, cfg.NextProtos)
	}
	certAt := func(at time.Time) *tls.Certificate {
		clock = at
		c, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := certAt(start)
	if !first.Leaf.IPAddresses[0].Equal(net.ParseIP("192.168.1.20")) {
		t.Errorf("certificate for %v, want 192.168.1.20", first.Leaf.IPAddresses)
	}
	if certAt(start.Add(28*24*time.Hour)) != first {
		t.Error("the certificate was replaced two days before its end; want it kept")
	}
	renewed := certAt(start.Add(29*24*time.Hour + time.Minute))
	if renewed == first || !renewed.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Error("the certificate was not replaced a day before its end")
	}
	if certAt(start.Add(29*24*time.Hour+2*time.Minute)) != renewed {
		t.Error("the renewed certificate was replaced again at once")
	}
}
