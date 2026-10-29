package main

import (
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
)

func productionVariant(variant string) string {
	require(variant == "S" || variant == "C" || variant == "V", "shared binary supports S/C/V only")
	if variant == "C" { return "research-certificate" }
	if variant == "V" { return "research-certificate-with-replay" }
	return "research-modified-stock"
}
func configureVariant(config *ethconfig.Config, variant string) {
	productionVariant(variant)
	config.EstimateGasCertificate = variant == "C" || variant == "V"
	config.EstimateGasCertificateReplay = variant == "V"
}
func assertVariant(backend *eth.Ethereum, variant string) {
	productionVariant(variant)
	require(backend.APIBackend.EstimateGasCertificateEnabled() == (variant == "C" || variant == "V"), "immutable certificate choice")
	require(backend.APIBackend.EstimateGasCertificateReplayEnabled() == (variant == "V"), "immutable replay choice")
}
