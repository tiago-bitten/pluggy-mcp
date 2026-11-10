package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pluggyBaseURL  = "https://api.pluggy.ai"
	requestTimeout = 30 * time.Second
	// A API key vale 2h; renovamos com folga.
	apiKeyTTL = 110 * time.Minute
	pageSize  = 500
)

var errUnauthorized = errors.New("pluggy: não autorizado (401)")

// Client é um cliente somente leitura da API da Pluggy.
type Client struct {
	http         *http.Client
	clientID     string
	clientSecret string

	mu      sync.Mutex
	apiKey  string
	expires time.Time
}

func NewClient(cfg *Config) *Client {
	return &Client{
		http:         &http.Client{Timeout: requestTimeout},
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
	}
}

// key devolve uma API key válida. Se stale não for vazio, ela é descartada
// (caso ainda seja a atual) e uma nova é gerada.
func (c *Client) key(ctx context.Context, stale string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apiKey != "" && c.apiKey != stale && time.Now().Before(c.expires) {
		return c.apiKey, nil
	}

	body, _ := json.Marshal(map[string]string{
		"clientId":     c.clientID,
		"clientSecret": c.clientSecret,
	})
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pluggyBaseURL+"/auth", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("pluggy auth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pluggy auth: HTTP %d (verifique PLUGGY_CLIENT_ID/PLUGGY_CLIENT_SECRET)", resp.StatusCode)
	}
	var out struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("pluggy auth: resposta inválida: %w", err)
	}
	if out.APIKey == "" {
		return "", errors.New("pluggy auth: resposta sem apiKey")
	}
	c.apiKey = out.APIKey
	c.expires = time.Now().Add(apiKeyTTL)
	return c.apiKey, nil
}

// get faz um GET autenticado e decodifica o JSON em out. Em caso de 401,
// renova a API key e tenta uma vez mais.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	key, err := c.key(ctx, "")
	if err != nil {
		return err
	}
	err = c.doGet(ctx, key, path, q, out)
	if errors.Is(err, errUnauthorized) {
		if key, err = c.key(ctx, key); err != nil {
			return err
		}
		err = c.doGet(ctx, key, path, q, out)
	}
	return err
}

func (c *Client) doGet(ctx context.Context, key, path string, q url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	u := pluggyBaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("pluggy GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		// Só o código e a mensagem genérica da API; nunca o corpo inteiro.
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		msg := e.Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("pluggy GET %s: HTTP %d %s", path, resp.StatusCode, msg)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("pluggy GET %s: resposta inválida: %w", path, err)
	}
	return nil
}

// ---- Modelos (só os campos que usamos) ----

// pTime aceita "2006-01-02T15:04:05.000Z" e "2006-01-02".
type pTime struct{ time.Time }

func (t *pTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("data inválida: %q", s)
}

