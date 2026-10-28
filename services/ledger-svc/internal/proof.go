package internal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type ProofEngine struct {
	secretKey []byte
}

func NewProofEngine(key string) *ProofEngine {
	return &ProofEngine{secretKey: []byte(key)}
}

func (p *ProofEngine) GenerateBalanceProof(walletID string, balance int64, nonce string) (string, error) {
	if len(p.secretKey) == 0 {
		return "", fmt.Errorf("proof secret key not configured")
	}
	message := fmt.Sprintf("%s:%d:%s", walletID, balance, nonce)
	mac := hmac.New(sha256.New, p.secretKey)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil)), nil
}
