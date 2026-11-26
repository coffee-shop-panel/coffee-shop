package nodes

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database"
)

// Proving to a node which panel is calling.
//
// A token establishes that the caller knows a secret. It does not establish
// which machine the caller is, and it travels in every request — so a token
// read out of a log, a backup, or a proxy that was keeping bodies is full
// access to that node for as long as nobody notices.
//
// A client certificate is the other half. The key never leaves the panel that
// holds it: what crosses the wire is a signature over data unique to that
// connection, which is worth nothing to anybody who captures it. A node set to
// require one refuses a caller with a perfectly good token and no certificate.
//
// The arrangement is the one the classic panel settled on and it is the simple one: the
// managing panel mints a certificate authority of its own, signs itself a
// client certificate with it, and an operator pastes the authority's public
// half into each node. One value to copy, and a node that trusts it trusts that
// panel and nothing else.

const (
	// KeyMTLSCACert and KeyMTLSCAKey hold the authority this panel signs its own
	// client certificate with, when it is the one managing nodes.
	KeyMTLSCACert = "nodes.mtls_ca_cert"
	KeyMTLSCAKey  = "nodes.mtls_ca_key"

	// KeyMTLSClientCert and KeyMTLSClientKey are that certificate.
	KeyMTLSClientCert = "nodes.mtls_client_cert"
	KeyMTLSClientKey  = "nodes.mtls_client_key"

	// KeyMTLSTrustCA is the authority this panel accepts when it is the node
	// being managed. Empty means a client certificate is not required.
	KeyMTLSTrustCA = "nodes.mtls_trust_ca"
)

// caLifetime is how long the authority is good for.
//
// Ten years, deliberately. It signs one certificate for one panel and is copied
// by hand into every node; an expiry short enough to be a security measure
// would instead be an outage nobody had a calendar entry for.
const caLifetime = 10 * 365 * 24 * time.Hour

// Identity is this panel's client certificate and the authority behind it.
type Identity struct {
	CACertPEM     string
	ClientCertPEM string
	ClientKeyPEM  string
}

// identityMu is held across the whole of EnsureIdentity: the identity is
// read and, the first time, minted and stored as four separate settings,
// and two first calls racing -- the syncer and the prober ask in the same
// moments -- could interleave them, or each mint its own authority and
// leave the second's stored. The authority is what operators paste into
// every node by hand; minting another one silently is not a thing a race
// gets to do.
var identityMu sync.Mutex

