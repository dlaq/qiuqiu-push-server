package main

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
)

func (s *Server) iconHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 405)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/icons/"), ".png")
	var content []byte
	if s.store.QueryRow("SELECT content FROM icon_assets WHERE id=?", id).Scan(&content) != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public,max-age=31536000,immutable")
	w.Header().Set("Content-Disposition", "inline")
	w.Write(content)
}
func (s *Server) uploadIcon(w http.ResponseWriter, r *http.Request, u User) (bool, error) {
	if r.URL.Path != "/api/v1/manage/icons" {
		return false, nil
	}
	if r.Method != "POST" {
		return true, &AppError{405, "method_not_allowed", "需要POST"}
	}
	if e := r.ParseMultipartForm(1 << 20); e != nil {
		return true, bad("图片请求过大或格式错误")
	}
	defer r.MultipartForm.RemoveAll()
	file, _, e := r.FormFile("file")
	if e != nil {
		return true, bad("请选择PNG或JPEG图片")
	}
	defer file.Close()
	raw, e := io.ReadAll(io.LimitReader(file, (900<<10)+1))
	if e != nil || len(raw) > 900<<10 {
		return true, bad("图片上限900KB")
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
		return true, bad("图片须为PNG/JPEG，最大4096像素")
	}
	source, _, e := image.Decode(bytes.NewReader(raw))
	if e != nil {
		return true, bad("图片解码失败")
	}
	target := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	bounds := source.Bounds()
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			target.Set(x, y, source.At(bounds.Min.X+x*bounds.Dx()/256, bounds.Min.Y+y*bounds.Dy()/256))
		}
	}
	var result bytes.Buffer
	if e = png.Encode(&result, target); e != nil {
		return true, e
	}
	id := uuid()
	if _, e = s.store.Exec("INSERT INTO icon_assets VALUES(?,?,?)", id, u.ID, result.Bytes()); e != nil {
		return true, e
	}
	respond(w, 201, params{"path": "/api/v1/icons/" + id + ".png", "notice": "图片链接可公开读取，请勿上传敏感图片"})
	return true, nil
}
