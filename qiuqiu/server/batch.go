package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// Batch sends retain each credential's scope and report each independent
// acceptance result. Credentials are never echoed into responses.
func (s *Server) compatibilityBatch(w http.ResponseWriter, r *http.Request) (bool, error) {
	bark := r.URL.Path == "/push"
	simple := r.URL.Path == "/api/send/message/simple-push"
	if !bark && !simple {
		return false, nil
	}
	if r.Method != "GET" && r.Method != "POST" {
		return false, nil
	}
	p, e := parseParams(r)
	if e != nil {
		return true, e
	}
	// Restore the body for the ordinary, single-message adapter.
	if r.Method != "GET" {
		r.Body = http.NoBody
		r.Body = ioBody(encode(p))
		r.Header.Set("Content-Type", "application/json")
	}
	field := "device_keys"
	if simple {
		field = "sptList"
	}
	keys := p.list(field)
	if len(keys) == 0 {
		return false, nil
	}
	if len(keys) > 100 {
		return true, bad("每批最多100个凭证")
	}
	if simple && len(keys) > 10 {
		return true, bad("sptList最多10个凭证")
	}
	delete(p, field)
	out := []params{}
	seen := map[string]params{}
	for index, key := range keys {
		if cached, ok := seen[key]; ok {
			item := params{}
			for k, v := range cached {
				item[k] = v
			}
			item["index"] = index
			item["duplicate"] = true
			out = append(out, item)
			continue
		}
		body := params{}
		for k, v := range p {
			body[k] = v
		}
		path := "/push"
		body["device_key"] = key
		if simple {
			path = "/api/send/message/simple-push"
			delete(body, "device_key")
			body["spt"] = key
		}
		req, _ := http.NewRequestWithContext(r.Context(), "POST", path, bytes.NewBufferString(encode(body)))
		req.RemoteAddr = r.RemoteAddr
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		s.ServeHTTP(recorder, req)
		var response params
		json.Unmarshal(recorder.Body.Bytes(), &response)
		item := params{"index": index, "http_status": recorder.Code, "code": response["code"], "message_id": response["message_id"]}
		if data, ok := response["data"].(map[string]any); ok {
			item["message_id"] = data["message_id"]
		}
		if recorder.Code >= 400 {
			item["error"] = response["error"]
			item["message"] = response["message"]
		}
		seen[key] = item
		out = append(out, item)
	}
	code := 200
	if simple {
		code = 1000
	}
	respond(w, 200, params{"code": code, "data": out, "extension": "qiuqiu.batch.v1", "message": "批量请求处理完成，请检查每项结果"})
	return true, nil
}

func ioBody(value string) io.ReadCloser { return io.NopCloser(strings.NewReader(value)) }
