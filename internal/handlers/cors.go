package handlers

import "net/http"

func EnableCors(w *http.ResponseWriter) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
	(*w).Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	(*w).Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-User-Id, X-User-Name, X-User-Avatar, X-Shopify-Event-Id, X-Shopify-Hmac-Sha256, X-Shopify-Topic, X-Shopify-Webhook-Id")
}
