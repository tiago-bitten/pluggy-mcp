package main

import (
	"testing"
	"time"
)

// Todos os dados aqui são sintéticos.

func ptr[T any](v T) *T { return &v }

func mustTime(t *testing.T, s string) pTime {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return pTime{v}
}

var (
	bankAcc = Account{ID: "acc-bank", Type: "BANK", Name: "Conta Teste", CurrencyCode: "BRL"}
	cardAcc = Account{ID: "acc-card", Type: "CREDIT", Name: "Cartão Teste", CurrencyCode: "BRL"}
)

func TestSignNormalization(t *testing.T) {
	cases := []struct {
		name   string
		acc    Account
		amount float64
		typ    string
		want   int64
	}{
		{"conta saída", bankAcc, -50.10, "DEBIT", -5010},
		{"conta entrada", bankAcc, 1200, "CREDIT", 120000},
		{"cartão compra (positivo na Pluggy)", cardAcc, 89.90, "DEBIT", -8990},
		{"cartão estorno/pagamento (negativo na Pluggy)", cardAcc, -30, "CREDIT", 3000},
		{"conta sem type usa sinal original", bankAcc, -10, "", -1000},
		{"cartão sem type inverte sinal", cardAcc, 10, "", -1000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := normalizeTx(Transaction{Amount: c.amount, Type: c.typ, CurrencyCode: "BRL"}, c.acc)
			if n.Cents != c.want {
				t.Errorf("Cents = %d, quer %d", n.Cents, c.want)
			}
		})
	}
}

func TestCentsRounding(t *testing.T) {
	// 0.1 + 0.2 em float não é 0.3; somando em centavos é.
	var total int64
	for _, v := range []float64{0.1, 0.2, 19.99, 0.01} {
		total += toCents(v)
	}
	if total != 2030 {
		t.Errorf("total = %d, quer 2030", total)
	}
	if got := fmtCents(-123405); got != "-1234.05" {
		t.Errorf("fmtCents = %q", got)
	}
	if got := fmtCents(7); got != "0.07" {
		t.Errorf("fmtCents = %q", got)
	}
}

func TestCardDatesAndInstallments(t *testing.T) {
	tx := Transaction{
		Amount: 100, Type: "DEBIT", CurrencyCode: "BRL",
		// 03:00Z = meia-noite em Brasília: o dia continua sendo 10/05.
		Date: mustTime(t, "2026-05-10T03:00:00Z"),
		CreditCardMetadata: &CreditCardMetadata{
			InstallmentNumber: ptr(3),
			TotalInstallments: ptr(10),
			PurchaseDate:      ptr(mustTime(t, "2026-03-02T15:00:00Z")),
			BillID:            ptr("bill-1"),
		},
	}
	n := normalizeTx(tx, cardAcc)
	if n.Date != "2026-05-10" {
		t.Errorf("Date = %q", n.Date)
	}
	if n.PurchaseDate != "2026-03-02" {
		t.Errorf("PurchaseDate = %q", n.PurchaseDate)
	}
	if n.Installment != "3/10" {
		t.Errorf("Installment = %q", n.Installment)
	}
	if n.BillID != "bill-1" {
		t.Errorf("BillID = %q", n.BillID)
	}
}

func TestPurchaseDateHiddenWhenSameDay(t *testing.T) {
	tx := Transaction{
		Amount: 10, Type: "DEBIT",
		Date: mustTime(t, "2026-05-10T03:00:00Z"),
		CreditCardMetadata: &CreditCardMetadata{
			PurchaseDate:      ptr(mustTime(t, "2026-05-10T18:30:00Z")),
			InstallmentNumber: ptr(1), TotalInstallments: ptr(1),
		},
	}
	n := normalizeTx(tx, cardAcc)
	if n.PurchaseDate != "" || n.Installment != "" {
		t.Errorf("PurchaseDate=%q Installment=%q, quer vazios", n.PurchaseDate, n.Installment)
	}
}

func TestDayUsesBrasiliaTime(t *testing.T) {
	// 01:20Z ainda é o dia anterior em Brasília.
	if got := day(mustTime(t, "2026-07-02T01:20:00Z").Time); got != "2026-07-01" {
		t.Errorf("day = %q", got)
	}
}

func TestInternationalCurrency(t *testing.T) {
	tx := Transaction{Amount: 20, AmountInAccountCurrency: ptr(110.37), CurrencyCode: "USD", Type: "DEBIT"}
	n := normalizeTx(tx, cardAcc)
	if n.Cents != -11037 || n.Currency != "BRL" {
		t.Errorf("convertido = %d %s", n.Cents, n.Currency)
	}
	if n.OrigCents != -2000 || n.OrigCurrency != "USD" {
		t.Errorf("original = %d %s", n.OrigCents, n.OrigCurrency)
	}

	// Convertido com sinal invertido continua seguindo o sinal do original.
	tx.AmountInAccountCurrency = ptr(-110.37)
	tx.Type = ""
	if n := normalizeTx(tx, cardAcc); n.Cents != -11037 {
		t.Errorf("convertido (sinal invertido) = %d", n.Cents)
	}
}

func TestInternationalWithoutConversion(t *testing.T) {
	n := normalizeTx(Transaction{Amount: 5, CurrencyCode: "EUR", Type: "DEBIT"}, cardAcc)
	if !n.Unconverted || n.Currency != "EUR" || n.Cents != -500 {
		t.Errorf("got %+v", n)
	}
}

func TestSameCurrencyIgnoresConverted(t *testing.T) {
	n := normalizeTx(Transaction{Amount: 50, AmountInAccountCurrency: ptr(49.0), CurrencyCode: "BRL", Type: "DEBIT"}, cardAcc)
	if n.Cents != -5000 || n.OrigCurrency != "" {
		t.Errorf("got %+v", n)
	}
}

func TestPTimeFormats(t *testing.T) {
	for _, s := range []string{`"2026-01-02T03:00:00.000Z"`, `"2026-01-02"`, `"2026-01-02T00:00:00"`} {
		var p pTime
		if err := p.UnmarshalJSON([]byte(s)); err != nil || p.IsZero() {
			t.Errorf("%s: err=%v", s, err)
		}
	}
	var p pTime
	if err := p.UnmarshalJSON([]byte(`null`)); err != nil || !p.IsZero() {
		t.Errorf("null: err=%v", err)
	}
}

func TestCursorFromNext(t *testing.T) {
	for _, next := range []string{
		"?accountId=x&after=abc%3D%3D",
		"accountId=x&after=abc%3D%3D",
		"https://api.pluggy.ai/v2/transactions?accountId=x&after=abc%3D%3D",
	} {
		got, err := cursorFromNext(next)
		if err != nil || got != "abc==" {
			t.Errorf("%s: got %q err=%v", next, got, err)
		}
	}
}
