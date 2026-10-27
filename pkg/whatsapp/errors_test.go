package whatsapp_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/logf"
)

func metaErr(status, code int) error {
	return whatsapp.ParseMetaAPIError(status, []byte(fmt.Sprintf(`{"error":{"code":%d,"message":"m"}}`, code)))
}

// The three categories. Only a 4xx WITH a documented rejection code proves the
// message was not accepted; the status alone never does.
func TestClassify_ThreeCategories(t *testing.T) {
	refused := []int{100, 131008, 131009, 131021, 131047, 131051, 132000, 132001, 132005, 132007, 132012, 132015, 132016, 190, 368}
	for _, code := range refused {
		assert.Equal(t, whatsapp.OutcomeRefused, whatsapp.Classify(metaErr(400, code)), "code %d", code)
	}
	for _, code := range []int{130429, 131056, 80007} {
		assert.Equal(t, whatsapp.OutcomeTransient, whatsapp.Classify(metaErr(429, code)), "throttle %d", code)
		assert.Equal(t, whatsapp.OutcomeTransient, whatsapp.Classify(metaErr(400, code)), "throttle %d on a 400", code)
	}

	unknown := map[string]error{
		"nil":                                    nil,
		"plain error":                            errors.New("something"),
		"408 request timeout, any body":          metaErr(408, 100),
		"408 without a Meta body":                whatsapp.ParseMetaAPIError(408, []byte("timeout")),
		"425 too early":                          metaErr(425, 100),
		"429 alone, no Meta body":                whatsapp.ParseMetaAPIError(429, []byte("slow down")),
		"429 with an unlisted code":              metaErr(429, 4),
		"4xx with an unlisted code":              metaErr(400, 999999),
		"4xx whose code is delivery-side 131026": metaErr(400, 131026),
		"4xx 131030 (not documented)":            metaErr(400, 131030),
		"4xx without a Meta body":                whatsapp.ParseMetaAPIError(400, []byte("bad request")),
		"5xx with a refusal code":                metaErr(500, 131008),
		"5xx server error 131000":                metaErr(500, 131000),
		"503":                                    metaErr(503, 133004),
		"2xx-looking status with an error body":  metaErr(200, 131008),
		"deadline exceeded":                      context.DeadlineExceeded,
		"read reset after the request":           &net.OpError{Op: "read", Err: errors.New("reset")},
	}
	for name, err := range unknown {
		assert.Equal(t, whatsapp.OutcomeUnknown, whatsapp.Classify(err), name)
		assert.False(t, whatsapp.NotSent(err), name)
	}

	assert.Equal(t, whatsapp.OutcomeRefused, whatsapp.Classify(whatsapp.NewNotSentError(errors.New("components"))), "failed before any request")
	assert.Equal(t, whatsapp.OutcomeRefused, whatsapp.Classify(&net.OpError{Op: "dial", Err: errors.New("refused")}), "never connected")
}

func TestClassify_SurvivesRedactionWithoutKeepingTheSecret(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want whatsapp.Outcome
	}{
		{metaErr(400, 131008), whatsapp.OutcomeRefused},
		{metaErr(429, 130429), whatsapp.OutcomeTransient},
		{errors.New("timeout 123456"), whatsapp.OutcomeUnknown},
	} {
		red := whatsapp.WithRedactedText(tc.err, "redacted "+whatsapp.SecretMask)
		assert.Equal(t, tc.want, whatsapp.Classify(red))
		assert.Equal(t, "redacted "+whatsapp.SecretMask, red.Error())
		assert.NoError(t, errors.Unwrap(red), "the original error, which may hold the secret, is not reachable")
	}
}

// End to end through the client: what really comes back from doRequest.
func TestClient_ClassifiesRealFailures(t *testing.T) {
	log := logf.New(logf.Opts{Level: logf.ErrorLevel})
	acct := &whatsapp.Account{PhoneID: "111", AccessToken: "tok", APIVersion: "v21.0"}
	send := func(c *whatsapp.Client) error {
		_, err := c.SendTemplateMessage(context.Background(), acct, whatsapp.Recipient{Phone: "5511999990000"}, "t", "pt_BR", nil)
		return err
	}
	serve := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}

	refuse := serve(400, `{"error":{"code":131008,"message":"missing"}}`)
	defer refuse.Close()
	assert.Equal(t, whatsapp.OutcomeRefused, whatsapp.Classify(send(whatsapp.NewWithBaseURL(log, refuse.URL))))

	throttle := serve(429, `{"error":{"code":130429,"message":"rate"}}`)
	defer throttle.Close()
	assert.Equal(t, whatsapp.OutcomeTransient, whatsapp.Classify(send(whatsapp.NewWithBaseURL(log, throttle.URL))))

	bare429 := serve(429, `slow down`)
	defer bare429.Close()
	assert.Equal(t, whatsapp.OutcomeUnknown, whatsapp.Classify(send(whatsapp.NewWithBaseURL(log, bare429.URL))), "a bare 429 proves nothing")

	other4xx := serve(400, `{"error":{"code":999999,"message":"new kind of error"}}`)
	defer other4xx.Close()
	assert.Equal(t, whatsapp.OutcomeUnknown, whatsapp.Classify(send(whatsapp.NewWithBaseURL(log, other4xx.URL))), "an undocumented 4xx is not assumed to be a refusal")

	boom := serve(503, ``)
	defer boom.Close()
	assert.Equal(t, whatsapp.OutcomeUnknown, whatsapp.Classify(send(whatsapp.NewWithBaseURL(log, boom.URL))))

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(600 * time.Millisecond) }))
	defer slow.Close()
	c := whatsapp.NewWithBaseURL(log, slow.URL)
	c.HTTPClient.Timeout = 100 * time.Millisecond
	assert.Equal(t, whatsapp.OutcomeUnknown, whatsapp.Classify(send(c)), "a timeout leaves the outcome unknown")

	require.Error(t, send(whatsapp.NewWithBaseURL(log, "http://127.0.0.1:1")))
	assert.Equal(t, whatsapp.OutcomeRefused, whatsapp.Classify(send(whatsapp.NewWithBaseURL(log, "http://127.0.0.1:1"))), "a connection that was never established")
}
