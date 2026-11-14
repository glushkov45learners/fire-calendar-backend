package api

import (
	"embed"
	"net/http"
)

// Birthday greeting page for a friend; not part of contract 3.
// Lives under /api because only /api is proxied to the backend in production.
//
//go:embed birthday
var birthdayFiles embed.FS

func birthdayHandler() http.Handler {
	return http.StripPrefix("/api/", http.FileServerFS(birthdayFiles))
}
