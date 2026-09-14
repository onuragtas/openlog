package sso

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	_ "crypto/sha1" // registers SHA-1 for RSA-OAEP MGF1 (key transport only, not signatures)
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/beevik/etree"
	"github.com/crewjam/saml/xmlenc"
)

// XML encryption of SAML assertions (D-088). crewjam/saml decrypts AES-CBC and AES-128-GCM and RSA-OAEP with the
// digest also used for MGF1; identity providers such as Keycloak (AES-256-GCM, RSA-OAEP with MGF1-SHA-256 or a
// digest URI from xmlenc) need the decrypters below. They are registered process-wide once (configureSAML); the
// document precheck allows only samlEncAlgs, so RSA PKCS#1 v1.5 and 3DES stay refused.

type gcmDecrypter struct {
	alg     string
	keySize int
}

func (d gcmDecrypter) Algorithm() string { return d.alg }

// Decrypt decrypts xenc:EncryptedData with AES-GCM (IV of 12 bytes before the ciphertext, 128-bit tag after it).
// key is the content key, or the SP private key when the element carries KeyInfo/EncryptedKey.
func (d gcmDecrypter) Decrypt(key interface{}, el *etree.Element) ([]byte, error) {
	if ek := el.FindElement("./KeyInfo/EncryptedKey"); ek != nil {
		if _, isKey := key.([]byte); !isKey {
			k, err := xmlenc.Decrypt(key, ek)
			if err != nil {
				return nil, err
			}
			key = k
		}
	}
	keyBuf, ok := key.([]byte)
	if !ok || len(keyBuf) != d.keySize {
		return nil, fmt.Errorf("AES-GCM needs a %d byte key", d.keySize)
	}
	ct, err := cipherValue(el)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(keyBuf)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ct) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("AES-GCM ciphertext is too short")
	}
	return aead.Open(nil, ct[:aead.NonceSize()], ct[aead.NonceSize():], nil)
}

type oaepDecrypter struct{ alg string }

func (d oaepDecrypter) Algorithm() string { return d.alg }

var oaepDigests = map[string]crypto.Hash{
	"":                                       crypto.SHA1,
	"http://www.w3.org/2000/09/xmldsig#sha1": crypto.SHA1,
	"http://www.w3.org/2001/04/xmlenc#sha256":       crypto.SHA256,
	"http://www.w3.org/2000/09/xmldsig#sha256":      crypto.SHA256, // non-standard URI used by some implementations
	"http://www.w3.org/2001/04/xmldsig-more#sha384": crypto.SHA384,
	"http://www.w3.org/2001/04/xmlenc#sha512":       crypto.SHA512,
}

var oaepMGFs = map[string]crypto.Hash{
	"": crypto.SHA1,
	"http://www.w3.org/2009/xmlenc11#mgf1sha1":   crypto.SHA1,
	"http://www.w3.org/2009/xmlenc11#mgf1sha224": crypto.SHA224,
	"http://www.w3.org/2009/xmlenc11#mgf1sha256": crypto.SHA256,
	"http://www.w3.org/2009/xmlenc11#mgf1sha384": crypto.SHA384,
	"http://www.w3.org/2009/xmlenc11#mgf1sha512": crypto.SHA512,
}

// Decrypt decrypts xenc:EncryptedKey with RSA-OAEP; key must be the SP's *rsa.PrivateKey.
func (d oaepDecrypter) Decrypt(key interface{}, el *etree.Element) ([]byte, error) {
	priv, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("RSA-OAEP key transport needs the SP's RSA key")
	}
	method := el.FindElement("./EncryptionMethod")
	if method == nil {
		return nil, errors.New("EncryptedKey has no EncryptionMethod")
	}
	digest := ""
	if dm := method.FindElement("./DigestMethod"); dm != nil {
		digest = dm.SelectAttrValue("Algorithm", "")
	}
	h, ok := oaepDigests[digest]
	if !ok {
		return nil, fmt.Errorf("RSA-OAEP digest %q is not supported", digest)
	}
	mgf := h
	if d.alg == "http://www.w3.org/2001/04/xmlenc#rsa-oaep-mgf1p" {
		mgf = crypto.SHA1 // the 2001 algorithm fixes MGF1 with SHA-1
	} else {
		name := ""
		if m := method.FindElement("./MGF"); m != nil {
			name = m.SelectAttrValue("Algorithm", "")
		}
		if mgf, ok = oaepMGFs[name]; !ok {
			return nil, fmt.Errorf("RSA-OAEP mask generation function %q is not supported", name)
		}
	}
	if p := method.FindElement("./OAEPparams"); p != nil && strings.TrimSpace(p.Text()) != "" {
		return nil, errors.New("RSA-OAEP parameters are not supported")
	}
	ct, err := cipherValue(el)
	if err != nil {
		return nil, err
	}
	return priv.Decrypt(nil, ct, &rsa.OAEPOptions{Hash: h, MGFHash: mgf})
}

func cipherValue(el *etree.Element) ([]byte, error) {
	cv := el.FindElement("./CipherData/CipherValue")
	if cv == nil {
		return nil, errors.New("no CipherData/CipherValue")
	}
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(cv.Text()), ""))
}

func registerDecrypters() {
	for _, d := range []xmlenc.Decrypter{
		gcmDecrypter{"http://www.w3.org/2009/xmlenc11#aes128-gcm", 16},
		gcmDecrypter{"http://www.w3.org/2009/xmlenc11#aes192-gcm", 24},
		gcmDecrypter{"http://www.w3.org/2009/xmlenc11#aes256-gcm", 32},
		oaepDecrypter{"http://www.w3.org/2001/04/xmlenc#rsa-oaep-mgf1p"},
		oaepDecrypter{"http://www.w3.org/2009/xmlenc11#rsa-oaep"},
	} {
		xmlenc.RegisterDecrypter(d)
	}
}
