// Package xprocess is a thin client for the customer's own X2 ERP API
// (https://api.atacadaodospisos.com.br/docs). The API is NOT real-time: it
// is a daily CSV re-export of X2, refreshed around 2h. See
// docs/superpowers/specs/2026-09-27-central-vendas-entrega2-xprocess-design.md
// §1 for the full context — every design decision here follows from that.
package xprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zerodha/logf"
)

// DefaultTimeout bounds a single request to the X2 API.
const DefaultTimeout = 15 * time.Second

// ErrPedidoNaoEncontrado is returned when the X2 API responds 404 for
// POST /api/pedido — either the numero_pedido/documento combination is
// wrong, or (just as likely, given the API is D-1) the pedido legitimately
// has not reached the API's daily data load yet. Callers must not treat
// this as "invalid" on its own — see the design's §5/§8 wording rule.
var ErrPedidoNaoEncontrado = errors.New("xprocess: pedido nao encontrado")

// ErrClienteNaoEncontrado is returned when GET /api/clientes finds no
// customer for the given documento — the customer has never bought
// anything at this store, or the documento was typed wrong.
var ErrClienteNaoEncontrado = errors.New("xprocess: cliente nao encontrado")

// Client talks to one organization's X2 API instance.
type Client struct {
	HTTPClient *http.Client
	Log        logf.Logger
	baseURL    string
}

// New creates a client for the given base URL (e.g.
// "https://api.atacadaodospisos.com.br", no trailing slash expected but
// tolerated). There is no package-level default base URL — unlike Meta's
// Graph API, every organization configures its own X2 instance.
func New(log logf.Logger, baseURL string) *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: DefaultTimeout},
		Log:        log,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// PedidoItem is one line item of a pedido, as returned by
// POST /api/pedido and GET /api/vendas. Only the fields this integration
// actually reads are declared — unknown JSON fields are ignored by
// encoding/json, no need to mirror the full X2 schema.
type PedidoItem struct {
	CodEmpresa  string  `json:"cod_empresa"`
	NumPedido   string  `json:"num_pedido"`
	CodCliente  string  `json:"cod_cliente"`
	CodVendedor string  `json:"cod_vendedor"`
	Status      string  `json:"status"`
	Total       string  `json:"total"` // BR decimal, comma separator — see ParseDecimalBR
	DataVenda   *string `json:"data_venda"`
}

type pedidoResponse struct {
	OK     bool         `json:"ok"`
	Pedido []PedidoItem `json:"pedido"`
}

