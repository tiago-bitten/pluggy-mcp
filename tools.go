package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverInstructions = `Acesso somente leitura aos dados de Open Finance do usuário via Pluggy (conector MeuPluggy).
Valores normalizados: negativo = dinheiro saindo, positivo = entrando. Valores em reais com ponto decimal.
Os dados são sincronizados pelo Meu Pluggy a cada ~24h e NÃO podem ser atualizados via API; se o usuário pedir dados mais recentes, explique isso e indique status_conexoes para ver a última atualização.`

const refreshNote = "Obs.: itens do MeuPluggy são sincronizados automaticamente a cada ~24h e não podem ser atualizados via API."

type Server struct {
	c       *Client
	itemIDs []string
}

func newMCPServer(c *Client, itemIDs []string) *mcp.Server {
	s := &Server{c: c, itemIDs: itemIDs}
	srv := mcp.NewServer(&mcp.Implementation{Name: "pluggy", Version: "1.0.0"}, &mcp.ServerOptions{Instructions: serverInstructions})

	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptrBool(false)}
	mcp.AddTool(srv, &mcp.Tool{Name: "listar_contas", Annotations: ro,
		Description: "Lista contas corrente, poupança e cartões de crédito de todos os Items, com tipo, nome e saldo (no cartão: saldo devedor, limite e datas da fatura atual)."},
		s.listarContas)
	mcp.AddTool(srv, &mcp.Tool{Name: "listar_transacoes", Annotations: ro,
		Description: "Lista transações (TSV). Filtros opcionais: conta, período (padrão: últimos 30 dias), texto, faixa de valor absoluto, tipo (saida/entrada) e categoria. Valor negativo = saída. No cartão, 'data' é o lançamento e 'compra' a data original quando diferente; 'parcela' no formato 3/10."},
		s.listarTransacoes)
	mcp.AddTool(srv, &mcp.Tool{Name: "resumo_gastos", Annotations: ro,
		Description: "Gastos (saídas) agrupados por categoria em um período, separando conta e cartão, com total geral. Pagamentos de fatura na conta são excluídos do total para não contar em dobro."},
		s.resumoGastos)
	mcp.AddTool(srv, &mcp.Tool{Name: "listar_faturas_cartao", Annotations: ro,
		Description: "Lista faturas dos cartões: fechamento, vencimento, valor total, mínimo, pagamentos e encargos. Inclui a fatura aberta (id 'aberta')."},
		s.listarFaturas)
	mcp.AddTool(srv, &mcp.Tool{Name: "transacoes_da_fatura", Annotations: ro,
		Description: "Transações que compõem uma fatura específica (id de listar_faturas_cartao, ou 'aberta' para a fatura atual ainda não fechada)."},
		s.transacoesDaFatura)
	mcp.AddTool(srv, &mcp.Tool{Name: "listar_investimentos", Annotations: ro,
		Description: "Posições atuais de investimentos com tipo, nome, saldo, moeda e rentabilidade quando disponível."},
		s.listarInvestimentos)
	mcp.AddTool(srv, &mcp.Tool{Name: "status_conexoes", Annotations: ro,
		Description: "Status de cada conexão (Item) e data da última sincronização."},
		s.statusConexoes)
	return srv
}

func ptrBool(b bool) *bool { return &b }

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// ---- helpers ----

func (s *Server) allAccounts(ctx context.Context) ([]Account, error) {
	var all []Account
	for _, id := range s.itemIDs {
		accs, err := s.c.ListAccounts(ctx, id)
		if err != nil {
			return nil, err
		}
		all = append(all, accs...)
	}
	return all, nil
}

