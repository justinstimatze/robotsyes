//go:build payments

// This file only compiles into a binary built with `-tags payments`.
// The default build gets payments_stub.go instead, so `go install
// ./cmd/robotsyes` with no tags never links in chit (and its own
// dependency graph) at all.
package main

import (
	"log"

	"github.com/justinstimatze/robotsyes/internal/config"
	"github.com/justinstimatze/robotsyes/internal/paymentgate/chitgate"
	"github.com/justinstimatze/robotsyes/internal/payments"
)

// buildMerchant returns nil when payments are disabled — proxy.New
// treats a nil Merchant as "no paid-overflow tier configured". Resolves
// Network/Asset defaults into cfg here, once, so both the merchant and
// the discovery document (which reads cfg.Payments back out of the
// Server) agree on the effective values instead of the discovery doc
// publishing an empty string when the operator relied on chitgate's
// built-in default.
func buildMerchant(cfg *config.Config) payments.Merchant {
	if !cfg.Payments.Enabled {
		return nil
	}
	if cfg.Payments.Network == "" {
		cfg.Payments.Network = chitgate.DefaultNetwork
	}
	if cfg.Payments.Asset == "" {
		cfg.Payments.Asset = chitgate.DefaultAsset
	}
	m, err := chitgate.New(chitgate.Config{
		PayoutAddress:        cfg.Payments.PayoutAddress,
		PriceCentsPerRequest: cfg.Payments.PriceCentsPerRequest,
		Network:              cfg.Payments.Network,
		Asset:                cfg.Payments.Asset,
	})
	if err != nil {
		log.Fatalf("robotsyes: %v", err)
	}
	return m
}
