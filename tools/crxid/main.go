//go:build ignore

package main

// Computes the Chrome extension ID from a .pem private key:
// ID = first 16 bytes of SHA256(SPKI DER public key), hex digits mapped to a-p.

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

func main() {
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		panic("no pem")
	}
	var pub rsa.PublicKey
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		pub = key.PublicKey
	} else if k8, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		switch k := k8.(type) {
		case *rsa.PrivateKey:
			pub = k.PublicKey
		default:
			panic("unsupported key type")
		}
	} else {
		panic("cannot parse key")
	}
	der, err := x509.MarshalPKIXPublicKey(&pub)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(der)
	id := make([]byte, 32)
	for i := 0; i < 32; i++ {
		nib := sum[i/2] >> 4
		if i%2 == 1 {
			nib = sum[i/2] & 0x0f
		}
		id[i] = 'a' + nib
	}
	fmt.Println(string(id))
}