// selectAccounts filtra por ID exato ou trecho do nome (sem diferenciar maiúsculas).
func selectAccounts(accs []Account, filter string, onlyCards bool) ([]Account, error) {
	var out []Account
	f := strings.ToLower(strings.TrimSpace(filter))
	for _, a := range accs {
		if onlyCards && a.Type != "CREDIT" {
			continue
		}
		if f == "" || a.ID == filter || strings.Contains(strings.ToLower(a.DisplayName()), f) ||
			strings.Contains(strings.ToLower(a.Name), f) || strings.Contains(strings.ToLower(subtypeLabel(a)), f) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nenhuma conta corresponde a %q; use listar_contas para ver os IDs", filter)
	}
	return out, nil
}

func subtypeLabel(a Account) string {
	switch a.Subtype {
	case "CHECKING_ACCOUNT":
		return "corrente"
	case "SAVINGS_ACCOUNT":
		return "poupança"
	case "CREDIT_CARD":
		return "cartão"
	}
	if a.Type == "CREDIT" {
		return "cartão"
	}
	return strings.ToLower(a.Subtype)
}

func parseDate(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), brt)
	if err != nil {
		return time.Time{}, fmt.Errorf("data inválida %q (use AAAA-MM-DD)", s)
	}
	return t, nil
}

// period resolve o intervalo [from, to] em dias de Brasília; padrão: últimos 30 dias.
func period(from, to string) (time.Time, time.Time, error) {
	now := time.Now().In(brt)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, brt)
	start := end.AddDate(0, 0, -30)
	var err error
	if to != "" {
		if end, err = parseDate(to); err != nil {
			return end, end, err
		}
		if from == "" {
			start = end.AddDate(0, 0, -30)
		}
	}
	if from != "" {
		if start, err = parseDate(from); err != nil {
			return start, start, err
		}
	}
	if start.After(end) {
		return start, end, errors.New("data_inicial é posterior a data_final")
	}
	return start, end, nil
}

// fetchTxs busca e normaliza as transações das contas em [from, to] (dias de
// Brasília). A consulta à API usa um dia de folga em cada ponta por causa do fuso.
func (s *Server) fetchTxs(ctx context.Context, accs []Account, from, to time.Time) ([]NTx, error) {
	fromS, toS := from.Format("2006-01-02"), to.Format("2006-01-02")
	var out []NTx
	for _, a := range accs {
		txs, err := s.c.ListTransactions(ctx, a.ID, from.AddDate(0, 0, -1), to.AddDate(0, 0, 1))
		if err != nil {
			return nil, err
		}
		for _, t := range txs {
			n := normalizeTx(t, a)
			if n.Date >= fromS && n.Date <= toS {
				out = append(out, n)
			}
		}
	}
	sortTxs(out)
	return out, nil
}

func sortTxs(txs []NTx) {
	sort.SliceStable(txs, func(i, j int) bool {
		if txs[i].Date != txs[j].Date {
			return txs[i].Date > txs[j].Date
		}
		return txs[i].Description < txs[j].Description
	})
}

func writeTxTable(b *strings.Builder, txs []NTx, limit int) {
	b.WriteString("data\tcompra\tparcela\tconta\tdescricao\tvalor\tmoeda\toriginal\tcategoria\tstatus\n")
	for i, t := range txs {
		if limit > 0 && i >= limit {
			fmt.Fprintf(b, "... %d transações omitidas (aumente 'limite' ou refine os filtros)\n", len(txs)-limit)
			break
		}
		orig := ""
		if t.OrigCurrency != "" {
			orig = fmtCents(t.OrigCents) + " " + t.OrigCurrency
		}
		fmt.Fprintf(b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			t.Date, t.PurchaseDate, t.Installment, tsvClean(t.AccountName), tsvClean(t.Description),
			fmtCents(t.Cents), t.Currency, orig, tsvClean(t.Category), t.Status)
	}
}

