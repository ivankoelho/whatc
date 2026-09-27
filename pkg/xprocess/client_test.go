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
