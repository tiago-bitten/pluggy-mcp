package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"
)

const checkTxDays = 90

// runCheck valida a autenticação e mostra apenas contagens por Item,
// sem nenhum dado sensível.
func runCheck(ctx context.Context, c *Client, itemIDs []string) error {
	if _, err := c.key(ctx, ""); err != nil {
		return err
	}
	fmt.Println("Autenticação OK.")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "item\tconector\tstatus\tcontas\tcartões\ttransações (%dd)\tfaturas\tinvestimentos\terros\n", checkTxDays)

	to := time.Now()
	from := to.AddDate(0, 0, -checkTxDays)
	failed := false
	for i, id := range itemIDs {
		label := fmt.Sprintf("#%d", i+1)
		var errs []string
		connector, status := "?", "?"
		if it, err := c.GetItem(ctx, id); err != nil {
			errs = append(errs, "item: "+err.Error())
		} else {
			connector, status = it.Connector.Name, it.Status
		}

		var nBank, nCard, nTx, nBills, nInv int
		accounts, err := c.ListAccounts(ctx, id)
		if err != nil {
			errs = append(errs, "contas: "+err.Error())
		}
		for _, a := range accounts {
			if a.Type == "CREDIT" {
				nCard++
				bills, err := c.ListBills(ctx, a.ID)
				if err != nil {
					errs = append(errs, "faturas: "+err.Error())
				}
				nBills += len(bills)
			} else {
				nBank++
			}
			txs, err := c.ListTransactions(ctx, a.ID, from, to)
			if err != nil {
				errs = append(errs, "transações: "+err.Error())
			}
			nTx += len(txs)
		}
		invs, err := c.ListInvestments(ctx, id)
		if err != nil {
			errs = append(errs, "investimentos: "+err.Error())
		}
		nInv = len(invs)

		errCol := "-"
		if len(errs) > 0 {
			failed = true
			errCol = fmt.Sprint(errs)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n", label, connector, status, nBank, nCard, nTx, nBills, nInv, errCol)
	}
	w.Flush()
	if failed {
		return fmt.Errorf("check concluído com erros")
	}
	return nil
}