// totals soma saídas e entradas por moeda.
func writeTotals(b *strings.Builder, txs []NTx) {
	type tot struct{ out, in int64 }
	m := map[string]*tot{}
	var curs []string
	for _, t := range txs {
		x := m[t.Currency]
		if x == nil {
			x = &tot{}
			m[t.Currency] = x
			curs = append(curs, t.Currency)
		}
		if t.Cents < 0 {
			x.out += t.Cents
		} else {
			x.in += t.Cents
		}
	}
	sort.Strings(curs)
	for _, c := range curs {
		x := m[c]
		fmt.Fprintf(b, "totais %s: saidas %s | entradas %s | liquido %s\n", c, fmtCents(x.out), fmtCents(x.in), fmtCents(x.out+x.in))
	}
}

// ---- 1. listar_contas ----

func (s *Server) listarContas(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	accs, err := s.allAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	b.WriteString("id\ttipo\tnome\tsaldo\tmoeda\tlimite\tdisponivel\tfechamento\tvencimento\n")
	for _, a := range accs {
		lim, disp, fech, venc := "", "", "", ""
		if cd := a.CreditData; cd != nil {
			if cd.CreditLimit != nil {
				lim = fmtCents(toCents(*cd.CreditLimit))
			}
			if cd.AvailableCreditLimit != nil {
				disp = fmtCents(toCents(*cd.AvailableCreditLimit))
			}
			if cd.BalanceCloseDate != nil {
				fech = day(cd.BalanceCloseDate.Time)
			}
			if cd.BalanceDueDate != nil {
				venc = day(cd.BalanceDueDate.Time)
			}
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, subtypeLabel(a), tsvClean(a.DisplayName()),
			fmtCents(toCents(a.Balance)), a.CurrencyCode, lim, disp, fech, venc)
	}
	b.WriteString("No cartão, 'saldo' é o valor devido na fatura atual (positivo = dívida).\n")
	return textResult(b.String()), nil, nil
}

// ---- 2. listar_transacoes ----

type txInput struct {
	Conta       string   `json:"conta,omitempty" jsonschema:"ID da conta (de listar_contas) ou trecho do nome/tipo (ex.: cartão, corrente). Padrão: todas."`
	DataInicial string   `json:"data_inicial,omitempty" jsonschema:"AAAA-MM-DD. Padrão: 30 dias antes da data final."`
	DataFinal   string   `json:"data_final,omitempty" jsonschema:"AAAA-MM-DD. Padrão: hoje."`
	Texto       string   `json:"texto,omitempty" jsonschema:"Trecho da descrição (sem diferenciar maiúsculas)."`
	ValorMin    *float64 `json:"valor_min,omitempty" jsonschema:"Valor absoluto mínimo em reais."`
	ValorMax    *float64 `json:"valor_max,omitempty" jsonschema:"Valor absoluto máximo em reais."`
	Tipo        string   `json:"tipo,omitempty" jsonschema:"'saida' ou 'entrada'."`
	Categoria   string   `json:"categoria,omitempty" jsonschema:"Trecho do nome da categoria."`
	Limite      int      `json:"limite,omitempty" jsonschema:"Máximo de linhas na resposta. Padrão: 300."`
}

func (s *Server) listarTransacoes(ctx context.Context, _ *mcp.CallToolRequest, in txInput) (*mcp.CallToolResult, any, error) {
	from, to, err := period(in.DataInicial, in.DataFinal)
	if err != nil {
		return nil, nil, err
	}
	accs, err := s.allAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	if accs, err = selectAccounts(accs, in.Conta, false); err != nil {
		return nil, nil, err
	}
	txs, err := s.fetchTxs(ctx, accs, from, to)
	if err != nil {
		return nil, nil, err
	}

	texto := strings.ToLower(in.Texto)
	cat := strings.ToLower(in.Categoria)
	tipo := strings.ToLower(strings.TrimSpace(in.Tipo))
	var filtered []NTx
	for _, t := range txs {
		if texto != "" && !strings.Contains(strings.ToLower(t.Description), texto) {
			continue
		}
		if cat != "" && !strings.Contains(strings.ToLower(t.Category), cat) {
			continue
		}
		if in.ValorMin != nil && absCents(t.Cents) < toCents(*in.ValorMin) {
			continue
		}
		if in.ValorMax != nil && absCents(t.Cents) > toCents(*in.ValorMax) {
			continue
		}
		if (strings.HasPrefix(tipo, "sa") && t.Cents >= 0) || (strings.HasPrefix(tipo, "en") && t.Cents <= 0) {
			continue
		}
		filtered = append(filtered, t)
	}

	limit := in.Limite
	if limit <= 0 {
		limit = 300
	}
	var b strings.Builder
	fmt.Fprintf(&b, "periodo %s a %s | %d transações\n", from.Format("2006-01-02"), to.Format("2006-01-02"), len(filtered))
	writeTotals(&b, filtered)
	writeTxTable(&b, filtered, limit)
	return textResult(b.String()), nil, nil
}

