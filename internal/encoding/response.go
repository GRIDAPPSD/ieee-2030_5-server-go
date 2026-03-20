package encoding

import (
	"log"
	"net/http"
)

// WriteXML marshals v as IEEE 2030.5 XML and writes it to the response.
// If NamespaceMiddleware is active, the Write() call will automatically
// rewrite the namespace to match the client's expected version.
func WriteXML(w http.ResponseWriter, status int, v any) {
	enc := NewXMLEncoder()
	data, err := enc.Marshal(v)
	if err != nil {
		log.Printf("encoding error: %v", err)
		http.Error(w, "internal encoding error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", ContentTypeSEPXML)
	w.WriteHeader(status)
	w.Write(data)
}

// MethodNotAllowed returns a 405 response with an Allow header.
func MethodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
