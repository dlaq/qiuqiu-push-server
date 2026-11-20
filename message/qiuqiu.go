package message

import (
	"encoding/json"
	"heckel.io/ntfy/v2/model"
)

func encodeQiuQiu(q *model.QiuQiuMetadata) string {
	if q == nil {
		return ""
	}
	b, _ := json.Marshal(q)
	return string(b)
}
func decodeQiuQiu(raw string) *model.QiuQiuMetadata {
	if raw == "" {
		return nil
	}
	var q model.QiuQiuMetadata
	if json.Unmarshal([]byte(raw), &q) != nil {
		return nil
	}
	return &q
}
