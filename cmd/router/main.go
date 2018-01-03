// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/negroni"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tsuru/kubernetes-router/api"
	"github.com/tsuru/kubernetes-router/kubernetes"
	"github.com/tsuru/kubernetes-router/router"
)

func main() {
	listenAddr := flag.String("listen-addr", ":8077", "Listen address")
	k8sNamespace := flag.String("k8s-namespace", "default", "Kubernetes namespace to create resources")
	k8sTimeout := flag.Duration("k8s-timeout", time.Second*10, "Kubernetes per-request timeout")
	k8sLabels := &MapFlag{}
	flag.Var(k8sLabels, "k8s-labels", "Labels to be added to each resource created. Expects KEY=VALUE format.")
	k8sAnnotations := &MapFlag{}
	flag.Var(k8sAnnotations, "k8s-annotations", "Annotations to be added to each resource created. Expects KEY=VALUE format.")
	ingressMode := flag.Bool("ingress-mode", false, "Creates ingress resources instead of LB services.")

	certFile := flag.String("cert-file", "", "Path to certificate used to serve https requests")
	keyFile := flag.String("key-file", "", "Path to private key used to serve https requests")

	optsToLabels := &MapFlag{}
	flag.Var(optsToLabels, "opts-to-label", "Mapping between router options and service labels. Expects KEY=VALUE format.")

	poolLabels := &MultiMapFlag{}
	flag.Var(poolLabels, "pool-labels", "Default labels for a given pool. Expects POOL={\"LABEL\":\"VALUE\"} format.")

	flag.Parse()

	err := flag.Lookup("logtostderr").Value.Set("true")
	if err != nil {
		log.Printf("failed to set log to stderr: %v\n", err)
	}

	base := &kubernetes.BaseService{
		Namespace:   *k8sNamespace,
		Timeout:     *k8sTimeout,
		Labels:      *k8sLabels,
		Annotations: *k8sAnnotations,
	}
	var service router.Service = &kubernetes.LBService{BaseService: base, OptsAsLabels: *optsToLabels, PoolLabels: *poolLabels}
	if *ingressMode {
		service = &kubernetes.IngressService{BaseService: base}
	}

	routerAPI := api.RouterAPI{IngressService: service}
	r := mux.NewRouter().StrictSlash(true)

	r.PathPrefix("/api").Handler(negroni.New(
		api.AuthMiddleware{
			User: os.Getenv("ROUTER_API_USER"),
			Pass: os.Getenv("ROUTER_API_PASSWORD"),
		},
		negroni.Wrap(routerAPI.Routes()),
	))
	r.HandleFunc("/healthcheck", routerAPI.Healthcheck)
	r.Handle("/metrics", promhttp.Handler())

	r.HandleFunc("/debug/pprof/", pprof.Index)
	r.HandleFunc("/debug/pprof/heap", pprof.Index)
	r.HandleFunc("/debug/pprof/mutex", pprof.Index)
	r.HandleFunc("/debug/pprof/goroutine", pprof.Index)
	r.HandleFunc("/debug/pprof/threadcreate", pprof.Index)
	r.HandleFunc("/debug/pprof/block", pprof.Index)
	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)

	n := negroni.New(negroni.NewLogger(), negroni.NewRecovery())
	n.UseHandler(r)

	server := http.Server{
		Addr:         *listenAddr,
		Handler:      n,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go handleSignals(&server)

	if *keyFile != "" && *certFile != "" {
		log.Printf("Started listening and serving TLS at %s", *listenAddr)
		if err := server.ListenAndServeTLS(*certFile, *keyFile); err != nil && err != http.ErrServerClosed {
			log.Fatalf("fail serve: %v", err)
		}
		return
	}
	log.Printf("Started listening and serving at %s", *listenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("fail serve: %v", err)
	}
}

func handleSignals(server *http.Server) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
	sig := <-signals
	log.Printf("Received %s. Terminating...", sig)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	err := server.Shutdown(ctx)
	if err != nil {
		log.Fatalf("Error during server shutdown: %v", err)
	}
	log.Print("Server shutdown succeeded.")
}
