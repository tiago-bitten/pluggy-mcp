# pluggy-mcp

Servidor MCP local e **somente leitura** para consultar seus dados de Open Finance (via [Pluggy](https://docs.pluggy.ai) / Meu Pluggy) no Claude Code. Transporte stdio: o Claude Code inicia o processo, sem porta aberta.

## Requisitos

- Go 1.25+ (exigido pelo `github.com/modelcontextprotocol/go-sdk` v1.8.0)
- Conta no Meu Pluggy com o banco conectado e uma aplicação em dashboard.pluggy.ai autorizada pelo conector **MeuPluggy**
- Client ID, Client Secret e o(s) Item ID(s)

## Instalação

1. Crie o `.env` a partir do exemplo e preencha no seu editor (nunca cole o secret em chats):

   ```bash
   cp .env.example .env
   ```

   `PLUGGY_ITEM_IDS` aceita vários IDs separados por vírgula.

2. Restrinja as permissões do `.env`:
   - Linux/macOS: `chmod 600 .env`
   - Windows: `icacls .env /inheritance:r /grant:r "%USERNAME%:F"`

3. Compile:

   ```bash
   go build -o bin/pluggy-mcp .
   ```

   No Windows, use `-o bin/pluggy-mcp.exe`.

4. Valide (mostra só contagens, sem dados sensíveis):

   ```bash
   bin/pluggy-mcp check
   ```

5. Registre no Claude Code (escopo de usuário, caminho absoluto; as credenciais ficam no `.env`):

   ```bash
   claude mcp add --scope user pluggy -- /caminho/absoluto/bin/pluggy-mcp
   ```

O `.env` é procurado na raiz do projeto a partir do caminho do executável (subindo de `bin/`), então o servidor funciona mesmo iniciado de outra pasta.

## Ferramentas

| Ferramenta | O que faz |
|---|---|
| `listar_contas` | Contas e cartões com tipo, nome, saldo, limite e datas da fatura atual |
| `listar_transacoes` | Transações com filtros de conta, período (padrão 30 dias), texto, valor, tipo e categoria |
| `resumo_gastos` | Gastos por categoria, separando conta e cartão, com total geral |
| `listar_faturas_cartao` | Faturas com fechamento, vencimento, total, mínimo, pagamentos e encargos |
| `transacoes_da_fatura` | Transações de uma fatura (ou `aberta` para a atual) |
| `listar_investimentos` | Posições com tipo, saldo, moeda e rentabilidade |
| `status_conexoes` | Status de cada Item e data da última sincronização |

## Convenções

- **Valor negativo = dinheiro saindo**, em conta e cartão (a Pluggy usa sinais opostos nos dois casos).
- Datas no fuso de Brasília. No cartão, `data` é o lançamento e `compra` é a data original, quando diferem. `parcela` vem como `3/10`.
- Compras internacionais aparecem convertidas para a moeda da conta, com o valor original em uma coluna separada.
- Somas feitas em centavos inteiros.
- Pagamentos de fatura na conta corrente ficam fora do total do `resumo_gastos`, para não contar duas vezes.
- Itens do MeuPluggy são sincronizados pelo Meu Pluggy a cada ~24h e **não podem ser atualizados via API**.

## Testes

```bash
go test ./...
```

Os testes usam apenas dados sintéticos.