// ---- 3. resumo_gastos ----

type periodInput struct {
	DataInicial string `json:"data_inicial,omitempty" jsonschema:"AAAA-MM-DD. Padrão: 30 dias antes da data final."`
	DataFinal   string `json:"data_final,omitempty" jsonschema:"AAAA-MM-DD. Padrão: hoje."`
}

func (s *Server) resumoGastos(ctx context.Context, _ *mcp.CallToolRequest, in periodInput) (*mcp.CallToolResult, any, error) {
	from, to, err := period(in.DataInicial, in.DataFinal)
	if err != nil {
		return nil, nil, err
	}
	accs, err := s.allAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	txs, err := s.fetchTxs(ctx, accs, from, to)
	if err != nil {
		return nil, nil, err
	}

	type agg struct {
		cents int64
		n     int
	}
	type section struct {
		cats              map[string]*agg
		out, in, billPaid int64
		unconverted       int
	}
	newSection := func() *section { return &section{cats: map[string]*agg{}} }
	secs := map[bool]*section{false: newSection(), true: newSection()}

	for _, t := range txs {
		sec := secs[t.Card]
		if t.Unconverted {
			sec.unconverted++
			continue
		}
		if t.Cents >= 0 {
			sec.in += t.Cents
			continue
		}
		if isCardBillPayment(t) {
			sec.billPaid += t.Cents
			continue
		}
		cat := t.Category
		if cat == "" {
			cat = "(sem categoria)"
		}
		a := sec.cats[cat]
		if a == nil {
			a = &agg{}
			sec.cats[cat] = a
		}
		a.cents += t.Cents
		a.n++
		sec.out += t.Cents
	}

	var b strings.Builder
	fmt.Fprintf(&b, "periodo %s a %s (valores em BRL, negativo = saída)\n", from.Format("2006-01-02"), to.Format("2006-01-02"))
	for _, card := range []bool{false, true} {
		sec := secs[card]
		name := "CONTA"
		if card {
			name = "CARTÃO (por data de lançamento)"
		}
		fmt.Fprintf(&b, "\n%s\ncategoria\tgasto\tqtd\n", name)
		cats := make([]string, 0, len(sec.cats))
		for c := range sec.cats {
			cats = append(cats, c)
		}
		sort.Slice(cats, func(i, j int) bool { return sec.cats[cats[i]].cents < sec.cats[cats[j]].cents })
		for _, c := range cats {
			fmt.Fprintf(&b, "%s\t%s\t%d\n", tsvClean(c), fmtCents(sec.cats[c].cents), sec.cats[c].n)
		}
		fmt.Fprintf(&b, "total gastos\t%s\n", fmtCents(sec.out))
		fmt.Fprintf(&b, "entradas/estornos\t%s\n", fmtCents(sec.in))
		if sec.billPaid != 0 {
			fmt.Fprintf(&b, "pagamento de fatura (fora do total)\t%s\n", fmtCents(sec.billPaid))
		}
		if sec.unconverted > 0 {
			fmt.Fprintf(&b, "transações em moeda estrangeira sem conversão (fora do total)\t%d\n", sec.unconverted)
		}
	}
	fmt.Fprintf(&b, "\nTOTAL GERAL DE GASTOS\t%s\n", fmtCents(secs[false].out+secs[true].out))
	return textResult(b.String()), nil, nil
}

