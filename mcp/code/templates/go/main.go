package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("Hello from your app\n")) })
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