// EnsureIdentity returns this panel's client identity, minting it the first
// time it is asked for.
//
// Lazily rather than at startup: a panel that manages no nodes has no use for
// one, and generating a key on every install for a feature most never turn on
// is work nobody asked for.
func EnsureIdentity(db *gorm.DB) (*Identity, error) {
	identityMu.Lock()
	defer identityMu.Unlock()
	caCert, haveCert, err := database.GetSetting(db, KeyMTLSCACert)
	if err != nil {
		return nil, err
	}
	caKey, haveKey, err := database.GetSetting(db, KeyMTLSCAKey)
	if err != nil {
		return nil, err
	}
	clientCert, haveClient, err := database.GetSetting(db, KeyMTLSClientCert)
	if err != nil {
		return nil, err
	}
	clientKey, haveClientKey, err := database.GetSetting(db, KeyMTLSClientKey)
	if err != nil {
		return nil, err
	}

	if haveCert && haveKey && haveClient && haveClientKey {
		return &Identity{CACertPEM: caCert, ClientCertPEM: clientCert, ClientKeyPEM: clientKey}, nil
	}

	if haveCert && haveKey {
		// A partial set from before the four were stored in one
		// transaction: the authority operators already pasted into every
		// node is still there, and only the client certificate is
		// missing. Mint just that, signed by the stored authority,
		// rather than a new one. An authority that no longer reads is
		// the one case that still mints fresh, as before.
		if cert, key, err := mintClientCert(caCert, caKey); err == nil {
			err = db.Transaction(func(tx *gorm.DB) error {
				if err := database.PutSetting(tx, KeyMTLSClientCert, cert); err != nil {
					return err
				}
				return database.PutSetting(tx, KeyMTLSClientKey, key)
			})
			if err != nil {
				return nil, err
			}
			return &Identity{CACertPEM: caCert, ClientCertPEM: cert, ClientKeyPEM: key}, nil
		}
	}

	id, err := mintIdentity()
	if err != nil {
		return nil, err
	}
	// All four in one transaction: a failure or a crash halfway through
	// the old one-by-one store left a partial set, and the next call
	// minted a brand-new authority, invalidating the one operators had
	// already pasted into every node.
	err = db.Transaction(func(tx *gorm.DB) error {
		for k, v := range map[string]string{
			KeyMTLSCACert:     id.CACertPEM,
			KeyMTLSCAKey:      id.caKeyPEM,
			KeyMTLSClientCert: id.ClientCertPEM,
			KeyMTLSClientKey:  id.ClientKeyPEM,
		} {
			if err := database.PutSetting(tx, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &id.Identity, nil
}

// mintClientCert issues a client certificate from a stored authority: the
// half of the identity a partial set is missing, without touching the
// authority operators already pasted into every node.
func mintClientCert(caCertPEM, caKeyPEM string) (string, string, error) {
	block, _ := pem.Decode([]byte(caCertPEM))
	if block == nil {
		return "", "", fmt.Errorf("nodes: the stored authority is not a certificate")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("nodes: read authority: %w", err)
	}
	block, _ = pem.Decode([]byte(caKeyPEM))
	if block == nil {
		return "", "", fmt.Errorf("nodes: the stored authority key is not a key")
	}
	caKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("nodes: read authority key: %w", err)
	}
	now := time.Now()
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("nodes: generate client key: %w", err)
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "W-UI managing panel"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(caLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		return "", "", fmt.Errorf("nodes: sign client certificate: %w", err)
	}
	clientKeyPEM, err := encodeKey(clientKey)
	if err != nil {
		return "", "", err
	}
	return encodeCert(clientDER), clientKeyPEM, nil
}

type mintedIdentity struct {
	Identity
	caKeyPEM string
}

func mintIdentity() (*mintedIdentity, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("nodes: generate authority key: %w", err)
	}
	now := time.Now()

	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "W-UI node authority"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("nodes: sign authority: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("nodes: read authority: %w", err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("nodes: generate client key: %w", err)
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "W-UI managing panel"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(caLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("nodes: sign client certificate: %w", err)
	}

	caKeyPEM, err := encodeKey(caKey)
	if err != nil {
		return nil, err
	}
	clientKeyPEM, err := encodeKey(clientKey)
	if err != nil {
		return nil, err
	}

	return &mintedIdentity{
		Identity: Identity{
			CACertPEM:     encodeCert(caDER),
			ClientCertPEM: encodeCert(clientDER),
			ClientKeyPEM:  clientKeyPEM,
		},
		caKeyPEM: caKeyPEM,
	}, nil
}

// TrustPool builds the set of authorities this panel accepts from a caller.
//
// Nil when nothing is configured, which is what says a client certificate is
// not required rather than that none is acceptable.
func TrustPool(pem string) (*x509.CertPool, error) {
	if pem == "" {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, fmt.Errorf("that does not look like a certificate: paste the whole block, " +
			"from BEGIN CERTIFICATE to END CERTIFICATE")
	}
	return pool, nil
}

// clientCertificate loads this panel's certificate for presenting to a node.
func clientCertificate(id *Identity) (tls.Certificate, error) {
	if id == nil || id.ClientCertPEM == "" || id.ClientKeyPEM == "" {
		return tls.Certificate{}, fmt.Errorf(
			"this panel has no client certificate yet; open the node's settings and copy " +
				"the authority once, which mints it")
	}
	return tls.X509KeyPair([]byte(id.ClientCertPEM), []byte(id.ClientKeyPEM))
}

func serial() *big.Int {
	// 128 bits, which is what a serial is for: telling two certificates apart,
	// not being guessed.
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

func encodeCert(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func encodeKey(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("nodes: encode key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}
