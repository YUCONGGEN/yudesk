package security

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"time"
)

func SelfSignedConfig(commonName string) (*tls.Config, error) {
	certPEM, keyPEM, err := generateSelfSigned(commonName)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}, nil
}

func LoadOrCreateServerConfig(certPath, keyPath, commonName string) (*tls.Config, string, error) {
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if os.IsNotExist(certErr) || os.IsNotExist(keyErr) {
		var err error
		certPEM, keyPEM, err = generateSelfSigned(commonName)
		if err != nil {
			return nil, "", err
		}
		if err = os.WriteFile(certPath, certPEM, 0644); err != nil {
			return nil, "", err
		}
		if err = os.WriteFile(keyPath, keyPEM, 0600); err != nil {
			return nil, "", err
		}
	} else if certErr != nil {
		return nil, "", certErr
	} else if keyErr != nil {
		return nil, "", keyErr
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, "", err
	}
	if len(cert.Certificate) == 0 {
		return nil, "", os.ErrInvalid
	}
	fingerprint := sha256.Sum256(cert.Certificate[0])
	formatted := strings.ToUpper(hex.EncodeToString(fingerprint[:]))
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}, formatted, nil
}

func generateSelfSigned(commonName string) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: commonName}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}
