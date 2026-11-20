package message_test

import (
	"github.com/stretchr/testify/require"
	"heckel.io/ntfy/v2/message"
	"heckel.io/ntfy/v2/model"
	"path/filepath"
	"testing"
	"time"
)

func TestQiuQiuMetadataSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c, e := message.NewSQLiteStore(path, "", 12*time.Hour, 0, 0, false)
	require.NoError(t, e)
	m := model.NewMessage(model.MessageEvent, "private", "中文正文")
	m.QiuQiu = &model.QiuQiuMetadata{MessageID: "qq-id", BusinessType: "server", Sound: "alarm", Icon: "server", SourceProtocol: "bark", ContentType: "html"}
	require.NoError(t, c.AddMessage(m))
	require.NoError(t, c.Close())
	c, e = message.NewSQLiteStore(path, "", 12*time.Hour, 0, 0, false)
	require.NoError(t, e)
	defer c.Close()
	got, e := c.Messages("private", model.NewSinceTime(0), false)
	require.NoError(t, e)
	require.Len(t, got, 1)
	require.Equal(t, m.QiuQiu, got[0].QiuQiu)
}
