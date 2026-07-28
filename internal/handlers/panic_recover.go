package handlers

import (
	"log"
	"net/http"
	"runtime/debug"
)

func recoverHandlerPanic(w http.ResponseWriter, r *http.Request, scope string) {
	if rec := recover(); rec != nil {
		log.Printf("[%s] panic serving %s %s: %v\n%s", scope, r.Method, r.URL.Path, rec, debug.Stack())
		http.Error(w, "RAG runtime panic", http.StatusInternalServerError)
	}
}