// ConsultarPedido calls POST /api/pedido with the exact pedido number and
// the customer's documento (CPF or CNPJ). The X2 API validates both
// together — numero_pedido alone is ambiguous (only unique per loja, not
// globally), so documento is never optional here (design §1, §3).
func (c *Client) ConsultarPedido(ctx context.Context, apiKey, numPedido, documento string) ([]PedidoItem, error) {
	payload, err := json.Marshal(map[string]string{
		"numero_pedido": numPedido,
		"documento":     documento,
	})
	if err != nil {
		return nil, fmt.Errorf("xprocess: failed to encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/pedido", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("xprocess: failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xprocess: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("xprocess: failed to read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrPedidoNaoEncontrado
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("xprocess: unexpected status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var parsed pedidoResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("xprocess: failed to parse response: %w", err)
	}
	return parsed.Pedido, nil
}

// ClienteItem is one row of GET /api/clientes. Only cod_cliente is read —
// the caller already knows the documento it searched by.
type ClienteItem struct {
	CodCliente string `json:"cod_cliente"`
}

type clientesResponse struct {
	OK       bool          `json:"ok"`
	Clientes []ClienteItem `json:"clientes"`
}

// ConsultarClientePorDocumento resolves a CPF/CNPJ to X2's internal
// cod_cliente — GET /api/vendas has no documento filter of its own (design
// doc's documented two-step lookup: GET /api/clientes?documento= ->
// cod_cliente -> GET /api/vendas?cod_cliente=). documento is sent as
// digits-only; the API normalizes internally (verified live: matches a
// cpf_cnpj stored with punctuation).
func (c *Client) ConsultarClientePorDocumento(ctx context.Context, apiKey, documento string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/clientes?documento="+documento, nil)
	if err != nil {
		return "", fmt.Errorf("xprocess: failed to build request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("xprocess: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("xprocess: failed to read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("xprocess: unexpected status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var parsed clientesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("xprocess: failed to parse response: %w", err)
	}
	if len(parsed.Clientes) == 0 {
		return "", ErrClienteNaoEncontrado
	}
	return parsed.Clientes[0].CodCliente, nil
}

type vendasResponse struct {
	OK     bool         `json:"ok"`
	Vendas []PedidoItem `json:"vendas"`
}

// ListarVendasPorCliente returns recent order LINE ITEMS for a customer
// (GET /api/vendas?cod_cliente=, one row per pedido item, not per pedido —
// group with GroupPedidos). limit is capped at 1000 by the X2 API itself;
// this does not re-validate it. There is no date filter — the API doesn't
// offer one (design doc's documented limitation) — so callers filter by
// date client-side after grouping.
func (c *Client) ListarVendasPorCliente(ctx context.Context, apiKey, codCliente string, limit int) ([]PedidoItem, error) {
	url := fmt.Sprintf("%s/api/vendas?cod_cliente=%s&limit=%d", c.baseURL, codCliente, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("xprocess: failed to build request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xprocess: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("xprocess: failed to read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("xprocess: unexpected status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var parsed vendasResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("xprocess: failed to parse response: %w", err)
	}
	return parsed.Vendas, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ParseDecimalBR parses an X2 numeric value, which always comes as a string
// with a comma decimal separator (e.g. "230,0418"), never a JSON number.
func ParseDecimalBR(s string) (float64, error) {
	return strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
}

// PedidoResumo aggregates a pedido's line items into the summary the
// reconciliation job actually needs: one status, one seller, one total.
type PedidoResumo struct {
	NumPedido   string
	CodCliente  string
	CodEmpresa  string
	CodVendedor string
	Status      string
	DataVenda   *string
	ValorTotal  float64
	Itens       []PedidoItem
}

// SummarizePedido aggregates ConsultarPedido's line items. Every item of one
// pedido shares the same status/empresa/vendedor (confirmed against the real
// API — see the design's §1 findings), so those come from the first item;
// ValorTotal sums each item's Total.
func SummarizePedido(items []PedidoItem) (PedidoResumo, error) {
	if len(items) == 0 {
		return PedidoResumo{}, errors.New("xprocess: cannot summarize an empty pedido")
	}
	resumo := PedidoResumo{
		NumPedido:   items[0].NumPedido,
		CodCliente:  items[0].CodCliente,
		CodEmpresa:  items[0].CodEmpresa,
		CodVendedor: items[0].CodVendedor,
		Status:      items[0].Status,
		DataVenda:   items[0].DataVenda,
		Itens:       items,
	}
	for _, item := range items {
		v, err := ParseDecimalBR(item.Total)
		if err != nil {
			return PedidoResumo{}, fmt.Errorf("xprocess: failed to parse item total %q: %w", item.Total, err)
		}
		resumo.ValorTotal += v
	}
	return resumo, nil
}

// GroupPedidos buckets ListarVendasPorCliente's flat, one-row-per-item
// result by (CodEmpresa, NumPedido) — the same key /api/vendas' own docs
// say to group by, since num_pedido alone repeats across lojas — and
// summarizes each group with SummarizePedido. Order of the returned slice
// follows first appearance in items, not sorted.
func GroupPedidos(items []PedidoItem) ([]PedidoResumo, error) {
	order := make([]string, 0)
	groups := make(map[string][]PedidoItem)
	for _, item := range items {
		key := item.CodEmpresa + "|" + item.NumPedido
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], item)
	}

	resumos := make([]PedidoResumo, 0, len(order))
	for _, key := range order {
		resumo, err := SummarizePedido(groups[key])
		if err != nil {
			return nil, err
		}
		resumos = append(resumos, resumo)
	}
	return resumos, nil
}
