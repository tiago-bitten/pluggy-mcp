package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Horário de Brasília (sem horário de verão desde 2019). A Pluggy devolve
// timestamps em UTC; o dia do lançamento é o dia em Brasília.
var brt = time.FixedZone("BRT", -3*60*60)

func toCents(v float64) int64 { return int64(math.Round(v * 100)) }

func absCents(c int64) int64 {
	if c < 0 {
		return -c
	}
	return c
}

// fmtCents formata centavos como "-1234.56".
func fmtCents(c int64) string {
	sign := ""
	if c < 0 {
		sign = "-"
	}
	a := absCents(c)
	return fmt.Sprintf("%s%d.%02d", sign, a/100, a%100)
}

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(brt).Format("2006-01-02")
}

// NTx é uma transação normalizada: Cents < 0 sempre significa dinheiro saindo.
type NTx struct {
	ID           string
	AccountID    string
	AccountName  string
	Card         bool
	Date         string // data de lançamento (Brasília)
	PurchaseDate string // data da compra, só quando difere do lançamento
	Installment  string // "3/10"
	Description  string
	Cents        int64  // na moeda da conta (ou na original, se Unconverted)
	Currency     string // moeda de Cents
	OrigCents    int64  // valor na moeda original (compras internacionais)
	OrigCurrency string // vazio quando não há conversão
	Unconverted  bool   // moeda difere da conta e a Pluggy não mandou o valor convertido
	Category     string
	BillID       string
	Status       string
}

// signed aplica a convenção "negativo = saída". O campo type (DEBIT/CREDIT)
// é o mais confiável; se vier vazio, usa a convenção da Pluggy por tipo de
// conta: no cartão positivo é compra, na conta positivo é entrada.
func signed(amount float64, txType, accountType string) int64 {
	c := toCents(amount)
	switch strings.ToUpper(txType) {
	case "DEBIT":
		return -absCents(c)
	case "CREDIT":
		return absCents(c)
	}
	if accountType == "CREDIT" {
		return -c
	}
	return c
}

func normalizeTx(t Transaction, a Account) NTx {
	n := NTx{
		ID:          t.ID,
		AccountID:   a.ID,
		AccountName: a.DisplayName(),
		Card:        a.Type == "CREDIT",
		Date:        day(t.Date.Time),
		Description: strings.TrimSpace(t.Description),
		Status:      t.Status,
	}
	if t.Category != nil {
		n.Category = *t.Category
	}

	accCur := a.CurrencyCode
	if accCur == "" {
		accCur = "BRL"
	}
	txCur := t.CurrencyCode
	if txCur == "" {
		txCur = accCur
	}
	n.Currency = accCur
	n.Cents = signed(t.Amount, t.Type, a.Type)
	if txCur != accCur {
		if t.AmountInAccountCurrency != nil && *t.AmountInAccountCurrency != 0 {
			n.OrigCents = n.Cents
			n.OrigCurrency = txCur
			n.Cents = signed(*t.AmountInAccountCurrency, t.Type, a.Type)
			// O convertido segue o sinal do original, seja qual for o sinal enviado.
			if (n.Cents < 0) != (n.OrigCents < 0) {
				n.Cents = -n.Cents
			}
		} else {
			n.Currency = txCur
			n.Unconverted = true
		}
	}

	if m := t.CreditCardMetadata; m != nil {
		if m.PurchaseDate != nil {
			if pd := day(m.PurchaseDate.Time); pd != "" && pd != n.Date {
				n.PurchaseDate = pd
			}
		}
		if m.InstallmentNumber != nil && m.TotalInstallments != nil && *m.TotalInstallments > 1 {
			n.Installment = fmt.Sprintf("%d/%d", *m.InstallmentNumber, *m.TotalInstallments)
		}
		if m.BillID != nil {
			n.BillID = *m.BillID
		}
	}
	return n
}

// isCardBillPayment identifica pagamento de fatura na conta corrente, que não
// deve ser somado como gasto (as compras já aparecem no cartão).
func isCardBillPayment(n NTx) bool {
	c := strings.ToLower(n.Category)
	return !n.Card && strings.Contains(c, "credit card payment")
}

// tsvClean remove tabs/quebras de linha que quebrariam a tabela.
func tsvClean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
