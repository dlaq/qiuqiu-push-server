package main

import (
	"net/http"
	"strings"
)

// Sender keys can inspect only messages created using that same key. A second
// key belonging to the same user does not gain access to another application's data.
func (s *Server) senderMessageHTTP(w http.ResponseWriter, r *http.Request) error {
	if r.Method != "GET" {
		return &AppError{405, "method_not_allowed", "需要GET"}
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var keyID, userID string
	if e := s.store.QueryRow("SELECT k.id,k.user_id FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.hash=? AND k.disabled=0 AND u.disabled=0", digest(token)).Scan(&keyID, &userID); e != nil {
		return &AppError{401, "unauthorized", "凭证无效或已禁用"}
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/messages/")
	var n int
	if e := s.store.QueryRow("SELECT count(*) FROM messages WHERE id=? AND key_id=? AND user_id=?", id, keyID, userID).Scan(&n); e != nil || n != 1 {
		return &AppError{404, "not_found", "消息不存在"}
	}
	_, e := s.trackingManage(w, r, User{ID: userID}, "messages/"+id)
	return e
}
