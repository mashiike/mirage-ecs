package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	//	"github.com/acidlemon/go-dumper"
	"github.com/methane/rproxy"
)

type proxyAction string

const (
	proxyAdd             = proxyAction("Add")
	proxyRemove          = proxyAction("Remove")
	proxyHandlerLifetime = 30 * time.Second
)

type proxyControl struct {
	Action    proxyAction
	Subdomain string
	IPAddress string
	Port      int
}

type ReverseProxy struct {
	mu        sync.RWMutex
	cfg       *Config
	domainMap map[string]proxyHandlers
}

func NewReverseProxy(cfg *Config) *ReverseProxy {
	return &ReverseProxy{
		cfg:       cfg,
		domainMap: make(map[string]proxyHandlers),
	}
}

func (r *ReverseProxy) ServeHTTPWithPort(w http.ResponseWriter, req *http.Request, port int) {
	subdomain := strings.ToLower(strings.Split(req.Host, ".")[0])

	if handler := r.findHandler(subdomain, port); handler != nil {
		log.Printf("[debug] proxy handler found for subdomain %s", subdomain)
		handler.ServeHTTP(w, req)
	} else {
		log.Printf("[warn] proxy handler not found for subdomain %s", subdomain)
		http.NotFound(w, req)
	}
}

func (r *ReverseProxy) Exists(subdomain string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.domainMap[subdomain]
	if exists {
		return true
	}
	for name, _ := range r.domainMap {
		if m, _ := path.Match(name, subdomain); m {
			return true
		}
	}
	return false
}

func (r *ReverseProxy) Subdomains() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ds := make([]string, 0, len(r.domainMap))
	for name, _ := range r.domainMap {
		ds = append(ds, name)
	}
	return ds
}

func (r *ReverseProxy) findHandler(subdomain string, port int) http.Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	log.Printf("[debug] findHandler for %s:%d", subdomain, port)

	proxyHandlers, ok := r.domainMap[subdomain]
	if !ok {
		for name, ph := range r.domainMap {
			if m, _ := path.Match(name, subdomain); m {
				proxyHandlers = ph
				break
			}
		}
		if proxyHandlers == nil {
			return nil
		}
	}

	handler, ok := proxyHandlers.Handler(port)
	if !ok {
		return nil
	}
	return handler
}

type proxyHandler struct {
	handler http.Handler
	timer   *time.Timer
}

func newProxyHandler(h http.Handler) *proxyHandler {
	return &proxyHandler{
		handler: h,
		timer:   time.NewTimer(proxyHandlerLifetime),
	}
}

func (h *proxyHandler) alive() bool {
	select {
	case <-h.timer.C:
		return false
	default:
		return true
	}
}

func (h *proxyHandler) extend() {
	h.timer.Reset(proxyHandlerLifetime) // extend lifetime
}

type proxyHandlers map[int]map[string]*proxyHandler

func (ph proxyHandlers) Handler(port int) (http.Handler, bool) {
	handlers := ph[port]
	if len(handlers) == 0 {
		return nil, false
	}
	for ipaddress, handler := range ph[port] {
		if handler.alive() {
			// return first (randomized by Go's map)
			return handler.handler, true
		} else {
			log.Printf("[info] proxy handler to %s is dead", ipaddress)
			delete(ph[port], ipaddress)
		}
	}
	return nil, false
}

func (ph proxyHandlers) exists(port int, ipaddress string) bool {
	if ph[port] == nil {
		return false
	}
	if h := ph[port][ipaddress]; h == nil {
		return false
	} else if h.alive() {
		log.Printf("[debug] proxy handler to %s extends lifetime", ipaddress)
		h.extend()
		return true
	} else {
		log.Printf("[info] proxy handler to %s is dead", ipaddress)
		delete(ph[port], ipaddress)
		return false
	}
}

func (ph proxyHandlers) add(port int, ipaddress string, h http.Handler) {
	if ph[port] == nil {
		ph[port] = make(map[string]*proxyHandler)
	}
	log.Printf("[info] new proxy handler to %s", ipaddress)
	ph[port][ipaddress] = newProxyHandler(h)
}

func (r *ReverseProxy) AddSubdomain(subdomain string, ipaddress string, targetPort int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var ph proxyHandlers
	if _ph, exists := r.domainMap[subdomain]; exists {
		ph = _ph
	} else {
		ph = make(proxyHandlers)
	}

	// create reverse proxy
	for _, v := range r.cfg.Listen.HTTP {
		if v.TargetPort != targetPort {
			continue
		}
		if ph.exists(v.ListenPort, ipaddress) {
			continue
		}
		destUrlString := fmt.Sprintf("http://%s:%d", ipaddress, v.TargetPort)
		destUrl, _ := url.Parse(destUrlString)
		handler := rproxy.NewSingleHostReverseProxy(destUrl)
		ph.add(v.ListenPort, ipaddress, handler)
		log.Printf("[info] add subdomain: %s:%d -> %s:%d", subdomain, v.ListenPort, ipaddress, targetPort)
	}
	r.domainMap[subdomain] = ph
}

func (r *ReverseProxy) RemoveSubdomain(subdomain string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	log.Println("[info] removing subdomain:", subdomain)
	delete(r.domainMap, subdomain)
}

func (r *ReverseProxy) Modify(action *proxyControl) {
	switch action.Action {
	case proxyAdd:
		r.AddSubdomain(action.Subdomain, action.IPAddress, action.Port)
	case proxyRemove:
		r.RemoveSubdomain(action.Subdomain)
	default:
		log.Printf("[error] unknown proxy action: %s", action.Action)
	}
}
