package server

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestServer_QiuQiuMetadataRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, databaseURL string) {
		s := newTestServer(t, newTestConfig(t, databaseURL))
		published := request(t, s, "POST", "/", `{"topic":"business","message":"中文消息","qiuqiu":{"message_id":"business-uuid","business_type":"security","business_name":"登录审计","icon":"security","sound":"alarm","source_protocol":"native","content_type":"text"}}`, nil)
		require.Equal(t, 200, published.Code, published.Body.String())
		msg := toMessage(t, published.Body.String())
		require.NotNil(t, msg.QiuQiu)
		require.Equal(t, "登录审计", msg.QiuQiu.BusinessName)
		cached := request(t, s, "GET", "/business/json?poll=1", "", nil)
		messages := toMessages(t, cached.Body.String())
		require.Len(t, messages, 1)
		require.Equal(t, msg.QiuQiu, messages[0].QiuQiu)
		plain := request(t, s, "PUT", "/business", "ordinary ntfy", nil)
		require.Nil(t, toMessage(t, plain.Body.String()).QiuQiu)
	})
}