// ---- 4. listar_faturas_cartao ----

type billsInput struct {
	Conta  string `json:"conta,omitempty" jsonschema:"ID ou trecho do nome do cartão. Padrão: todos."`
	Limite int    `json:"limite,omitempty" jsonschema:"Número de faturas mais recentes por cartão. Padrão: 12."`
}

func sortBillsDesc(bills []Bill) {
	sort.Slice(bills, func(i, j int) bool { return bills[i].DueDate.After(bills[j].DueDate.Time) })
}

func (s *Server) listarFaturas(ctx context.Context, _ *mcp.CallToolRequest, in billsInput) (*mcp.CallToolResult, any, error) {
	accs, err := s.allAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	cards, err := selectAccounts(accs, in.Conta, true)
	if err != nil {
		return nil, nil, err
	}
	limit := in.Limite
	if limit <= 0 {
		limit = 12
	}
	var b strings.Builder
	for _, a := range cards {
		bills, err := s.c.ListBills(ctx, a.ID)
		if err != nil {
			return nil, nil, err
		}
		sortBillsDesc(bills)
		fmt.Fprintf(&b, "cartão %s (%s)\n", tsvClean(a.DisplayName()), a.ID)
		b.WriteString("id\tfechamento\tvencimento\ttotal\tminimo\tpagamentos\tencargos\n")
		if cd := a.CreditData; cd != nil {
			fech, venc := "", ""
			if cd.BalanceCloseDate != nil {
				fech = day(cd.BalanceCloseDate.Time)
			}
			if cd.BalanceDueDate != nil {
				venc = day(cd.BalanceDueDate.Time)
			}
			fmt.Fprintf(&b, "aberta\t%s\t%s\t%s\t\t\t(saldo atual do cartão)\n", fech, venc, fmtCents(toCents(a.Balance)))
		}
		for i, bill := range bills {
			if i >= limit {
				fmt.Fprintf(&b, "... %d faturas mais antigas omitidas\n", len(bills)-limit)
				break
			}
			fech, min := "", ""
			if bill.BillClosingDate != nil {
				fech = day(bill.BillClosingDate.Time)
			}
			if bill.MinimumPaymentAmount != nil {
				min = fmtCents(toCents(*bill.MinimumPaymentAmount))
			}
			var paid int64
			var modes []string
			for _, p := range bill.Payments {
				paid += toCents(p.Amount)
				d := ""
				if p.PaymentDate != nil {
					d = day(p.PaymentDate.Time) + " "
				}
				modes = append(modes, d+p.ValueType)
			}
			pag := fmtCents(paid)
			if len(modes) > 0 {
				pag += " (" + strings.Join(modes, "; ") + ")"
			}
			var charges int64
			var types []string
			for _, f := range bill.FinanceCharges {
				charges += toCents(f.Amount)
				types = append(types, f.Type+" "+fmtCents(toCents(f.Amount)))
			}
			enc := fmtCents(charges)
			if len(types) > 0 {
				enc += " (" + strings.Join(types, "; ") + ")"
			}
			cur := ""
			if bill.TotalAmountCurrencyCode != "" && bill.TotalAmountCurrencyCode != "BRL" {
				cur = " " + bill.TotalAmountCurrencyCode
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s%s\t%s\t%s\t%s\n", bill.ID, fech, day(bill.DueDate.Time),
				fmtCents(toCents(bill.TotalAmount)), cur, min, pag, enc)
		}
		b.WriteString("\n")
	}
	b.WriteString("Valores da fatura como reportados pela Pluggy (total positivo = valor a pagar).\n")
	return textResult(b.String()), nil, nil
}

// ---- 5. transacoes_da_fatura ----

