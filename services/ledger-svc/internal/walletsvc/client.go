package walletsvc

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	clientCertFile := os.Getenv("LEDGER_SVC_CLIENT_CERT_FILE")
	clientKeyFile := os.Getenv("LEDGER_SVC_CLIENT_KEY_FILE")
	caCertFile := os.Getenv("LEDGER_SVC_CA_FILE")

	if clientCertFile == "" || clientKeyFile == "" || caCertFile == "" {
		panic(fmt.Sprintf(
			"missing required env vars for mTLS: LEDGER_SVC_CLIENT_CERT_FILE=%s, LEDGER_SVC_CLIENT_KEY_FILE=%s, LEDGER_SVC_CA_FILE=%s",
			clientCertFile, clientKeyFile, caCertFile,
		))
	}

	cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
	if err != nil {
		panic(fmt.Sprintf("failed to load client certificate: %v", err))
	}

	caCertPEM, err := os.ReadFile(caCertFile)
	if err != nil {
		panic(fmt.Sprintf("failed to read CA certificate file: %v", err))
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCertPEM) {
		panic("failed to parse CA certificate")
	}
	slog.Info("mTLS client configured", "cert", clientCertFile, "key", clientKeyFile, "ca", caCertFile, "subject", cert.Leaf.Subject)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		MinVersion:   tls.VersionTLS12,
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	slog.Info("mTLS client configured for outgoing calls to ledger-svc")

	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   8 * time.Second,
			Transport: transport,
		},
	}
}

func (c *Client) GetWallet(ctx context.Context, id string) (*domain.Wallet, error) {
	url := fmt.Sprintf("%s/wallets/%s", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("walletsvc.GetWallet: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("walletsvc.GetWallet: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, domain.ErrWalletNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := readBody(resp)
		return nil, fmt.Errorf("walletsvc.GetWallet: %s (status %d)", body, resp.StatusCode)
	}

	var w domain.Wallet
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return nil, fmt.Errorf("walletsvc.GetWallet: decode: %w", err)
	}
	return &w, nil
}

type depositRequest struct {
	Amount      int64  `json:"amount"`
	ReferenceID string `json:"reference_id"`
}

func (c *Client) Deposit(ctx context.Context, walletID string, amount int64, refID string) error {
	return c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/internal/wallets/%s/credit", walletID), depositRequest{
		Amount:      amount,
		ReferenceID: refID,
	})
}

type withdrawRequest struct {
	Amount      int64  `json:"amount"`
	ReferenceID string `json:"reference_id"`
}

func (c *Client) Withdraw(ctx context.Context, walletID string, amount int64, refID string) error {
	url := fmt.Sprintf("%s/wallets/%s/withdraw", c.baseURL, walletID)
	body, err := json.Marshal(withdrawRequest{Amount: amount, ReferenceID: refID})
	if err != nil {
		return fmt.Errorf("walletsvc.Withdraw: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("walletsvc.Withdraw: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("walletsvc.Withdraw: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusPaymentRequired:
		return domain.ErrInsufficientFunds
	case http.StatusNotFound:
		return domain.ErrWalletNotFound
	default:
		respBody, _ := readBody(resp)
		return fmt.Errorf("walletsvc.Withdraw: %s (status %d)", respBody, resp.StatusCode)
	}
}

func (c *Client) doRequest(ctx context.Context, method, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("walletsvc%s: marshal: %w", path, err)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("walletsvc%s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("walletsvc%s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := readBody(resp)
		return fmt.Errorf("walletsvc%s: %s (status %d)", path, respBody, resp.StatusCode)
	}
	return nil
}

func readBody(resp *http.Response) (string, error) {
	buf := make([]byte, 512)
	n, err := resp.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return "", err
	}
	return string(buf[:n]), nil
}
