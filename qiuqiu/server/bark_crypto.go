package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) barkCryptoManage(w http.ResponseWriter, r *http.Request, u User, resource string) (bool, error) {
	if resource != "keys/encryption" {
		return false, nil
	}
	if r.Method != "POST" {
		return true, &AppError{405, "method_not_allowed", "需要POST"}
	}
	var p struct {
		ID        string `json:"id"`
		Algorithm string `json:"algorithm"`
		Key       string `json:"key"`
		IV        string `json:"iv"`
	}
	if e := decode(r, &p); e != nil {
		return true, e
	}
	if _, e := aes.NewCipher([]byte(p.Key)); e != nil {
		return true, bad("AES密钥必须为16/24/32字节")
	}
	p.Algorithm = strings.ToLower(p.Algorithm)
	if p.Algorithm != "cbc" && p.Algorithm != "gcm" {
		return true, bad("算法为cbc或gcm")
	}
	var n int
	if e := s.store.QueryRow("SELECT count(*) FROM api_keys WHERE id=? AND user_id=? AND kind='bark' AND disabled=0", p.ID, u.ID).Scan(&n); e != nil {
		return true, e
	}
	if n != 1 {
		return true, forbidden()
	}
	encrypted, e := s.sealConfig(ChannelConfig{"algorithm": p.Algorithm, "key": p.Key, "iv": p.IV})
	if e != nil {
		return true, e
	}
	_, e = s.store.Exec("INSERT INTO key_encryption VALUES(?,?) ON CONFLICT(key_id) DO UPDATE SET config=excluded.config", p.ID, encrypted)
	if e == nil {
		respond(w, 200, params{"success": true})
	}
	return true, e
}

func (s *Server) decryptBark(token string, p params) (params, error) {
	key, e := s.store.key(token, "bark")
	if e != nil {
		return nil, e
	}
	var sealed string
	if s.store.QueryRow("SELECT config FROM key_encryption WHERE key_id=?", key.ID).Scan(&sealed) != nil {
		return nil, bad("此Bark凭证未配置解密密钥")
	}
	raw, e := base64.StdEncoding.DecodeString(sealed)
	if e != nil {
		return nil, bad("解密配置无效")
	}
	block, _ := aes.NewCipher(s.channelKey)
	g, _ := cipher.NewGCM(block)
	if len(raw) < g.NonceSize() {
		return nil, bad("解密配置无效")
	}
	config, e := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte("qiuqiu.channels.v1"))
	if e != nil {
		return nil, bad("解密配置无效")
	}
	c := ChannelConfig{}
	if json.Unmarshal(config, &c) != nil {
		return nil, bad("解密配置无效")
	}
	data, e := base64.StdEncoding.DecodeString(p.str("ciphertext"))
	if e != nil {
		return nil, bad("Bark密文须为Base64")
	}
	aesBlock, e := aes.NewCipher([]byte(c["key"]))
	if e != nil {
		return nil, bad("AES密钥无效")
	}
	iv := []byte(first(p.str("iv"), c["iv"]))
	var plain []byte
	if c["algorithm"] == "gcm" {
		g, e := cipher.NewGCM(aesBlock)
		if e != nil || len(iv) != g.NonceSize() {
			return nil, bad("GCM IV须为12字节")
		}
		plain, e = g.Open(nil, iv, data, nil)
		if e != nil {
			return nil, bad("Bark解密失败")
		}
	} else {
		if len(iv) != aes.BlockSize || len(data) == 0 || len(data)%aes.BlockSize != 0 {
			return nil, bad("CBC IV须为16字节，密文须对齐")
		}
		plain = make([]byte, len(data))
		cipher.NewCBCDecrypter(aesBlock, iv).CryptBlocks(plain, data)
		pad := int(plain[len(plain)-1])
		if pad < 1 || pad > aes.BlockSize || pad > len(plain) {
			return nil, bad("Bark解密失败")
		}
		for _, v := range plain[len(plain)-pad:] {
			if int(v) != pad {
				return nil, bad("Bark解密失败")
			}
		}
		plain = plain[:len(plain)-pad]
	}
	out := params{}
	if json.Unmarshal(plain, &out) != nil {
		return nil, bad("Bark解密正文不是JSON")
	}
	return out, nil
}