type billTxInput struct {
	FaturaID string `json:"fatura_id" jsonschema:"ID da fatura (de listar_faturas_cartao) ou 'aberta' para a fatura atual."`
	Conta    string `json:"conta,omitempty" jsonschema:"Cartão, necessário só com 'aberta' quando houver mais de um."`
}

func (s *Server) transacoesDaFatura(ctx context.Context, _ *mcp.CallToolRequest, in billTxInput) (*mcp.CallToolResult, any, error) {
	accs, err := s.allAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	cards, err := selectAccounts(accs, in.Conta, true)
	if err != nil {
		return nil, nil, err
	}
	id := strings.TrimSpace(in.FaturaID)
	if strings.EqualFold(id, "aberta") {
		return s.openBillTxs(ctx, cards)
	}

	for _, a := range cards {
		bills, err := s.c.ListBills(ctx, a.ID)
		if err != nil {
			return nil, nil, err
		}
		sortBillsDesc(bills)
		for i, bill := range bills {
			if bill.ID != id {
				continue
			}
			// Janela de datas ampla em volta do ciclo; o vínculo é feito pelo billId.
			closing := bill.DueDate.Time.AddDate(0, 0, -7)
			if bill.BillClosingDate != nil {
				closing = bill.BillClosingDate.Time
			}
			var prevClosing time.Time
			if i+1 < len(bills) {
				prev := bills[i+1]
				prevClosing = prev.DueDate.Time.AddDate(0, 0, -7)
				if prev.BillClosingDate != nil {
					prevClosing = prev.BillClosingDate.Time
				}
			} else {
				prevClosing = closing.AddDate(0, -1, 0)
			}
			from := prevClosing.AddDate(0, 0, -45).In(brt)
			to := bill.DueDate.Time.AddDate(0, 0, 10).In(brt)
			txs, err := s.fetchTxs(ctx, []Account{a}, from, to)
			if err != nil {
				return nil, nil, err
			}
			var sel []NTx
			for _, t := range txs {
				if t.BillID == id {
					sel = append(sel, t)
				}
			}
			method := "vinculadas pelo billId"
			if len(sel) == 0 {
				// Sem billId (conectores não Open Finance): usa o ciclo entre fechamentos.
				method = "estimadas pelo ciclo entre fechamentos (sem billId)"
				pc, cl := day(prevClosing), day(closing)
				for _, t := range txs {
					if t.Date > pc && t.Date <= cl {
						sel = append(sel, t)
					}
				}
			}
			var b strings.Builder
			fmt.Fprintf(&b, "fatura %s | cartão %s | vencimento %s | total da fatura %s\n", id, tsvClean(a.DisplayName()),
				day(bill.DueDate.Time), fmtCents(toCents(bill.TotalAmount)))
			fmt.Fprintf(&b, "%d transações %s\n", len(sel), method)
			writeTotals(&b, sel)
			writeTxTable(&b, sel, 0)
			return textResult(b.String()), nil, nil
		}
	}
	return nil, nil, fmt.Errorf("fatura %q não encontrada; use listar_faturas_cartao", id)
}

