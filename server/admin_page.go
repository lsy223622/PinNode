package main

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"net/http"
)

//go:embed web/admin.html
var adminPage []byte

//go:embed web/mark.svg
var adminMark []byte

const adminMarkCacheControl = "public, max-age=86400"

func (s *Service) serveAdminPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_page_failed", "生成页面安全参数失败")
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	page := bytes.ReplaceAll(adminPage, []byte("{{CSP_NONCE}}"), []byte(nonce))
	page = bytes.ReplaceAll(page, []byte("{{MANAGED_DEVICE_TAG}}"), []byte(managedDeviceTag))
	page = bytes.ReplaceAll(page, []byte("{{BUILD_BADGE}}"), []byte(buildBadge))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+nonce+"'; script-src 'nonce-"+nonce+"'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(page)
}

func (s *Service) serveAdminMark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "方法不支持")
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", adminMarkCacheControl)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(adminMark)
}
