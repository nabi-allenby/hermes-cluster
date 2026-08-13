// T01 spike PoC — out-of-cluster verification of a projected ServiceAccount
// token via JWKS, enforcing token.kubernetes.io.pod.name == gatewayId.
// Stdlib only; models exactly what a JWKS-verifying connector would do.
// Spike evidence only — NOT production code.
//
// Usage:
//
//	go run verify.go -jwks jwks.json -iss <issuer> -aud <audience> \
//	    -gateway-id <id> -token-file token.jwt
package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
)

type jwks struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

type claims struct {
	Iss        string   `json:"iss"`
	Sub        string   `json:"sub"`
	Aud        audience `json:"aud"`
	Exp        int64    `json:"exp"`
	Nbf        int64    `json:"nbf"`
	Kubernetes struct {
		Namespace string `json:"namespace"`
		Pod       struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"pod"`
		ServiceAccount struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"serviceaccount"`
	} `json:"kubernetes.io"`
}

// aud is a string in single-audience tokens, an array otherwise.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = []string{s}
		return nil
	}
	var ss []string
	if err := json.Unmarshal(b, &ss); err != nil {
		return err
	}
	*a = ss
	return nil
}

func fail(step, format string, args ...any) {
	fmt.Printf("FAIL [%s] %s\n", step, fmt.Sprintf(format, args...))
	os.Exit(1)
}

func ok(step, format string, args ...any) {
	fmt.Printf("ok   [%s] %s\n", step, fmt.Sprintf(format, args...))
}

func main() {
	jwksPath := flag.String("jwks", "", "path to JWKS JSON")
	iss := flag.String("iss", "", "expected issuer")
	aud := flag.String("aud", "", "expected audience")
	gatewayID := flag.String("gateway-id", "", "gatewayId the caller presented")
	tokenFile := flag.String("token-file", "", "path to the JWT")
	flag.Parse()

	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		fail("read", "%v", err)
	}
	token := strings.TrimSpace(string(raw))
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		fail("parse", "not a JWS compact serialization (%d parts)", len(parts))
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		fail("parse", "header b64: %v", err)
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		fail("parse", "header json: %v", err)
	}
	if header.Alg != "RS256" {
		fail("alg", "unexpected alg %q (kind/minikube service-account keys are RSA)", header.Alg)
	}
	ok("alg", "RS256, kid=%s", header.Kid)

	kb, err := os.ReadFile(*jwksPath)
	if err != nil {
		fail("jwks", "%v", err)
	}
	var keys jwks
	if err := json.Unmarshal(kb, &keys); err != nil {
		fail("jwks", "%v", err)
	}
	var pub *rsa.PublicKey
	for _, k := range keys.Keys {
		if k.Kid != header.Kid || k.Kty != "RSA" {
			continue
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			fail("jwks", "modulus b64: %v", err)
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			fail("jwks", "exponent b64: %v", err)
		}
		pub = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	}
	if pub == nil {
		fail("jwks", "no RSA key with kid=%s in JWKS", header.Kid)
	}
	ok("jwks", "matched kid in JWKS (%d key(s))", len(keys.Keys))

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		fail("sig", "b64: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		fail("sig", "RS256 signature invalid: %v", err)
	}
	ok("sig", "RS256 signature valid")

	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		fail("claims", "b64: %v", err)
	}
	var c claims
	if err := json.Unmarshal(cb, &c); err != nil {
		fail("claims", "json: %v", err)
	}

	if c.Iss != *iss {
		fail("iss", "issuer %q != expected %q", c.Iss, *iss)
	}
	ok("iss", "%s", c.Iss)

	found := false
	for _, a := range c.Aud {
		if a == *aud {
			found = true
		}
	}
	if !found {
		fail("aud", "audience %v does not include %q", c.Aud, *aud)
	}
	ok("aud", "%v includes %q", c.Aud, *aud)

	now := time.Now().Unix()
	if now >= c.Exp {
		fail("exp", "token expired %ds ago (exp=%d now=%d)", now-c.Exp, c.Exp, now)
	}
	if now < c.Nbf {
		fail("nbf", "token not yet valid")
	}
	ok("exp", "valid for another %ds", c.Exp-now)

	if c.Kubernetes.Pod.Name == "" {
		fail("bind", "no kubernetes.io/pod claim — token is not pod-bound")
	}
	ok("bind", "pod-bound: pod=%s uid=%s sa=%s ns=%s",
		c.Kubernetes.Pod.Name, c.Kubernetes.Pod.UID,
		c.Kubernetes.ServiceAccount.Name, c.Kubernetes.Namespace)

	if c.Kubernetes.Pod.Name != *gatewayID {
		fail("gateway", "token.pod.name %q != presented gatewayId %q — REJECT provision",
			c.Kubernetes.Pod.Name, *gatewayID)
	}
	ok("gateway", "token.pod.name == gatewayId (%s) — provision allowed", *gatewayID)
	fmt.Println("VERDICT: ACCEPT")
}