type Item struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	ExecutionStatus string `json:"executionStatus"`
	LastUpdatedAt   *pTime `json:"lastUpdatedAt"`
	NextAutoSyncAt  *pTime `json:"nextAutoSyncAt"`
	Connector       struct {
		Name string `json:"name"`
	} `json:"connector"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type Account struct {
	ID            string  `json:"id"`
	ItemID        string  `json:"itemId"`
	Type          string  `json:"type"`    // BANK | CREDIT
	Subtype       string  `json:"subtype"` // CHECKING_ACCOUNT | SAVINGS_ACCOUNT | CREDIT_CARD
	Name          string  `json:"name"`
	MarketingName *string `json:"marketingName"`
	Balance       float64 `json:"balance"`
	CurrencyCode  string  `json:"currencyCode"`
	CreditData    *struct {
		CreditLimit          *float64 `json:"creditLimit"`
		AvailableCreditLimit *float64 `json:"availableCreditLimit"`
		BalanceCloseDate     *pTime   `json:"balanceCloseDate"`
		BalanceDueDate       *pTime   `json:"balanceDueDate"`
	} `json:"creditData"`
}

func (a *Account) DisplayName() string {
	if a.MarketingName != nil && *a.MarketingName != "" {
		return *a.MarketingName
	}
	return a.Name
}

type CreditCardMetadata struct {
	InstallmentNumber *int     `json:"installmentNumber"`
	TotalInstallments *int     `json:"totalInstallments"`
	TotalAmount       *float64 `json:"totalAmount"`
	PurchaseDate      *pTime   `json:"purchaseDate"`
	BillID            *string  `json:"billId"`
}

type Transaction struct {
	ID                      string              `json:"id"`
	AccountID               string              `json:"accountId"`
	Description             string              `json:"description"`
	Date                    pTime               `json:"date"`
	Amount                  float64             `json:"amount"`
	AmountInAccountCurrency *float64            `json:"amountInAccountCurrency"`
	CurrencyCode            string              `json:"currencyCode"`
	Category                *string             `json:"category"`
	Type                    string              `json:"type"`   // DEBIT | CREDIT
	Status                  string              `json:"status"` // POSTED | PENDING
	CreditCardMetadata      *CreditCardMetadata `json:"creditCardMetadata"`
}

type FinanceCharge struct {
	Type         string  `json:"type"`
	Amount       float64 `json:"amount"`
	CurrencyCode string  `json:"currencyCode"`
}

type BillPayment struct {
	ValueType    string  `json:"valueType"`
	PaymentDate  *pTime  `json:"paymentDate"`
	PaymentMode  string  `json:"paymentMode"`
	Amount       float64 `json:"amount"`
	CurrencyCode string  `json:"currencyCode"`
}

type Bill struct {
	ID                      string          `json:"id"`
	DueDate                 pTime           `json:"dueDate"`
	BillClosingDate         *pTime          `json:"billClosingDate"`
	TotalAmount             float64         `json:"totalAmount"`
	TotalAmountCurrencyCode string          `json:"totalAmountCurrencyCode"`
	MinimumPaymentAmount    *float64        `json:"minimumPaymentAmount"`
	FinanceCharges          []FinanceCharge `json:"financeCharges"`
	Payments                []BillPayment   `json:"payments"`
}

type Investment struct {
	ID                   string   `json:"id"`
	ItemID               string   `json:"itemId"`
	Type                 string   `json:"type"`
	Subtype              *string  `json:"subtype"`
	Name                 string   `json:"name"`
	Balance              float64  `json:"balance"`
	Amount               *float64 `json:"amount"`
	AmountOriginal       *float64 `json:"amountOriginal"`
	AmountProfit         *float64 `json:"amountProfit"`
	CurrencyCode         string   `json:"currencyCode"`
	Status               *string  `json:"status"`
	AnnualRate           *float64 `json:"annualRate"`
	LastMonthRate        *float64 `json:"lastMonthRate"`
	LastTwelveMonthsRate *float64 `json:"lastTwelveMonthsRate"`
	Rate                 *float64 `json:"rate"`
	RateType             *string  `json:"rateType"`
	FixedAnnualRate      *float64 `json:"fixedAnnualRate"`
	DueDate              *pTime   `json:"dueDate"`
}

// ---- Endpoints ----

type page[T any] struct {
	Page       int `json:"page"`
	TotalPages int `json:"totalPages"`
	Results    []T `json:"results"`
}

// getAllPages percorre endpoints paginados por número de página.
func getAllPages[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	var all []T
	q.Set("pageSize", strconv.Itoa(pageSize))
	for p := 1; ; p++ {
		q.Set("page", strconv.Itoa(p))
		var res page[T]
		if err := c.get(ctx, path, q, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Results...)
		if p >= res.TotalPages || len(res.Results) == 0 {
			return all, nil
		}
	}
}

func (c *Client) GetItem(ctx context.Context, id string) (*Item, error) {
	var it Item
	if err := c.get(ctx, "/items/"+url.PathEscape(id), nil, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

func (c *Client) ListAccounts(ctx context.Context, itemID string) ([]Account, error) {
	return getAllPages[Account](ctx, c, "/accounts", url.Values{"itemId": {itemID}})
}

func (c *Client) ListBills(ctx context.Context, accountID string) ([]Bill, error) {
	return getAllPages[Bill](ctx, c, "/bills", url.Values{"accountId": {accountID}})
}

func (c *Client) ListInvestments(ctx context.Context, itemID string) ([]Investment, error) {
	return getAllPages[Investment](ctx, c, "/investments", url.Values{"itemId": {itemID}})
}

// ListTransactions busca todas as transações de uma conta no intervalo
// [from, to] (datas inclusivas), seguindo o cursor do GET /v2/transactions.
func (c *Client) ListTransactions(ctx context.Context, accountID string, from, to time.Time) ([]Transaction, error) {
	q := url.Values{
		"accountId": {accountID},
		"dateFrom":  {from.Format("2006-01-02")},
		"dateTo":    {to.Format("2006-01-02")},
	}
	var all []Transaction
	for {
		var res struct {
			Results []Transaction `json:"results"`
			Next    *string       `json:"next"`
		}
		if err := c.get(ctx, "/v2/transactions", q, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Results...)
		if res.Next == nil || *res.Next == "" || len(res.Results) == 0 {
			return all, nil
		}
		after, err := cursorFromNext(*res.Next)
		if err != nil || after == "" || after == q.Get("after") {
			return all, nil
		}
		q.Set("after", after)
	}
}

// cursorFromNext extrai o parâmetro "after" do campo next, que pode vir como
// query string ("?accountId=...&after=...") ou URL completa.
func cursorFromNext(next string) (string, error) {
	s := next
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[i+1:]
	}
	v, err := url.ParseQuery(s)
	if err != nil {
		return "", err
	}
	return v.Get("after"), nil
}
