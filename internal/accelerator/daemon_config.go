package accelerator

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultMaxTaskBytes = int64(6 * 1024 * 1024 * 1024)
	defaultRetention    = time.Hour
)

type DaemonConfig struct {
	Listen              string `json:"listen"`
	CertFile            string `json:"cert_file"`
	KeyFile             string `json:"key_file"`
	SpoolDir            string `json:"spool_dir"`
	AuthKey             string `json:"auth_key"`
	MaxTaskBytes        int64  `json:"max_task_bytes"`
	AllowPrivateTargets bool   `json:"allow_private_targets,omitempty"`
	SyncSpool           bool   `json:"sync_spool,omitempty"`
}

func Initialize(configPath, stateDir, listen, password string) (string, error) {
	return InitializeWithOptions(configPath, stateDir, listen, password, false)
}

func InitializeWithOptions(configPath, stateDir, listen, password string, allowPrivateTargets bool) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", errors.New("accelerator password is required")
	}
	if listen == "" {
		listen = ":7775"
	}
	absoluteState, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("resolving state directory: %w", err)
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("resolving config path: %w", err)
	}
	if _, err := os.Stat(absoluteConfig); err == nil {
		return "", fmt.Errorf("accelerator config already exists: %s", absoluteConfig)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(absoluteState, 0700); err != nil {
		return "", fmt.Errorf("creating state directory: %w", err)
	}
	if err := os.Chmod(absoluteState, 0700); err != nil {
		return "", err
	}
	certPath := filepath.Join(absoluteState, "identity.crt")
	keyPath := filepath.Join(absoluteState, "identity.key")
	fingerprint, fingerprintBytes, err := generateIdentity(certPath, keyPath)
	if err != nil {
		return "", err
	}
	authKey, err := DeriveAuthKey(password, fingerprintBytes)
	if err != nil {
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
		return "", err
	}
	spoolDir := filepath.Join(absoluteState, "spool")
	if err := os.MkdirAll(spoolDir, 0700); err != nil {
		return "", err
	}
	cfg := DaemonConfig{
		Listen:              listen,
		CertFile:            certPath,
		KeyFile:             keyPath,
		SpoolDir:            spoolDir,
		AuthKey:             base64.RawStdEncoding.EncodeToString(authKey),
		MaxTaskBytes:        defaultMaxTaskBytes,
		AllowPrivateTargets: allowPrivateTargets,
	}
	if err := writePrivateJSON(absoluteConfig, cfg); err != nil {
		return "", err
	}
	return fingerprint, nil
}

func LoadDaemonConfig(path string) (DaemonConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return DaemonConfig{}, fmt.Errorf("reading accelerator config: %w", err)
	}
	var cfg DaemonConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DaemonConfig{}, fmt.Errorf("parsing accelerator config: %w", err)
	}
	if cfg.Listen == "" || cfg.CertFile == "" || cfg.KeyFile == "" || cfg.SpoolDir == "" || cfg.AuthKey == "" {
		return DaemonConfig{}, errors.New("accelerator config is incomplete")
	}
	if cfg.MaxTaskBytes <= 0 {
		cfg.MaxTaskBytes = defaultMaxTaskBytes
	}
	if _, err := base64.RawStdEncoding.DecodeString(cfg.AuthKey); err != nil {
		return DaemonConfig{}, errors.New("accelerator auth key is invalid")
	}
	return cfg, nil
}

func ConfigFingerprint(path string) (string, error) {
	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(cfg.CertFile)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("accelerator certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return Fingerprint(certificate.Raw), nil
}

func Fingerprint(rawCertificate []byte) string {
	sum := sha256.Sum256(rawCertificate)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func generateIdentity(certPath, keyPath string) (string, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", nil, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return "", nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "stake-accelerator"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost", "stake-accelerator"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return "", nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", nil, err
	}
	if err := writePrivatePEM(certPath, "CERTIFICATE", der); err != nil {
		return "", nil, err
	}
	if err := writePrivatePEM(keyPath, "PRIVATE KEY", keyDER); err != nil {
		_ = os.Remove(certPath)
		return "", nil, err
	}
	sum := sha256.Sum256(der)
	return Fingerprint(der), sum[:], nil
}

func writePrivatePEM(path, kind string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := pem.Encode(file, &pem.Block{Type: kind, Bytes: data}); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}

func writePrivateJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}
