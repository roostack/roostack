// Command roostack-fakecloud is a pretend Roostack Cloud for development
// (internal/cloud/cloudtest): it answers a lab's side of the API, so
// linking can be tried end to end on a dev VM (SPEC.md §6.6). Never
// packaged.
//
//	roostack-fakecloud -ca-out /tmp/fakecloud-ca.pem
//
// A lab on a dev VM reaches it at https://10.0.2.2:8443 (QEMU's address
// for the host) with, in lab.yaml:
//
//	cloud:
//	  url: https://10.0.2.2:8443
//	  ca_file: /var/lib/roostack/fakecloud-ca.pem
//
// The confirm page is served over plain HTTP on -page, for a browser on
// the host.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	"roostack.cloud/roostack/internal/cloud/cloudtest"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "the API's address (HTTPS)")
	hosts := flag.String("hosts", "127.0.0.1,10.0.2.2,localhost", "names and addresses in its HTTPS certificate")
	page := flag.String("page", "127.0.0.1:8480", "where the confirm page is served (plain HTTP)")
	caOut := flag.String("ca-out", "fakecloud-ca.pem", "where to write its certificate authority")
	flag.Parse()

	f := cloudtest.New()
	f.PageURL = "http://" + strings.Replace(*page, "127.0.0.1", "localhost", 1)
	if err := os.WriteFile(*caOut, f.CAPEM(), 0o644); err != nil {
		log.Fatal(err)
	}
	go func() { log.Fatal(http.ListenAndServe(*page, f)) }()
	srv := &http.Server{Addr: *listen, Handler: logged(f), TLSConfig: f.TLSConfig(strings.Split(*hosts, ",")...)}
	log.Printf("fake Roostack Cloud: API https://%s, confirm page %s, CA in %s", *listen, f.PageURL, *caOut)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := "anonymous"
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			who = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, who)
		h.ServeHTTP(w, r)
	})
}
