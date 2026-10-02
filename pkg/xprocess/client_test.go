package xprocess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/logf"
)

func testLog() logf.Logger { return logf.New(logf.Opts{}) }

func TestParseDecimalBR(t *testing.T) {
	got, err := ParseDecimalBR("230,0418")
	require.NoError(t, err)
	assert.InDelta(t, 230.0418, got, 0.0001)

	got, err = ParseDecimalBR("0,0")
	require.NoError(t, err)
	assert.Equal(t, 0.0, got)

	_, err = ParseDecimalBR("not-a-number")
	assert.Error(t, err)
}

func TestClient_ConsultarPedido_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/pedido", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"pedido":[
			{"cod_empresa":"7","num_pedido":"666","cod_cliente":"9446","cod_vendedor":"2586","cod_produto":"22393","quantidade":"13,62","total":"230,0418","status":"SEPARACAO","data_venda":null},
			{"cod_empresa":"7","num_pedido":"666","cod_cliente":"9446","cod_vendedor":"2586","cod_produto":"17736","quantidade":"18,0","total":"178,2","status":"SEPARACAO","data_venda":null}
		]}`))
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	items, err := c.ConsultarPedido(context.Background(), "test-key", "666", "12345678900")
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "SEPARACAO", items[0].Status)
	assert.Equal(t, "7", items[0].CodEmpresa)
}

func TestClient_ConsultarPedido_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Pedido nao encontrado"}`))
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	_, err := c.ConsultarPedido(context.Background(), "test-key", "666", "00000000000")
	assert.True(t, errors.Is(err, ErrPedidoNaoEncontrado))
}

func TestClient_ConsultarPedido_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	_, err := c.ConsultarPedido(context.Background(), "test-key", "666", "00000000000")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrPedidoNaoEncontrado))
}

func TestSummarizePedido(t *testing.T) {
	items := []PedidoItem{
		{CodEmpresa: "7", NumPedido: "666", CodVendedor: "2586", Status: "SEPARACAO", Total: "230,0418"},
		{CodEmpresa: "7", NumPedido: "666", CodVendedor: "2586", Status: "SEPARACAO", Total: "178,2"},
	}
	resumo, err := SummarizePedido(items)
	require.NoError(t, err)
	assert.Equal(t, "SEPARACAO", resumo.Status)
	assert.Equal(t, "7", resumo.CodEmpresa)
	assert.Equal(t, "2586", resumo.CodVendedor)
	assert.InDelta(t, 408.2418, resumo.ValorTotal, 0.0001)
	assert.Len(t, resumo.Itens, 2)
}

func TestSummarizePedido_Empty(t *testing.T) {
	_, err := SummarizePedido(nil)
	assert.Error(t, err)
}

func TestClient_ConsultarClientePorDocumento_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/clientes", r.URL.Path)
		assert.Equal(t, "07378783000190", r.URL.Query().Get("documento"))
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		_, _ = w.Write([]byte(`{"ok":true,"total":1,"clientes":[{"cod_cliente":"3","nome":"CERAMICA SERRA AZUL LTDA","cpf_cnpj":"07.378.783/0001-90"}]}`))
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	codCliente, err := c.ConsultarClientePorDocumento(context.Background(), "test-key", "07378783000190")
	require.NoError(t, err)
	assert.Equal(t, "3", codCliente)
}

