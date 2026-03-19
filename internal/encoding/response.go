package encoding

import (
	"log"
	"net/http"
)

// WriteXML marshals v as IEEE 2030.5 XML and writes it to the response.
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