func (s *Server) openBillTxs(ctx context.Context, cards []Account) (*mcp.CallToolResult, any, error) {
	if len(cards) > 1 {
		return nil, nil, errors.New("há mais de um cartão; informe 'conta'")
	}
	a := cards[0]
	bills, err := s.c.ListBills(ctx, a.ID)
	if err != nil {
		return nil, nil, err
	}
	sortBillsDesc(bills)
	now := time.Now().In(brt)
	from := now.AddDate(0, -2, 0)
	if len(bills) > 0 {
		last := bills[0].DueDate.Time.AddDate(0, 0, -7)
		if bills[0].BillClosingDate != nil {
			last = bills[0].BillClosingDate.Time
		}
		from = last.AddDate(0, 0, -10).In(brt)
	}
	closeDay := ""
	to := now.AddDate(0, 2, 0)
	if cd := a.CreditData; cd != nil && cd.BalanceCloseDate != nil {
		closeDay = day(cd.BalanceCloseDate.Time)
		to = cd.BalanceCloseDate.Time.AddDate(0, 0, 1).In(brt)
	}
	txs, err := s.fetchTxs(ctx, []Account{a}, from, to)
	if err != nil {
		return nil, nil, err
	}
	var sel []NTx
	for _, t := range txs {
		if t.BillID == "" && (closeDay == "" || t.Date <= closeDay) {
			sel = append(sel, t)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "fatura aberta | cartão %s | fechamento %s | saldo atual %s\n", tsvClean(a.DisplayName()), closeDay, fmtCents(toCents(a.Balance)))
	fmt.Fprintf(&b, "%d transações ainda sem fatura fechada\n", len(sel))
	writeTotals(&b, sel)
	writeTxTable(&b, sel, 0)
	return textResult(b.String()), nil, nil
}

// ---- 6. listar_investimentos ----

func (s *Server) listarInvestimentos(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	var b strings.Builder
	b.WriteString("tipo\tsubtipo\tnome\tsaldo\tmoeda\tvalor_investido\tlucro\trent_mes\trent_12m\ttaxa_anual\tindexador\tvencimento\tstatus\n")
	totals := map[string]int64{}
	n := 0
	opt := func(v *float64) string {
		if v == nil {
			return ""
		}
		return fmt.Sprintf("%g", *v)
	}
	optMoney := func(v *float64) string {
		if v == nil {
			return ""
		}
		return fmtCents(toCents(*v))
	}
	optStr := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	for _, id := range s.itemIDs {
		invs, err := s.c.ListInvestments(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		for _, inv := range invs {
			n++
			totals[inv.CurrencyCode] += toCents(inv.Balance)
			rate := inv.AnnualRate
			if rate == nil {
				rate = inv.FixedAnnualRate
			}
			venc := ""
			if inv.DueDate != nil {
				venc = day(inv.DueDate.Time)
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				inv.Type, optStr(inv.Subtype), tsvClean(inv.Name), fmtCents(toCents(inv.Balance)), inv.CurrencyCode,
				optMoney(inv.AmountOriginal), optMoney(inv.AmountProfit), opt(inv.LastMonthRate), opt(inv.LastTwelveMonthsRate),
				opt(rate), optStr(inv.RateType), venc, optStr(inv.Status))
		}
	}
	if n == 0 {
		return textResult("Nenhum investimento encontrado nas conexões configuradas."), nil, nil
	}
	for cur, t := range totals {
		fmt.Fprintf(&b, "total %s\t%s\n", cur, fmtCents(t))
	}
	b.WriteString("Taxas de rentabilidade como reportadas pela Pluggy.\n")
	return textResult(b.String()), nil, nil
}

// ---- 7. status_conexoes ----

func (s *Server) statusConexoes(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	var b strings.Builder
	b.WriteString("item\tconector\tstatus\texecucao\tultima_atualizacao\tproxima_sync\terro\n")
	for _, id := range s.itemIDs {
		it, err := s.c.GetItem(ctx, id)
		if err != nil {
			fmt.Fprintf(&b, "%s\t\t\t\t\t\t%s\n", id, tsvClean(err.Error()))
			continue
		}
		fmtT := func(p *pTime) string {
			if p == nil || p.IsZero() {
				return ""
			}
			return p.In(brt).Format("2006-01-02 15:04")
		}
		e := ""
		if it.Error != nil {
			e = tsvClean(it.Error.Code + ": " + it.Error.Message)
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", it.ID, tsvClean(it.Connector.Name), it.Status, it.ExecutionStatus,
			fmtT(it.LastUpdatedAt), fmtT(it.NextAutoSyncAt), e)
	}
	b.WriteString("Horários em Brasília. " + refreshNote + "\n")
	return textResult(b.String()), nil, nil
}