func TestClient_ConsultarClientePorDocumento_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"total":0,"clientes":[]}`))
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	_, err := c.ConsultarClientePorDocumento(context.Background(), "test-key", "00000000000")
	assert.True(t, errors.Is(err, ErrClienteNaoEncontrado))
}

func TestClient_ListarVendasPorCliente_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/vendas", r.URL.Path)
		assert.Equal(t, "3", r.URL.Query().Get("cod_cliente"))
		assert.Equal(t, "50", r.URL.Query().Get("limit"))
		_, _ = w.Write([]byte(`{"ok":true,"total":1,"vendas":[
			{"cod_empresa":"23","num_pedido":"20101","cod_cliente":"3","cod_vendedor":null,"status":"FECHADO","data_venda":"2022-05-23T00:00:00","total":"10392,8064"}
		]}`))
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	items, err := c.ListarVendasPorCliente(context.Background(), "test-key", "3", 50)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "20101", items[0].NumPedido)
	assert.Equal(t, "FECHADO", items[0].Status)
}

func TestGroupPedidos_GroupsByEmpresaAndNumPedido(t *testing.T) {
	// Two lines of pedido 20101/empresa 23, one line of a different pedido
	// (2/empresa 1) — mirrors GET /api/vendas' real one-row-per-item shape.
	items := []PedidoItem{
		{CodEmpresa: "23", NumPedido: "20101", CodCliente: "3", Status: "FECHADO", Total: "100,0"},
		{CodEmpresa: "23", NumPedido: "20101", CodCliente: "3", Status: "FECHADO", Total: "50,0"},
		{CodEmpresa: "1", NumPedido: "2", CodCliente: "19", Status: "CANCELADO", Total: "61,6"},
	}
	resumos, err := GroupPedidos(items)
	require.NoError(t, err)
	require.Len(t, resumos, 2)

	assert.Equal(t, "20101", resumos[0].NumPedido)
	assert.Equal(t, "23", resumos[0].CodEmpresa)
	assert.InDelta(t, 150.0, resumos[0].ValorTotal, 0.0001)
	assert.Len(t, resumos[0].Itens, 2)

	assert.Equal(t, "2", resumos[1].NumPedido)
	assert.Equal(t, "1", resumos[1].CodEmpresa)
	assert.InDelta(t, 61.6, resumos[1].ValorTotal, 0.0001)
}

// Freight is informational and separate from the total: Total = Subtotal - Desconto,
// and the pedido's freight is the sum of the lines' vl_frete (values from a real
// 8-line order: they add up to exactly 100,00).
func TestSummarizePedido_SumsFreightSeparatelyFromTotal(t *testing.T) {
	items := []PedidoItem{
		{CodEmpresa: "40", NumPedido: "1", Status: "SEPARACAO", Total: "1000,50", VlFrete: "0,87"},
		{CodEmpresa: "40", NumPedido: "1", Status: "SEPARACAO", Total: "500,00", VlFrete: "99,13"},
	}
	r, err := SummarizePedido(items)
	require.NoError(t, err)
	assert.InDelta(t, 1500.50, r.ValorTotal, 1e-9, "freight is not part of the total")
	assert.InDelta(t, 100.00, r.ValorFrete, 1e-9)
}

func TestSummarizePedido_MissingOrMalformedFreightCountsAsZero(t *testing.T) {
	items := []PedidoItem{
		{NumPedido: "1", Status: "FECHADO", Total: "10,0"},
		{NumPedido: "1", Status: "FECHADO", Total: "5,0", VlFrete: "n/a"},
		{NumPedido: "1", Status: "FECHADO", Total: "5,0", VlFrete: "2,5"},
	}
	r, err := SummarizePedido(items)
	require.NoError(t, err)
	assert.InDelta(t, 20.0, r.ValorTotal, 1e-9)
	assert.InDelta(t, 2.5, r.ValorFrete, 1e-9)
}

func TestClient_ListarLojas_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/lojas", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		_, _ = w.Write([]byte(`{"ok":true,"total":2,"lojas":[
			{"cod_empresa":"40","razao_social_empresa":"ATACADAO DOS PISOS LTDA  (PORTO)","cnpj_empresa":"58.675.622/0003-61","data_referencia_dados":"2026-10-01T01:18:33"},
			{"cod_empresa":"41","razao_social_empresa":"ATACADAO DOS PISOS LTDA  (SERRINHA)","cnpj_empresa":"58.675.622/0005-23","data_referencia_dados":"2026-10-01T01:18:33"}
		]}`))
	}))
	defer srv.Close()

	lojas, err := New(testLog(), srv.URL).ListarLojas(context.Background(), "test-key")
	require.NoError(t, err)
	require.Len(t, lojas, 2)
	assert.Equal(t, Loja{CodEmpresa: "40", RazaoSocialEmpresa: "ATACADAO DOS PISOS LTDA  (PORTO)", CNPJEmpresa: "58.675.622/0003-61"}, lojas[0])
}

func TestClient_ListarLojas_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid key"}`))
	}))
	defer srv.Close()
	_, err := New(testLog(), srv.URL).ListarLojas(context.Background(), "bad")
	assert.Error(t, err)
}
