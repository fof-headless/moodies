// Package mitm provides the embedded TLS-intercepting CONNECT proxy that
// replaces the mitmproxy (Python) subprocess. Everything runs in-process:
// CA management, per-host cert signing, and HTTP/1.1 flow logging.
package mitm

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA holds the certificate authority used to sign per-host leaf certs on the fly.
type CA struct {
	cert  *x509.Certificate
	key   *rsa.PrivateKey
	mu    sync.Mutex
	cache map[string]*tls.Certificate // host → leaf cert, permanent for process lifetime
}

// GenerateCA creates a new RSA-4096 self-signed CA and writes:
//
//	bundlePath — private key + certificate (PEM, mode 0600)
//	certPath   — certificate only          (PEM, mode 0644; used for keychain trust
//	             and NODE_EXTRA_CA_CERTS)
//
// Returns nil immediately if bundlePath already exists so the call is safe to
// repeat on re-install (existing trust is preserved).
func GenerateCA(bundlePath, certPath string) error {
	if _, err := os.Stat(bundlePath); err == nil {
		return nil // already generated; don't rotate the key
	}
	for _, p := range []string{bundlePath, certPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(p), err)
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "moodies CA", Organization: []string{"moodies"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create cert: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// Bundle: key first then cert — same layout as mitmproxy-ca.pem so LoadCA
	// accepts both formats transparently.
	bundle := append(append([]byte(nil), keyPEM...), certPEM...)
	if err := os.WriteFile(bundlePath, bundle, 0600); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	return nil
}

// LoadCA parses a PEM bundle that contains an RSA private key and a
// certificate (in either order). Accepts both our own GenerateCA layout and
// the mitmproxy mitmproxy-ca.pem layout so existing installs keep working.
func LoadCA(bundlePath string) (*CA, error) {
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle %s: %w", bundlePath, err)
	}

	var keyBlock, certBlock *pem.Block
	for rest := data; len(rest) > 0; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		switch b.Type {
		case "RSA PRIVATE KEY", "PRIVATE KEY", "EC PRIVATE KEY":
			keyBlock = b
		case "CERTIFICATE":
			certBlock = b
		}
	}
	if keyBlock == nil {
		return nil, fmt.Errorf("%s: no private key PEM block", bundlePath)
	}
	if certBlock == nil {
		return nil, fmt.Errorf("%s: no certificate PEM block", bundlePath)
	}

	tlsCert, err := tls.X509KeyPair(
		pem.EncodeToMemory(certBlock),
		pem.EncodeToMemory(keyBlock),
	)
	if err != nil {
		return nil, fmt.Errorf("parse key pair: %w", err)
	}

	x509Cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}

	rsaKey, ok := tlsCert.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("CA private key is not RSA (got %T)", tlsCert.PrivateKey)
	}

	return &CA{
		cert:  x509Cert,
		key:   rsaKey,
		cache: make(map[string]*tls.Certificate),
	}, nil
}

// LeafCert returns a TLS certificate for host signed by this CA, generating
// and caching one if it hasn't been seen before.
// Thread-safe; multiple proxy goroutines may call concurrently.
func (ca *CA) LeafCert(host string) (*tls.Certificate, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	if c, ok := ca.cache[host]; ok {
		return c, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign leaf cert for %s: %w", host, err)
	}

	cert := &tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key}
	ca.cache[host] = cert
	return cert, nil
}
