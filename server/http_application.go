package server

import "net/http"

type httpEndpoint struct {
	pattern string
	handler http.HandlerFunc
}

// httpApplication assembles one transport surface. Controller behavior and the
// extension registrations remain external; the root keeps API auth/CORS apart
// from the public frontend and retains the original ServeMux semantics.
type httpApplication struct{ server *Server }

func (application httpApplication) handler() http.Handler {
	s := application.server
	mux := http.NewServeMux()
	s.registerSideRoutes(mux)
	application.install(mux, s.httpPrelude())
	s.registerFindingTraffic(mux)
	application.install(mux, s.httpRemainder())
	api := cors(s.requireAuth(s.updateAdmission(mux)))
	root := http.NewServeMux()
	root.HandleFunc("/api/update/maintenance", s.updateMaintenance)
	root.Handle("/api/", api)
	root.Handle("/", s.webuiHandler())
	return root
}

func (application httpApplication) install(mux *http.ServeMux, endpoints []httpEndpoint) {
	for _, endpoint := range endpoints {
		mux.HandleFunc(endpoint.pattern, endpoint.handler)
	}
}

type crossOriginHTTP struct{ next http.Handler }

func (policy crossOriginHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(204)
		return
	}
	policy.next.ServeHTTP(w, r)
}
